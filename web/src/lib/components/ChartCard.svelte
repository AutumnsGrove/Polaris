<script lang="ts">
	import type { ChartSpec } from '$lib/types';

	let { chart }: { chart: ChartSpec } = $props();

	// A fixed viewBox with hand-picked padding, scaled by the SVG's own
	// preserveAspectRatio — no charting library, matching the hand-rolled
	// instinct already established elsewhere in this codebase (the R2
	// client, the calculator's own evaluator). Values are unitless
	// viewBox coordinates, not pixels.
	const VB_W = 300;
	const VB_H = 150;
	const PAD_LEFT = 34;
	const PAD_RIGHT = 10;
	const PAD_TOP = 10;
	const PAD_BOTTOM = 24;

	const seriesColors = ['var(--color-accent)', 'var(--color-accent-2)'];

	// line/bar assume every series shares the same point count and x
	// order (true for both v1 sources: weather's High/Low, and whatever
	// the model builds from one comparable data set) — a mismatched
	// series count just renders as short as its own points array, not a
	// crash.
	let allYValues = $derived((chart.series ?? []).flatMap((s) => s.points.map((p) => p.y)));
	let yMin = $derived(chart.kind === 'bar' ? Math.min(0, ...allYValues) : Math.min(...allYValues));
	let yMax = $derived(Math.max(...allYValues, yMin + 1));

	function scaleX(index: number, count: number): number {
		if (count <= 1) return PAD_LEFT + (VB_W - PAD_LEFT - PAD_RIGHT) / 2;
		return PAD_LEFT + (index / (count - 1)) * (VB_W - PAD_LEFT - PAD_RIGHT);
	}

	function scaleY(value: number): number {
		const range = yMax - yMin || 1;
		return VB_H - PAD_BOTTOM - ((value - yMin) / range) * (VB_H - PAD_TOP - PAD_BOTTOM);
	}

	function linePath(points: { x: string | number; y: number }[]): string {
		return points.map((p, i) => `${i === 0 ? 'M' : 'L'} ${scaleX(i, points.length)} ${scaleY(p.y)}`).join(' ');
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
				y1={VB_H - PAD_BOTTOM}
				x2={VB_W - PAD_RIGHT}
				y2={VB_H - PAD_BOTTOM}
				class="axis-line"
			/>
			<text x={PAD_LEFT - 4} y={scaleY(yMax) + 3} class="axis-label" text-anchor="end">{yMax.toFixed(0)}</text>
			<text x={PAD_LEFT - 4} y={scaleY(yMin) + 3} class="axis-label" text-anchor="end">{yMin.toFixed(0)}</text>

			{#if chart.kind === 'bar'}
				{#each chart.series?.[0]?.points ?? [] as point, i (i)}
					{@const barWidth = (VB_W - PAD_LEFT - PAD_RIGHT) / (chart.series![0].points.length * 1.5)}
					{@const cx = scaleX(i, chart.series![0].points.length)}
					<rect
						x={cx - barWidth / 2}
						y={scaleY(point.y)}
						width={barWidth}
						height={VB_H - PAD_BOTTOM - scaleY(point.y)}
						rx="2"
						class="bar"
					/>
					<text x={cx} y={VB_H - PAD_BOTTOM + 10} class="axis-label" text-anchor="middle">{point.x}</text>
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
