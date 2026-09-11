import { forceSimulation, forceLink, forceManyBody, forceX, forceY, forceCollide } from 'd3-force';
import type { Star, StarEdgePair } from './types';

// layoutStars turns Constellation's stars/edges into real (x, y)
// positions for the Map page and the Star-detail mini-map, sharing one
// force-directed layout instead of the mockup's hand-placed coordinates
// (which don't exist for real, unbounded data). star_edges is treated as
// a genuine graph — linked stars pull toward each other via forceLink, so
// the map actually reflects the "constellation" metaphor instead of being
// a plain tag cloud grouped by category.
//
// Determinism: d3-force never calls Math.random() unless two nodes land
// on the exact same coordinate (a rare tie-break jiggle inside
// forceManyBody/forceCollide) — a node with no x/y set is otherwise
// placed by a deterministic phyllotaxis spiral keyed to its *index* in
// the nodes array. Sorting stars by id before building that array is
// therefore what actually makes this reproducible across reloads for the
// same data, not anything explicit here — same input order in, same
// layout out.
const TICKS = 300;
const LINK_DISTANCE = 70;
const CHARGE_STRENGTH = -90;
const COLLIDE_RADIUS = 26;
const CLUSTER_STRENGTH = 0.12;
// Minimum halo radius (a lone star in its own category still reads as a
// cluster) and the margin added beyond the farthest member, so a dot right
// at the edge doesn't render flush against the glow's boundary.
const MIN_CLUSTER_RADIUS = 34;
const CLUSTER_HALO_PAD = 22;

export interface LayoutNode {
	id: number;
	star: Star;
	x: number;
	y: number;
}

export interface LayoutEdge {
	starAId: number;
	starBId: number;
	reasoning: string;
}

export interface ClusterLabel {
	category: string;
	x: number;
	y: number;
	// radius: how far this category's halo glow extends — the centroid-to-
	// farthest-member distance plus a fixed pad, not a fixed constant, so a
	// tightly-packed category doesn't get an oversized halo and a spread-out
	// one doesn't get clipped. See CLUSTER_HALO_PAD below.
	radius: number;
}

export interface LayoutResult {
	nodes: LayoutNode[];
	nodeById: Map<number, LayoutNode>;
	edges: LayoutEdge[];
	clusterLabels: ClusterLabel[];
}

export interface LayoutOptions {
	width: number;
	height: number;
	// padding: how close a node's center is allowed to get to the canvas
	// edge after normalization — keeps a node's label (rendered to the
	// right of its dot, not centered on it) from getting clipped by the
	// container's own edge.
	padding: number;
}

const DEFAULT_OPTIONS: LayoutOptions = { width: 360, height: 620, padding: 60 };

export function layoutStars(
	stars: Star[],
	edgePairs: StarEdgePair[],
	options: Partial<LayoutOptions> = {}
): LayoutResult {
	const opts = { ...DEFAULT_OPTIONS, ...options };
	if (stars.length === 0) return { nodes: [], nodeById: new Map(), edges: [], clusterLabels: [] };

	const sorted = [...stars].sort((a, b) => a.id - b.id);
	const nodes: LayoutNode[] = sorted.map((star) => ({ id: star.id, star, x: 0, y: 0 }));
	const nodeById = new Map(nodes.map((n) => [n.id, n]));

	// d3-force's forceLink mutates each link's source/target from a plain
	// id into the resolved node object once the force initializes — done
	// with our own separate array so the LayoutEdge type returned to
	// callers stays plain ids/reasoning, not d3-internal node references.
	const simLinks = edgePairs
		.filter((e) => nodeById.has(e.star_a_id) && nodeById.has(e.star_b_id))
		.map((e) => ({ source: e.star_a_id, target: e.star_b_id }));

	// Category clustering: each distinct category anchors to a fixed point
	// on a ring around the canvas center, so the mockup's grouped-by-
	// category feel survives alongside edge-driven placement — a personal
	// star with no edges still lands near its own topic cluster instead of
	// drifting to the canvas origin.
	const categories = [...new Set(sorted.map((s) => s.category))].sort();
	const cx = opts.width / 2;
	const cy = opts.height / 2;
	const clusterRadius = Math.min(opts.width, opts.height) * 0.32;
	const anchors = new Map<string, { x: number; y: number }>();
	categories.forEach((cat, i) => {
		const angle = (i / Math.max(categories.length, 1)) * 2 * Math.PI;
		anchors.set(cat, {
			x: cx + clusterRadius * Math.cos(angle),
			y: cy + clusterRadius * Math.sin(angle)
		});
	});
	const anchorFor = (n: LayoutNode) => anchors.get(n.star.category) ?? { x: cx, y: cy };

	const sim = forceSimulation(nodes)
		.force(
			'link',
			forceLink(simLinks)
				.id((d: unknown) => (d as LayoutNode).id)
				.distance(LINK_DISTANCE)
				.strength(0.6)
		)
		.force('charge', forceManyBody().strength(CHARGE_STRENGTH))
		.force('collide', forceCollide(COLLIDE_RADIUS))
		.force(
			'x',
			forceX<LayoutNode>((d) => anchorFor(d).x).strength(CLUSTER_STRENGTH)
		)
		.force(
			'y',
			forceY<LayoutNode>((d) => anchorFor(d).y).strength(CLUSTER_STRENGTH)
		)
		.stop();

	for (let i = 0; i < TICKS; i++) sim.tick();

	// forceManyBody's repulsion has no outer boundary — nothing above
	// stops a node from settling well outside [0, width] x [0, height],
	// which is exactly what was happening (nodes rendering behind the
	// sidebar, off the right edge, etc.): the SVG viewBox/container clips
	// at the nominal canvas size, but the simulation itself never knew
	// that size was a hard limit. Rescaling+translating the whole
	// point set to fit [padding, width-padding] x [padding, height-padding]
	// after the fact is a plain affine transform — every relative
	// distance/clustering the simulation produced is preserved, it just
	// guarantees the result actually fits the visible box, regardless of
	// how far charge/link forces spread things out.
	//
	// A single uniform scale factor (min of the two axis ratios), not
	// independent x/y scales — using separate scales would stretch/squash
	// the simulation's actual shape to exactly fill a container of any
	// aspect ratio, distorting the relative distances that make "close on
	// the map" mean "actually linked/similar." The unused axis is instead
	// centered within its own target range.
	const pad = opts.padding;
	if (nodes.length > 0) {
		const xs = nodes.map((n) => n.x);
		const ys = nodes.map((n) => n.y);
		const minX = Math.min(...xs);
		const maxX = Math.max(...xs);
		const minY = Math.min(...ys);
		const maxY = Math.max(...ys);
		const spanX = maxX - minX || 1;
		const spanY = maxY - minY || 1;
		const targetW = Math.max(opts.width - pad * 2, 1);
		const targetH = Math.max(opts.height - pad * 2, 1);
		const scale = nodes.length === 1 ? 1 : Math.min(targetW / spanX, targetH / spanY);
		const offsetX = (targetW - spanX * scale) / 2;
		const offsetY = (targetH - spanY * scale) / 2;
		for (const n of nodes) {
			n.x = nodes.length === 1 ? opts.width / 2 : pad + offsetX + (n.x - minX) * scale;
			n.y = nodes.length === 1 ? opts.height / 2 : pad + offsetY + (n.y - minY) * scale;
		}
	}

	const edges: LayoutEdge[] = edgePairs
		.filter((e) => nodeById.has(e.star_a_id) && nodeById.has(e.star_b_id))
		.map((e) => ({ starAId: e.star_a_id, starBId: e.star_b_id, reasoning: e.reasoning }));

	// clusterLabels: the centroid of each category's actual (post-
	// normalize) node positions — not the pre-simulation anchor points
	// above, which the link/charge forces routinely pull nodes away from.
	// Only emitted once there's more than one category, matching the
	// mockup's intent (a single-category library doesn't need a label
	// pointing at everything on screen).
	const clusterLabels: ClusterLabel[] = [];
	if (categories.length > 1) {
		for (const cat of categories) {
			const inCat = nodes.filter((n) => n.star.category === cat);
			if (inCat.length === 0) continue;
			const cx2 = inCat.reduce((sum, n) => sum + n.x, 0) / inCat.length;
			const cy2 = inCat.reduce((sum, n) => sum + n.y, 0) / inCat.length;
			const farthest = Math.max(...inCat.map((n) => Math.hypot(n.x - cx2, n.y - cy2)));
			clusterLabels.push({
				category: cat,
				x: cx2,
				y: cy2,
				radius: Math.max(MIN_CLUSTER_RADIUS, farthest + CLUSTER_HALO_PAD)
			});
		}
	}

	return { nodes, nodeById, edges, clusterLabels };
}
