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
					<span class="pill"><UiText text={rule.then} /><UiSources src={rule.src} /></span>
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

	/* The pick is usually a sentence, not a word (seen live), so it is a
	   block beside the arrow rather than a pill that wraps into a stadium:
	   a pill's full radius turns into a blob once the text runs to a few lines. */
	.then {
		display: flex;
		gap: var(--space-sm);
		align-items: flex-start;
		margin-top: var(--space-sm);
	}

	.arrow {
		flex: none;
		padding-top: var(--space-xs);
		color: var(--color-accent);
	}

	.pill {
		flex: 1;
		min-width: 0;
		padding: var(--space-xs) var(--space-md);
		border-radius: var(--radius-md);
		background: var(--color-accent-soft);
		font-weight: 600;
		overflow-wrap: anywhere;
	}
</style>
