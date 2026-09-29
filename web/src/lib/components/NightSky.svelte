<script lang="ts">
	// Ambient night sky behind the start screen: twinkling parallax stars, a
	// Milky Way wash, an occasional comet, and constellations that draw
	// themselves in at random clear spots and retract again. Decorative only
	// (aria-hidden, pointer-events none). All tunables: lib/nightSky/config.ts;
	// mockup it was built from: mockups/polaris-living-sky.html.
	import { onMount } from 'svelte';
	import { SKY } from '$lib/nightSky/config';
	import { CONSTELLATIONS } from '$lib/nightSky/constellations';
	import { Comet } from '$lib/nightSky/comet';
	import { SkyDirector, type Viewport } from '$lib/nightSky/director';
	import { mulberry32 } from '$lib/nightSky/rng';
	import { SkyRenderer, readPalette } from '$lib/nightSky/renderer';

	let canvas: HTMLCanvasElement;

	onMount(() => {
		const g = canvas.getContext('2d');
		const host = canvas.parentElement;
		if (!g || !host) return;

		const seed = (Math.random() * 2 ** 32) >>> 0;
		const director = new SkyDirector(SKY, CONSTELLATIONS, mulberry32(seed));
		const comet = new Comet(SKY.comet, mulberry32(seed ^ 0x9e3779b9));
		const renderer = new SkyRenderer(g, SKY, readPalette(document.documentElement));
		const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
		const ptr = { x: 0, y: 0 };
		const ptrTarget = { x: 0, y: 0 };

		// Rects the constellations must stay clear of, re-measured on every
		// spawn so a resized window or an open keyboard is always respected.
		function viewport(): Viewport {
			const box = canvas.getBoundingClientRect();
			const avoid = Array.from(host!.querySelectorAll(SKY.avoidSelector)).map((el) => {
				const r = el.getBoundingClientRect();
				return { x: r.left - box.left, y: r.top - box.top, w: r.width, h: r.height };
			});
			return { w: renderer.w, h: renderer.h, avoid };
		}

		function fit() {
			const b = canvas.getBoundingClientRect();
			const dpr = Math.min(2, window.devicePixelRatio || 1);
			canvas.width = Math.max(1, Math.round(b.width * dpr));
			canvas.height = Math.max(1, Math.round(b.height * dpr));
			renderer.resize(b.width, b.height, dpr);
		}
		fit();
		const ro = new ResizeObserver(() => {
			fit();
			if (reduceMotion) paintStill();
		});
		ro.observe(canvas);

		// Reduced motion: no loop, one still frame with two constellations
		// fully drawn so the screen still reads as a sky.
		function paintStill() {
			director.active = [];
			director.spawn(0, viewport());
			director.spawn(0, viewport());
			const t = 20;
			for (const inst of director.active) inst.start = t - inst.drawTime - 0.1;
			renderer.draw(t, { x: 0, y: 0 }, director.active, null);
		}

		const themeObserver = new MutationObserver(() => {
			renderer.setPalette(readPalette(document.documentElement));
		});
		themeObserver.observe(document.documentElement, {
			attributes: true,
			attributeFilter: ['data-theme']
		});

		if (reduceMotion) {
			paintStill();
			return () => {
				ro.disconnect();
				themeObserver.disconnect();
			};
		}

		const onPointer = (e: PointerEvent) => {
			ptrTarget.x = e.clientX / window.innerWidth - 0.5;
			ptrTarget.y = e.clientY / window.innerHeight - 0.5;
		};
		window.addEventListener('pointermove', onPointer, { passive: true });

		// Own clock (not the rAF timestamp) so a hidden tab doesn't fast-forward
		// every animation to its end the moment it becomes visible again.
		let clock = 0;
		let last: number | null = null;
		let raf = 0;
		const minFrame = 1000 / SKY.maxFps;

		function tick(now: number) {
			raf = requestAnimationFrame(tick);
			if (document.hidden) {
				last = null;
				return;
			}
			if (last !== null && now - last < minFrame - 1) return;
			if (last !== null) clock += Math.min(0.1, (now - last) / 1000);
			last = now;
			// Light theme keeps the plain glow; the sky is a night-only treatment.
			if (document.documentElement.getAttribute('data-theme') === 'light') return;
			ptr.x += (ptrTarget.x - ptr.x) * 0.06;
			ptr.y += (ptrTarget.y - ptr.y) * 0.06;
			director.update(clock, viewport());
			renderer.draw(clock, ptr, director.active, comet.pose(clock, renderer.w, renderer.h));
		}
		raf = requestAnimationFrame(tick);

		return () => {
			cancelAnimationFrame(raf);
			ro.disconnect();
			themeObserver.disconnect();
			window.removeEventListener('pointermove', onPointer);
		};
	});
</script>

<canvas bind:this={canvas} class="night-sky" aria-hidden="true"></canvas>

<style>
	.night-sky {
		position: absolute;
		inset: 0;
		width: 100%;
		height: 100%;
		z-index: var(--z-behind);
		pointer-events: none;
	}

	:global(:root[data-theme='light']) .night-sky {
		display: none;
	}
</style>
