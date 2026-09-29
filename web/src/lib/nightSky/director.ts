import type { SkyConfig } from './config';
import { normalize, type ConstellationDef, type UnitShape } from './constellations';
import { orient, pickPlacement, type Placed, type Point, type Rect } from './placement';
import { between, type Rng } from './rng';

// One constellation currently on screen.
export interface SkyInstance {
	shape: UnitShape;
	placed: Placed;
	// Footprint including the label below; the region no other constellation may enter.
	rect: Rect;
	// Offset of the shape's own top-left inside `rect` (centers it above the label).
	inset: number;
	start: number;
	drawTime: number;
	total: number;
	// Per-point time (seconds into the draw) at which each star lights.
	litAt: number[];
}

export interface Viewport {
	w: number;
	h: number;
	avoid: Rect[];
}

// revealTime maps wall-clock time to "how far along the draw-in animation is
// this constellation". Drawing plays 0 -> drawTime, holds there, then plays
// back drawTime -> 0, so the retract is literally the draw-in run backwards
// with every edge and star reusing the same progress math.
export function revealTime(inst: SkyInstance, t: number, hold: number): number {
	const l = t - inst.start;
	if (l < inst.drawTime) return Math.max(0, l);
	if (l < inst.drawTime + hold) return inst.drawTime;
	return Math.max(0, inst.drawTime - (l - inst.drawTime - hold));
}

// litTimes: a star lights when the first edge touching it begins (as a start
// point) or nearly finishes (as an end point), so lighting order follows the
// drawn lines rather than the arbitrary order of the points array.
export function litTimes(shape: UnitShape, secondsPerEdge: number): number[] {
	const lit = shape.points.map(() => Infinity);
	shape.edges.forEach(([a, b], i) => {
		lit[a] = Math.min(lit[a], i * secondsPerEdge);
		lit[b] = Math.min(lit[b], (i + 1) * secondsPerEdge - 0.15);
	});
	return lit.map((v) => (Number.isFinite(v) ? v : 0));
}

// SkyDirector owns which constellations are on screen and where. Pure logic
// (no canvas, no DOM): the component feeds it time and the current viewport
// and draws whatever `active` holds.
export class SkyDirector {
	active: SkyInstance[] = [];
	private nextSpawn: number;
	private recent: Point[] = [];
	private pool: UnitShape[];

	constructor(
		private cfg: SkyConfig,
		defs: ConstellationDef[],
		private rng: Rng
	) {
		this.pool = defs.map(normalize);
		this.nextSpawn = cfg.constellations.firstDelay;
	}

	update(t: number, vp: Viewport): void {
		const c = this.cfg.constellations;
		this.active = this.active.filter((i) => t - i.start <= i.total);
		if (t < this.nextSpawn || this.active.length >= c.maxConcurrent) return;
		this.nextSpawn = this.spawn(t, vp)
			? t + between(this.rng, c.spawnGapMin, c.spawnGapMax)
			: t + this.cfg.placement.retryDelay;
	}

	// spawn places one constellation not already on screen. False when the pool
	// is exhausted or nothing fits.
	spawn(t: number, vp: Viewport): boolean {
		const c = this.cfg.constellations;
		const showing = new Set(this.active.map((i) => i.shape));
		const free = this.pool.filter((s) => !showing.has(s));
		if (!free.length) return false;
		const shape = free[Math.floor(this.rng() * free.length)];

		const base = Math.min(vp.w, vp.h * 0.6) * c.sizeFrac;
		const size =
			Math.min(c.maxSizePx, Math.max(c.minSizePx, base)) *
			between(this.rng, c.sizeJitter[0], c.sizeJitter[1]);
		const tilt = (between(this.rng, -c.maxTiltDeg, c.maxTiltDeg) * Math.PI) / 180;
		const placed = orient(shape, size, this.rng() < 0.5, tilt);

		const footprintW = Math.max(placed.w, shape.name.length * c.labelCharPx);
		const footprintH = placed.h + c.labelHeightPx;
		const spot = pickPlacement(
			{
				w: footprintW,
				h: footprintH,
				viewport: { w: vp.w, h: vp.h },
				avoid: vp.avoid,
				others: this.active.map((i) => i.rect),
				recent: this.recent
			},
			this.cfg.placement,
			this.rng
		);
		if (!spot) return false;

		const drawTime = shape.edges.length * c.secondsPerEdge + 0.4;
		this.active.push({
			shape,
			placed,
			rect: spot,
			inset: (footprintW - placed.w) / 2,
			start: t,
			drawTime,
			total: drawTime * 2 + c.hold,
			litAt: litTimes(shape, c.secondsPerEdge)
		});
		this.recent.push({ x: spot.x + spot.w / 2, y: spot.y + spot.h / 2 });
		if (this.recent.length > this.cfg.placement.recentMemory) this.recent.shift();
		return true;
	}
}
