import { describe, it, expect } from 'vitest';
import { MapCardState, directionsUrl, pinLabels } from './mapCard.svelte';
import { applyStreamingEvent } from './turnEvents';
import { buildTimelineFromEvents } from './stateHelpers';
import type { ChatTurn, MapMarker, MapPayload, ServerEvent, StoredEvent } from './types';

const marker = (id: string, extra: Partial<MapMarker> = {}): MapMarker => ({
	id,
	label: id.toUpperCase(),
	lat: 47.6,
	lon: -122.3,
	...extra
});

const card = (): MapPayload => ({
	id: 'map-abc123',
	version: 1,
	kind: 'map',
	title: 'Coffee',
	view: { center: [47.6, -122.3], zoom: 15 },
	markers: [marker('needle', { kind: 'landmark' }), marker('a'), marker('b', { kind: 'muted' })],
	shapes: [{ id: 's1', type: 'circle', lat: 47.6, lon: -122.3, radius_m: 800, layer: 'radius' }],
	layers: [
		{ id: 'radius', label: 'Radius', default_on: true },
		{ id: 'route', label: 'Route', default_on: false }
	]
});

describe('pinLabels', () => {
	it('stars the landmark and numbers every other pin in listed order, muted ones included', () => {
		// Must match tools/show_map.go's describeMap and mapsnap/render.go, or "pin 2"
		// means different things in the card, the model's description and its snapshot.
		expect(pinLabels(card().markers)).toEqual({ needle: '★', a: '1', b: '2' });
	});

	it('does not number a landmark that sits in the middle of the list', () => {
		const labels = pinLabels([marker('a'), marker('l', { kind: 'landmark' }), marker('b')]);
		expect(labels).toEqual({ a: '1', l: '★', b: '2' });
	});
});

describe('directionsUrl', () => {
	it('hands off to an external maps link with the pin coordinates', () => {
		expect(directionsUrl(marker('a'))).toBe(
			'https://www.openstreetmap.org/directions?to=47.6%2C-122.3'
		);
	});

	it('has no link for a pin without coordinates (an image-card pin)', () => {
		expect(directionsUrl({ id: 'p', label: 'P', x: 10, y: 20 })).toBeNull();
	});
});

describe('MapCardState', () => {
	it("starts each layer at the model's default_on", () => {
		const s = new MapCardState(card());
		expect(s.layerOn).toEqual({ radius: true, route: false });
	});

	it('toggles a layer and labels independently', () => {
		const s = new MapCardState(card());
		s.toggleLayer('route');
		s.toggleLabels();
		expect(s.layerOn.route).toBe(true);
		expect(s.layerOn.radius).toBe(true);
		expect(s.labelsOn).toBe(false);
	});

	it('selects a pin, resolves it, and clears on a second tap', () => {
		const s = new MapCardState(card());
		expect(s.selected).toBeNull();
		s.select('a');
		expect(s.selected?.label).toBe('A');
		s.select('b');
		expect(s.selected?.id).toBe('b');
		s.select('b');
		expect(s.selected).toBeNull();
	});

	it('toggles expanded', () => {
		const s = new MapCardState(card());
		s.toggleExpanded();
		expect(s.expanded).toBe(true);
	});
});

describe('a show_map card reaching the timeline', () => {
	const result = (extra: Record<string, unknown> = {}) =>
		({ type: 'tool_result', tool: 'show_map', result: 'ok', map: card(), ...extra }) as unknown as ServerEvent;

	it('attaches the card live, matched by call_id', () => {
		const turn: ChatTurn = { role: 'assistant', content: '', streaming: true };
		applyStreamingEvent(turn, { type: 'tool_call', tool: 'show_map', call_id: 'c1' } as ServerEvent);
		applyStreamingEvent(turn, result({ call_id: 'c1' }));
		expect(turn.timeline?.[0]).toMatchObject({ kind: 'tool', tool: 'show_map', done: true });
		expect((turn.timeline?.[0] as { map?: MapPayload }).map?.id).toBe('map-abc123');
	});

	it('attaches the card live via the name fallback when no call_id is sent', () => {
		const turn: ChatTurn = { role: 'assistant', content: '', streaming: true };
		applyStreamingEvent(turn, { type: 'tool_call', tool: 'show_map' } as ServerEvent);
		applyStreamingEvent(turn, result());
		expect((turn.timeline?.[0] as { map?: MapPayload }).map?.markers).toHaveLength(3);
	});

	const stored = (message: string, data: unknown): StoredEvent => ({
		id: 1,
		level: 'info',
		source: 'tool.show_map',
		message,
		data: JSON.stringify(data),
		created_at: ''
	});

	it('rebuilds the card on reload, matched by call_id', () => {
		// The gap `show` hit: fine live, a bare chip after a hard reload.
		const tl = buildTimelineFromEvents([
			stored('tool call started', { args: { kind: 'map' }, call_id: 'c1' }),
			stored('tool call finished', { result: 'ok', call_id: 'c1', map: card() })
		]);
		expect(tl).toHaveLength(1);
		expect((tl[0] as { map?: MapPayload }).map?.id).toBe('map-abc123');
		expect((tl[0] as { map?: MapPayload }).map?.snapshot).toBeUndefined();
	});

	it('rebuilds the card on reload via the name fallback for events with no call_id', () => {
		const tl = buildTimelineFromEvents([
			stored('tool call started', { args: {} }),
			stored('tool call finished', { result: 'ok', map: card() })
		]);
		expect((tl[0] as { map?: MapPayload }).map?.layers).toHaveLength(2);
	});

	it('leaves a failed call (no map) as an ordinary finished tool item', () => {
		const tl = buildTimelineFromEvents([
			stored('tool call started', { args: {}, call_id: 'c1' }),
			stored('tool call finished', { result: 'error: too many markers', call_id: 'c1' })
		]);
		expect(tl[0]).toMatchObject({ kind: 'tool', done: true, result: 'error: too many markers' });
		expect((tl[0] as { map?: MapPayload }).map).toBeUndefined();
	});
});
