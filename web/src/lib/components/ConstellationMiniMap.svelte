<script lang="ts">
	import { goto } from '$app/navigation';
	import { layoutStars } from '$lib/constellationLayout';
	import type { Star, StarEdge } from '$lib/types';

	// ConstellationMiniMap is the Star detail screen's "Nearby in the
	// constellation" panel (mockup screen 2's .constellation-block) — a
	// bounded, non-interactive-feeling preview of the same force-directed
	// layout the full Map page uses, scoped to just this star and its
	// direct neighbors. Takes exactly what GET /api/constellation/stars/{id}
	// already returns (centerStar + its own edges) plus each neighbor's
	// Star object (fetched by the caller, star/[id]/+page.svelte, via a
	// few bounded loadStarDetail calls) — deliberately not the full
	// map dataset, which would be a much heavier fetch for a 3-node
	// preview.
	let {
		centerStar,
		neighborStars,
		edges
	}: {
		centerStar: Star;
		neighborStars: Star[];
		edges: StarEdge[];
	} = $props();

	const WIDTH = 320;
	const HEIGHT = 90;

	const layout = $derived.by(() => {
		const stars = [centerStar, ...neighborStars];
		const edgePairs = edges
			.filter((e) => neighborStars.some((s) => s.id === e.other_star_id))
			.map((e) => ({ star_a_id: centerStar.id, star_b_id: e.other_star_id, reasoning: e.reasoning }));
		// padding: layoutStars' default (60) assumes a canvas tall enough
		// to spare that much margin on every side — this panel is only 90px
		// tall, so the default would invert the usable height negative.
		// 20 leaves enough room for a node's label without clipping at this
		// scale.
		return layoutStars(stars, edgePairs, { width: WIDTH, height: HEIGHT, padding: 20 });
	});
</script>

{#if layout.nodes.length > 1}
	<div class="constellation-block">
		<div class="constellation-label">{layout.nodes.length - 1} related star{layout.nodes.length === 2 ? '' : 's'}</div>
		<div class="canvas" style="width: {WIDTH}px; height: {HEIGHT}px;">
			<svg class="lines" viewBox="0 0 {WIDTH} {HEIGHT}">
				{#each layout.edges as edge (edge.starAId + '-' + edge.starBId)}
					{@const a = layout.nodeById.get(edge.starAId)}
					{@const b = layout.nodeById.get(edge.starBId)}
					{#if a && b}
						<line x1={a.x} y1={a.y} x2={b.x} y2={b.y} />
					{/if}
				{/each}
			</svg>
			{#each layout.nodes as node (node.id)}
				<button
					class="node"
					class:self={node.id === centerStar.id}
					class:personal={node.star.is_personal}
					style="left: {node.x}px; top: {node.y}px;"
					onclick={() => node.id !== centerStar.id && goto(`/constellation/star/${node.id}`)}
					disabled={node.id === centerStar.id}
				>
					<span class="dot"></span>
					<span class="label">{node.star.title}</span>
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
		margin: 0 auto;
	}
	.lines {
		position: absolute;
		inset: 0;
		pointer-events: none;
	}
	.lines line {
		stroke: var(--color-accent-2);
		stroke-width: 1;
		opacity: 0.4;
	}
	.node {
		position: absolute;
		transform: translate(-50%, -50%);
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 6px;
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		font: inherit;
		width: 90px;
	}
	.node:disabled {
		cursor: default;
	}
	.node .dot {
		width: 9px;
		height: 9px;
		border-radius: var(--radius-full);
		background: var(--color-accent-2);
		box-shadow: 0 0 8px 1px color-mix(in srgb, var(--color-accent-2) 45%, transparent);
	}
	.node.self .dot {
		background: var(--color-accent);
		box-shadow: 0 0 8px 1px color-mix(in srgb, var(--color-accent) 45%, transparent);
	}
	.node.personal .dot {
		background: var(--color-personal);
		box-shadow: 0 0 8px 1px color-mix(in srgb, var(--color-personal) 45%, transparent);
	}
	.node .label {
		font-size: 11px;
		text-align: center;
		color: var(--color-text);
		line-height: 1.2;
	}
	.node.self .label {
		font-weight: 600;
	}
</style>
