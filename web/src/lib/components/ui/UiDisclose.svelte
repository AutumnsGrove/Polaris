<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	let { block, loc }: { block: Extract<UiBlock, { kind: 'disclose' }>; loc?: string } = $props();
</script>

<!-- Native <details>: no JS, and the open state lives in the DOM node, which
     UiBlocks keeps mounted while the answer streams. Open by default: a folded
     section read as an answer that stopped short ("why is it so short?") in
     real use; the reader can still fold it away. `open` is a constant, so a
     reader's own toggle is never reset by later tokens. -->
<details class="ui-disclose" open>
	<summary>
		<span class="ttl">{block.title ?? 'More detail'}</span>
		{#if block.hint}<span class="hint">{block.hint}</span>{/if}
	</summary>
	<div class="body">
		{#each block.paras as p, i (i)}
			<p><UiText text={p} loc="{loc}.{i}.p" /></p>
		{/each}
	</div>
</details>

<style>
	.ui-disclose {
		margin: var(--space-md) 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
	}

	summary {
		display: flex;
		gap: var(--space-md);
		align-items: baseline;
		justify-content: space-between;
		padding: var(--space-sm) var(--space-lg);
		font-size: 14px;
		cursor: pointer;
	}

	.ttl {
		font-weight: 600;
	}

	.hint {
		flex: none;
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.body {
		padding: 0 var(--space-lg) var(--space-sm);
		font-size: 13.5px;
		color: var(--color-text-dim);
	}

	.body p {
		margin: 0 0 var(--space-sm);
	}
</style>
