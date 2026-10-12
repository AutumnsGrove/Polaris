<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	// A contested question, one card per camp. No verdict and no strength meter
	// on purpose: the model always understood "rank the evidence" as a licence
	// to invent a weight, so only each camp's own line and its sources are
	// checked (see uiblocks/sites.go, `views`).
	let { block, loc }: { block: Extract<UiBlock, { kind: 'views' }>; loc?: string } = $props();
</script>

<div class="ui-views">
	{#if block.title}
		<div class="vt">{block.title}</div>
	{/if}
	{#each block.camps as c, i (i)}
		<div class="camp">
			<span class="who">{c.who}</span>
			<p class="stance"><UiText text={c.stance} loc="{loc}.{i}.stance" /></p>
			{#if c.t}
				<p class="t"><UiText text={c.t} loc="{loc}.{i}.t" /></p>
			{/if}
			<UiSources src={c.src} loc="{loc}.{i}.src" />
		</div>
	{/each}
</div>

<style>
	.ui-views {
		display: grid;
		gap: var(--space-sm);
		margin: var(--space-md) 0;
	}

	.vt {
		margin: 0 0 var(--space-xs);
		font-family: var(--font-serif);
		font-size: 17px;
		line-height: 1.35;
		overflow-wrap: break-word;
	}

	.camp {
		padding: var(--space-md) var(--space-lg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
		font-size: 13.5px;
		min-width: 0;
	}

	.who {
		display: inline-block;
		margin-bottom: var(--space-xs);
		padding: 1px 10px;
		border-radius: var(--radius-full);
		background: color-mix(in srgb, var(--color-accent-2) 14%, transparent);
		color: var(--color-accent-2);
		font-size: 11px;
		letter-spacing: 0.04em;
	}

	.stance {
		margin: 0 0 2px;
		font-size: 14.5px;
		font-weight: 600;
		overflow-wrap: break-word;
	}

	.t {
		margin: 0;
		color: var(--color-text-dim);
		overflow-wrap: break-word;
	}
</style>
