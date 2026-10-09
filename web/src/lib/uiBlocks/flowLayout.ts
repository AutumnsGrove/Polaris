import type { FlowEdge, FlowNode } from './types';

// Layout for a `flow` block, kept out of the component so the tricky cases
// (cycles, edges to nodes that have not streamed in, merges) are unit-tested
// without a DOM. docs/plans/intelligent-ui.md, "`flow`", sets the rules:
//
//  - BFS from the first node: a layer of one node is a card, a layer of several
//    is a side-by-side branch row, and the edge label sits above the node it
//    leads to.
//  - A node no known edge reaches yet is "waiting": it renders dotted below the
//    chain and slots in when its edge arrives.
//  - A back-edge (to a node in the same or an earlier layer) is never drawn as a
//    line; the source gets a "back to <title>" note. Cycles are legal and are
//    never recursed into, because each node is placed exactly once.

export interface FlowCell {
	node: FlowNode;
	/** Label of the edge that placed this node, shown above it. */
	label?: string;
	/** Titles this node loops back to. */
	back: string[];
}

export interface FlowLayout {
	layers: FlowCell[][];
	waiting: FlowNode[];
}

export function layoutFlow(nodes: FlowNode[], edges: FlowEdge[]): FlowLayout {
	if (nodes.length === 0) return { layers: [], waiting: [] };

	const byId = new Map(nodes.map((n) => [n.n, n]));
	const layerOf = new Map<string, number>();
	const layers: FlowCell[][] = [[{ node: nodes[0], back: [] }]];
	layerOf.set(nodes[0].n, 0);

	for (let i = 0; i < layers.length; i++) {
		const next: FlowCell[] = [];
		for (const cell of layers[i]) {
			for (const e of edges) {
				if (e.from !== cell.node.n) continue;
				const target = byId.get(e.to);
				// The target may simply not have streamed in yet.
				if (!target) continue;
				const placed = layerOf.get(target.n);
				if (placed === undefined) {
					layerOf.set(target.n, i + 1);
					next.push({ node: target, label: e.l, back: [] });
				} else if (placed <= i && !cell.back.includes(target.t)) {
					cell.back.push(target.t);
				}
				// placed > i: two branches merging into one node, nothing to draw.
			}
		}
		if (next.length) layers.push(next);
	}

	return { layers, waiting: nodes.filter((n) => !layerOf.has(n.n)) };
}
