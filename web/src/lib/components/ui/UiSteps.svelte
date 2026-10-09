<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	let { block }: { block: Extract<UiBlock, { kind: 'steps' }> } = $props();
</script>

<div class="ui-steps">
	{#if block.title}<div class="title">{block.title}</div>{/if}
	<ol>
		{#each block.steps as step, i (i)}
			<li>
				<div class="head">
					<b><UiText text={step.i} /></b>
					<!-- A duration chip only when the model supplied one (decision 13). -->
					{#if step.t}<span class="t">{step.t}</span>{/if}
				</div>
				{#if step.d}<span class="d"><UiText text={step.d} /></span>{/if}
			</li>
		{/each}
	</ol>
</div>

<style>
	.ui-steps {
		margin: var(--space-md) 0;
	}

	.title {
		margin-bottom: var(--space-sm);
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-dim);
	}

	/* The numbered rail: a counter circle per step joined by a hairline. */
	ol {
		margin: 0;
		padding: 0;
		list-style: none;
		counter-reset: step;
	}

	li {
		position: relative;
		padding: 0 0 var(--space-lg) 40px;
		font-size: 14px;
		counter-increment: step;
	}

	li::before {
		content: counter(step);
		position: absolute;
		left: 0;
		top: 0;
		display: grid;
		place-items: center;
		width: 28px;
		height: 28px;
		border: 1.5px solid var(--color-accent);
		border-radius: var(--radius-full);
		background: var(--color-bg);
		color: var(--color-accent);
		font-size: 12.5px;
		font-weight: 600;
	}

	li::after {
		content: '';
		position: absolute;
		left: 13.5px;
		top: 30px;
		bottom: 2px;
		width: 1.5px;
		background: var(--color-border-strong);
	}

	li:last-child {
		padding-bottom: 0;
	}

	li:last-child::after {
		display: none;
	}

	.head {
		display: flex;
		gap: var(--space-sm);
		align-items: baseline;
		justify-content: space-between;
	}

	b {
		font-weight: 600;
	}

	.t {
		flex: none;
		padding: 0 var(--space-sm);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-full);
		font-family: var(--font-mono);
		font-size: 11.5px;
		color: var(--color-text-dim);
		white-space: nowrap;
	}

	.d {
		display: block;
		font-size: 13px;
		color: var(--color-text-dim);
	}
</style>
