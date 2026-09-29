import { describe, expect, it } from 'vitest';
import { SKY } from './config';
import { normalize } from './constellations';
import { intersects, orient, pickPlacement, type Rect } from './placement';
import { mulberry32 } from './rng';

const viewport = { w: 390, h: 780 };
// Roughly where the heading + composer sit on a phone.
const content: Rect[] = [{ x: 20, y: 280, w: 350, h: 220 }];

describe('intersects', () => {
	it('treats rects closer than the padding as clashing', () => {
		const a = { x: 0, y: 0, w: 10, h: 10 };
		const b = { x: 14, y: 0, w: 10, h: 10 };
		expect(intersects(a, b, 0)).toBe(false);
		expect(intersects(a, b, 3)).toBe(true);
	});

	it('does not clash for rects far apart', () => {
		expect(intersects({ x: 0, y: 0, w: 5, h: 5 }, { x: 100, y: 100, w: 5, h: 5 }, 10)).toBe(false);
	});
});

describe('orient', () => {
	const shape = normalize({
		name: 'T',
		points: [[0, 0], [1, 0], [1, 0.5]],
		edges: [[0, 1], [1, 2]]
	});

	it('anchors the bounding box at the origin and scales to the requested size', () => {
		const p = orient(shape, 100, false, 0);
		expect(Math.min(...p.points.map((q) => q[0]))).toBeCloseTo(0);
		expect(Math.min(...p.points.map((q) => q[1]))).toBeCloseTo(0);
		expect(p.w).toBeCloseTo(100);
		expect(p.h).toBeCloseTo(50);
	});

	it('mirroring swaps which side the third point sits on', () => {
		const flat = orient(shape, 100, false, 0);
		const flipped = orient(shape, 100, true, 0);
		expect(flat.points[2][0]).toBeCloseTo(100);
		expect(flipped.points[2][0]).toBeCloseTo(0);
	});

	it('keeps every point inside the reported box after tilting', () => {
		const p = orient(shape, 100, false, 0.5);
		for (const [x, y] of p.points) {
			expect(x).toBeGreaterThanOrEqual(-1e-9);
			expect(y).toBeGreaterThanOrEqual(-1e-9);
			expect(x).toBeLessThanOrEqual(p.w + 1e-9);
			expect(y).toBeLessThanOrEqual(p.h + 1e-9);
		}
	});
});

describe('pickPlacement', () => {
	const req = (over: Partial<Parameters<typeof pickPlacement>[0]> = {}) => ({
		w: 120,
		h: 100,
		viewport,
		avoid: content,
		others: [],
		recent: [],
		...over
	});

	it('never lands on the content or off the edge, across many seeds', () => {
		for (let seed = 1; seed <= 200; seed++) {
			const r = pickPlacement(req(), SKY.placement, mulberry32(seed));
			expect(r).not.toBeNull();
			if (!r) continue;
			expect(intersects(r, content[0])).toBe(false);
			expect(r.x).toBeGreaterThanOrEqual(SKY.placement.edgeMarginPx);
			expect(r.y).toBeGreaterThanOrEqual(SKY.placement.edgeMarginPx);
			expect(r.x + r.w).toBeLessThanOrEqual(viewport.w - SKY.placement.edgeMarginPx);
			expect(r.y + r.h).toBeLessThanOrEqual(viewport.h - SKY.placement.edgeMarginPx);
		}
	});

	it('keeps clear of constellations already on screen', () => {
		const others: Rect[] = [{ x: 200, y: 20, w: 150, h: 120 }];
		for (let seed = 1; seed <= 200; seed++) {
			const r = pickPlacement(req({ others }), SKY.placement, mulberry32(seed));
			if (r) expect(intersects(r, others[0], SKY.placement.gapPx / 2)).toBe(false);
		}
	});

	it('returns null instead of overlapping when nothing fits', () => {
		const fullScreen: Rect[] = [{ x: 0, y: 0, w: viewport.w, h: viewport.h }];
		expect(pickPlacement(req({ avoid: fullScreen }), SKY.placement, mulberry32(1))).toBeNull();
	});

	it('returns null when the footprint is larger than the viewport', () => {
		expect(pickPlacement(req({ w: 999 }), SKY.placement, mulberry32(1))).toBeNull();
	});

	it('spreads placements out instead of reusing a few homes', () => {
		const seen = new Set<string>();
		for (let seed = 1; seed <= 40; seed++) {
			const r = pickPlacement(req(), SKY.placement, mulberry32(seed));
			if (r) seen.add(`${Math.round(r.x / 40)},${Math.round(r.y / 40)}`);
		}
		expect(seen.size).toBeGreaterThan(8);
	});
});
