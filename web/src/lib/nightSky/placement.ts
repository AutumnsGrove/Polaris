import type { SkyConfig } from './config';
import type { UnitShape } from './constellations';
import { between, type Rng } from './rng';

export interface Rect {
	x: number;
	y: number;
	w: number;
	h: number;
}
export interface Point {
	x: number;
	y: number;
}

// intersects reports whether two rects overlap once `pad` px of clear space is
// added around each one (so two rects that merely sit close count as clashing).
export function intersects(a: Rect, b: Rect, pad = 0): boolean {
	return (
		a.x - pad < b.x + b.w + pad &&
		a.x + a.w + pad > b.x - pad &&
		a.y - pad < b.y + b.h + pad &&
		a.y + a.h + pad > b.y - pad
	);
}

export interface Placed {
	// Points relative to the shape's own top-left, already mirrored, tilted and scaled.
	points: [number, number][];
	w: number;
	h: number;
}

// orient mirrors, tilts (radians, about the shape's center) and scales a unit
// shape, then re-anchors it so its bounding box starts at (0, 0). Mirror and
// tilt are what let 8 shapes look like many more across a session.
export function orient(shape: UnitShape, size: number, mirror: boolean, tilt: number): Placed {
	const cos = Math.cos(tilt);
	const sin = Math.sin(tilt);
	const rotated = shape.points.map(([px, py]) => {
		const x = (mirror ? 1 - px : px) - 0.5;
		const y = py - 0.5;
		return [x * cos - y * sin, x * sin + y * cos] as [number, number];
	});
	const x0 = Math.min(...rotated.map((p) => p[0]));
	const y0 = Math.min(...rotated.map((p) => p[1]));
	const points = rotated.map(([x, y]) => [(x - x0) * size, (y - y0) * size] as [number, number]);
	return {
		points,
		w: Math.max(...points.map((p) => p[0])),
		h: Math.max(...points.map((p) => p[1]))
	};
}

export interface PlacementRequest {
	// Footprint to place (shape plus its label), in px.
	w: number;
	h: number;
	viewport: { w: number; h: number };
	// Rects that must stay clear (heading, composer).
	avoid: Rect[];
	// Rects of constellations already on screen.
	others: Rect[];
	// Centers of recently used spots, newest last.
	recent: Point[];
}

const center = (r: Rect): Point => ({ x: r.x + r.w / 2, y: r.y + r.h / 2 });

// pickPlacement finds a spot for a footprint that overlaps neither the content
// nor any other constellation, preferring the one farthest from everything
// already showing and from recent spots. Returns null when nothing fits (a
// tiny window, or a screen the content already fills) so the caller can retry
// later instead of forcing an overlap.
export function pickPlacement(
	req: PlacementRequest,
	cfg: SkyConfig['placement'],
	rng: Rng
): Rect | null {
	const { w, h, viewport } = req;
	const minX = cfg.edgeMarginPx;
	const minY = cfg.edgeMarginPx;
	const maxX = viewport.w - cfg.edgeMarginPx - w;
	const maxY = viewport.h - cfg.edgeMarginPx - h;
	if (maxX < minX || maxY < minY) return null;

	const anchors = [...req.others.map(center), ...req.recent];
	let best: Rect | null = null;
	let bestScore = -1;
	let found = 0;
	for (let i = 0; i < cfg.tries && found < cfg.candidates; i++) {
		const cand: Rect = { x: between(rng, minX, maxX), y: between(rng, minY, maxY), w, h };
		if (req.avoid.some((r) => intersects(cand, r, cfg.avoidPadPx / 2))) continue;
		if (req.others.some((r) => intersects(cand, r, cfg.gapPx / 2))) continue;
		found++;
		const c = center(cand);
		const score = anchors.length
			? Math.min(...anchors.map((a) => Math.hypot(a.x - c.x, a.y - c.y)))
			: Infinity;
		if (score > bestScore) {
			bestScore = score;
			best = cand;
		}
	}
	return best;
}
