<script lang="ts">
	import { ChevronRight, Info, CheckCheck } from '@lucide/svelte';
	import type { Citation } from '$lib/types';
	import { sourceHostname as hostname } from '$lib/citations';

	let { citations }: { citations: Citation[] } = $props();

	// Sources start collapsed — a 15-result answer was burying the actual
	// answer under a wall of full-width pills. Count-only toggle up front,
	// full list is one click away for anyone who wants to skim every
	// source at once; inline citation chips (see renderInlineCitations)
	// already open a source directly, so this isn't the only way in.
	let sourcesOpen = $state(false);
</script>

<div class="sources">
	<button class="sources-toggle" onclick={() => (sourcesOpen = !sourcesOpen)}>
		<span class="sources-count">{citations.length}</span>
		<span>{citations.length === 1 ? 'Source' : 'Sources'}</span>
		<ChevronRight size={12} class={sourcesOpen ? 'chevron open' : 'chevron'} />
	</button>
	<span
		class="sources-info"
		title="A check mark means that specific claim was checked against its source — absence doesn't mean the source is wrong, just not (yet) checked."
	>
		<Info size={12} />
	</span>
	{#if sourcesOpen}
		<div class="citations">
			{#each citations as c, i (c.url)}
				<a
					class="source-chip"
					href={c.url}
					target="_blank"
					rel="noreferrer"
					title={c.verified ? `${c.title || c.url} — found in source` : c.title || c.url}
				>
					{#if c.image_url}
						<img class="source-thumb" src={c.image_url} alt="" loading="lazy" />
					{:else}
						<span class="source-index">{i + 1}</span>
					{/if}
					<span class="source-text">
						<span class="source-title">
							{#if c.verified}
								<CheckCheck size={11} class="source-verified-icon" />
							{/if}
							<span class="source-title-text">{c.title || hostname(c.url)}</span>
						</span>
						<span class="source-domain">{hostname(c.url)}</span>
					</span>
				</a>
			{/each}
		</div>
	{/if}
</div>

<style>
	.sources {
		margin-top: var(--space-md);
	}

	.sources-toggle {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		border: none;
		background: transparent;
		padding: var(--space-xs) 0;
		font-size: 12px;
		color: var(--color-text-dim);
		transition: color 0.15s var(--ease-out-expo);
	}

	.sources-toggle:hover {
		color: var(--color-text);
	}

	/* Explains what an absent check mark does and doesn't mean — see
	   docs/plans/source-verification-badge.md's UI section: "no mark"
	   covers three different real states (not checked, not supported,
	   below confidence threshold), so it should never read as "this
	   source is bad." cursor: help, not pointer — this is a tooltip
	   target, not a click target. */
	.sources-info {
		display: inline-flex;
		align-items: center;
		color: var(--color-text-dim);
		cursor: help;
	}

	.sources-count {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 16px;
		height: 16px;
		padding: 0 var(--space-xs);
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		font-size: 10px;
		font-variant-numeric: tabular-nums;
		color: var(--color-text-dim);
	}

	.citations {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
	}

	/* Fixed max-width + ellipsis is the whole fix — a 90-character arXiv
	   title no longer forces its own pill to the width of the page. Index
	   badge gives a stable visual anchor since these aren't referenced by
	   number anywhere else in the answer text (the model just hyperlinks
	   inline); it's a scan aid, not a citation marker. */
	.source-chip {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		max-width: 220px;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: var(--space-xs) var(--space-sm);
		text-decoration: none;
		box-shadow: var(--shadow-xs);
		transition: background-color 0.15s var(--ease-out-expo), box-shadow 0.15s var(--ease-out-expo);
	}

	.source-chip:hover {
		background: var(--color-surface-3);
		box-shadow: var(--shadow-sm);
	}

	.source-index {
		flex-shrink: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		width: 15px;
		height: 15px;
		border-radius: 50%;
		background: color-mix(in srgb, var(--color-accent-2) 20%, transparent);
		color: var(--color-accent-2);
		font-size: 9.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
	}

	/* Takes over from .source-index whenever a citation carries a real
	   thumbnail (see tools/registry.go's Citation.ImageURL) — a slightly
	   rounded square (not a circle) since it's showing real art (album
	   covers, etc.), not an abstract badge. Kept small and compressed on
	   purpose, per the same "calm over clever" brief every other bit of
	   chrome in this app follows — it should read as a recognizable
	   thumbnail at a glance, not a decorative hero image. */
	.source-thumb {
		flex-shrink: 0;
		width: 28px;
		height: 28px;
		border-radius: var(--radius-sm);
		object-fit: cover;
		box-shadow: var(--shadow-xs);
	}

	.source-text {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.source-title {
		display: flex;
		align-items: center;
		gap: 3px;
		min-width: 0;
		font-size: 12px;
		color: var(--color-text);
	}

	.source-title-text {
		min-width: 0;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	/* The source-list chip's own aggregate "found in source" mark — see
	   ChatTurnView.svelte's .source-chip loop and Citation.verified's doc
	   comment. Same --color-accent-2 treatment as the inline chip's own
	   mark above, just via a real Svelte icon component here instead of
	   raw SVG (this markup isn't DOM-string-injected like the inline
	   chips are). */
	.source-title :global(.source-verified-icon) {
		flex-shrink: 0;
		color: var(--color-accent-2);
	}

	.source-domain {
		font-size: 10px;
		color: var(--color-text-dim);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
