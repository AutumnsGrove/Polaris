<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { appState } from '$lib/state.svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { marked } from '$lib/markdown';
	import DOMPurify from 'dompurify';
	import ConstellationMiniMap from '$lib/components/ConstellationMiniMap.svelte';
	import ConstellationReconcileSheet from '$lib/components/ConstellationReconcileSheet.svelte';
	import { ArrowLeft, MoreVertical, MessageCircle, Pencil, Link2 } from '@lucide/svelte';
	import { iconForCategory } from '$lib/categoryIcons';
	import type { ConstellationStarDetail, Star, StarEdge } from '$lib/types';

	const starId = $derived(Number(page.params.id));

	let detail = $state<ConstellationStarDetail | null>(null);
	let loading = $state(true);
	let neighborStars = $state<Star[]>([]);
	let showEdit = $state(false);
	let showMenu = $state(false);
	let renaming = $state(false);
	let renameValue = $state('');

	async function load(id: number) {
		loading = true;
		detail = await constellationState.loadStarDetail(id);
		loading = false;
		if (!detail) return;
		// Neighbor stars for the mini-map — bounded to the first 3 edges,
		// fetched directly rather than pulling the whole map dataset for a
		// 3-node preview (see ConstellationMiniMap's own doc comment).
		const toFetch = detail.edges.slice(0, 3);
		const results = await Promise.all(toFetch.map((e) => constellationState.loadStarDetail(e.other_star_id)));
		neighborStars = results.filter((r): r is ConstellationStarDetail => r !== null).map((r) => r.star);
	}

	// $effect (not onMount) so navigating from one star's detail page to
	// another (e.g. clicking a mini-map neighbor) re-triggers the load —
	// SvelteKit reuses this component across param changes on the same
	// route, so onMount alone would only fire once.
	$effect(() => {
		void load(starId);
	});

	const Icon = $derived(iconForCategory(detail?.star.category ?? ''));

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
		renameValue = detail.star.title;
		renaming = true;
		showMenu = false;
	}

	async function saveRename() {
		if (!detail || !renameValue.trim()) return;
		const result = await constellationState.patchStar(detail.star.id, { title: renameValue.trim() });
		if (result.star) detail = { ...detail, star: result.star };
		renaming = false;
	}

	async function toggleDisabled() {
		if (!detail) return;
		const result = await constellationState.patchStar(detail.star.id, { disabled: !detail.star.disabled });
		if (result.star) detail = { ...detail, star: result.star };
		showMenu = false;
	}
</script>

<svelte:head>
	<title>{detail?.star.title ?? 'Star'} — Constellation</title>
</svelte:head>

<header class="header">
	<button class="icon-btn" onclick={() => goto('/constellation')} aria-label="Back">
		<ArrowLeft size={18} />
	</button>
	<span class="crumb">{detail?.star.category ?? ''}</span>
	<div class="menu-wrap">
		<button class="icon-btn" onclick={() => (showMenu = !showMenu)} aria-label="More">
			<MoreVertical size={16} />
		</button>
		{#if showMenu}
			<div class="menu">
				<button onclick={startRename}>Rename</button>
				<button onclick={toggleDisabled}>{detail?.star.disabled ? 'Enable' : 'Disable'}</button>
			</div>
		{/if}
	</div>
</header>

<div class="content">
	{#if loading}
		<p class="empty">Loading…</p>
	{:else if !detail}
		<p class="empty">Couldn't find that star.</p>
	{:else}
		<div class="tile-row">
			<div class="tile" class:personal={detail.star.is_personal}>
				<Icon size={18} />
			</div>
			<div>
				<span class="badge">{detail.star.status === 'auto' ? 'Auto' : detail.star.status}</span>
				{#if detail.star.confidence}
					<div class="confidence">{detail.star.confidence}</div>
				{/if}
			</div>
		</div>

		{#if renaming}
			<form
				class="rename-form"
				onsubmit={(e) => {
					e.preventDefault();
					void saveRename();
				}}
			>
				<input type="text" bind:value={renameValue} autofocus />
				<button type="submit" class="btn btn-accent">Save</button>
				<button type="button" class="btn" onclick={() => (renaming = false)}>Cancel</button>
			</form>
		{:else}
			<h1>{detail.star.title}</h1>
		{/if}

		<div class="tags-row">
			{#each detail.star.tags as tag (tag)}
				<span class="tag">{tag}</span>
			{/each}
		</div>
		<div class="meta-row">
			Updated {formatDate(detail.star.updated_at)}
			{#if detail.first_discussed_at}
				&middot; first noted {formatDate(detail.first_discussed_at)}
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
		z-index: var(--z-dropdown, 20);
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
	.empty {
		max-width: 46ch;
		margin: var(--space-2xl) auto;
		text-align: center;
		font-size: 13.5px;
		color: var(--color-text-dim);
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
		background: var(--color-accent-soft);
		color: var(--color-accent);
		flex-shrink: 0;
	}
	.tile.personal {
		background: color-mix(in srgb, var(--color-personal) 16%, transparent);
		color: var(--color-personal);
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
	.rename-form {
		display: flex;
		gap: var(--space-sm);
		margin-bottom: var(--space-md);
	}
	.rename-form input {
		flex: 1;
		font: inherit;
		font-size: 18px;
		padding: var(--space-xs) var(--space-sm);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
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
		border-radius: 8px;
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
