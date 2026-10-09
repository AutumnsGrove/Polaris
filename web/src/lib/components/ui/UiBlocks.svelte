<script lang="ts">
	import { setContext } from 'svelte';
	import type { Citation, VerificationMark } from '$lib/types';
	import { parseUi } from '$lib/uiBlocks/parse';
	import UiCallout from './UiCallout.svelte';
	import UiStat from './UiStat.svelte';
	import UiCompare from './UiCompare.svelte';
	import UiSteps from './UiSteps.svelte';
	import UiTimeline from './UiTimeline.svelte';
	import UiChecklist from './UiChecklist.svelte';
	import UiProCon from './UiProCon.svelte';
	import UiChoose from './UiChoose.svelte';
	import UiFacts from './UiFacts.svelte';
	import UiFlow from './UiFlow.svelte';
	import UiTabs from './UiTabs.svelte';
	import UiDisclose from './UiDisclose.svelte';
	import UiQuote from './UiQuote.svelte';
	import UiClaim from './UiClaim.svelte';

	// One ```ui fence. The whole body is re-parsed whenever it grows (cheap:
	// parse.ts caps it at 40 lines) and the blocks are keyed by index, so a
	// block that already exists is updated in place rather than remounted —
	// that is what lets a comparison fill in row by row mid-stream.
	//
	// `fence` is this fence's ordinal among the answer's ui fences and
	// `verification` the turn's "found in source" marks. Together with a block's
	// index they form the address ("<fence>.<block>.<item>.<field>#<n>") a
	// block link is ticked by; the server builds the same strings
	// (uiblocks/sites.go). Each block gets its `loc` prefix below, the
	// block index being the index in parseUi's output, raw rows included.
	let {
		src,
		citations,
		fence = 0,
		verification
	}: { src: string; citations: Citation[]; fence?: number; verification?: VerificationMark[] } = $props();

	// Getters so UiText re-derives when citations or marks arrive mid-stream
	// (marks land after the turn, so they always do).
	setContext('ui-citations', () => citations);
	setContext('ui-verification', () => verification);

	let blocks = $derived(parseUi(src));
</script>

<div class="ui-blocks">
	{#each blocks as block, i (i)}
		{@const loc = `${fence}.${i}`}
		{#if block.kind === 'callout'}
			<UiCallout {block} {loc} />
		{:else if block.kind === 'stat'}
			<UiStat {block} {loc} />
		{:else if block.kind === 'compare'}
			<UiCompare {block} {loc} />
		{:else if block.kind === 'steps'}
			<UiSteps {block} {loc} />
		{:else if block.kind === 'timeline'}
			<UiTimeline {block} {loc} />
		{:else if block.kind === 'checklist'}
			<UiChecklist {block} {loc} />
		{:else if block.kind === 'procon'}
			<UiProCon {block} {loc} />
		{:else if block.kind === 'choose'}
			<UiChoose {block} {loc} />
		{:else if block.kind === 'facts'}
			<UiFacts {block} {loc} />
		{:else if block.kind === 'flow'}
			<UiFlow {block} {loc} />
		{:else if block.kind === 'tabs'}
			<UiTabs {block} {loc} />
		{:else if block.kind === 'disclose'}
			<UiDisclose {block} {loc} />
		{:else if block.kind === 'quote'}
			<UiQuote {block} {loc} />
		{:else if block.kind === 'claim'}
			<UiClaim {block} {loc} />
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
