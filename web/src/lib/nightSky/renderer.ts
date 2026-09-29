import type { SkyConfig } from './config';
import type { CometPose } from './comet';
import { revealTime, type SkyInstance } from './director';
import { mulberry32 } from './rng';

// Colors come from the app's own theme tokens (read once per mount) so the sky
// follows any future palette change. Canvas can't resolve var(), and per-shape
// transparency is done with globalAlpha, so these are plain solid colors.
export interface Palette {
	warm: string; // --color-accent
	cool: string; // --color-accent-2
	core: string; // bright star centers
	mono: string; // label font family
}

interface Star {
	x: number;
	y: number;
	r: number;
	phase: number;
	speed: number;
	warm: boolean;
	alpha: number;
}

const TAU = Math.PI * 2;
const clamp = (x: number) => Math.max(0, Math.min(1, x));
const ease = (x: number) => x * x * (3 - 2 * x);

export function readPalette(el: Element): Palette {
	const css = getComputedStyle(el);
	const pick = (name: string, fallback: string) => css.getPropertyValue(name).trim() || fallback;
	return {
		warm: pick('--color-accent', 'oklch(82% 0.17 78)'),
		cool: pick('--color-accent-2', 'oklch(78% 0.06 235)'),
		core: pick('--color-text', 'oklch(93% 0.01 75)'),
		mono: pick('--font-mono', 'ui-monospace, monospace')
	};
}

// SkyRenderer draws one frame of the sky onto a canvas. It holds no schedule of
// its own: the twinkling field is generated once, the Milky Way is pre-rendered
// to an offscreen canvas on resize, and constellations/comet are handed in per
// frame by the component.
export class SkyRenderer {
	private layers: Star[][];
	private bg: HTMLCanvasElement | null = null;
	w = 0;
	h = 0;

	constructor(
		private g: CanvasRenderingContext2D,
		private cfg: SkyConfig,
		private palette: Palette
	) {
		this.layers = cfg.layers.map((L) => {
			const r = mulberry32(L.seed);
			return Array.from({ length: L.count }, () => ({
				x: r(),
				y: r(),
				r: 0.4 + Math.pow(r(), 3) * L.maxRadius,
				phase: r() * TAU,
				speed: 0.4 + r() * 1.4,
				warm: r() < cfg.warmStarChance,
				alpha: 0.25 + r() * 0.5
			}));
		});
	}

	setPalette(p: Palette): void {
		this.palette = p;
		if (this.w) this.buildBackdrop();
	}

	resize(w: number, h: number, dpr: number): void {
		this.w = w;
		this.h = h;
		this.g.setTransform(dpr, 0, 0, dpr, 0, 0);
		this.buildBackdrop();
	}

	// The band and dust never move, so paint them once instead of ~250 fills per frame.
	private buildBackdrop(): void {
		const { w, h } = this;
		const mw = this.cfg.milkyWay;
		const cv = this.bg ?? document.createElement('canvas');
		cv.width = Math.max(1, Math.round(w));
		cv.height = Math.max(1, Math.round(h));
		const g = cv.getContext('2d');
		if (!g) return;
		const diag = Math.hypot(w, h);
		g.save();
		g.translate(w / 2, h / 2);
		g.rotate(mw.tilt);
		const half = diag * mw.halfWidthFrac;
		// Solid color first, then an alpha mask via destination-in: gradient stops
		// can't take theme colors with alpha on every browser, plain black can.
		g.fillStyle = this.palette.cool;
		g.fillRect(-diag, -half, diag * 2, half * 2);
		const mask = g.createLinearGradient(0, -half, 0, half);
		mask.addColorStop(0, 'rgba(0,0,0,0)');
		mask.addColorStop(0.5, `rgba(0,0,0,${mw.washAlpha})`);
		mask.addColorStop(1, 'rgba(0,0,0,0)');
		g.globalCompositeOperation = 'destination-in';
		g.fillStyle = mask;
		g.fillRect(-diag, -half, diag * 2, half * 2);
		g.restore();
		g.globalCompositeOperation = 'source-over';
		const r = mulberry32(77);
		const sn = Math.sin(mw.tilt);
		const cs = Math.cos(mw.tilt);
		g.fillStyle = this.palette.cool;
		for (let i = 0; i < mw.dustCount; i++) {
			const x = r() * w;
			const y = r() * h;
			const d = Math.abs((x - w / 2) * sn + (y - h / 2) * cs) / half;
			if (d < 1) {
				g.globalAlpha = (1 - d) * mw.dustAlpha;
				g.fillRect(x, y, 0.9, 0.9);
			}
		}
		g.globalAlpha = 1;
		this.bg = cv;
	}

	private dot(x: number, y: number, r: number, color: string, alpha: number): void {
		const g = this.g;
		g.globalAlpha = Math.max(0, alpha);
		g.fillStyle = color;
		g.beginPath();
		g.arc(x, y, r, 0, TAU);
		g.fill();
	}

	// A soft glow from three stacked discs of shrinking radius: a single flat
	// low-alpha disc reads as a grey coin, and canvas gradients can't take a
	// theme color with alpha on every browser.
	private glow(x: number, y: number, r: number, color: string, alpha: number): void {
		this.dot(x, y, r, color, alpha * 0.3);
		this.dot(x, y, r * 0.62, color, alpha * 0.45);
		this.dot(x, y, r * 0.32, color, alpha * 0.7);
	}

	draw(
		t: number,
		ptr: { x: number; y: number },
		instances: SkyInstance[],
		comet: CometPose | null
	): void {
		const { g, w, h, palette: pal } = this;
		g.clearRect(0, 0, w, h);
		g.globalAlpha = 1;
		if (this.bg) g.drawImage(this.bg, 0, 0, w, h);

		this.cfg.layers.forEach((L, li) => {
			for (const s of this.layers[li]) {
				const x = ((s.x + t * L.drift) % 1) * w + ptr.x * L.parallaxPx;
				const y = s.y * h + ptr.y * L.parallaxPx;
				const a = s.alpha * (0.65 + 0.35 * Math.sin(t * s.speed + s.phase)) * L.brightness;
				const color = s.warm ? pal.warm : pal.cool;
				this.dot(x, y, s.r, color, a);
				if (s.r > 1.3) this.glow(x, y, s.r * 4, color, a * 0.3);
			}
		});

		for (const inst of instances) this.constellation(inst, t, ptr);
		if (comet) this.drawComet(comet);
		g.globalAlpha = 1;
	}

	private constellation(inst: SkyInstance, t: number, ptr: { x: number; y: number }): void {
		const { g, palette: pal, cfg } = this;
		const c = cfg.constellations;
		const v = revealTime(inst, t, c.hold);
		const ox = inst.rect.x + inst.inset + ptr.x * 14;
		const oy = inst.rect.y + ptr.y * 14;
		const P = (i: number): [number, number] => [
			ox + inst.placed.points[i][0],
			oy + inst.placed.points[i][1]
		];

		g.lineWidth = 1;
		inst.shape.edges.forEach(([ia, ib], i) => {
			const p = ease(clamp((v - i * c.secondsPerEdge) / c.secondsPerEdge));
			if (p <= 0) return;
			const a = P(ia);
			const b = P(ib);
			g.globalAlpha = 0.32 + 0.06 * Math.sin(t * 0.9 + i);
			g.strokeStyle = pal.warm;
			g.beginPath();
			g.moveTo(a[0], a[1]);
			g.lineTo(a[0] + (b[0] - a[0]) * p, a[1] + (b[1] - a[1]) * p);
			g.stroke();
		});
		inst.shape.points.forEach((_, i) => {
			const lit = ease(clamp((v - inst.litAt[i]) / 0.35));
			if (lit <= 0) return;
			const [x, y] = P(i);
			const pulse = 0.85 + 0.15 * Math.sin(t * 1.2 + i * 1.7);
			this.glow(x, y, 11 * lit, pal.warm, 0.3 * lit * pulse);
			this.dot(x, y, 1.9, pal.core, (0.7 + 0.3 * pulse) * lit);
		});

		const la = clamp((v - inst.drawTime + 0.9) / 0.9) * c.labelOpacity;
		if (la > 0) {
			g.globalAlpha = la;
			g.fillStyle = pal.cool;
			g.font = `500 9px ${pal.mono}`;
			g.textAlign = 'center';
			g.fillText(inst.shape.name, inst.rect.x + inst.rect.w / 2 + ptr.x * 14, oy + inst.rect.h - 4);
		}
	}

	private drawComet(cm: CometPose): void {
		const { g, palette: pal } = this;
		const steps = 22;
		g.lineCap = 'round';
		g.strokeStyle = pal.warm;
		for (let i = 0; i < steps; i++) {
			const f0 = i / steps;
			const f1 = (i + 1) / steps;
			g.globalAlpha = 0.5 * Math.pow(1 - f0, 2.2) * cm.fade;
			g.lineWidth = 2.2 * (1 - f0 * 0.7);
			g.beginPath();
			g.moveTo(cm.x - Math.cos(cm.angle) * cm.tailLength * f0, cm.y - Math.sin(cm.angle) * cm.tailLength * f0);
			g.lineTo(cm.x - Math.cos(cm.angle) * cm.tailLength * f1, cm.y - Math.sin(cm.angle) * cm.tailLength * f1);
			g.stroke();
		}
		this.glow(cm.x, cm.y, 10, pal.warm, 0.5 * cm.fade);
		this.dot(cm.x, cm.y, 2.4, pal.core, cm.fade);
	}
}
