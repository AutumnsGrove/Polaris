<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	let { block, loc }: { block: Extract<UiBlock, { kind: 'timeline' }>; loc?: string } = $props();
</script>

<!-- The vertical rail, date above the text (decision 7): it copes with long
     spans such as "14 Mar – 2 Apr 2005" where a side-by-side ledger would not. -->
<div class="ui-timeline">
	<!-- div + role, not ol/li: ChatTurnView's `.prose ul/ol { padding-left }`
	     would indent the rail, and element selectors here can't outrank it. -->
	<div class="list" role="list">
		{#each block.events as ev, i (i)}
			<div class="item" role="listitem">
				<div class="when">{ev.when}</div>
				<div class="what"><UiText text={ev.i} loc="{loc}.{i}.i" /><UiSources src={ev.src} loc="{loc}.{i}.src" /></div>
			</div>
		{/each}
	</div>
</div>

<style>
	.ui-timeline {
		margin: var(--space-md) 0;
	}

	.item {
		position: relative;
		padding: 0 0 var(--space-lg) var(--space-xl);
		font-size: 14px;
	}

	.item::before {
		content: '';
		position: absolute;
		left: 0;
		top: 5px;
		width: 9px;
		height: 9px;
		border-radius: var(--radius-full);
		background: var(--color-accent);
	}

	.item::after {
		content: '';
		position: absolute;
		left: 4px;
		top: 18px;
		bottom: 0;
		width: 1.5px;
		background: var(--color-border-strong);
	}

	.item:last-child {
		padding-bottom: 0;
	}

	.item:last-child::after {
		display: none;
	}

	.when {
		font-family: var(--font-mono);
		font-size: 11.5px;
		color: var(--color-text-dim);
	}

	.what {
		overflow-wrap: anywhere;
	}
</style>
