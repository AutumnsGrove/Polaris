<script lang="ts">
	import type { ChartSpec } from '$lib/types';
	import { Sun, CloudSun, Cloud, CloudFog, CloudDrizzle, CloudRain, CloudSnow, CloudLightning } from '@lucide/svelte';

	let { chart }: { chart: ChartSpec } = $props();

	// range's fixed icon vocabulary (see registry.go's weatherCodeIcon) —
	// "cloudy" is also the fallback for any key this map doesn't
	// recognize, so a future WMO code this hasn't been taught about yet
	// degrades to a plausible-looking icon instead of rendering nothing.
	const weatherIcons: Record<string, typeof Sun> = {
		clear: Sun,
		'partly-cloudy': CloudSun,
		cloudy: Cloud,
		fog: CloudFog,
		drizzle: CloudDrizzle,
		rain: CloudRain,
		snow: CloudSnow,
		thunderstorm: CloudLightning
	};
	function iconFor(key: string) {
		return weatherIcons[key] ?? Cloud;
	}

	// A fixed viewBox with hand-picked padding, scaled by the SVG's own
	// preserveAspectRatio — no charting library, matching the hand-rolled
	// instinct already established elsewhere in this codebase (the R2
	// client, the calculator's own evaluator). Values are unitless
	// viewBox coordinates, not pixels. Only line/bar are SVG-plotted at
	// all — range/timeline/meter are plain HTML/CSS (see below).
	const VB_W = 300;
	const VB_H = 150;
	const PAD_LEFT = 34;
	const PAD_RIGHT = 10;
	const PAD_TOP = 10;
	const PAD_BOTTOM = 24;

	// A bar chart with more than this many bars rotates its x-axis labels
	// instead of leaving them flat — live-tested against a 12-bar chart
	// (visualize's own cap) and flat labels for that many categories just
	// ran into each other ("CaliforniaTexasFloridaNewYork...", completely
	// unreadable). 6 is comfortably below where that starts happening.
	const BAR_CROWD_THRESHOLD = 6;

	const seriesColors = ['var(--color-accent)', 'var(--color-accent-2)'];

	// line/bar assume every series shares the same point count and x
	// order (true for both v1 sources: weather's High/Low, and whatever
	// the model builds from one comparable data set) — a mismatched
	// series count just renders as short as its own points array, not a
	// crash.
	let allYValues = $derived((chart.series ?? []).flatMap((s) => s.points.map((p) => p.y)));
	let yMin = $derived(chart.kind === 'bar' ? Math.min(0, ...allYValues) : Math.min(...allYValues));
	let yMax = $derived(Math.max(...allYValues, yMin + 1));

	let barCount = $derived(chart.series?.[0]?.points.length ?? 0);
	let barCrowded = $derived(chart.kind === 'bar' && barCount > BAR_CROWD_THRESHOLD);

	// Longest category label among a crowded bar chart's bars — drives
	// bottomPad below. Not measured against the real rendered font (SVG
	// has no cheap client-side text-measurement without a canvas trick);
	// a per-character estimate is enough to size padding correctly, it
	// doesn't need to be exact.
	let longestLabelChars = $derived(
		barCrowded ? Math.max(0, ...(chart.series?.[0]?.points.map((p) => String(p.x).length) ?? [])) : 0
	);

	// bar draws a number directly above each mark (there's no hover/
	// tooltip in a static SVG, so on-chart labels are the only way to
	// read an exact value) — that needs headroom above the tallest bar
	// the plain line chart's axis-label-only layout didn't. topPad is a
	// flat constant since bar values are always short numbers.
	//
	// bottomPad for a crowded bar chart is NOT a flat constant, and that
	// was a real bug live-tested and found: a longer category label
	// rotated -40° (see barCrowded's transform below) swings its leading
	// character further down as well as sideways, and a flat pad sized
	// for a typical single-word label ("Texas") let a longer two-word one
	// ("Pennsylvania", "North Carolina") swing far enough down to cross
	// the SVG's own bottom edge — silently clipped by the SVG's default
	// overflow:hidden, the exact same failure mode as xLabelAnchor's
	// left/right edge case, just on the vertical axis instead. The
	// constants below come from the actual rotation math: at -40°, a
	// label's leading character ends up sin(40°)≈0.643 of its own
	// (estimated ~4.5 viewBox-units-per-character) width below the
	// label's own anchor point — so the pad has to grow with the longest
	// label's length, not just its rotation.
	let topPad = $derived(chart.kind === 'bar' ? PAD_TOP + 14 : PAD_TOP);
	let bottomPad = $derived(barCrowded ? Math.max(PAD_BOTTOM, 12 + longestLabelChars * 3) : PAD_BOTTOM);

	function scaleX(index: number, count: number): number {
		if (count <= 1) return PAD_LEFT + (VB_W - PAD_LEFT - PAD_RIGHT) / 2;
		return PAD_LEFT + (index / (count - 1)) * (VB_W - PAD_LEFT - PAD_RIGHT);
	}

	function scaleY(value: number): number {
		const range = yMax - yMin || 1;
		return VB_H - bottomPad - ((value - yMin) / range) * (VB_H - topPad - bottomPad);
	}

	function linePath(points: { x: string | number; y: number }[]): string {
		return points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${scaleX(i, points.length)} ${scaleY(p.y)}`).join(' ');
	}

	// x-axis category labels are text-anchor="middle" by default, which
	// lets the first and last label's text overflow past the viewBox's
	// left/right edge — SVG's default overflow:hidden on the outer <svg>
	// then silently clips it (this is exactly how "Phoenix" rendered as
	// "Phoeni" in practice). Anchoring the two edge labels to start/end
	// instead keeps every label's text within bounds. Moot once a chart
	// is crowded enough to rotate (see barCrowded) — rotated labels are
	// always anchor="end" regardless of position.
	function xLabelAnchor(index: number, count: number): 'start' | 'middle' | 'end' {
		if (count <= 1) return 'middle';
		if (index === 0) return 'start';
		if (index === count - 1) return 'end';
		return 'middle';
	}

	// Compact display for a bar's on-chart value label ("8.5M" not
	// "8546038") — a raw large number would be wider than most bars.
	function formatCompact(n: number): string {
		const abs = Math.abs(n);
		const trim = (v: number) => (Math.round(v * 10) / 10).toString();
		if (abs >= 1e9) return trim(n / 1e9) + 'B';
		if (abs >= 1e6) return trim(n / 1e6) + 'M';
		if (abs >= 1e3) return trim(n / 1e3) + 'K';
		return trim(n);
	}

	// range's day labels — chart.x is an ISO date ("2026-09-04") for
	// range's one real source (weather.go's setWeatherChart); formatted
	// short ("Sep 4") since the full date is redundant with the
	// top-to-bottom row ordering. Falls back to the raw string for
	// anything that doesn't parse as a date rather than showing "Invalid
	// Date" — range is Tier-1-only today, but this keeps a future non-
	// weather Tier-1 source from rendering garbage if its dates aren't
	// ISO-formatted.
	function formatShortDate(x: string | number): string {
		const s = String(x);
		const d = new Date(s.includes('T') ? s : s + 'T00:00:00');
		if (isNaN(d.getTime())) return s;
		return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
	}

	// range — Tier-1-only (see registry.go's ChartSpec doc comment), never
	// offered to the model via visualize's kind enum, set exclusively by
	// weather.go's setWeatherChart. Contract: series[0] is High, series[1]
	// is Low, same point count/order.
	//
	// Deliberately not a coordinate-plotted SVG chart — that was tried
	// first (both a two-line chart, then an SVG range-bar-per-day chart)
	// and neither read well: no hover/tooltip in a static SVG made a
	// shared numeric axis useless, and stuffing high AND low onto one
	// axis was the real problem, not the mark shape. This instead mirrors
	// a real weather app's daily list: one row per day, a horizontal
	// gradient bar positioned on a shared min/max scale across every row,
	// with the low/high numbers sitting directly at the bar's own ends —
	// nothing to read off an axis at all.
	let rangeRows = $derived(
		chart.kind === 'range' && chart.series?.[0]
			? chart.series[0].points.map((highPoint, i) => ({
					date: formatShortDate(highPoint.x),
					high: highPoint.y,
					low: chart.series?.[1]?.points[i]?.y ?? highPoint.y,
					icon: chart.icons?.[i]
				}))
			: []
	);
	let rangeMin = $derived(rangeRows.length ? Math.min(...rangeRows.map((r) => r.low)) : 0);
	let rangeMax = $derived(rangeRows.length ? Math.max(...rangeRows.map((r) => r.high)) : 1);
	function rangeBarStyle(row: { high: number; low: number }): string {
		const span = rangeMax - rangeMin || 1;
		const startPct = ((row.low - rangeMin) / span) * 100;
		// Floors the visible width at 3% — a day whose high and low are
		// very close (or a single-day forecast, hypothetically) would
		// otherwise render an invisible sliver instead of a readable bar.
		const widthPct = Math.max(((row.high - row.low) / span) * 100, 3);
		return `left: ${startPct}%; width: ${widthPct}%`;
	}

	// meter's fill switches to the danger color at the same >=90%-of-max
	// threshold ThreadMenu.svelte's context-usage readout already uses
	// (.info-row.hot) — "getting close to a cap" should look the same
	// everywhere in the app, not just in the thread menu.
	let meterPercent = $derived(
		chart.value ? Math.max(0, Math.min(100, ((chart.value.current - chart.value.min) / (chart.value.max - chart.value.min || 1)) * 100)) : 0
	);
	let meterHot = $derived(meterPercent >= 90);
</script>

<div class="chart-card">
	<div class="chart-title">{chart.title}</div>

	{#if chart.kind === 'line' || chart.kind === 'bar'}
		<svg viewBox="0 0 {VB_W} {VB_H}" class="chart-svg" role="img" aria-label={chart.title}>
			<line
				x1={PAD_LEFT}
				y1={VB_H - bottomPad}
				x2={VB_W - PAD_RIGHT}
				y2={VB_H - bottomPad}
				class="axis-line"
			/>
			{#if chart.kind === 'line'}
				<text x={PAD_LEFT - 4} y={scaleY(yMax) + 3} class="axis-label" text-anchor="end">{yMax.toFixed(0)}</text>
				<text x={PAD_LEFT - 4} y={scaleY(yMin) + 3} class="axis-label" text-anchor="end">{yMin.toFixed(0)}</text>
			{/if}

			{#if chart.kind === 'bar'}
				{#each chart.series?.[0]?.points ?? [] as point, i (i)}
					{@const barWidth = (VB_W - PAD_LEFT - PAD_RIGHT) / (barCount * 1.5)}
					{@const cx = scaleX(i, barCount)}
					{@const labelY = VB_H - bottomPad + 10}
					<rect
						x={cx - barWidth / 2}
						y={scaleY(point.y)}
						width={barWidth}
						height={VB_H - bottomPad - scaleY(point.y)}
						rx="2"
						class="bar"
					/>
					<text x={cx} y={scaleY(point.y) - 4} class="bar-value" text-anchor="middle">{formatCompact(point.y)}</text>
					{#if barCrowded}
						<text x={cx} y={labelY} class="axis-label" text-anchor="end" transform="rotate(-40 {cx} {labelY})">{point.x}</text>
					{:else}
						<text x={cx} y={labelY} class="axis-label" text-anchor={xLabelAnchor(i, barCount)}>{point.x}</text>
					{/if}
				{/each}
			{:else}
				{#each chart.series ?? [] as series, si (series.label)}
					<path d={linePath(series.points)} class="line-path" style="stroke: {seriesColors[si % seriesColors.length]}" />
					{#each series.points as point, i (i)}
						<circle
							cx={scaleX(i, series.points.length)}
							cy={scaleY(point.y)}
							r="2.5"
							style="fill: {seriesColors[si % seriesColors.length]}"
						/>
					{/each}
				{/each}
			{/if}
		</svg>

		{#if (chart.series?.length ?? 0) > 1 || chart.x_label || chart.y_label}
			<div class="chart-legend">
				{#each chart.series ?? [] as series, si (series.label)}
					<span class="legend-item">
						<span class="legend-dot" style="background: {seriesColors[si % seriesColors.length]}"></span>
						{series.label}
					</span>
				{/each}
				{#if chart.x_label}<span class="legend-axis">{chart.x_label}</span>{/if}
			</div>
		{/if}
	{:else if chart.kind === 'range'}
		<div class="range-list">
			{#each rangeRows as row, i (i)}
				<div class="range-row">
					<span class="range-date">{row.date}</span>
					{#if row.icon}
						{@const Icon = iconFor(row.icon)}
						<Icon size={16} class="range-icon" />
					{/if}
					<span class="range-low">{Math.round(row.low)}°</span>
					<div class="range-track">
						<div class="range-fill" style={rangeBarStyle(row)}></div>
					</div>
					<span class="range-high">{Math.round(row.high)}°</span>
				</div>
			{/each}
		</div>
	{:else if chart.kind === 'timeline'}
		<ol class="timeline">
			{#each chart.events ?? [] as event, i (i)}
				<li class="timeline-item">
					<span class="timeline-dot"></span>
					<span class="timeline-date">{event.date}</span>
					<span class="timeline-label">{event.label}</span>
				</li>
			{/each}
		</ol>
	{:else if chart.kind === 'meter' && chart.value}
		<div class="meter">
			<div class="meter-track">
				<div class="meter-fill" class:hot={meterHot} style="width: {meterPercent}%"></div>
			</div>
			<div class="meter-labels">
				<span class="meter-current" class:hot={meterHot}>{chart.value.current}{chart.value.label ? ` ${chart.value.label}` : ''}</span>
				<span class="meter-range">{chart.value.min} – {chart.value.max}</span>
			</div>
		</div>
	{/if}
</div>

<style>
	.chart-card {
		margin-top: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		max-width: 420px;
	}

	.chart-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text);
		margin-bottom: var(--space-sm);
	}

	.chart-svg {
		width: 100%;
		height: auto;
		display: block;
	}

	.axis-line {
		stroke: var(--color-border-strong);
		stroke-width: 1;
	}

	.axis-label {
		fill: var(--color-text-dim);
		font-size: 8px;
	}

	.line-path {
		fill: none;
		stroke-width: 2;
		stroke-linejoin: round;
		stroke-linecap: round;
	}

	.bar {
		fill: var(--color-accent);
	}

	.bar-value {
		fill: var(--color-text);
		font-size: 8px;
		font-weight: 600;
	}

	.chart-legend {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-md);
		margin-top: var(--space-sm);
		font-size: 11px;
		color: var(--color-text-dim);
	}

	.legend-item {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
	}

	.legend-dot {
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		flex-shrink: 0;
	}

	.legend-axis {
		margin-left: auto;
	}

	.range-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.range-row {
		display: grid;
		grid-template-columns: 40px 16px 24px 1fr 28px;
		align-items: center;
		gap: var(--space-sm);
	}

	.range-date {
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.range-row :global(.range-icon) {
		color: var(--color-text-dim);
	}

	.range-low {
		font-size: 12px;
		color: var(--color-text-dim);
		text-align: right;
	}

	.range-high {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text);
	}

	.range-track {
		position: relative;
		height: 6px;
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
	}

	.range-fill {
		position: absolute;
		top: 0;
		height: 100%;
		border-radius: var(--radius-full);
		background: linear-gradient(90deg, var(--color-accent-2), var(--color-accent));
	}

	.timeline {
		list-style: none;
		margin: 0;
		padding: 0;
		border-left: 2px solid var(--color-border-strong);
	}

	.timeline-item {
		position: relative;
		padding: var(--space-xs) 0 var(--space-md) var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.timeline-item:last-child {
		padding-bottom: 0;
	}

	.timeline-dot {
		position: absolute;
		left: -5px;
		top: 10px;
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		background: var(--color-accent);
	}

	.timeline-date {
		font-size: 11px;
		color: var(--color-text-dim);
	}

	.timeline-label {
		font-size: 13px;
		color: var(--color-text);
	}

	.meter-track {
		width: 100%;
		height: 10px;
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		box-shadow: var(--shadow-well);
		overflow: hidden;
	}

	.meter-fill {
		height: 100%;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		transition: width 0.3s var(--ease-out-expo);
	}

	.meter-fill.hot {
		background: var(--color-danger);
	}

	@media (prefers-reduced-motion: reduce) {
		.meter-fill {
			transition: none;
		}
	}

	.meter-labels {
		display: flex;
		justify-content: space-between;
		margin-top: var(--space-xs);
		font-size: 12px;
	}

	.meter-current {
		font-weight: 600;
		color: var(--color-text);
	}

	.meter-current.hot {
		color: var(--color-danger);
	}

	.meter-range {
		color: var(--color-text-dim);
	}
</style>
