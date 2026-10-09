import { describe, it, expect } from 'vitest';
import { layoutFlow } from './flowLayout';
import type { FlowEdge, FlowNode } from './types';

const node = (n: string, t = n.toUpperCase()): FlowNode => ({ n, t, decision: false, src: [] });
const edge = (from: string, to: string, l?: string): FlowEdge => ({ from, to, l });
const ids = (l: ReturnType<typeof layoutFlow>) => l.layers.map((layer) => layer.map((c) => c.node.n));

describe('layoutFlow', () => {
	it('is empty for no nodes', () => {
		expect(layoutFlow([], [])).toEqual({ layers: [], waiting: [] });
	});

	it('lays a chain out one node per layer', () => {
		const l = layoutFlow([node('a'), node('b'), node('c')], [edge('a', 'b'), edge('b', 'c')]);
		expect(ids(l)).toEqual([['a'], ['b'], ['c']]);
		expect(l.waiting).toEqual([]);
	});

	it('puts branches in one layer, labelled by the edge that led to them', () => {
		const l = layoutFlow([node('a'), node('y'), node('n')], [edge('a', 'y', 'Yes'), edge('a', 'n', 'No')]);
		expect(ids(l)).toEqual([['a'], ['y', 'n']]);
		expect(l.layers[1].map((c) => c.label)).toEqual(['Yes', 'No']);
	});

	it('shows a node with no incoming edge as waiting, then places it when its edge arrives', () => {
		const nodes = [node('a'), node('b')];
		expect(layoutFlow(nodes, []).waiting.map((n) => n.n)).toEqual(['b']);
		expect(layoutFlow(nodes, [edge('a', 'b')]).waiting).toEqual([]);
	});

	it('ignores an edge naming a node that has not arrived yet', () => {
		const l = layoutFlow([node('a')], [edge('a', 'later')]);
		expect(ids(l)).toEqual([['a']]);
	});

	it('records a back-edge as a note on the source and does not recurse a cycle', () => {
		const l = layoutFlow([node('a', 'Check'), node('b', 'Fix')], [edge('a', 'b'), edge('b', 'a', 'Retry')]);
		expect(ids(l)).toEqual([['a'], ['b']]);
		expect(l.layers[1][0].back).toEqual(['Check']);
	});

	it('does not note a merge of two branches as a back-edge', () => {
		const l = layoutFlow(
			[node('a'), node('x'), node('y'), node('z')],
			[edge('a', 'x'), edge('a', 'y'), edge('x', 'z'), edge('y', 'z')]
		);
		expect(ids(l)).toEqual([['a'], ['x', 'y'], ['z']]);
		expect(l.layers.flat().every((c) => c.back.length === 0)).toBe(true);
	});

	it('is total: a self-contained cycle that never reaches the first node leaves it waiting', () => {
		const l = layoutFlow([node('a'), node('b'), node('c')], [edge('b', 'c'), edge('c', 'b')]);
		expect(ids(l)).toEqual([['a']]);
		expect(l.waiting.map((n) => n.n)).toEqual(['b', 'c']);
	});
});
