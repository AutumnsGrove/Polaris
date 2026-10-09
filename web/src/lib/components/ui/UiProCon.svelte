<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	let { block }: { block: Extract<UiBlock, { kind: 'procon' }> } = $props();
</script>

<!-- +/– symbols, never colour alone (plan, catalog). -->
<div class="ui-procon">
	<section class="pro">
		<h4>{block.proHead ?? 'Pros'}</h4>
		<ul>
			{#each block.pros as p, i (i)}
				<li><b aria-hidden="true">+</b><span><UiText text={p} /></span></li>
			{/each}
		</ul>
	</section>
	<section class="con">
		<h4>{block.conHead ?? 'Cons'}</h4>
		<ul>
			{#each block.cons as c, i (i)}
				<li><b aria-hidden="true">–</b><span><UiText text={c} /></span></li>
			{/each}
		</ul>
	</section>
</div>

<style>
	.ui-procon {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: var(--space-md);
		margin: var(--space-md) 0;
	}

	/* Two columns even on a phone: the items are short, and stacking would
	   put the cons a full screen away from the pros they answer. */
	section {
		min-width: 0;
		padding: var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
	}

	h4 {
		margin: 0 0 var(--space-sm);
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-dim);
	}

	ul {
		margin: 0;
		padding: 0;
		list-style: none;
	}

	li {
		display: flex;
		gap: var(--space-sm);
		padding: 2px 0;
		font-size: 13.5px;
	}

	b {
		flex: none;
		width: 1ch;
		font-weight: 700;
	}

	.pro b {
		color: var(--color-syntax-addition);
	}

	.con b {
		color: var(--color-danger);
	}

	span {
		min-width: 0;
		overflow-wrap: anywhere;
	}
</style>
