<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { appState } from '$lib/state.svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { layoutStars } from '$lib/constellationLayout';
	import StarCard from '$lib/components/StarCard.svelte';
	import ConstellationSettingsModal from '$lib/components/ConstellationSettingsModal.svelte';
	import { PanelLeft, Search, Settings, Sparkles, Inbox as InboxIcon, Rows3, Star as StarIcon } from '@lucide/svelte';
	import type { Star } from '$lib/types';

	let view = $state<'library' | 'map'>('library');
	let showSettings = $state(false);

	onMount(() => {
		void constellationState.loadLibrary();
	});

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

	const byCategory = $derived.by(() => {
		const groups = new Map<string, Star[]>();
		for (const star of constellationState.libraryStars) {
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
		<h1 class="page-title">Constellation</h1>
		{#if view === 'map' && unconfirmedCount > 0}
			<span class="unconfirmed-pill">{unconfirmedCount} unconfirmed</span>
		{/if}
	</div>
	<div class="header-right">
		<button class="icon-btn" title="Search" aria-label="Search" disabled>
			<Search size={18} />
		</button>
		<button class="icon-btn" onclick={() => (showSettings = true)} title="Constellation settings">
			<Settings size={18} />
		</button>
	</div>
</header>

<div class="content" class:map-content={view === 'map'}>
	{#if view === 'library'}
		{#if !constellationState.libraryLoaded}
			<p class="empty">Loading your library…</p>
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

			{#if constellationState.aboutYouStars.length > 0}
				<details class="section" open>
					<summary class="section-header">About you</summary>
					<div class="card-list">
						{#each constellationState.aboutYouStars as star (star.id)}
							<StarCard {star} onclick={() => goto(`/constellation/star/${star.id}`)} />
						{/each}
					</div>
				</details>
			{/if}

			{#if constellationState.rejectedStars.length > 0}
				<details class="section">
					<summary class="section-header">Rejected</summary>
					<div class="card-list">
						{#each constellationState.rejectedStars as star (star.id)}
							<div class="rejected-row">
								<StarCard {star} onclick={() => goto(`/constellation/star/${star.id}`)} />
								<button
									class="btn restore-btn"
									onclick={() => constellationState.restoreStar(star.id)}
								>
									Restore
								</button>
							</div>
						{/each}
					</div>
				</details>
			{/if}
		{/if}
	{:else if !constellationState.mapData}
		<p class="empty">Loading the map…</p>
	{:else if mapLayout && mapLayout.nodes.length > 0}
		<div
			class="map-area"
			bind:clientWidth={mapAreaWidth}
			bind:clientHeight={mapAreaHeight}
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
							class:personal={a.star.is_personal || b.star.is_personal}
						/>
					{/if}
				{/each}
			</svg>
			{#each mapLayout.clusterLabels as cluster (cluster.category)}
				<div class="cluster-label" style="left: {cluster.x}px; top: {cluster.y}px;">
					{cluster.category}
				</div>
			{/each}
			{#each mapLayout.nodes as node (node.id)}
				<button
					class="map-node"
					class:personal={node.star.is_personal}
					style="left: {node.x}px; top: {node.y}px;"
					onclick={() => goto(`/constellation/star/${node.id}`)}
				>
					<span class="dot"></span>
					<span class="label">{node.star.title}</span>
				</button>
			{/each}
		</div>
	{:else}
		<p class="empty">Nothing to map yet.</p>
	{/if}
</div>

<div class="tabbar">
	<button class="tab" class:active={view === 'library'} onclick={() => (view = 'library')}>
		<Rows3 size={18} />
		<span>Library</span>
	</button>
	<button class="tab" class:active={view === 'map'} onclick={() => (view = 'map')}>
		<StarIcon size={18} />
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
		font-family: var(--font-serif);
		font-size: 20px;
		font-weight: 700;
	}
	.unconfirmed-pill {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-accent-2);
		background: color-mix(in srgb, var(--color-accent-2) 14%, transparent);
		border: 1px solid color-mix(in srgb, var(--color-accent-2) 30%, transparent);
		padding: 4px var(--space-sm);
		border-radius: var(--radius-full);
		white-space: nowrap;
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
		/* No more overflow/scroll — layoutStars' bounding-box normalize
		   (see constellationLayout.ts) sizes every node to fit exactly
		   within this container's own clientWidth/clientHeight, so there's
		   nothing left to scroll to find. */
		overflow: hidden;
	}
	.lines {
		position: absolute;
		inset: 0;
		width: 100%;
		height: 100%;
		pointer-events: none;
	}
	.lines line {
		stroke: var(--color-accent-2);
		stroke-width: 1;
		opacity: 0.35;
	}
	.lines line.personal {
		stroke: var(--color-personal);
	}
	.cluster-label {
		position: absolute;
		transform: translate(-50%, -50%);
		font-size: 10px;
		font-weight: 600;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		opacity: 0.6;
		pointer-events: none;
		white-space: nowrap;
	}
	.map-node {
		position: absolute;
		transform: translate(-50%, -50%);
		display: flex;
		align-items: center;
		gap: 7px;
		background: none;
		border: none;
		padding: 4px;
		cursor: pointer;
		font: inherit;
	}
	.map-node .dot {
		width: 11px;
		height: 11px;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		box-shadow: 0 0 10px 2px color-mix(in srgb, var(--color-accent) 45%, transparent);
		flex-shrink: 0;
	}
	.map-node.personal .dot {
		background: var(--color-personal);
		box-shadow: 0 0 10px 2px color-mix(in srgb, var(--color-personal) 45%, transparent);
	}
	.map-node .label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text);
		white-space: nowrap;
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
