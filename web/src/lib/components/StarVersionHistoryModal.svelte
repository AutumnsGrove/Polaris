<script lang="ts">
	import { X, ChevronLeft, RotateCcw } from '@lucide/svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { appState } from '$lib/state.svelte';
	import { marked } from '$lib/markdown';
	import DOMPurify from 'dompurify';
	import type { Star, StarVersion } from '$lib/types';

	// Same render path the star detail page's own body uses (see
	// +page.svelte's renderBody) — a version preview that only showed the
	// one-line summary gave no real way to compare two versions' actual
	// content before reverting.
	function renderBody(body: string): string {
		return DOMPurify.sanitize(marked.parse(body || '') as string);
	}

	// "See version history" — issue #100. Two screens in one modal (a list,
	// and a tapped-into preview) rather than two separate modals: a version
	// history is inherently a drill-down, and stacking two modal-backdrops
	// would double the dimming and require its own back-stack handling for
	// no real benefit over a simple in-component screen toggle.
	let {
		star,
		onClose,
		onReverted
	}: {
		star: Star;
		onClose: () => void;
		onReverted: (star: Star) => void;
	} = $props();

	let versions = $state<StarVersion[]>([]);
	let loading = $state(true);
	let selected = $state<StarVersion | null>(null);
	let reverting = $state(false);

	$effect(() => {
		void load();
	});

	async function load() {
		loading = true;
		versions = await constellationState.getStarVersions(star.id);
		loading = false;
	}

	function formatDateTime(iso: string): string {
		return new Date(iso).toLocaleString('en-US', {
			month: 'short',
			day: 'numeric',
			hour: 'numeric',
			minute: '2-digit'
		});
	}

	// Labels name who/what made the change that this snapshot predates —
	// "Update 3" reads as "the star's content going into its 3rd update",
	// matching store.StarVersion's own doc comment on version_number.
	const sourceLabels: Record<StarVersion['source'], string> = {
		weaver: 'Weaver',
		manual_edit: 'Edit star',
		refine: 'Refine',
		revert: 'Reverted'
	};

	async function revert(version: StarVersion) {
		reverting = true;
		const result = await constellationState.revertStar(star.id, version.version_number);
		reverting = false;
		if (result.star) {
			onReverted(result.star);
			onClose();
		} else {
			appState.showToast(result.error || "Couldn't revert that star");
		}
	}
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Version history">
		<div class="modal-panel-header">
			{#if selected}
				<button class="icon-btn" onclick={() => (selected = null)} aria-label="Back to list">
					<ChevronLeft size={18} />
				</button>
			{/if}
			<h2>{selected ? `Update ${selected.version_number}` : 'Version history'}</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		{#if selected}
			<div class="version-meta">
				{sourceLabels[selected.source]} &middot; {formatDateTime(selected.created_at)}
			</div>
			<div class="version-preview">
				<h3>{selected.title || star.title}</h3>
				<p class="version-summary">{selected.summary}</p>
				<!-- eslint-disable-next-line svelte/no-at-html-tags -->
				<div class="version-body">{@html renderBody(selected.body)}</div>
			</div>
			<div class="modal-actions">
				<button class="btn" onclick={() => (selected = null)}>Back</button>
				<button class="btn btn-accent" onclick={() => revert(selected!)} disabled={reverting}>
					<RotateCcw size={15} />
					{reverting ? 'Reverting…' : `Revert star to Update ${selected.version_number}`}
				</button>
			</div>
		{:else if loading}
			<p class="version-empty">Loading…</p>
		{:else if versions.length === 0}
			<p class="version-empty">No earlier versions — this star hasn't been updated yet.</p>
		{:else}
			<div class="version-list">
				{#each versions as version (version.id)}
					<button class="version-row" onclick={() => (selected = version)}>
						<span class="version-number">Update {version.version_number}</span>
						<span class="version-row-meta">
							{sourceLabels[version.source]} &middot; {formatDateTime(version.created_at)}
						</span>
					</button>
				{/each}
			</div>
		{/if}
	</div>
</div>

<style>
	.version-empty {
		color: var(--color-text-dim);
		font-size: 13px;
		padding: var(--space-lg) 0;
	}
	.version-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		max-height: 50vh;
		overflow-y: auto;
	}
	.version-row {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 2px;
		width: 100%;
		text-align: left;
		padding: var(--space-md);
		border-radius: var(--radius-md);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		font: inherit;
		color: var(--color-text);
		cursor: pointer;
	}
	.version-row:hover {
		background: var(--color-surface-3);
	}
	.version-number {
		font-size: 13px;
		font-weight: 600;
	}
	.version-row-meta {
		font-size: 12px;
		color: var(--color-text-dim);
	}
	.version-meta {
		font-size: 12px;
		color: var(--color-text-dim);
		margin-bottom: var(--space-md);
	}
	.version-preview {
		max-height: 50vh;
		overflow-y: auto;
	}
	.version-preview h3 {
		font-family: var(--font-serif);
		font-size: 17px;
		font-weight: 500;
		margin: 0 0 var(--space-sm);
	}
	.version-summary {
		font-size: 14px;
		line-height: 1.6;
		font-style: italic;
		color: var(--color-text-dim);
	}
	/* Same recipe as +page.svelte's .star-body — a version preview should
	   read like the real star body, not a stripped-down summary card. */
	.version-body :global(h1),
	.version-body :global(h2),
	.version-body :global(h3) {
		font-family: var(--font-serif);
		font-size: 15px;
		font-weight: 500;
		margin: var(--space-lg) 0 var(--space-sm);
	}
	.version-body :global(h1:first-child),
	.version-body :global(h2:first-child),
	.version-body :global(h3:first-child) {
		margin-top: var(--space-md);
	}
	.version-body :global(p) {
		font-size: 14px;
		line-height: 1.6;
		margin: 0 0 var(--space-md);
	}
</style>
