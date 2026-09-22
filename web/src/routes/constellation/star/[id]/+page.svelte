<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { appState } from '$lib/state.svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { marked } from '$lib/markdown';
	import DOMPurify from 'dompurify';
	import ConstellationMiniMap from '$lib/components/ConstellationMiniMap.svelte';
	import ConstellationReconcileSheet from '$lib/components/ConstellationReconcileSheet.svelte';
	import EditTextModal from '$lib/components/EditTextModal.svelte';
	import StarVersionHistoryModal from '$lib/components/StarVersionHistoryModal.svelte';
	import { ArrowLeft, MoreVertical, MessageCircle, Pencil, Link2 } from '@lucide/svelte';
	import { iconForCategory } from '$lib/categoryIcons';
	import { colorForCategory } from '$lib/categoryColors';
	import type { ConstellationStarDetail, Star, StarEdge } from '$lib/types';

	const starId = $derived(Number(page.params.id));

	let detail = $state<ConstellationStarDetail | null>(null);
	let loading = $state(true);
	let neighborStars = $state<Star[]>([]);
	// versionCount: how many times this star has been content-merged, for
	// the "Update N" badge in meta-row — a plain count of star_versions
	// rows, not fetched via the full history modal (which loads its own
	// copy on open) so the badge can show without the modal ever opening.
	let versionCount = $state(0);
	let showEdit = $state(false);
	let showMenu = $state(false);
	let renaming = $state(false);
	let showHistory = $state(false);
	let menuRootEl = $state<HTMLDivElement | null>(null);

	// loadSeq guards against a stale response clobbering a newer one — this
	// component instance is reused across param changes (see the $effect
	// below), so clicking through two mini-map neighbors quickly can leave
	// two loadStarDetail calls in flight at once; if the older one resolves
	// after the newer one, only the request whose sequence number is still
	// current is allowed to write into `detail`.
	let loadSeq = 0;

	async function load(id: number) {
		const seq = ++loadSeq;
		loading = true;
		const result = await constellationState.loadStarDetail(id);
		if (seq !== loadSeq) return;
		detail = result;
		loading = false;
		if (!detail) return;
		versionCount = (await constellationState.getStarVersions(id)).length;
		if (seq !== loadSeq) return;
		// Neighbor stars for the mini-map — every edge, fetched directly
		// rather than pulling the whole map dataset for what's usually a
		// small preview (see ConstellationMiniMap's own doc comment).
		// ConstellationMiniMap itself collapses the list past a handful of
		// rows; no cap belongs here, since a star with dozens of edges
		// should still be able to show all of them once expanded.
		const results = await Promise.all(
			detail.edges.map((e) => constellationState.loadStarDetail(e.other_star_id))
		);
		if (seq !== loadSeq) return;
		neighborStars = results.filter((r): r is ConstellationStarDetail => r !== null).map((r) => r.star);
	}

	// $effect (not onMount) so navigating from one star's detail page to
	// another (e.g. clicking a mini-map neighbor) re-triggers the load —
	// SvelteKit reuses this component across param changes on the same
	// route, so onMount alone would only fire once.
	$effect(() => {
		void load(starId);
	});

	// Click-outside-to-close, same pattern as ThreadMenu.svelte's
	// handleWindowClick — without it, the overflow menu never closed on an
	// outside tap at all.
	function handleWindowClick(e: MouseEvent) {
		if (showMenu && menuRootEl && !menuRootEl.contains(e.target as Node)) showMenu = false;
	}

	const Icon = $derived(iconForCategory(detail?.star.category ?? ''));
	const starColor = $derived(colorForCategory(detail?.star.category ?? ''));

	function renderBody(body: string): string {
		return DOMPurify.sanitize(marked.parse(body || '') as string);
	}

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
	}

	async function continueInChat() {
		if (!detail) return;
		const seed = `Tell me more about "${detail.star.title}": ${detail.star.summary}`;
		appState.newThread();
		await goto('/');
		appState.send(seed, undefined, undefined, undefined, undefined, undefined, 'constellation', detail.star.title);
	}

	async function submitEdit(text: string) {
		if (!detail) return { error: 'Star not loaded.' };
		const result = await constellationState.editStar(detail.star.id, text);
		if (result.star) detail = { ...detail, star: result.star };
		return { error: result.error };
	}

	function startRename() {
		if (!detail) return;
		renaming = true;
		showMenu = false;
	}

	async function saveRename(newTitle: string) {
		if (!detail) return;
		const result = await constellationState.patchStar(detail.star.id, { title: newTitle });
		if (result.star) {
			detail = { ...detail, star: result.star };
			renaming = false;
		} else {
			// Left open (not closed) on failure — same reasoning as
			// ThreadMenu.svelte's regenerateTitle — so the person can see
			// what they typed and retry instead of the modal just vanishing
			// with no explanation and the title silently reverting.
			appState.showToast(result.error || "Couldn't rename that star");
		}
	}

	function openHistory() {
		showMenu = false;
		showHistory = true;
	}

	function handleReverted(star: Star) {
		if (!detail) return;
		detail = { ...detail, star };
		versionCount += 1;
	}

	async function toggleDisabled() {
		if (!detail) return;
		const result = await constellationState.patchStar(detail.star.id, { disabled: !detail.star.disabled });
		if (result.star) {
			detail = { ...detail, star: result.star };
		} else {
			appState.showToast(result.error || "Couldn't update that star");
		}
		showMenu = false;
	}
</script>

<svelte:head>
	<title>{detail?.star.title ?? 'Star'} — Constellation</title>
</svelte:head>

<svelte:window onclick={handleWindowClick} />

<header class="header">
	<button class="icon-btn" onclick={() => goto('/constellation')} aria-label="Back">
		<ArrowLeft size={18} />
	</button>
	<span class="crumb">{detail?.star.category ?? ''}</span>
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="menu-wrap" bind:this={menuRootEl} onclick={(e) => e.stopPropagation()}>
		<button
			class="icon-btn"
			onclick={() => (showMenu = !showMenu)}
			aria-label="More"
			aria-haspopup="menu"
			aria-expanded={showMenu}
		>
			<MoreVertical size={16} />
		</button>
		{#if showMenu}
			<div class="menu" role="menu">
				<button onclick={startRename} role="menuitem">Rename</button>
				<button onclick={openHistory} role="menuitem">See version history</button>
				<button onclick={toggleDisabled} role="menuitem"
					>{detail?.star.disabled ? 'Enable' : 'Disable'}</button
				>
			</div>
		{/if}
	</div>
</header>

<div class="content">
	{#if loading}
		<p class="constellation-empty">Loading…</p>
	{:else if !detail}
		<p class="constellation-empty">Couldn't find that star.</p>
	{:else}
		<div class="tile-row">
			<div class="tile" style="--star-color: {starColor}">
				<Icon size={18} />
			</div>
			<div>
				<span class="badge">{detail.star.status === 'auto' ? 'Auto' : detail.star.status}</span>
				{#if detail.star.confidence}
					<div class="confidence">{detail.star.confidence}</div>
				{/if}
			</div>
		</div>

		<h1>{detail.star.title}</h1>

		<div class="tags-row">
			{#each detail.star.tags as tag (tag)}
				<span class="tag">{tag}</span>
			{/each}
		</div>
		<div class="meta-row">
			Star #{detail.star.id} &middot; Updated {formatDate(detail.star.updated_at)}
			{#if detail.first_discussed_at}
				&middot; first noted {formatDate(detail.first_discussed_at)}
			{/if}
			{#if versionCount > 0}
				&middot; Update {versionCount}
			{/if}
		</div>

		<!-- eslint-disable-next-line svelte/no-at-html-tags -->
		<div class="star-body">{@html renderBody(detail.star.body)}</div>

		{#if detail.sources.length > 0}
			<div class="block-label">Linked articles</div>
			<div class="chip-row">
				{#each detail.sources as source (source.thread_id)}
					<button class="source-chip" onclick={() => goto(`/t/${source.thread_id}`)}>
						<span class="icon-tile"><Link2 size={13} /></span>
						"{constellationState.resolveSourceTitle(source.thread_id)}" &mdash; {formatDate(
							source.thread_created_at
						)}
					</button>
				{/each}
			</div>
		{/if}

		{#if detail.edges.length > 0 && neighborStars.length > 0}
			<div class="block-label">Nearby in the constellation</div>
			<ConstellationMiniMap centerStar={detail.star} {neighborStars} edges={detail.edges} />
		{/if}

		<div class="bottom-bar">
			<div class="bottom-actions">
				<button class="continue-btn" onclick={continueInChat}>
					<MessageCircle size={16} />
					Continue in chat
				</button>
				<button class="edit-btn" onclick={() => (showEdit = true)} aria-label="Edit star">
					<Pencil size={17} />
				</button>
			</div>
		</div>
	{/if}
</div>

{#if showEdit && detail}
	<ConstellationReconcileSheet
		mode="edit"
		starTitle={detail.star.title}
		onSubmit={submitEdit}
		onClose={() => (showEdit = false)}
	/>
{/if}

{#if renaming && detail}
	<EditTextModal
		heading="Rename star"
		initialValue={detail.star.title}
		placeholder="Star title"
		maxLength={200}
		onSave={saveRename}
		onCancel={() => (renaming = false)}
	/>
{/if}

{#if showHistory && detail}
	<StarVersionHistoryModal
		star={detail.star}
		onClose={() => (showHistory = false)}
		onReverted={handleReverted}
	/>
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: max(var(--space-lg), env(safe-area-inset-top)) var(--space-md) var(--space-md);
		box-shadow: var(--shadow-well);
	}
	.crumb {
		font-size: 12px;
		color: var(--color-text-dim);
		text-transform: uppercase;
		letter-spacing: 0.06em;
		font-weight: 600;
	}
	.menu-wrap {
		position: relative;
	}
	.menu {
		position: absolute;
		top: 100%;
		right: 0;
		margin-top: var(--space-xs);
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-md);
		display: flex;
		flex-direction: column;
		min-width: 140px;
		z-index: var(--z-dropdown);
		overflow: hidden;
	}
	.menu button {
		padding: var(--space-sm) var(--space-md);
		text-align: left;
		background: none;
		border: none;
		font: inherit;
		font-size: 13px;
		color: var(--color-text);
		cursor: pointer;
	}
	.menu button:hover {
		background: var(--color-surface-2);
	}

	.content {
		flex: 1;
		overflow-y: auto;
		padding: var(--space-xs) var(--space-lg) 140px;
	}
	.tile-row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		margin-bottom: var(--space-lg);
	}
	.tile {
		width: 40px;
		height: 40px;
		border-radius: var(--radius-md);
		display: flex;
		align-items: center;
		justify-content: center;
		background: color-mix(in srgb, var(--star-color) 16%, transparent);
		color: var(--star-color);
		flex-shrink: 0;
	}
	.badge {
		display: inline-flex;
		font-size: 11px;
		font-weight: 600;
		padding: 3px var(--space-md);
		border-radius: var(--radius-full);
		background: var(--color-accent-soft);
		color: var(--color-accent);
		text-transform: capitalize;
	}
	.confidence {
		font-size: 11px;
		color: var(--color-text-dim);
		margin-top: 3px;
	}

	h1 {
		font-family: var(--font-serif);
		font-size: 26px;
		font-weight: 500;
		line-height: 1.2;
		margin: 0 0 var(--space-md);
	}
	.tags-row {
		display: flex;
		gap: var(--space-xs);
		flex-wrap: wrap;
		margin-bottom: var(--space-md);
	}
	.tag {
		font-size: 11px;
		padding: 3px var(--space-md);
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		color: var(--color-text-dim);
	}
	.meta-row {
		font-size: 12px;
		color: var(--color-text-dim);
		padding-bottom: var(--space-lg);
		border-bottom: 1px solid var(--color-border);
		margin-bottom: var(--space-lg);
	}

	.star-body :global(h1),
	.star-body :global(h2),
	.star-body :global(h3) {
		font-family: var(--font-serif);
		font-size: 17px;
		font-weight: 500;
		margin: var(--space-xl) 0 var(--space-sm);
	}
	.star-body :global(h1:first-child),
	.star-body :global(h2:first-child),
	.star-body :global(h3:first-child) {
		margin-top: 0;
	}
	.star-body :global(p) {
		font-size: 14px;
		line-height: 1.6;
		margin: 0 0 var(--space-md);
	}

	.block-label {
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		margin: var(--space-xl) 0 var(--space-md);
	}
	.chip-row {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}
	.source-chip {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		border-radius: var(--radius-md);
		background: var(--color-surface);
		border: 1px solid var(--color-border);
		font-size: 13px;
		width: 100%;
		text-align: left;
		font: inherit;
		color: inherit;
		cursor: pointer;
	}
	.icon-tile {
		width: 26px;
		height: 26px;
		border-radius: var(--radius-sm);
		flex-shrink: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--color-surface-3);
		color: var(--color-text-dim);
	}

	.bottom-bar {
		position: fixed;
		left: 0;
		right: 0;
		bottom: 0;
		padding: var(--space-lg) var(--space-lg) max(var(--space-lg), env(safe-area-inset-bottom));
		background: linear-gradient(to top, var(--color-bg) 60%, transparent);
	}
	.bottom-actions {
		display: flex;
		gap: var(--space-md);
		max-width: 640px;
		margin: 0 auto;
	}
	.continue-btn {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-sm);
		padding: var(--space-md);
		border-radius: var(--radius-full);
		background: var(--color-accent);
		color: var(--color-bg);
		font-size: 14px;
		font-weight: 600;
		border: none;
		cursor: pointer;
		font: inherit;
	}
	.edit-btn {
		flex-shrink: 0;
		width: 46px;
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: var(--radius-full);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border-strong);
		color: var(--color-text);
		cursor: pointer;
	}
</style>
