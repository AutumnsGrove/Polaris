import type { MapMarker, MapPayload } from './types';

/**
 * The text on each pin, keyed by marker id: landmarks are a star, every other pin
 * is numbered 1, 2, 3… in the order the model listed them. This is the same
 * numbering the server tells the model about (tools/show_map.go's describeMap) and
 * stamps on the snapshot (mapsnap/render.go), so "pin 2" means one thing on the
 * card, in the model's description, and in its snapshot — keep all three in step.
 */
export function pinLabels(markers: MapMarker[]): Record<string, string> {
	const out: Record<string, string> = {};
	let n = 0;
	for (const m of markers) {
		out[m.id] = m.kind === 'landmark' ? '★' : String(++n);
	}
	return out;
}

/** Where a pin's Directions link goes: an external maps app, not turn-by-turn in Polaris. */
export function directionsUrl(m: MapMarker): string | null {
	if (m.lat == null || m.lon == null) return null;
	return `https://www.openstreetmap.org/directions?to=${encodeURIComponent(`${m.lat},${m.lon}`)}`;
}

/**
 * One map card's interaction state — which pin is selected, which layer toggles
 * are on, whether it's expanded full-screen. Per-card (not a singleton): a thread
 * can hold several maps, each with its own toggles. Marker ↔ list selection lives
 * here, shared by the Leaflet pins and the list rows, rather than each keeping a
 * copy to sync by hand (docs/STANDARDS.md).
 */
export class MapCardState {
	selectedId = $state<string | null>(null);
	expanded = $state(false);
	labelsOn = $state(true);
	layerOn = $state<Record<string, boolean>>({});
	readonly labels: Record<string, string>;

	constructor(readonly map: MapPayload) {
		this.layerOn = Object.fromEntries(map.layers.map((l) => [l.id, l.default_on]));
		this.labels = pinLabels(map.markers);
	}

	/** A getter, not a `$derived` field: field initialisers run before the constructor's
	 *  `map` parameter property is assigned, and reading `selectedId` ($state) here is
	 *  reactive on its own. */
	get selected(): MapMarker | null {
		return this.map.markers.find((m) => m.id === this.selectedId) ?? null;
	}

	/** Tapping the selected pin again clears it. */
	select(id: string): void {
		this.selectedId = this.selectedId === id ? null : id;
	}

	toggleLayer(id: string): void {
		this.layerOn[id] = !this.layerOn[id];
	}

	toggleLabels(): void {
		this.labelsOn = !this.labelsOn;
	}

	toggleExpanded(): void {
		this.expanded = !this.expanded;
	}
}
