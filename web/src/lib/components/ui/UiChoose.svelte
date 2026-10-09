<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	let { block }: { block: Extract<UiBlock, { kind: 'choose' }> } = $props();
</script>

<!-- "It depends": decision rules instead of a table, each row a situation
     and the pick that follows from it. -->
<div class="ui-choose">
	{#if block.title}<div class="title">{block.title}</div>{/if}
	<!-- div + role, not ul/li: `.prose ul { padding-left }` would indent it. -->
	<div class="list" role="list">
		{#each block.rules as rule, i (i)}
			<div class="rule" role="listitem">
				<div class="if"><span class="lead">If</span> <UiText text={rule.if} /></div>
				<div class="then">
					<span class="arrow" aria-hidden="true">→</span>
					<span class="pill"><UiText text={rule.then} /></span>
					<UiSources src={rule.src} />
				</div>
			</div>
		{/each}
	</div>
</div>

<style>
	.ui-choose {
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

	.list {
		display: grid;
		gap: var(--space-sm);
	}

	.rule {
		padding: var(--space-md) var(--space-lg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
		font-size: 14px;
	}

	.if {
		overflow-wrap: anywhere;
	}

	.lead {
		color: var(--color-text-dim);
	}

	.then {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		align-items: baseline;
		margin-top: var(--space-xs);
	}

	.arrow {
		color: var(--color-accent);
	}

	.pill {
		padding: 0 var(--space-md);
		border-radius: var(--radius-full);
		background: var(--color-accent-soft);
		font-weight: 600;
		overflow-wrap: anywhere;
	}
</style>
