<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	let { block, loc }: { block: Extract<UiBlock, { kind: 'procon' }>; loc?: string } = $props();
</script>

<!-- +/– symbols, never colour alone (plan, catalog). -->
<div class="ui-procon">
	<section class="pro">
		<h4>{block.proHead ?? 'Pros'}</h4>
		<!-- div + role, not ul/li: `.prose ul { padding-left }` would indent it. -->
		<div role="list">
			{#each block.pros as p, i (i)}
				<div class="row" role="listitem"><b aria-hidden="true">+</b><span><UiText text={p} loc="{loc}.{i}.pro" /></span></div>
			{/each}
		</div>
	</section>
	<section class="con">
		<h4>{block.conHead ?? 'Cons'}</h4>
		<div role="list">
			{#each block.cons as c, i (i)}
				<div class="row" role="listitem"><b aria-hidden="true">–</b><span><UiText text={c} loc="{loc}.{i}.con" /></span></div>
			{/each}
		</div>
	</section>
</div>

<style>
	.ui-procon {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: var(--space-md);
		margin: var(--space-md) 0;
	}

	/* Stacked on phones: the prompt asks for short points, but real answers
	   write full sentences (seen live through /api/ask), and half a 390px
	   screen wraps those mid-word. Side by side once there is room. */
	@media (max-width: 559px) {
		.ui-procon {
			grid-template-columns: 1fr;
		}
	}

	section {
		min-width: 0;
		padding: var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
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

	.row {
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
