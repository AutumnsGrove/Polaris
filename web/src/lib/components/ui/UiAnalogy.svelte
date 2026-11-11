<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';

	// "X is like Y", and — the point of the block — where it stops being true.
	// The model's own teaching device, so no source and no verification tick
	// (accent block). `x`/`y` are the two column headings and may be absent.
	let { block }: { block: Extract<UiBlock, { kind: 'analogy' }> } = $props();
</script>

<div class="ui-analogy">
	<div class="ah">
		<span class="eyebrow">Think of it as</span>
		<div class="t"><UiText text={block.like} /></div>
	</div>

	{#if block.pairs.length}
		<div class="cols">
			<span>{block.x ?? ''}</span><span></span><span>{block.y ?? ''}</span>
		</div>
		<div class="map">
			{#each block.pairs as p, i (i)}
				<span class="l"><UiText text={p.x} /></span>
				<span class="ar" aria-hidden="true">↔</span>
				<span class="r"><UiText text={p.y} /></span>
			{/each}
		</div>
	{/if}

	{#if block.breaks}
		<div class="brk">
			<div class="tn"><span class="tg" aria-hidden="true">≠</span>Where it breaks</div>
			<p><UiText text={block.breaks} /></p>
		</div>
	{/if}
</div>

<style>
	.ui-analogy {
		margin: var(--space-md) 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
		overflow: hidden;
	}

	.ah {
		padding: var(--space-md) var(--space-lg);
		background: var(--color-surface-2);
	}

	.eyebrow {
		font-size: 11px;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		font-weight: 600;
	}

	.ah .t {
		margin-top: 2px;
		font-family: var(--font-serif);
		font-size: 18px;
		line-height: 1.3;
		overflow-wrap: break-word;
	}

	.cols {
		display: grid;
		grid-template-columns: 1fr 22px 1fr;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-lg) 0;
		font-size: 11px;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		font-weight: 600;
	}

	.map {
		display: grid;
		grid-template-columns: 1fr 22px 1fr;
		gap: 0 var(--space-sm);
		padding: 0 var(--space-lg) var(--space-sm);
		font-size: 13.5px;
		align-items: baseline;
	}

	.map > * {
		padding: 7px 0;
		border-bottom: 1px solid var(--color-border);
		overflow-wrap: break-word;
	}

	.map > :nth-last-child(-n + 3) {
		border-bottom: 0;
	}

	.l {
		color: var(--color-text);
	}

	.ar {
		color: var(--color-text-dim);
		text-align: center;
	}

	.r {
		color: var(--color-accent-2);
		font-weight: 500;
	}

	.brk {
		padding: var(--space-md) var(--space-lg);
		border-top: 1px solid var(--color-border);
		background: color-mix(in srgb, var(--color-accent) 6%, var(--color-surface));
		font-size: 13.5px;
	}

	.tn {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: 2px;
		font-size: 11px;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		font-weight: 700;
		color: var(--color-accent);
	}

	.tg {
		display: grid;
		place-items: center;
		width: 18px;
		height: 18px;
		border: 1.5px solid currentColor;
		border-radius: 50%;
		font-size: 11px;
		line-height: 1;
	}

	.brk p {
		margin: 0;
		padding-left: 26px;
		overflow-wrap: break-word;
	}
</style>
