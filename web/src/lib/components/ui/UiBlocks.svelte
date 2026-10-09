<script lang="ts">
	import { setContext } from 'svelte';
	import type { Citation } from '$lib/types';
	import { parseUi } from '$lib/uiBlocks/parse';
	import UiCallout from './UiCallout.svelte';
	import UiStat from './UiStat.svelte';
	import UiCompare from './UiCompare.svelte';
	import UiSteps from './UiSteps.svelte';

	// One ```ui fence. The whole body is re-parsed whenever it grows (cheap:
	// parse.ts caps it at 40 lines) and the blocks are keyed by index, so a
	// block that already exists is updated in place rather than remounted —
	// that is what lets a comparison fill in row by row mid-stream.
	let { src, citations }: { src: string; citations: Citation[] } = $props();

	// Getter so UiText re-derives when citations arrive mid-stream.
	setContext('ui-citations', () => citations);

	let blocks = $derived(parseUi(src));
</script>

<div class="ui-blocks">
	{#each blocks as block, i (i)}
		{#if block.kind === 'callout'}
			<UiCallout {block} />
		{:else if block.kind === 'stat'}
			<UiStat {block} />
		{:else if block.kind === 'compare'}
			<UiCompare {block} />
		{:else if block.kind === 'steps'}
			<UiSteps {block} />
		{:else}
			<!-- A line the grammar couldn't use: shown muted, never an error and never blanking the rest. -->
			<div class="raw">{block.text}</div>
		{/if}
	{/each}
</div>

<style>
	.raw {
		margin: var(--space-xs) 0;
		padding: var(--space-xs) var(--space-sm);
		border-radius: var(--radius-sm);
		background: var(--color-surface);
		font-family: var(--font-mono);
		font-size: 11.5px;
		color: var(--color-text-dim);
		overflow-wrap: anywhere;
	}
</style>
