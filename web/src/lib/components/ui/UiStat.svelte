<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	let { block }: { block: Extract<UiBlock, { kind: 'stat' }> } = $props();
</script>

<div class="ui-stat">
	{#if block.label}<div class="lbl">{block.label}</div>{/if}
	<div class="big">{block.value}</div>
	{#if block.note || block.src.length}
		<div class="note">
			{#if block.note}<UiText text={block.note} />{/if}
			<UiSources src={block.src} />
		</div>
	{/if}
</div>

<style>
	.ui-stat {
		margin: var(--space-md) 0;
		padding: var(--space-md) var(--space-lg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
	}

	.big {
		font-size: 38px;
		font-weight: 600;
		letter-spacing: -0.02em;
		line-height: 1.1;
		color: var(--color-accent);
		overflow-wrap: anywhere;
	}

	.lbl {
		font-size: 12.5px;
		color: var(--color-text-dim);
	}

	.note {
		margin-top: var(--space-xs);
		font-size: 12.5px;
		color: var(--color-text-dim);
	}
</style>
