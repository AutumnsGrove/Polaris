import { describe, expect, it } from 'vitest';
import { Comet } from './comet';
import { SKY } from './config';
import { CONSTELLATIONS, normalize } from './constellations';
import { SkyDirector, litTimes, revealTime, type Viewport } from './director';
import { intersects } from './placement';
import { mulberry32 } from './rng';

const vp: Viewport = { w: 390, h: 780, avoid: [{ x: 20, y: 290, w: 350, h: 200 }] };

// Runs the director in 0.25s steps and records the peak overlap facts.
function simulate(seed: number, seconds: number) {
	const d = new SkyDirector(SKY, CONSTELLATIONS, mulberry32(seed));
	let maxActive = 0;
	let dupes = 0;
	let overlaps = 0;
	let onContent = 0;
	const names = new Set<string>();
	for (let t = 0; t < seconds; t += 0.25) {
		d.update(t, vp);
		maxActive = Math.max(maxActive, d.active.length);
		const seen = d.active.map((i) => i.shape.name);
		if (new Set(seen).size !== seen.length) dupes++;
		seen.forEach((n) => names.add(n));
		d.active.forEach((a, i) => {
			if (intersects(a.rect, vp.avoid[0])) onContent++;
			d.active.slice(i + 1).forEach((b) => {
				if (intersects(a.rect, b.rect)) overlaps++;
			});
		});
	}
	return { maxActive, dupes, overlaps, onContent, names };
}

describe('SkyDirector', () => {
	it('never shows the same constellation twice at once, or overlaps anything', () => {
		for (let seed = 1; seed <= 25; seed++) {
			const r = simulate(seed, 600);
			expect(r.dupes).toBe(0);
			expect(r.overlaps).toBe(0);
			expect(r.onContent).toBe(0);
		}
	});

	it('respects the concurrency cap', () => {
		for (let seed = 1; seed <= 25; seed++) {
			expect(simulate(seed, 600).maxActive).toBeLessThanOrEqual(SKY.constellations.maxConcurrent);
		}
	});

	it('eventually rotates through the whole pool', () => {
		expect(simulate(7, 1500).names.size).toBe(CONSTELLATIONS.length);
	});

	it('waits for firstDelay before showing anything', () => {
		const d = new SkyDirector(SKY, CONSTELLATIONS, mulberry32(3));
		d.update(SKY.constellations.firstDelay - 0.1, vp);
		expect(d.active).toHaveLength(0);
		d.update(SKY.constellations.firstDelay, vp);
		expect(d.active).toHaveLength(1);
	});

	it('retries soon, not never, when there is no room', () => {
		const blocked: Viewport = { w: 390, h: 780, avoid: [{ x: 0, y: 0, w: 390, h: 780 }] };
		const d = new SkyDirector(SKY, CONSTELLATIONS, mulberry32(3));
		d.update(SKY.constellations.firstDelay, blocked);
		expect(d.active).toHaveLength(0);
		d.update(SKY.constellations.firstDelay + SKY.placement.retryDelay, vp);
		expect(d.active).toHaveLength(1);
	});

	it('removes a constellation once its full cycle has played', () => {
		const d = new SkyDirector(SKY, CONSTELLATIONS, mulberry32(3));
		d.update(SKY.constellations.firstDelay, vp);
		const inst = d.active[0];
		d.update(inst.start + inst.total + 0.1, vp);
		expect(d.active).not.toContain(inst);
	});
});

describe('revealTime', () => {
	const d = new SkyDirector(SKY, CONSTELLATIONS, mulberry32(3));
	d.update(SKY.constellations.firstDelay, vp);
	const inst = d.active[0];
	const hold = SKY.constellations.hold;

	it('retracts along exactly the path it drew in', () => {
		for (const dt of [0.3, 1.1, inst.drawTime - 0.2]) {
			const drawing = revealTime(inst, inst.start + dt, hold);
			const retracting = revealTime(inst, inst.start + inst.drawTime + hold + (inst.drawTime - dt), hold);
			expect(retracting).toBeCloseTo(drawing);
		}
	});

	it('holds fully drawn between draw-in and retract', () => {
		expect(revealTime(inst, inst.start + inst.drawTime + hold / 2, hold)).toBe(inst.drawTime);
	});

	it('starts and ends fully hidden', () => {
		expect(revealTime(inst, inst.start, hold)).toBe(0);
		expect(revealTime(inst, inst.start + inst.total, hold)).toBeCloseTo(0);
	});
});

describe('litTimes', () => {
	it('lights a star when the first line touching it begins', () => {
		const shape = normalize({ name: 'T', points: [[0, 0], [1, 0], [2, 0]], edges: [[0, 1], [1, 2]] });
		const t = litTimes(shape, 0.5);
		expect(t[0]).toBe(0);
		expect(t[1]).toBeCloseTo(0.35); // end of edge 0 beats start of edge 1 (0.5)
		expect(t[2]).toBeCloseTo(0.85);
	});

	it('gives an unconnected point a finite time instead of Infinity', () => {
		const shape = normalize({ name: 'T', points: [[0, 0], [1, 0], [2, 2]], edges: [[0, 1]] });
		expect(litTimes(shape, 0.5)[2]).toBe(0);
	});
});

describe('Comet', () => {
	it('stays quiet until firstDelay, crosses, then waits out a gap', () => {
		const c = new Comet(SKY.comet, mulberry32(5));
		expect(c.pose(SKY.comet.firstDelay - 1, 390, 780)).toBeNull();
		expect(c.pose(SKY.comet.firstDelay, 390, 780)).not.toBeNull();
		const after = SKY.comet.firstDelay + SKY.comet.duration * 1.4;
		expect(c.pose(after, 390, 780)).toBeNull();
		expect(c.pose(after + SKY.comet.gapMin - 1, 390, 780)).toBeNull();
	});
});

describe('constellation pool', () => {
	it('has valid edge indices and unique names for every entry', () => {
		const names = new Set<string>();
		for (const def of CONSTELLATIONS) {
			expect(names.has(def.name)).toBe(false);
			names.add(def.name);
			for (const [a, b] of def.edges) {
				expect(a).toBeLessThan(def.points.length);
				expect(b).toBeLessThan(def.points.length);
			}
		}
	});
});
