<script lang="ts">
	import { goto } from '$app/navigation';
	import type { Star, StarEdge } from '$lib/types';
	import { colorForCategory } from '$lib/categoryColors';

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
	// Half of .node .dot's own width (8px) — the connecting line's x2 is
	// drawn at NEIGHBOR_X, meant to land on the neighbor dot's *center*.
	// .node itself only centers vertically (translateY(-50%) — its width
	// varies with the title's length, so there's no fixed box to center
	// horizontally around); its `left` is a plain box edge. Shifting that
	// edge left by the dot's own radius puts the dot (the row's first,
	// non-shrinking flex child, flush against the box's left edge) exactly
	// on NEIGHBOR_X instead of ~4px to its right, which is what made the
	// line look like it stopped short of the dot instead of touching it.
	const NEIGHBOR_DOT_RADIUS = 4;

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
	const labelMaxWidth = $derived(
		Math.max(80, canvasWidth - (NEIGHBOR_X - NEIGHBOR_DOT_RADIUS) - RIGHT_PAD)
	);
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
					style="left: {NEIGHBOR_X - NEIGHBOR_DOT_RADIUS}px; top: {TOP_PAD +
						i * ROW_HEIGHT}px; max-width: {labelMaxWidth}px; --star-color: {colorForCategory(
						star.category
					)}"
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
		gap: var(--space-sm);
		background: none;
		border: none;
		/* Vertical padding alone enlarges the tap target — translateY(-50%)
		   re-centers on this element's own (padded) height, so the dot
		   stays exactly on its row's y. A matching negative margin (as
		   .map-node above briefly had) would NOT cancel out here: for an
		   absolutely positioned element, `top` places the *margin* edge,
		   so a negative margin pulls the dot away from that y by the same
		   amount — confirmed live as a 4px "too high" drift on every row. */
		padding: var(--space-xs) var(--space-xs) var(--space-xs) 0;
		cursor: pointer;
		font: inherit;
		text-align: left;
		overflow: hidden;
	}
	.node .dot {
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		background: var(--star-color);
		box-shadow: 0 0 7px 1px color-mix(in srgb, var(--star-color) 45%, transparent);
		flex-shrink: 0;
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
