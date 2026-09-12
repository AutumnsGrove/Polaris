<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { constellationState } from '$lib/constellation.svelte';
	import ConstellationReconcileSheet from '$lib/components/ConstellationReconcileSheet.svelte';
	import { ArrowLeft, Check, Pencil, X } from '@lucide/svelte';
	import { iconForCategory } from '$lib/categoryIcons';
	import { colorForCategory } from '$lib/categoryColors';
	import type { ConstellationStarDetail } from '$lib/types';

	const starId = $derived(Number(page.params.id));

	let detail = $state<ConstellationStarDetail | null>(null);
	let loading = $state(true);
	let showRefine = $state(false);
	let acting = $state(false);
	let error = $state('');

	async function load(id: number) {
		loading = true;
		detail = await constellationState.loadStarDetail(id);
		loading = false;
	}
	$effect(() => {
		void load(starId);
	});

	const Icon = $derived(iconForCategory(detail?.star.category ?? ''));
	const starColor = $derived(colorForCategory(detail?.star.category ?? ''));

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
	}

	async function act(action: 'approve' | 'discard') {
		if (!detail || acting) return;
		acting = true;
		error = '';
		const result = await constellationState.reviewStar(detail.star.id, action);
		acting = false;
		if (result.error) {
			error = result.error;
			return;
		}
		await goto('/constellation/inbox');
	}

	async function submitRefine(text: string) {
		if (!detail) return { error: 'Star not loaded.' };
		const result = await constellationState.reviewStar(detail.star.id, 'refine', text);
		// Refine deliberately stays on this screen instead of navigating back
		// to the inbox list (unlike approve/discard in act() above) — the
		// whole point is to let the person see what changed and keep
		// refining or decide to approve/discard from here, not get swept
		// back to the list before they've even seen the revision. Refetches
		// directly rather than calling load(), which would flip `loading`
		// back to true and flash the whole view to "Loading…" for what
		// should read as an in-place update.
		if (!result.error) detail = await constellationState.loadStarDetail(detail.star.id);
		return { error: result.error };
	}
</script>

<svelte:head>
	<title>Review — Constellation</title>
</svelte:head>

<header class="header">
	<button class="icon-btn" onclick={() => goto('/constellation/inbox')} aria-label="Back">
		<ArrowLeft size={18} />
	</button>
	<span class="crumb">Inbox</span>
	<div class="spacer"></div>
</header>

<div class="content">
	{#if loading}
		<p class="empty">Loading…</p>
	{:else if !detail}
		<p class="empty">Couldn't find that star.</p>
	{:else}
		<div class="tile-row">
			<div class="tile" style="--star-color: {starColor}">
				<Icon size={18} />
			</div>
			<div>
				<span class="badge">Proposed</span>
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
			First noted {formatDate(detail.star.created_at)}
			{#if detail.sources.length > 0}
				&middot; from {detail.sources.length} thread{detail.sources.length === 1 ? '' : 's'}
			{/if}
		</div>

		{#if detail.reasoning}
			<div class="why-block">
				<div class="why-label">Why this needs a look</div>
				<div class="why-text">&ldquo;{detail.reasoning}&rdquo;</div>
			</div>
		{/if}

		<div class="review-body">
			<h2>What Weaver drafted</h2>
			<p>{detail.star.body || detail.star.summary}</p>
		</div>

		{#if error}
			<p class="error-text">{error}</p>
		{/if}
	{/if}
</div>

{#if detail}
	<div class="bottom-bar">
		<button class="approve-btn" onclick={() => act('approve')} disabled={acting}>
			<Check size={16} />
			Approve
		</button>
		<div class="secondary-row">
			<button class="refine-btn" onclick={() => (showRefine = true)} disabled={acting}>
				<Pencil size={14} />
				Refine
			</button>
			<button class="discard-btn" onclick={() => act('discard')} disabled={acting}>
				<X size={14} />
				Discard
			</button>
		</div>
	</div>
{/if}

{#if showRefine && detail}
	<ConstellationReconcileSheet
		mode="refine"
		starTitle={detail.star.title}
		onSubmit={submitRefine}
		onClose={() => (showRefine = false)}
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
	.spacer {
		width: 36px;
	}

	.content {
		flex: 1;
		overflow-y: auto;
		padding: var(--space-xs) var(--space-lg) 160px;
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
		background: color-mix(in srgb, var(--star-color) 15%, transparent);
		color: var(--star-color);
		flex-shrink: 0;
	}
	.badge {
		display: inline-flex;
		font-size: 11px;
		font-weight: 600;
		padding: 3px var(--space-md);
		border-radius: var(--radius-full);
		background: color-mix(in srgb, var(--color-accent-2) 16%, transparent);
		color: var(--color-accent-2);
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

	.why-block {
		border-radius: var(--radius-lg);
		background: color-mix(in srgb, var(--color-accent-2) 9%, var(--color-surface));
		border: 1px solid color-mix(in srgb, var(--color-accent-2) 26%, var(--color-border));
		padding: var(--space-md) var(--space-lg);
		margin: var(--space-xs) 0 var(--space-xl);
	}
	.why-label {
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-accent-2);
		margin-bottom: var(--space-sm);
	}
	.why-text {
		font-size: 13px;
		line-height: 1.55;
		color: var(--color-text-dim);
		font-style: italic;
	}

	.review-body h2 {
		font-family: var(--font-serif);
		font-size: 17px;
		font-weight: 500;
		margin: 0 0 var(--space-sm);
	}
	.review-body p {
		font-size: 14px;
		line-height: 1.6;
		margin: 0;
	}
	.error-text {
		margin-top: var(--space-lg);
		font-size: 12.5px;
		color: var(--color-danger);
	}

	.bottom-bar {
		position: fixed;
		left: 0;
		right: 0;
		bottom: 0;
		padding: var(--space-lg) var(--space-lg) max(var(--space-lg), env(safe-area-inset-bottom));
		background: linear-gradient(to top, var(--color-bg) 68%, transparent);
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		max-width: 640px;
		margin: 0 auto;
	}
	.approve-btn {
		width: 100%;
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
	.approve-btn:disabled,
	.refine-btn:disabled,
	.discard-btn:disabled {
		opacity: 0.6;
		cursor: default;
	}
	.secondary-row {
		display: flex;
		gap: var(--space-sm);
	}
	.refine-btn,
	.discard-btn {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-xs);
		padding: var(--space-sm);
		border-radius: var(--radius-full);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border-strong);
		color: var(--color-text);
		font-size: 13px;
		font-weight: 500;
		font: inherit;
		cursor: pointer;
	}
	.discard-btn {
		color: var(--color-text-dim);
	}
</style>
