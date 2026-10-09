<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	let { block, loc }: { block: Extract<UiBlock, { kind: 'quote' }>; loc?: string } = $props();
</script>

<figure class="ui-quote">
	<blockquote><UiText text={block.text} /></blockquote>
	{#if block.by || block.src.length}
		<figcaption>
			{#if block.by}<span class="by">— <UiText text={block.by} /></span>{/if}
			<UiSources src={block.src} loc="{loc}.0.src" />
		</figcaption>
	{/if}
</figure>

<style>
	.ui-quote {
		margin: var(--space-md) 0;
		padding: var(--space-xs) 0 var(--space-xs) var(--space-lg);
		border-left: 2px solid var(--color-accent);
	}

	/* A pull-quote, not the prose blockquote: serif, larger, no tint. The
	   `.ui-quote blockquote` selector (two classes' worth) is there to outrank
	   ChatTurnView's `.prose :global(blockquote)` padding and border. */
	.ui-quote blockquote {
		margin: 0;
		padding: 0;
		border: 0;
		font-family: var(--font-serif);
		font-size: 17px;
		line-height: 1.45;
		overflow-wrap: break-word;
	}

	figcaption {
		margin-top: var(--space-xs);
		font-size: 12.5px;
		color: var(--color-text-dim);
	}
</style>
