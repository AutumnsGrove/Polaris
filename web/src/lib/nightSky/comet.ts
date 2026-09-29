import type { SkyConfig } from './config';
import { between, type Rng } from './rng';

export interface CometPose {
	x: number;
	y: number;
	// Direction the comet travels, radians.
	angle: number;
	// 1 while crossing, easing to 0 as it fades after the head leaves.
	fade: number;
	tailLength: number;
}

// Comet emits an occasional slow crossing on a random arc. Stateful but pure:
// give it a time and a size, get back where to draw it (or null between crossings).
export class Comet {
	private next: number;
	private run: { t0: number; x0: number; y0: number; dx: number; dy: number } | null = null;

	constructor(
		private cfg: SkyConfig['comet'],
		private rng: Rng
	) {
		this.next = cfg.firstDelay;
	}

	pose(t: number, w: number, h: number): CometPose | null {
		if (!this.run && t >= this.next) {
			this.run = {
				t0: t,
				x0: w * between(this.rng, 0.55, 1.05),
				y0: h * between(this.rng, 0.06, 0.36),
				dx: -w * 0.9,
				dy: h * between(this.rng, 0.2, 0.32)
			};
		}
		if (!this.run) return null;
		const p = (t - this.run.t0) / this.cfg.duration;
		if (p > 1.35) {
			this.run = null;
			this.next = t + between(this.rng, this.cfg.gapMin, this.cfg.gapMax);
			return null;
		}
		const eased = 1 - Math.pow(1 - Math.min(p, 1), 2);
		return {
			x: this.run.x0 + this.run.dx * eased,
			y: this.run.y0 + this.run.dy * eased,
			angle: Math.atan2(this.run.dy, this.run.dx),
			fade: p < 0.85 ? 1 : Math.max(0, 1 - (p - 0.85) / 0.5),
			tailLength: Math.min(w, h) * this.cfg.tailFrac
		};
	}
}
