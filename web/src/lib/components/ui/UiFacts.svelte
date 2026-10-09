<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	let { block, loc }: { block: Extract<UiBlock, { kind: 'facts' }>; loc?: string } = $props();
</script>

<!-- The at-a-glance card for one named thing; replaces the old stat strip. -->
<div class="ui-facts">
	{#if block.title || block.sub}
		<header>
			{#if block.title}<div class="ttl">{block.title}</div>{/if}
			{#if block.sub}<div class="sub">{block.sub}</div>{/if}
		</header>
	{/if}
	<dl>
		{#each block.rows as row, i (i)}
			<dt>{row.k}</dt>
			<dd><UiText text={row.v} loc="{loc}.{i}.v" /><UiSources src={row.src} loc="{loc}.{i}.src" /></dd>
		{/each}
	</dl>
</div>

<style>
	.ui-facts {
		margin: var(--space-md) 0;
		padding: var(--space-md) var(--space-lg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
	}

	header {
		margin-bottom: var(--space-sm);
		padding-bottom: var(--space-sm);
		border-bottom: 1px solid var(--color-border);
	}

	.ttl {
		font-size: 15px;
		font-weight: 600;
	}

	.sub {
		font-size: 12.5px;
		color: var(--color-text-dim);
	}

	dl {
		display: grid;
		grid-template-columns: minmax(70px, 34%) 1fr;
		gap: var(--space-xs) var(--space-md);
		margin: 0;
		font-size: 13.5px;
	}

	dt {
		font-size: 12px;
		color: var(--color-text-dim);
	}

	dd {
		margin: 0;
		min-width: 0;
		overflow-wrap: anywhere;
	}
</style>
