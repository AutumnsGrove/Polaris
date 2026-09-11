<script lang="ts">
	import { goto } from '$app/navigation';
	import type { Star, StarEdge } from '$lib/types';

	// ConstellationMiniMap is the Star detail screen's "Nearby in the
	// constellation" panel. Deliberately NOT the full Map's force-directed
	// layout scaled down — a physics simulation crammed into a ~90px-tall
	// panel had nowhere to put 2-3 titles without stacking them directly on
	// top of each other (the original bug this component was rewritten to
	// fix). Instead: this star anchors a fixed left column, and each
	// neighbor gets its own fixed-height row fanning out to the right —
	// deterministic placement, so "no overlap" is guaranteed by
	// construction rather than hoped for from a collision force that only
	// ever tracked dot centers, never label width.
	//
	// Takes exactly what GET /api/constellation/stars/{id} already returns
	// (centerStar + its own edges) plus each neighbor's Star object
	// (fetched by the caller, star/[id]/+page.svelte, via a few bounded
	// loadStarDetail calls) — deliberately not the full map dataset, which
	// would be a much heavier fetch for a 3-node preview.
	let {
		centerStar,
		neighborStars,
		edges
	}: {
		centerStar: Star;
		neighborStars: Star[];
		edges: StarEdge[];
	} = $props();

	const ROW_HEIGHT = 30;
	const TOP_PAD = 15;
	const HUB_X = 20;
	const NEIGHBOR_X = 54;
	const RIGHT_PAD = 14;

	// neighborStars already carries the caller's own order/bound (the first
	// 3 edges — see star/[id]/+page.svelte's load()); this just drops any
	// edge whose star didn't come back (a deleted/rejected neighbor).
	const rows = $derived(neighborStars.filter((s) => edges.some((e) => e.other_star_id === s.id)));

	// Measured, not assumed — the card this sits in has different available
	// widths depending on sidebar/viewport state, and a fixed canvas width
	// either clipped long titles unnecessarily or wasted space.
	let canvasWidth = $state(280);
	const canvasHeight = $derived(rows.length === 0 ? 0 : TOP_PAD * 2 + Math.max(0, rows.length - 1) * ROW_HEIGHT);
	const hubY = $derived(canvasHeight / 2);
	const labelMaxWidth = $derived(Math.max(80, canvasWidth - NEIGHBOR_X - RIGHT_PAD));
</script>

{#if rows.length > 0}
	<div class="constellation-block">
		<div class="constellation-label">{rows.length} related star{rows.length === 1 ? '' : 's'}</div>
		<div class="canvas" bind:clientWidth={canvasWidth} style="height: {canvasHeight}px;">
			<svg class="lines" viewBox="0 0 {canvasWidth} {canvasHeight}" preserveAspectRatio="none">
				{#each rows as star, i (star.id)}
					<line x1={HUB_X} y1={hubY} x2={NEIGHBOR_X} y2={TOP_PAD + i * ROW_HEIGHT} />
				{/each}
			</svg>
			<div class="hub-dot" style="left: {HUB_X}px; top: {hubY}px;" aria-hidden="true"></div>
			{#each rows as star, i (star.id)}
				<button
					class="node"
					class:personal={star.is_personal}
					style="left: {NEIGHBOR_X}px; top: {TOP_PAD + i * ROW_HEIGHT}px; max-width: {labelMaxWidth}px;"
					onclick={() => goto(`/constellation/star/${star.id}`)}
				>
					<span class="dot"></span>
					<span class="label">{star.title}</span>
				</button>
			{/each}
		</div>
	</div>
{/if}

<style>
	.constellation-block {
		border-radius: var(--radius-lg);
		background: var(--color-surface);
		border: 1px solid var(--color-border);
		padding: var(--space-lg) var(--space-md) var(--space-md);
		position: relative;
		overflow: hidden;
	}
	.constellation-label {
		font-size: 11px;
		font-weight: 600;
		color: var(--color-text-dim);
		margin-bottom: var(--space-md);
	}
	.canvas {
		position: relative;
		width: 100%;
	}
	.lines {
		position: absolute;
		inset: 0;
		width: 100%;
		height: 100%;
		pointer-events: none;
	}
	.lines line {
		stroke: var(--color-accent-2);
		stroke-width: 1;
		opacity: 0.4;
	}
	/* hub-dot: this star, anchoring the fan — no label of its own (the page
	   it sits on already says whose neighbors these are) and no click
	   target, just the visual anchor the lines fan out from. */
	.hub-dot {
		position: absolute;
		transform: translate(-50%, -50%);
		width: 10px;
		height: 10px;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		box-shadow: 0 0 9px 1px color-mix(in srgb, var(--color-accent) 45%, transparent);
	}
	.node {
		position: absolute;
		transform: translate(0, -50%);
		display: flex;
		align-items: center;
		gap: 7px;
		background: none;
		border: none;
		padding: 4px 4px 4px 0;
		margin: -4px 0;
		cursor: pointer;
		font: inherit;
		text-align: left;
		overflow: hidden;
	}
	.node .dot {
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		background: var(--color-accent-2);
		box-shadow: 0 0 7px 1px color-mix(in srgb, var(--color-accent-2) 45%, transparent);
		flex-shrink: 0;
	}
	.node.personal .dot {
		background: var(--color-personal);
		box-shadow: 0 0 7px 1px color-mix(in srgb, var(--color-personal) 45%, transparent);
	}
	.node .label {
		flex: 1;
		min-width: 0;
		font-size: 11.5px;
		line-height: 1.25;
		color: var(--color-text);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
