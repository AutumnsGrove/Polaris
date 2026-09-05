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

	// BAR_CROWD_THRESHOLD is 6 — live-tested against visualize's own 12-bar
	// cap per the component's own comment ("CaliforniaTexasFloridaNewYork"
	// running together unreadable at 0 rotation). Below the threshold,
	// labels stay flat; at/above it, they rotate -40deg instead.
	it('does not rotate bar labels when at or under the crowd threshold', () => {
		const points = Array.from({ length: 6 }, (_, i) => ({ x: `Cat${i}`, y: i + 1 }));
		const chart: ChartSpec = { kind: 'bar', title: 'Not crowded', series: [{ label: 'A', points }] };
		const { container } = render(ChartCard, { chart });
		const rotated = Array.from(container.querySelectorAll('text')).some((el) =>
			(el.getAttribute('transform') ?? '').includes('rotate')
		);
		expect(rotated).toBe(false);
	});

	it('rotates bar labels once over the crowd threshold', () => {
		const points = Array.from({ length: 7 }, (_, i) => ({ x: `Category${i}`, y: i + 1 }));
		const chart: ChartSpec = { kind: 'bar', title: 'Crowded', series: [{ label: 'A', points }] };
		const { container } = render(ChartCard, { chart });
		const rotated = Array.from(container.querySelectorAll('text')).some((el) =>
			(el.getAttribute('transform') ?? '').includes('rotate(-40')
		);
		expect(rotated).toBe(true);
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
		expect(getByText('Sep 4')).toBeTruthy();
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
