import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import ChartCard from './ChartCard.svelte';
import type { ChartSpec } from '$lib/types';

describe('ChartCard — line/bar', () => {
	it('renders one point per series entry and the title', () => {
		const chart: ChartSpec = {
			kind: 'line',
			title: 'Temps',
			series: [
				{
					label: 'High',
					points: [
						{ x: 'Mon', y: 70 },
						{ x: 'Tue', y: 72 },
						{ x: 'Wed', y: 68 }
					]
				}
			]
		};
		const { container, getByText } = render(ChartCard, { chart });

		expect(getByText('Temps')).toBeTruthy();
		expect(container.querySelectorAll('circle').length).toBe(3);
	});

	// Regression coverage for the same class of bug the backend's
	// handleVisualize now rejects before it ever reaches here (an
	// empty-points series slipping through to ctx.SetChart produced
	// Infinity/NaN coordinates client-side). Defense in depth: even if a
	// malformed chart ever did reach this component, it must degrade to
	// "nothing plotted" rather than throwing and taking the rest of the
	// message down with it.
	it('does not throw on a series with no points', () => {
		const chart: ChartSpec = { kind: 'line', title: 'Empty', series: [{ label: 'A', points: [] }] };
		expect(() => render(ChartCard, { chart })).not.toThrow();
	});

	it('a single-point series renders one centered point, not a crash', () => {
		const chart: ChartSpec = { kind: 'line', title: 'One point', series: [{ label: 'A', points: [{ x: 'Mon', y: 5 }] }] };
		const { container } = render(ChartCard, { chart });
		const circle = container.querySelector('circle');
		expect(circle).toBeTruthy();
		expect(circle?.getAttribute('cx')).not.toBe('NaN');
		expect(circle?.getAttribute('cy')).not.toBe('NaN');
	});

	// Rotation is no longer gated on bar count. The old BAR_CROWD_THRESHOLD
	// logic (rotate once a chart gets crowded, stay flat below) was
	// live-tested against the crowding direction — "CaliforniaTexasFlorida
	// NewYork" running together at 0 rotation — but a thread on the potato
	// found the mirror-image hole: a chart with FEW bars whose category
	// titles were long overflowed just as badly flat, text running into
	// the next bar's label and out past the SVG's bottom edge. Long labels
	// need rotation at any count, so bar labels now always rotate -40°.
	it('rotates bar labels at every bar count, even a small one', () => {
		const points = Array.from({ length: 3 }, (_, i) => ({ x: `Cat${i}`, y: i + 1 }));
		const chart: ChartSpec = { kind: 'bar', title: 'Small', series: [{ label: 'A', points }] };
		const { container } = render(ChartCard, { chart });
		const rotated = Array.from(container.querySelectorAll('text')).some((el) =>
			(el.getAttribute('transform') ?? '').includes('rotate(-40')
		);
		expect(rotated).toBe(true);
	});

	it('rotates bar labels for long category titles too', () => {
		const points = Array.from({ length: 3 }, (_, i) => ({ x: `A very long category title ${i}`, y: i + 1 }));
		const chart: ChartSpec = { kind: 'bar', title: 'Few bars, long labels', series: [{ label: 'A', points }] };
		const { container } = render(ChartCard, { chart });
		const rotated = Array.from(container.querySelectorAll('text')).some((el) =>
			(el.getAttribute('transform') ?? '').includes('rotate(-40')
		);
		expect(rotated).toBe(true);
	});

	// A rotated label's far end swings cos(40°)≈0.77 of its width LEFT of
	// its anchor — for a long-titled few-bar chart the first bar's label
	// would cross negative viewBox x and get silently clipped by the SVG's
	// default overflow:hidden (the same failure as the bottom-edge case,
	// just on the horizontal axis). leftPad must grow with the longest
	// label, which pushes the first bar right of the flat PAD_LEFT=34.
	it('grows the left pad with the longest label so the first rotated label stays in the viewBox', () => {
		const points = [{ x: 'A really long point title', y: 10 }];
		const chart: ChartSpec = { kind: 'bar', title: 'Long first label', series: [{ label: 'A', points }] };
		const { container } = render(ChartCard, { chart });
		const firstBar = container.querySelector('.bar') as SVGElement;
		const x = parseFloat(firstBar.getAttribute('x') ?? '0');
		const rotated = Array.from(container.querySelectorAll('text')).some((el) =>
			(el.getAttribute('transform') ?? '').includes('rotate(-40')
		);
		expect(rotated).toBe(true);
		expect(x).toBeGreaterThan(34);
	});

	it('shows the legend for multiple series but not for one with no axis labels', () => {
		const single: ChartSpec = { kind: 'line', title: 'One series', series: [{ label: 'A', points: [{ x: '1', y: 1 }] }] };
		const oneRender = render(ChartCard, { chart: single });
		expect(oneRender.container.querySelector('.chart-legend')).toBeNull();
		oneRender.unmount();

		const multi: ChartSpec = {
			kind: 'line',
			title: 'Two series',
			series: [
				{ label: 'A', points: [{ x: '1', y: 1 }] },
				{ label: 'B', points: [{ x: '1', y: 2 }] }
			]
		};
		const { getByText } = render(ChartCard, { chart: multi });
		expect(getByText('A')).toBeTruthy();
		expect(getByText('B')).toBeTruthy();
	});
});

describe('ChartCard — range (weather, Tier 1 only)', () => {
	it('renders one row per day with a formatted short date and both temperatures', () => {
		const chart: ChartSpec = {
			kind: 'range',
			title: 'Forecast',
			series: [
				{ label: 'High', points: [{ x: '2026-09-04', y: 75 }] },
				{ label: 'Low', points: [{ x: '2026-09-04', y: 58 }] }
			],
			icons: ['clear']
		};
		const { getByText } = render(ChartCard, { chart });
		expect(getByText('Fri Sep 4')).toBeTruthy();
		expect(getByText('75°')).toBeTruthy();
		expect(getByText('58°')).toBeTruthy();
	});

	it('falls back to the raw string for a non-ISO date instead of "Invalid Date"', () => {
		const chart: ChartSpec = {
			kind: 'range',
			title: 'Forecast',
			series: [{ label: 'High', points: [{ x: 'not-a-date', y: 75 }] }]
		};
		const { getByText, queryByText } = render(ChartCard, { chart });
		expect(getByText('not-a-date')).toBeTruthy();
		expect(queryByText(/Invalid Date/i)).toBeNull();
	});

	// weatherIcons' fallback in ChartCard.svelte: an icon key not in the
	// fixed vocabulary (a future WMO code this hasn't been taught yet)
	// must still render *some* icon rather than nothing — this only
	// confirms it doesn't throw, since the fallback renders the same Cloud
	// component `cloudy` does and Lucide icons carry no distinguishing
	// text/role to query by.
	it('does not throw on an unrecognized icon key', () => {
		const chart: ChartSpec = {
			kind: 'range',
			title: 'Forecast',
			series: [{ label: 'High', points: [{ x: '2026-09-04', y: 75 }] }],
			icons: ['tornado-of-frogs']
		};
		expect(() => render(ChartCard, { chart })).not.toThrow();
	});

	it('floors a near-zero high/low spread to a visible 3% bar width instead of an invisible sliver', () => {
		// Two days sharing one min/max scale: day 1 sets a wide 50-90 range,
		// day 2's own high/low (70/70.05) is a sliver on that shared scale —
		// ((70.05-70)/40)*100 = 0.125%, which rangeBarStyle's Math.max(...,
		// 3) must floor up to a visible 3% rather than an invisible bar.
		const chart: ChartSpec = {
			kind: 'range',
			title: 'Forecast',
			series: [
				{
					label: 'High',
					points: [
						{ x: '2026-09-04', y: 90 },
						{ x: '2026-09-05', y: 70.05 }
					]
				},
				{
					label: 'Low',
					points: [
						{ x: '2026-09-04', y: 50 },
						{ x: '2026-09-05', y: 70 }
					]
				}
			]
		};
		const { container } = render(ChartCard, { chart });
		const fills = container.querySelectorAll('.range-fill');
		expect(fills.length).toBe(2);
		expect((fills[1] as HTMLElement).style.width).toBe('3%');
	});
});

describe('ChartCard — timeline', () => {
	it('renders every event in order', () => {
		const chart: ChartSpec = {
			kind: 'timeline',
			title: 'History',
			events: [
				{ date: '2026-01-01', label: 'First' },
				{ date: '2026-02-01', label: 'Second' }
			]
		};
		const { container } = render(ChartCard, { chart });
		const labels = Array.from(container.querySelectorAll('.timeline-label')).map((el) => el.textContent);
		expect(labels).toEqual(['First', 'Second']);
	});
});

describe('ChartCard — meter', () => {
	it('computes the fill percentage from current/min/max', () => {
		const chart: ChartSpec = { kind: 'meter', title: 'Usage', value: { current: 25, min: 0, max: 100, label: 'tokens' } };
		const { container } = render(ChartCard, { chart });
		const fill = container.querySelector('.meter-fill') as HTMLElement;
		expect(fill.style.width).toBe('25%');
		expect(fill.classList.contains('hot')).toBe(false);
	});

	// Same >=90%-of-max "hot" threshold ThreadMenu.svelte's context-usage
	// readout already uses (.info-row.hot) — see the component's own
	// comment on why that has to match everywhere in the app.
	it('switches to the hot state at the 90% threshold', () => {
		const chart: ChartSpec = { kind: 'meter', title: 'Usage', value: { current: 90, min: 0, max: 100, label: 'tokens' } };
		const { container } = render(ChartCard, { chart });
		expect(container.querySelector('.meter-fill')?.classList.contains('hot')).toBe(true);
	});

	it('clamps the fill percentage to [0, 100] for out-of-range values', () => {
		const over: ChartSpec = { kind: 'meter', title: 'Usage', value: { current: 150, min: 0, max: 100, label: '' } };
		const overRender = render(ChartCard, { chart: over });
		expect((overRender.container.querySelector('.meter-fill') as HTMLElement).style.width).toBe('100%');
		overRender.unmount();

		const under: ChartSpec = { kind: 'meter', title: 'Usage', value: { current: -10, min: 0, max: 100, label: '' } };
		const { container } = render(ChartCard, { chart: under });
		expect((container.querySelector('.meter-fill') as HTMLElement).style.width).toBe('0%');
	});

	it('does not throw when min equals max', () => {
		const chart: ChartSpec = { kind: 'meter', title: 'Usage', value: { current: 5, min: 5, max: 5, label: '' } };
		expect(() => render(ChartCard, { chart })).not.toThrow();
	});
});
