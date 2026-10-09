<script lang="ts">
	import type { UiBlock } from '$lib/uiBlocks/types';
	import { layoutFlow } from '$lib/uiBlocks/flowLayout';
	import UiText from './UiText.svelte';
	import UiSources from './UiSources.svelte';

	let { block, loc }: { block: Extract<UiBlock, { kind: 'flow' }>; loc?: string } = $props();

	let layout = $derived(layoutFlow(block.nodes, block.edges));

	// A node's verification address uses its index in `block.nodes` (arrival
	// order), NOT where the layout drew it: a node moves between layers as edges
	// stream in, and the server (gateway/uiblocks/sites.go) knows only arrival order.
	const at = (n: string) => `${loc}.${block.nodes.findIndex((x) => x.n === n)}`;

	// Expanded nodes, keyed by node id and NOT by position: a node moves from the
	// "waiting" area into the chain when its edge streams in, so a positional
	// key would hand its open state to whichever node took the slot. Local only
	// (decision 1: static + local toggles), never persisted.
	let open = $state<Record<string, boolean>>({});
</script>

<div class="ui-flow">
	{#each layout.layers as layer, li (li)}
		<!-- Its own element, not a ::before on the layer: a branch layer is a flex
		     row, where a pseudo-element becomes a flex item pinned to the left. -->
		{#if li > 0}<div class="link" aria-hidden="true"></div>{/if}
		<div class="layer" class:branch={layer.length > 1}>
			{#each layer as cell (cell.node.n)}
				{@const expandable = !!cell.node.d || cell.node.src.length > 0}
				<div class="cell">
					{#if cell.label}<div class="edge-label">{cell.label}</div>{/if}
					<div class="node" class:decision={cell.node.decision}>
						{#if expandable}
							<button
								type="button"
								class="head"
								aria-expanded={!!open[cell.node.n]}
								onclick={() => (open[cell.node.n] = !open[cell.node.n])}
							>
								{#if cell.node.decision}<span class="q" aria-hidden="true">?</span>{/if}
								<span class="t"><UiText text={cell.node.t} loc="{at(cell.node.n)}.t" /></span>
								<span class="chev" aria-hidden="true">{open[cell.node.n] ? '▾' : '▸'}</span>
							</button>
							{#if open[cell.node.n]}
								<div class="detail">
									{#if cell.node.d}<UiText text={cell.node.d} loc="{at(cell.node.n)}.d" />{/if}
									<UiSources src={cell.node.src} loc="{at(cell.node.n)}.src" />
								</div>
							{/if}
						{:else}
							<div class="head static">
								{#if cell.node.decision}<span class="q" aria-hidden="true">?</span>{/if}
								<span class="t"><UiText text={cell.node.t} loc="{at(cell.node.n)}.t" /></span>
							</div>
						{/if}
						{#each cell.back as title (title)}
							<div class="back">↩ back to {title}</div>
						{/each}
						{#each cell.side as title (title)}
							<div class="back">→ also leads to {title}</div>
						{/each}
					</div>
				</div>
			{/each}
		</div>
	{/each}

	{#if layout.waiting.length}
		<!-- Nodes no edge reaches (yet). Mid-stream this is "its edge is coming";
		     on a finished answer it is just an unconnected step, still readable. -->
		<div class="waiting">
			<div class="waiting-label">Not connected yet</div>
			{#each layout.waiting as n (n.n)}
				<div class="node dotted"><div class="head static"><span class="t"><UiText text={n.t} loc="{at(n.n)}.t" /></span></div></div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.ui-flow {
		margin: var(--space-md) 0;
	}

	/* A short connector between layers. */
	.link {
		width: 1.5px;
		height: var(--space-md);
		margin: 0 auto;
		background: var(--color-border-strong);
	}

	.layer.branch {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
	}

	/* Branches sit side by side while they fit, then stack on a narrow phone. */
	.layer.branch .cell {
		flex: 1 1 150px;
		min-width: 0;
	}

	.edge-label {
		margin-bottom: var(--space-xs);
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		text-align: center;
		color: var(--color-accent);
	}

	.node {
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface);
		font-size: 14px;
	}

	.node.decision {
		border-color: color-mix(in srgb, var(--color-accent) 55%, var(--color-border));
	}

	.node.dotted {
		margin-top: var(--space-xs);
		border-style: dashed;
		background: transparent;
		color: var(--color-text-dim);
	}

	.head {
		display: flex;
		gap: var(--space-sm);
		align-items: baseline;
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		border: 0;
		background: none;
		color: inherit;
		font: inherit;
		font-weight: 600;
		text-align: left;
	}

	button.head {
		cursor: pointer;
	}

	/* break-word, not anywhere: `anywhere` lets a branch column shrink below a
	   word's width, which split "yeasty/beery/pleasantly" mid-word in a live run. */
	.t {
		flex: 1;
		min-width: 0;
		overflow-wrap: break-word;
	}

	.q {
		flex: none;
		color: var(--color-accent);
		font-weight: 700;
	}

	.chev {
		flex: none;
		color: var(--color-text-dim);
	}

	.detail {
		padding: 0 var(--space-md) var(--space-sm);
		font-size: 13px;
		color: var(--color-text-dim);
		overflow-wrap: anywhere;
	}

	/* A back-edge is a note, never a drawn line (plan, "`flow`"). */
	.back {
		padding: 0 var(--space-md) var(--space-sm);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.waiting {
		margin-top: var(--space-md);
	}

	.waiting-label {
		font-size: 11px;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--color-text-dim);
	}
</style>
