<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { select } from 'd3-selection';
	import { zoom, zoomIdentity, type ZoomBehavior, type ZoomTransform } from 'd3-zoom';
	import { appState } from '$lib/state.svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { layoutStars, type LayoutNode } from '$lib/constellationLayout';
	import { colorForCategory } from '$lib/categoryColors';
	import StarCard from '$lib/components/StarCard.svelte';
	import ConstellationSettingsModal from '$lib/components/ConstellationSettingsModal.svelte';
	import {
		PanelLeft,
		Search,
		Settings,
		Sparkles,
		Inbox as InboxIcon,
		Library,
		Map as MapIcon,
		Maximize2,
		X
	} from '@lucide/svelte';
	import type { Star } from '$lib/types';

	let view = $state<'library' | 'map'>('library');
	let showSettings = $state(false);

	onMount(() => {
		void constellationState.loadLibrary();
	});
	onDestroy(() => clearTimeout(searchDebounce));

	// Search: a debounced fetch against SearchLibraryStars, not a client-side
	// filter over already-loaded stars — FTS5 relevance ranking over the
	// full body text finds things a substring match over just title/summary
	// wouldn't, and this way it works the same regardless of how many stars
	// are actually loaded into libraryStars/aboutYouStars right now.
	let searchOpen = $state(false);
	let searchQuery = $state('');
	let searchResults = $state<Star[] | null>(null);
	let searchLoading = $state(false);
	let searchInputEl = $state<HTMLInputElement | null>(null);
	let searchDebounce: ReturnType<typeof setTimeout> | undefined;

	function openSearch() {
		searchOpen = true;
		requestAnimationFrame(() => searchInputEl?.focus());
	}
	function closeSearch() {
		searchOpen = false;
		searchQuery = '';
		searchResults = null;
		clearTimeout(searchDebounce);
	}
	function onSearchInput() {
		clearTimeout(searchDebounce);
		const query = searchQuery.trim();
		if (!query) {
			searchResults = null;
			searchLoading = false;
			return;
		}
		searchLoading = true;
		searchDebounce = setTimeout(async () => {
			const results = await constellationState.searchStars(query);
			// Stale-response guard: if the box was cleared or changed again
			// while this request was in flight, don't clobber that with an
			// answer to a query that's no longer current.
			if (searchQuery.trim() === query) {
				searchResults = results;
				searchLoading = false;
			}
		}, 250);
	}

	// Map data is loaded lazily on first switch to that tab rather than on
	// every Library load — it's a separate, heavier fetch (every star +
	// every edge, unfiltered by section) that the default Library view
	// never needs.
	let mapLoaded = $state(false);
	$effect(() => {
		if (view === 'map' && !mapLoaded) {
			mapLoaded = true;
			void constellationState.loadMap();
		}
	});

	// Personal and topical stars are grouped into the same per-category
	// sections — each StarCard now colors itself by category (see
	// categoryColors.ts), not by is_personal, so a category section can
	// freely mix both without losing anything. A dedicated flat "About you"
	// bucket made sense when personal stars were a small minority of the
	// library; it stopped scaling once Weaver became personal-only
	// extraction and every star became is_personal — at that point a single
	// undifferentiated bucket is strictly worse than the category sections
	// topical stars always got.
	const byCategory = $derived.by(() => {
		const groups = new Map<string, Star[]>();
		for (const star of [...constellationState.libraryStars, ...constellationState.aboutYouStars]) {
			const list = groups.get(star.category) ?? [];
			list.push(star);
			groups.set(star.category, list);
		}
		return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));
	});

	// Just a plain star count — ConstellationStats has no "drawn from N
	// threads" figure to pair with it (that would need a distinct-thread
	// count over star_sources, which the backend doesn't currently expose),
	// so the mockup's "42 stars · drawn from 160 threads" meta-line is
	// simplified to what's actually available rather than guessed at.
	const totalStarsNote = $derived(
		`${constellationState.libraryStars.length + constellationState.aboutYouStars.length} star${
			constellationState.libraryStars.length + constellationState.aboutYouStars.length === 1 ? '' : 's'
		}`
	);

	// Sized to the actual rendered viewport (bind:clientWidth/clientHeight
	// below), not a fixed constant — a fixed 800x900 canvas meant most of
	// the graph rendered off-screen, requiring a scroll to find it, which
	// was most of why the map read as sparse/disconnected: nodes and their
	// connecting lines were there, just not in the visible viewport.
	// layoutStars' own bounding-box normalize (see constellationLayout.ts)
	// guarantees every node fits whatever size is passed in.
	let mapAreaWidth = $state(360);
	let mapAreaHeight = $state(620);
	const mapLayout = $derived.by(() => {
		if (!constellationState.mapData) return null;
		return layoutStars(constellationState.mapData.stars, constellationState.mapData.edges, {
			width: mapAreaWidth,
			height: mapAreaHeight
		});
	});
	// GET /api/constellation/map deliberately filters to auto/confirmed
	// stars only (gateway/constellation_routes.go's handleGetConstellationMap
	// hardcodes StarFilter{Statuses: []string{"auto", "confirmed"}}) — a
	// proposed star never appears in mapData.stars at all, so a count
	// derived from that array can never be anything but 0. Read the real
	// count from stats instead (loaded by loadLibrary, already fetched by
	// the time this tab is reachable).
	const unconfirmedCount = $derived(constellationState.stats?.star_counts_by_status['proposed'] ?? 0);

	// --- Map interaction: tap-to-reveal + pan/zoom ---------------------
	//
	// Showing every star's title at once was the original bug (see
	// constellationLayout.ts's collision force, which only ever kept dot
	// *centers* apart — never aware of label text width). The fix isn't a
	// denser layout, it's not rendering all the text at once: stars are
	// plain dots by default, and tapping one lifts a single floating label
	// above it. Tapping the same star again (or its label) opens it;
	// tapping anything else — empty space, or a different star — moves or
	// clears the selection instead.
	let selectedStarId = $state<number | null>(null);
	const selectedNode = $derived.by((): LayoutNode | null => {
		if (selectedStarId === null || !mapLayout) return null;
		return mapLayout.nodeById.get(selectedStarId) ?? null;
	});

	function handleStarClick(node: LayoutNode, event: MouseEvent) {
		event.stopPropagation();
		if (selectedStarId === node.id) {
			goto(`/constellation/star/${node.id}`);
		} else {
			selectedStarId = node.id;
		}
	}

	// Pan/zoom is layered on top of the same fixed layout rather than
	// re-flowing it — zooming in doesn't recompute positions, it just
	// magnifies the existing spacing, which is exactly what makes a dense
	// cluster easier to aim at on a phone. scaleExtent's floor of 1 is
	// deliberate: layoutStars already normalizes every node to fit
	// [0, width] x [0, height] at scale 1, so zooming out further would
	// only ever add empty margin, never reveal more of the graph.
	const ZOOM_SCALE_MIN = 1;
	const ZOOM_SCALE_MAX = 4;
	let mapAreaEl = $state<HTMLDivElement | null>(null);
	let zoomTransform = $state<ZoomTransform>(zoomIdentity);
	const zoomBehavior: ZoomBehavior<HTMLDivElement, unknown> = zoom<HTMLDivElement, unknown>()
		.scaleExtent([ZOOM_SCALE_MIN, ZOOM_SCALE_MAX])
		.on('zoom', (event) => {
			zoomTransform = event.transform;
		});
	const isZoomedOrPanned = $derived(zoomTransform.k !== 1 || zoomTransform.x !== 0 || zoomTransform.y !== 0);

	function resetZoom() {
		if (!mapAreaEl) return;
		select(mapAreaEl).call(zoomBehavior.transform, zoomIdentity);
	}

	// Re-bind (and re-extent) whenever the map container mounts or resizes
	// — mapAreaEl is only non-null while the Map tab's {#if} block is
	// actually rendered, so switching back to Library and returning here
	// re-runs this rather than leaving a stale listener on a detached node.
	$effect(() => {
		if (!mapAreaEl) return;
		const bounds: [[number, number], [number, number]] = [
			[0, 0],
			[mapAreaWidth, mapAreaHeight]
		];
		zoomBehavior.extent(bounds).translateExtent(bounds);
		select(mapAreaEl).call(zoomBehavior);
	});
</script>

<svelte:head>
	<title>Constellation — Polaris</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		{#if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<h1 class="page-title"><span class="wordmark">Constellation</span></h1>
		{#if view === 'map' && unconfirmedCount > 0}
			<span class="unconfirmed-pill">{unconfirmedCount} unconfirmed</span>
		{/if}
	</div>
	<div class="header-right">
		<button class="icon-btn" title="Search" aria-label="Search" onclick={openSearch}>
			<Search size={18} />
		</button>
		<button class="icon-btn" onclick={() => (showSettings = true)} title="Constellation settings">
			<Settings size={18} />
		</button>
	</div>
</header>

{#if searchOpen}
	<div class="search-bar-wrap">
		<div class="thread-search">
			<Search size={14} class="icon-search" aria-hidden="true" />
			<input
				bind:this={searchInputEl}
				bind:value={searchQuery}
				oninput={onSearchInput}
				type="text"
				placeholder="Search your library"
				spellcheck="false"
				onkeydown={(e) => e.key === 'Escape' && closeSearch()}
			/>
			<button class="icon-btn clear-btn" onclick={closeSearch} aria-label="Close search">
				<X size={13} />
			</button>
		</div>
	</div>
{/if}

<div class="content" class:map-content={view === 'map'}>
	{#if searchOpen && searchQuery.trim()}
		{#if searchLoading}
			<p class="empty">Searching…</p>
		{:else if searchResults && searchResults.length > 0}
			<p class="meta-line">
				{searchResults.length} result{searchResults.length === 1 ? '' : 's'}
			</p>
			<div class="card-list">
				{#each searchResults as star (star.id)}
					<StarCard {star} onclick={() => goto(`/constellation/star/${star.id}`)} />
				{/each}
			</div>
		{:else}
			<p class="empty">No stars match "{searchQuery.trim()}".</p>
		{/if}
	{:else if view === 'library'}
		{#if !constellationState.libraryLoaded}
			<p class="empty">Loading your library…</p>
		{:else if constellationState.libraryError && constellationState.libraryStars.length === 0 && constellationState.aboutYouStars.length === 0}
			<p class="empty">Couldn't load your library — check your connection and try again.</p>
		{:else if constellationState.libraryStars.length === 0 && constellationState.aboutYouStars.length === 0}
			<p class="empty">
				No stars yet. Constellation builds itself from threads you've already had — check back
				after it's had a chance to run, or trigger a backfill from Settings.
			</p>
		{:else}
			<p class="meta-line">{totalStarsNote}</p>

			<div class="banner-stack">
				{#if constellationState.digest?.show}
					<button class="banner digest" onclick={() => goto('/constellation/week')}>
						<Sparkles size={16} class="banner-icon" />
						<span class="label">
							This week: {constellationState.digest.new_count} new, {constellationState.digest
								.links_count} link{constellationState.digest.links_count === 1 ? '' : 's'} surfaced
							{#if constellationState.digest.highlight}
								<small>{constellationState.digest.highlight}</small>
							{/if}
						</span>
					</button>
				{/if}
				{#if constellationState.inboxStars.length > 0 || (constellationState.stats?.star_counts_by_status['proposed'] ?? 0) > 0}
					<button class="banner inbox" onclick={() => goto('/constellation/inbox')}>
						<InboxIcon size={16} class="banner-icon" />
						<span class="label">
							{constellationState.stats?.star_counts_by_status['proposed'] ?? constellationState.inboxStars.length}
							star{(constellationState.stats?.star_counts_by_status['proposed'] ?? 0) === 1 ? '' : 's'}
							proposed
							<small>Awaiting your review in the Inbox</small>
						</span>
					</button>
				{/if}
			</div>

			{#each byCategory as [category, stars] (category)}
				<details class="section" open>
					<summary class="section-header">{category}</summary>
					<div class="card-list">
						{#each stars as star (star.id)}
							<StarCard {star} onclick={() => goto(`/constellation/star/${star.id}`)} />
						{/each}
					</div>
				</details>
			{/each}

			{#if constellationState.rejectedStars.length > 0}
				<details class="section">
					<summary class="section-header">Rejected</summary>
					<div class="card-list">
						{#each constellationState.rejectedStars as star (star.id)}
							<div class="rejected-row">
								<StarCard {star} onclick={() => goto(`/constellation/star/${star.id}`)} />
								<button
									class="btn restore-btn"
									onclick={async () => {
										const result = await constellationState.restoreStar(star.id);
										if (result.error) appState.showToast(result.error);
									}}
								>
									Restore
								</button>
							</div>
						{/each}
					</div>
				</details>
			{/if}
		{/if}
	{:else if !constellationState.mapLoaded}
		<p class="empty">Loading the map…</p>
	{:else if constellationState.mapError}
		<p class="empty">Couldn't load the map — check your connection and try again.</p>
	{:else if mapLayout && mapLayout.nodes.length > 0}
		<!-- Background tap-to-deselect, same as elsewhere in the app (see
		     ThreadMenu.svelte) — this is a dismiss surface behind real
		     interactive controls (the star buttons), not itself a control. -->
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="map-area"
			bind:this={mapAreaEl}
			bind:clientWidth={mapAreaWidth}
			bind:clientHeight={mapAreaHeight}
			onclick={() => (selectedStarId = null)}
		>
			<div
				class="zoom-canvas"
				style="transform: translate({zoomTransform.x}px, {zoomTransform.y}px) scale({zoomTransform.k});"
			>
				<svg class="lines" viewBox="0 0 {mapAreaWidth} {mapAreaHeight}">
					{#each mapLayout.edges as edge (edge.starAId + '-' + edge.starBId)}
						{@const a = mapLayout.nodeById.get(edge.starAId)}
						{@const b = mapLayout.nodeById.get(edge.starBId)}
						{#if a && b}
							<line
								x1={a.x}
								y1={a.y}
								x2={b.x}
								y2={b.y}
								style="--star-color: {colorForCategory(a.star.category)}"
							/>
						{/if}
					{/each}
				</svg>
				{#each mapLayout.clusterLabels as cluster (cluster.category)}
					<div
						class="cluster-label"
						style="left: {cluster.x}px; top: {cluster.y -
							cluster.radius -
							8}px; transform: translate(-50%, -50%) scale({1 / zoomTransform.k});"
					>
						{cluster.category}
					</div>
				{/each}
				{#each mapLayout.nodes as node (node.id)}
					<button
						class="map-node"
						class:selected={node.id === selectedStarId}
						style="left: {node.x}px; top: {node.y}px; --star-color: {colorForCategory(
							node.star.category
						)}"
						onclick={(event) => handleStarClick(node, event)}
						aria-label={node.star.title}
					>
						<span class="dot"></span>
					</button>
				{/each}
				{#if selectedNode}
					<button
						class="star-label-chip"
						style="left: {selectedNode.x}px; top: {selectedNode.y}px;"
						onclick={(event) => handleStarClick(selectedNode, event)}
					>
						{selectedNode.star.title}
					</button>
				{/if}
			</div>
		</div>
		<button class="fit-btn" class:visible={isZoomedOrPanned} onclick={resetZoom} title="Reset view">
			<Maximize2 size={14} />
			Fit
		</button>
		<p class="map-hint">Pinch or scroll to zoom · drag to pan · tap a star to see its name</p>
	{:else}
		<p class="empty">Nothing to map yet.</p>
	{/if}
</div>

<div class="tabbar">
	<button class="tab" class:active={view === 'library'} onclick={() => (view = 'library')}>
		<Library size={18} />
		<span>Library</span>
	</button>
	<button class="tab" class:active={view === 'map'} onclick={() => (view = 'map')}>
		<MapIcon size={18} />
		<span>Map</span>
	</button>
</div>

{#if showSettings}
	<ConstellationSettingsModal onClose={() => (showSettings = false)} />
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: max(var(--space-lg), env(safe-area-inset-top)) var(--space-lg) var(--space-lg);
		box-shadow: var(--shadow-well);
	}
	.header-left {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		min-width: 0;
	}
	.header-right {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}
	.page-title {
		margin: 0;
		font-size: 20px;
	}
	/* Reserved brand-face treatment (see app.css's --font-wordmark) — this
	   page's title renders the literal word "Constellation" as a name, same
	   as "Polaris"/"Pulsar" get elsewhere in the app. */
	.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		letter-spacing: 0.02em;
	}
	.unconfirmed-pill {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-accent-2);
		background: color-mix(in srgb, var(--color-accent-2) 14%, transparent);
		border: 1px solid color-mix(in srgb, var(--color-accent-2) 30%, transparent);
		padding: var(--space-xs) var(--space-sm);
		border-radius: var(--radius-full);
		white-space: nowrap;
	}

	/* Same box treatment as Sidebar.svelte's .thread-search ("Search past
	   chats") — one shared visual language for "search" across the app
	   rather than a second, differently-styled bar just for Constellation. */
	.search-bar-wrap {
		padding: var(--space-sm) var(--space-lg) 0;
	}
	.thread-search {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		transition: border-color 0.15s var(--ease-out-expo);
	}
	.thread-search:focus-within {
		border-color: var(--color-accent);
	}
	.thread-search :global(.icon-search) {
		flex-shrink: 0;
		color: var(--color-text-dim);
	}
	.thread-search input {
		flex: 1;
		min-width: 0;
		border: none;
		background: transparent;
		font: inherit;
		font-size: 13px;
		color: var(--color-text);
	}
	.thread-search input:focus {
		outline: none;
	}
	.thread-search input::placeholder {
		color: var(--color-text-dim);
	}
	.clear-btn {
		flex-shrink: 0;
		width: 20px;
		height: 20px;
	}

	.content {
		flex: 1;
		overflow-y: auto;
		padding: 0 var(--space-lg) 80px;
	}
	.content.map-content {
		padding: 0;
		overflow: hidden;
		position: relative;
	}

	.empty {
		max-width: 46ch;
		margin: var(--space-2xl) auto;
		text-align: center;
		font-size: 13.5px;
		line-height: 1.6;
		color: var(--color-text-dim);
	}
	.meta-line {
		padding: var(--space-sm) var(--space-xs) var(--space-md);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.banner-stack {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin-bottom: var(--space-xl);
	}
	.banner {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-md);
		border-radius: var(--radius-lg);
		border: 1px solid var(--color-border);
		background: var(--color-surface);
		text-align: left;
		cursor: pointer;
		width: 100%;
		font: inherit;
		color: inherit;
	}
	.banner.digest {
		background: linear-gradient(160deg, var(--color-accent-soft), var(--color-surface));
	}
	.banner.digest :global(.banner-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
	}
	.banner.inbox {
		background: color-mix(in srgb, var(--color-accent-2) 12%, transparent);
		border-color: color-mix(in srgb, var(--color-accent-2) 30%, transparent);
	}
	.banner.inbox :global(.banner-icon) {
		color: var(--color-accent-2);
		flex-shrink: 0;
	}
	.banner .label {
		font-size: 13px;
		font-weight: 500;
		flex: 1;
	}
	.banner .label small {
		display: block;
		font-size: 11px;
		font-weight: 400;
		color: var(--color-text-dim);
		margin-top: 1px;
	}

	.section {
		margin-bottom: var(--space-xl);
	}
	.section-header {
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		padding: var(--space-sm) var(--space-xs);
		cursor: pointer;
		list-style: none;
	}
	.section-header::-webkit-details-marker {
		display: none;
	}
	.card-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}
	.rejected-row {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}
	.restore-btn {
		align-self: flex-end;
		font-size: 12px;
		padding: 4px var(--space-md);
	}

	.map-area {
		position: relative;
		width: 100%;
		height: 100%;
		overflow: hidden;
		/* Stops the browser's own touch scrolling/pull-to-refresh from
		   fighting d3-zoom's own pan/pinch handling on the same element. */
		touch-action: none;
	}
	/* zoom-canvas carries the pan/zoom transform d3-zoom reports — every
	   coordinate inside it (lines, halos, dots, the label chip) is still in
	   layoutStars' original [0, width] x [0, height] space; this is a plain
	   CSS transform on top, not a re-layout. transform-origin stays at the
	   canvas's own (0,0) to match the translate/scale math d3-zoom emits
	   (it reports translate-then-scale from the origin, not from center). */
	.zoom-canvas {
		position: absolute;
		inset: 0;
		width: 100%;
		height: 100%;
		transform-origin: 0 0;
	}
	.lines {
		position: absolute;
		inset: 0;
		width: 100%;
		height: 100%;
		pointer-events: none;
	}
	.lines line {
		stroke: var(--star-color);
		stroke-width: 1;
		opacity: 0.35;
	}
	.cluster-label {
		position: absolute;
		/* transform (translate + counter-zoom scale) is set inline — see the
		   template — since it depends on the live zoom level, not something
		   plain CSS can express. */
		transform-origin: center;
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		opacity: 0.7;
		pointer-events: none;
		white-space: nowrap;
	}
	.map-node {
		position: absolute;
		transform: translate(-50%, -50%);
		display: flex;
		background: none;
		border: none;
		/* padding alone enlarges the tap target — translate(-50%, -50%)
		   centers on this element's own border box, padding included, so
		   the visible dot stays exactly on (node.x, node.y) with no extra
		   margin needed. A matching negative margin (added in an earlier
		   pass to avoid "wasting" layout space) does NOT cancel out here
		   the way it would in normal flow: for an absolutely positioned
		   element, left/top place the *margin* edge, so a negative margin
		   pulls the actual dot away from (node.x, node.y) by that same
		   amount — an 8px up-left drift confirmed live (dots landing 8px
		   off from where the connecting lines terminate). */
		padding: var(--space-sm);
		cursor: pointer;
		font: inherit;
	}
	.map-node .dot {
		width: 11px;
		height: 11px;
		border-radius: var(--radius-full);
		background: var(--star-color);
		box-shadow: 0 0 10px 2px color-mix(in srgb, var(--star-color) 45%, transparent);
		flex-shrink: 0;
	}
	.map-node.selected .dot {
		box-shadow:
			0 0 0 3px var(--color-bg),
			0 0 0 5px var(--star-color);
	}
	/* star-label-chip: the one title the map ever shows text for at a time
	   — see handleStarClick's doc comment. Tapping it (same as tapping the
	   dot again) opens the star. */
	.star-label-chip {
		position: absolute;
		transform: translate(-50%, calc(-100% - 14px));
		max-width: 180px;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-sm);
		padding: 6px var(--space-sm);
		font: inherit;
		font-size: 12px;
		font-weight: 500;
		line-height: 1.3;
		color: var(--color-text);
		text-align: center;
		box-shadow: var(--shadow-md);
		cursor: pointer;
		z-index: 2;
	}
	.fit-btn {
		position: absolute;
		top: var(--space-md);
		right: var(--space-md);
		display: flex;
		align-items: center;
		gap: 5px;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-full);
		padding: 6px var(--space-md) 6px var(--space-sm);
		font: inherit;
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-dim);
		box-shadow: var(--shadow-sm);
		cursor: pointer;
		opacity: 0;
		pointer-events: none;
		transition: opacity 0.15s ease;
	}
	.fit-btn.visible {
		opacity: 1;
		pointer-events: auto;
	}
	.map-hint {
		position: absolute;
		bottom: var(--space-md);
		left: 0;
		right: 0;
		margin: 0;
		text-align: center;
		font-size: 10.5px;
		color: var(--color-text-dim);
		opacity: 0.75;
		pointer-events: none;
		text-shadow: 0 1px 4px rgba(0, 0, 0, 0.6);
	}

	.tabbar {
		display: flex;
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-bg) 94%, transparent);
		backdrop-filter: blur(10px);
		flex-shrink: 0;
	}
	.tab {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 4px;
		padding: 11px 0 max(12px, env(safe-area-inset-bottom));
		color: var(--color-text-dim);
		background: none;
		border: none;
		cursor: pointer;
		font: inherit;
	}
	.tab.active {
		color: var(--color-accent);
	}
	.tab span {
		font-size: 11px;
		font-weight: 500;
	}
</style>
