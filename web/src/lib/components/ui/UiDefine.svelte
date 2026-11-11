<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	// Terms in plain English. The model's own definition, never presented as a
	// quote of a source, so there is no source line and no verification tick
	// (accent block, like `quote`). Also-names are short labels, not links.
	let { block }: { block: Extract<UiBlock, { kind: 'define' }> } = $props();
</script>

<div class="ui-define">
	{#each block.entries as e, i (i)}
		<div class="entry">
			<div class="term">{e.term}</div>
			<p class="means"><UiText text={e.means} /></p>
			{#if e.also.length}
				<div class="also">Also called {#each e.also as a, j (j)}<i>{a}</i>{/each}</div>
			{/if}
			{#if e.ex}
				<p class="ex"><UiText text={e.ex} /></p>
			{/if}
		</div>
	{/each}
</div>

<style>
	.ui-define {
		margin: var(--space-md) 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
		overflow: hidden;
	}

	.entry {
		padding: var(--space-md) var(--space-lg);
		border-top: 1px solid var(--color-border);
	}

	.entry:first-child {
		border-top: 0;
	}

	.term {
		font-family: var(--font-serif);
		font-size: 18px;
		line-height: 1.3;
		overflow-wrap: break-word;
	}

	.means {
		margin: 2px 0 0;
		font-size: 13.5px;
		overflow-wrap: break-word;
	}

	.also {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-xs);
		align-items: center;
		margin-top: 6px;
		font-size: 11.5px;
		color: var(--color-text-dim);
	}

	.also i {
		font-style: normal;
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-full);
		padding: 0 var(--space-sm);
	}

	.ex {
		margin: 6px 0 0;
		padding-left: 10px;
		border-left: 2px solid var(--color-border-strong);
		font-size: 12.5px;
		color: var(--color-text-dim);
		overflow-wrap: break-word;
	}
</style>
