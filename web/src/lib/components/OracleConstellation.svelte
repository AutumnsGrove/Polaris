<script lang="ts">
	// Oracle mode's "reading" choreography (docs/plans/oracle-mode.md) — one
	// faint star per enabled check, lit in deterministic zig-zag order, then
	// joined by a line, then folded away so ChatTurnView.svelte can swap in
	// the margin note in its place. Ported from mockups/oracle-mode.html's
	// `buildConstellation`/`play` — same star-position formula and timing,
	// just driven by Svelte state instead of direct DOM/innerHTML calls.
	// Self-contained (own timers, own fold-out) rather than living inline in
	// the already-large ChatTurnView, same reasoning as
	// ConstellationUsageModal.svelte being its own file.
	let {
		checkCount,
		// Flips true the instant the turn's first real tool call streams in
		// — Oracle's own "reading" moment shouldn't visibly block the turn
		// that's already underway, so this cuts the choreography short
		// (skip straight to fully-lit + folded) instead of playing out its
		// full ~1s regardless. See the mockup's own comment: "cut short if
		// the first tool call arrives".
		cutShort = false,
		// Fires once, when the constellation finishes folding (naturally or
		// via cutShort) — ChatTurnView uses this to swap this component out
		// for the margin note.
		onDone
	}: {
		checkCount: number;
		cutShort?: boolean;
		onDone?: () => void;
	} = $props();

	const width = 220;
	const height = 34;

	// Same deterministic zig-zag as the mockup: evenly spaced on X, a stable
	// pseudo-random height via a phase-shifted sine so it reads as a real
	// asterism instead of a chart line, not actual randomness (which would
	// re-shuffle on every re-render).
	const stars = $derived.by(() => {
		const n = Math.max(1, checkCount);
		return Array.from({ length: n }, (_, i) => {
			const x = 8 + (i * (width - 16)) / Math.max(1, n - 1);
			const y = 6 + ((Math.sin(i * 2.3 + 0.7) + 1) / 2) * (height - 12);
			return { x, y, r: i % 3 === 1 ? 2.2 : 1.7 };
		});
	});
	const points = $derived(stars.map((s) => `${s.x.toFixed(1)},${s.y.toFixed(1)}`).join(' '));

	let litCount = $state(0);
	let lineDrawn = $state(false);
	let folded = $state(false);
	let visible = $state(false);
	let timers: ReturnType<typeof setTimeout>[] = [];
	let done = false;

	const reduceMotion =
		typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

	function at(ms: number, fn: () => void) {
		timers.push(setTimeout(fn, reduceMotion ? 0 : ms));
	}

	function clearTimers() {
		timers.forEach(clearTimeout);
		timers = [];
	}

	function fold() {
		if (done) return;
		done = true;
		clearTimers();
		litCount = stars.length;
		lineDrawn = true;
		folded = true;
		onDone?.();
	}

	$effect(() => {
		visible = true;
		const n = stars.length;
		const step = Math.min(140, 900 / n);
		for (let i = 0; i < n; i++) {
			at(i * step, () => {
				litCount = i + 1;
			});
		}
		const lineAt = n * step;
		at(lineAt, () => {
			lineDrawn = true;
		});
		at(lineAt + 650, fold);
		return clearTimers;
	});

	// cutShort can flip true well before the natural sequence above would
	// have finished (a fast tool call) or after (a slow Jev response) —
	// either way, the first one to actually happen wins; fold() itself is
	// idempotent via `done`.
	$effect(() => {
		if (cutShort) fold();
	});
</script>

<svg
	class="const"
	class:visible
	class:folded
	viewBox="0 0 {width} {height}"
	preserveAspectRatio="xMinYMid meet"
>
	<polyline
		class="line"
		{points}
		pathLength="1"
		stroke-dasharray="1"
		stroke-dashoffset={lineDrawn ? '0' : '1'}
	/>
	{#each stars as star, i (i)}
		<circle class="star" class:lit={i < litCount} cx={star.x} cy={star.y} r={star.r} />
	{/each}
</svg>

<style>
	.const {
		width: 100%;
		max-width: 220px;
		height: 34px;
		overflow: visible;
		opacity: 0;
		transform-origin: 7px 17px;
		transition: opacity 0.25s var(--ease-out-expo);
	}
	.const.visible {
		opacity: 1;
	}
	.const.folded {
		opacity: 0;
		transform: scale(0.08);
		transition:
			opacity 0.4s ease-in,
			transform 0.45s ease-in;
	}
	.line {
		stroke: var(--color-accent);
		stroke-width: 0.9;
		fill: none;
		opacity: 0.5;
		transition: stroke-dashoffset 0.5s var(--ease-out-expo);
	}
	.star {
		fill: var(--color-border-strong);
		transition: fill 0.35s var(--ease-out-expo);
	}
	.star.lit {
		fill: var(--color-accent);
	}
</style>
