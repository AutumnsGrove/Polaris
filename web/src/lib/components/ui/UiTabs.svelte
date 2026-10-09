<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	let { block }: { block: Extract<UiBlock, { kind: 'tabs' }> } = $props();

	// Selected tab is local state. The block is re-parsed on every streamed
	// token but this instance is kept (UiBlocks keys by index), so a reader's
	// choice survives later tabs arriving. Clamped because tabs only ever grow.
	let picked = $state(0);
	let active = $derived(Math.min(picked, Math.max(block.tabs.length - 1, 0)));
</script>

<div class="ui-tabs">
	<div class="bar" role="tablist">
		{#each block.tabs as t, i (i)}
			<button
				type="button"
				role="tab"
				aria-selected={active === i}
				class:on={active === i}
				onclick={() => (picked = i)}
			>
				{t.tab}
			</button>
		{/each}
	</div>
	{#if block.tabs[active]}
		<div class="panel" role="tabpanel"><UiText text={block.tabs[active].text} /></div>
	{/if}
</div>

<style>
	.ui-tabs {
		margin: var(--space-md) 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
		overflow: hidden;
	}

	/* Scrolls sideways rather than wrapping: six labels can outrun a phone. */
	.bar {
		display: flex;
		overflow-x: auto;
		border-bottom: 1px solid var(--color-border);
		background: var(--color-surface-2);
	}

	button {
		flex: none;
		padding: var(--space-sm) var(--space-lg);
		border: 0;
		border-bottom: 2px solid transparent;
		background: none;
		color: var(--color-text-dim);
		font: inherit;
		font-size: 13px;
		font-weight: 600;
		white-space: nowrap;
		cursor: pointer;
	}

	button.on {
		border-bottom-color: var(--color-accent);
		color: var(--color-text);
	}

	.panel {
		padding: var(--space-md) var(--space-lg);
		font-size: 14px;
		overflow-wrap: anywhere;
	}
</style>
