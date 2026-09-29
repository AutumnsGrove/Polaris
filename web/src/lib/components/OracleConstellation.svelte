<script lang="ts">
	// Oracle mode's "reading" choreography (docs/plans/oracle-mode.md) — one
	// faint star per enabled check, lit in deterministic zig-zag order, then
	// joined by a line, where it holds fully lit — its stars shimmering slowly,
	// see the .const.built rule in the stylesheet — until the turn's first real
	// output folds it away so ChatTurnView.svelte can swap in the margin note in
	// its place. Ported from mockups/oracle-mode.html's
	// `buildConstellation`/`play` — same star-position formula and timing,
	// just driven by Svelte state instead of direct DOM/innerHTML calls.
	// Self-contained (own timers, own fold-out) rather than living inline in
	// the already-large ChatTurnView, same reasoning as
	// ConstellationUsageModal.svelte being its own file.
	let {
		checkCount,
		// Flips true the instant the turn's first real output streams in (a
		// tool call, a reasoning step, or the first answer token) — Oracle's
		// own "reading" moment shouldn't visibly block the turn that's
		// already underway, so this cuts the choreography short (skip straight
		// to fully-lit + folded) instead of holding longer. See the mockup's
		// own comment: "cut short if the first tool call arrives". This is
		// deliberately the *only* thing that ends the animation — see the
		// hold note in the playback $effect below.
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
		// No natural fold timer on purpose: once fully lit, the constellation
		// holds in place until the turn's first real output arrives (cutShort
		// above), rather than folding away ~650ms after the line draws. Oracle
		// usually resolves several seconds before the model emits its first
		// token, so folding on a timer left a bare margin note (or empty space)
		// during that wait — the "reading" animation is the nicest thing on
		// screen then, so it stays until there's real output to show instead.
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
	class:built={lineDrawn}
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
		<!-- --i drives the built-state shimmer's per-star phase below; stars
		     are emitted left to right, so the highlight reads as one soft wave
		     travelling along the asterism. -->
		<circle
			class="star"
			class:lit={i < litCount}
			cx={star.x}
			cy={star.y}
			r={star.r}
			style="--i: {i}"
		/>
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

	/* The svg can sit here fully built for several seconds after Oracle
	   resolves but before the model emits its first token — see the hold note
	   in the playback $effect. Rather than a frozen frame during that dead
	   time, once built the stars themselves shimmer: a soft accent highlight
	   travels point to point, matching the composer Oracle trigger's glow
	   (ComposerMenu.svelte's .trigger.oracle.reading::after) in colour and
	   feel, but painted on the points instead of a band sweeping over them
	   (which read as a block drawn on top of the constellation). Each star
	   pulses between its lit accent and a brighter tint, phase-offset by --i
	   so the bright crest moves left to right; 0.2s x ~17 checks ~= the 3.4s
	   cycle, roughly one wave per pass. Only the points animate — the joining
	   line stays a quiet constant backdrop. */
	.const.built .star.lit {
		animation: star-shimmer 3.4s ease-in-out infinite;
		animation-delay: calc(var(--i, 0) * -0.2s);
	}

	@keyframes star-shimmer {
		0%,
		100% {
			fill: var(--color-accent);
		}
		50% {
			fill: color-mix(in srgb, var(--color-accent) 55%, white);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.const.built .star.lit {
			animation: none;
		}
	}
</style>
