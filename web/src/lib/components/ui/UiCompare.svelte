<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	let { block }: { block: Extract<UiBlock, { kind: 'compare' }> } = $props();

	// The cards layout has no per-row place for a row's sources (a row spans
	// every card), so they collect into one line under the cards instead.
	let cardSources = $derived([...new Set(block.rows.flatMap((r) => r.src))]);
</script>

<!--
	Layout is the client's call, never the model's (plan, "Block catalog"):
	both are rendered and CSS shows one by viewport, so there is no resize
	listener and no flash of the wrong layout on first paint. display:none
	also drops the hidden one from the accessibility tree.
-->
<div class="ui-compare">
	<div class="cards">
		{#each block.cols as col, ci (ci)}
			<div class="card" class:pick={block.pick === ci}>
				{#if block.pick === ci}<span class="ribbon">Pick</span>{/if}
				<div class="ttl">{col}</div>
				<dl>
					{#each block.rows as row, ri (ri)}
						<dt>{row.row}</dt>
						<dd><UiText text={row.v[ci]} /></dd>
					{/each}
				</dl>
			</div>
		{/each}
		{#if cardSources.length}<div class="card-sources"><UiSources src={cardSources} /></div>{/if}
	</div>

	<div class="scroll">
		<table>
			<thead>
				<tr>
					<th></th>
					{#each block.cols as col, ci (ci)}
						<th class:pick={block.pick === ci}>
							{#if block.pick === ci}<span class="tag">Pick</span>{/if}
							{col}
						</th>
					{/each}
				</tr>
			</thead>
			<tbody>
				{#each block.rows as row, ri (ri)}
					<tr>
						<td>{row.row}<UiSources src={row.src} /></td>
						{#each row.v as cell, ci (ci)}
							<td class:pick={block.pick === ci}><UiText text={cell} /></td>
						{/each}
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</div>

<style>
	.ui-compare {
		margin: var(--space-md) 0;
	}

	/* ---- phones: stacked option cards, the pick called out ---- */
	.cards {
		display: grid;
		gap: var(--space-md);
	}

	.scroll {
		display: none;
	}

	.card {
		position: relative;
		padding: var(--space-md) var(--space-lg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
	}

	.card.pick {
		border-color: var(--color-accent);
		background: color-mix(in srgb, var(--color-accent) 8%, var(--color-surface));
	}

	.ribbon {
		position: absolute;
		top: -9px;
		left: 14px;
		padding: 1px var(--space-sm);
		border-radius: var(--radius-full);
		background: var(--color-accent);
		color: var(--color-bg);
		font-size: 10px;
		font-weight: 700;
		letter-spacing: 0.05em;
		text-transform: uppercase;
	}

	.ttl {
		font-size: 14.5px;
		font-weight: 600;
	}

	dl {
		display: grid;
		grid-template-columns: 78px 1fr;
		gap: 2px var(--space-md);
		margin: var(--space-sm) 0 0;
		font-size: 13px;
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

	.card-sources {
		font-size: 12.5px;
	}

	/* ---- wide: a table with a sticky first column, side-scroll if needed ---- */
	@media (min-width: 600px) {
		.cards {
			display: none;
		}

		.scroll {
			display: block;
			overflow-x: auto;
			border: 1px solid var(--color-border);
			border-radius: var(--radius-lg);
			background: var(--color-surface);
		}

		/* Everything below is prefixed `.ui-compare .scroll` on purpose:
		   ChatTurnView's `.prose :global(table|th|td)` rules (block display,
		   max-content width, a full 1px border on every cell) would
		   otherwise win. Svelte scopes an element selector as
		   `table:where(.svelte-x)`, which adds NO specificity, so a bare
		   `.scroll table` ties `.prose table` exactly and source order
		   decides. Two real classes outrank it. */
		.ui-compare .scroll table {
			display: table;
			width: 100%;
			max-width: none;
			margin: 0;
			overflow: visible;
			border-collapse: separate;
			border-spacing: 0;
			font-size: 13px;
		}

		.ui-compare .scroll th,
		.ui-compare .scroll td {
			min-width: 104px;
			padding: var(--space-sm) var(--space-md);
			border: 0;
			border-bottom: 1px solid var(--color-border);
			text-align: left;
			vertical-align: top;
			white-space: normal;
		}

		.ui-compare .scroll tr:last-child td {
			border-bottom: 0;
		}

		.ui-compare .scroll th {
			background: var(--color-surface-2);
			font-size: 12.5px;
			font-weight: 600;
		}

		.ui-compare .scroll th:first-child,
		.ui-compare .scroll td:first-child {
			position: sticky;
			left: 0;
			z-index: 1;
			min-width: 78px;
			background: var(--color-surface-2);
			color: var(--color-text-dim);
			font-size: 12px;
		}

		.ui-compare .scroll td.pick {
			background: var(--color-accent-soft);
		}

		.ui-compare .scroll th.pick {
			background: color-mix(in srgb, var(--color-accent) 22%, var(--color-surface-2));
		}

		.tag {
			display: block;
			color: var(--color-accent);
			font-size: 10px;
			font-weight: 700;
			letter-spacing: 0.05em;
			text-transform: uppercase;
		}
	}
</style>
