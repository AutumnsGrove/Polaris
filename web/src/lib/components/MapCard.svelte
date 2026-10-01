<script lang="ts">
	// show_map's interactive card — see docs/plans/interactive-maps.md. Renders the
	// server-resolved `map` payload (never the model's raw args): pan/zoom, tap a pin
	// or a list row to select it, toggle the model's drawn layers, expand full-screen.
	//
	// Leaflet is imported lazily inside onMount: it touches `window` at import time
	// and is ~150KB that only a thread that actually shows a map should pay for.
	import 'leaflet/dist/leaflet.css';
	import { onMount, tick, untrack } from 'svelte';
	import type * as Leaflet from 'leaflet';
	import { Maximize2, Minimize2, Navigation } from '@lucide/svelte';
	import type { MapPayload, MapMarker } from '$lib/types';
	import { MapCardState, directionsUrl } from '$lib/mapCard.svelte';

	let { map }: { map: MapPayload } = $props();

	// The payload is immutable per tool_result (an update arrives as a new tool call
	// with a new payload), so the state is built once from the initial prop.
	const card = untrack(() => new MapCardState(map));

	// Public OSM, for the *live* card only: OSM's policy allows viewport-driven
	// tile requests from a user's own browser (it needs the Referer the browser
	// sends by default — the app sets no restrictive Referrer-Policy). The server's
	// snapshot renderer must never use this host; see config.Maps.
	const TILE_URL = 'https://tile.openstreetmap.org/{z}/{x}/{y}.png';
	const ATTRIBUTION =
		'&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap</a> contributors';

	let mapEl: HTMLDivElement | undefined = $state();
	let ready = $state(false);
	let failed = $state(false);

	// Leaflet objects aren't reactive; the effects below read card state and apply it.
	let L: typeof Leaflet;
	let lmap: Leaflet.Map | undefined;
	const markerObjs = new Map<string, Leaflet.Marker>();
	const groups = new Map<string, Leaflet.LayerGroup>();

	/** Leaflet's tooltip API takes an HTML string; labels are model-written text that
	 *  may have come from web pages, so build a text node instead of passing markup. */
	function textEl(text: string): HTMLElement {
		const el = document.createElement('span');
		el.textContent = text;
		return el;
	}

	function pinIcon(m: MapMarker, selected: boolean): Leaflet.DivIcon {
		// card.labels[...] is a star or digits we generated — safe to interpolate.
		return L.divIcon({
			className: '',
			html: `<div class="mc-pin ${m.kind ?? 'place'}${selected ? ' sel' : ''}">${card.labels[m.id]}</div>`,
			iconSize: [28, 28],
			iconAnchor: [14, 14]
		});
	}

	/** Degrees spanned by a circle's radius, for framing (no map projection needed). */
	function circleExtent(lat: number, lon: number, radiusM: number): [number, number][] {
		const dLat = radiusM / 111320;
		const dLon = dLat / Math.max(0.01, Math.cos((lat * Math.PI) / 180));
		return [
			[lat - dLat, lon - dLon],
			[lat + dLat, lon + dLon]
		];
	}

	function build() {
		if (!mapEl) return;
		lmap = L.map(mapEl, { zoomControl: true, attributionControl: true });
		L.tileLayer(TILE_URL, { maxZoom: 19, attribution: ATTRIBUTION }).addTo(lmap);

		const frame = L.latLngBounds([]);
		for (const l of map.layers) groups.set(l.id, L.layerGroup());

		// Colours come from CSS classes (see .mc-shape below), not Leaflet's `color`
		// option: Leaflet writes that into SVG presentation attributes, where
		// `var(--token)` doesn't resolve, so a themed colour has to be a stylesheet rule.
		const shapeStyle = { weight: 3, fillOpacity: 0.08, className: 'mc-shape' };
		const lineStyle = { weight: 3, className: 'mc-shape mc-line' };
		for (const s of map.shapes) {
			let layer: Leaflet.Layer | undefined;
			if (s.type === 'circle' && s.lat != null && s.lon != null && s.radius_m) {
				layer = L.circle([s.lat, s.lon], { ...shapeStyle, radius: s.radius_m });
				for (const p of circleExtent(s.lat, s.lon, s.radius_m)) frame.extend(p);
			} else if (s.points?.length) {
				const pts = s.points.map((p) => [p[0], p[1]] as [number, number]);
				pts.forEach((p) => frame.extend(p));
				if (s.type === 'polygon') layer = L.polygon(pts, shapeStyle);
				else
					layer = L.polyline(pts, {
						...lineStyle,
						// Routes dashed, arrows solid — matches the snapshot renderer.
						dashArray: s.type === 'polyline' ? '8 8' : undefined
					});
			}
			if (!layer) continue;
			if (s.label) {
				(layer as Leaflet.Path).bindTooltip(textEl(s.label), {
					permanent: true,
					direction: s.type === 'circle' ? 'bottom' : 'center',
					className: 'mc-shape-label'
				});
			}
			groups.get(s.layer)?.addLayer(layer);
		}

		for (const m of map.markers) {
			if (m.lat == null || m.lon == null) continue;
			if (m.kind !== 'muted') frame.extend([m.lat, m.lon]);
			const mk = L.marker([m.lat, m.lon], { icon: pinIcon(m, false), keyboard: true, title: m.label });
			mk.bindTooltip(textEl(m.label), { direction: 'top', offset: [0, -14], permanent: true });
			mk.on('click', () => card.select(m.id));
			mk.addTo(lmap);
			markerObjs.set(m.id, mk);
		}

		// Frame everything the model drew rather than trusting a fixed center/zoom: a
		// 10-minute-walk ring or a far pin would otherwise overflow the card. Muted
		// (out-of-range) pins are left out so one distant pin doesn't blow out the frame.
		const v = map.view;
		if (v.bounds) lmap.fitBounds(v.bounds, { padding: [16, 16] });
		else if (frame.isValid()) lmap.fitBounds(frame, { padding: [24, 24], maxZoom: v.zoom ?? 17 });
		else if (v.center) lmap.setView(v.center, v.zoom ?? 15);
		else lmap.setView([0, 0], 2);
	}

	onMount(() => {
		let disposed = false;
		(async () => {
			try {
				const mod = await import('leaflet');
				if (disposed) return;
				L = (mod.default ?? mod) as typeof Leaflet;
				if (map.kind === 'map') build();
				ready = true;
			} catch (err) {
				console.error('map card failed to load', err);
				failed = true;
			}
		})();
		return () => {
			disposed = true;
			lmap?.remove();
			lmap = undefined;
		};
	});

	// Layer toggles: real Leaflet layer groups, so flipping one is instant.
	$effect(() => {
		if (!ready || !lmap) return;
		for (const [id, group] of groups) {
			if (card.layerOn[id]) group.addTo(lmap);
			else lmap.removeLayer(group);
		}
	});

	// Selection: restyle the pins, and bring the selected one into view.
	$effect(() => {
		if (!ready || !lmap) return;
		const sel = card.selectedId;
		for (const m of map.markers) markerObjs.get(m.id)?.setIcon(pinIcon(m, m.id === sel));
		const picked = sel ? markerObjs.get(sel) : undefined;
		if (picked) lmap.panTo(picked.getLatLng());
	});

	// Expand/collapse resizes the container; Leaflet caches its size and would keep
	// clipping tiles to the old box without this.
	$effect(() => {
		void card.expanded;
		if (!ready || !lmap) return;
		tick().then(() => lmap?.invalidateSize());
	});

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && card.expanded) card.toggleExpanded();
	}

	const directions = $derived(card.selected ? directionsUrl(card.selected) : null);
</script>

<svelte:window onkeydown={onKeydown} />

<div class="map-card" class:expanded={card.expanded} class:labels-off={!card.labelsOn}>
	<div class="mc-head">
		<div class="mc-title">{map.title || (map.kind === 'image' ? 'Annotated image' : 'Map')}</div>
		<button
			class="mc-icon-btn"
			aria-label={card.expanded ? 'Collapse map' : 'Expand map'}
			onclick={() => card.toggleExpanded()}
		>
			{#if card.expanded}<Minimize2 size={16} />{:else}<Maximize2 size={16} />{/if}
		</button>
	</div>

	{#if map.kind === 'image' && map.image}
		<!-- Image cards (annotations over a picture) get their pan/zoom viewer in a later
		     phase of the plan; until then show the picture and its pin list. -->
		<img class="mc-image" src={map.image.url} alt={map.title || 'annotated image'} />
	{:else}
		<div class="mc-map" bind:this={mapEl} role="application" aria-label={map.title || 'Interactive map'}></div>
		{#if failed}
			<div class="mc-note">The map couldn't load.</div>
		{/if}
	{/if}

	{#if map.layers.length || map.markers.length}
		<div class="mc-chips">
			{#each map.layers as layer (layer.id)}
				<button
					class="mc-chip"
					aria-pressed={card.layerOn[layer.id]}
					onclick={() => card.toggleLayer(layer.id)}
				>
					{layer.label}
				</button>
			{/each}
			<button class="mc-chip" aria-pressed={card.labelsOn} onclick={() => card.toggleLabels()}>
				Labels
			</button>
		</div>
	{/if}

	{#if map.markers.length}
		<div class="mc-foot" aria-live="polite">
			{#if card.selected}
				<div class="mc-foot-name">{card.selected.label}</div>
				<div class="mc-foot-detail">
					{card.selected.detail ?? ''}
					{#if directions}
						<a class="mc-directions" href={directions} target="_blank" rel="noopener noreferrer">
							<Navigation size={12} /> Directions
						</a>
					{/if}
				</div>
			{:else}
				<div class="mc-foot-detail">Tap a pin or a row.</div>
			{/if}
		</div>
		<div class="mc-list">
			{#each map.markers as m (m.id)}
				<button class="mc-row" class:sel={card.selectedId === m.id} onclick={() => card.select(m.id)}>
					<span class="mc-pin static {m.kind ?? 'place'}">{card.labels[m.id]}</span>
					<span class="mc-row-text">
						<span class="mc-row-label">{m.label}</span>
						{#if m.detail}<span class="mc-row-detail">{m.detail}</span>{/if}
					</span>
				</button>
			{/each}
		</div>
	{/if}
</div>

<style>
	.map-card {
		background: var(--color-surface);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		overflow: hidden;
		max-width: 640px;
		margin: var(--space-md) 0;
	}
	.map-card.expanded {
		position: fixed;
		inset: 0;
		z-index: var(--z-modal);
		max-width: none;
		margin: 0;
		border-radius: 0;
		border: 0;
		display: flex;
		flex-direction: column;
		overflow-y: auto;
	}
	.mc-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
	}
	.mc-title {
		font-size: 0.9rem;
		font-weight: 600;
		color: var(--color-text);
	}
	.mc-icon-btn {
		display: grid;
		place-items: center;
		min-width: 44px;
		min-height: 44px;
		background: transparent;
		border: 0;
		border-radius: var(--radius-full);
		color: var(--color-text-dim);
		cursor: pointer;
	}
	.mc-icon-btn:hover {
		background: var(--color-surface-2);
		color: var(--color-text);
	}
	.mc-map {
		height: 320px;
		background: var(--color-surface-2);
	}
	.expanded .mc-map {
		flex: 1;
		height: auto;
		min-height: 240px;
	}
	.mc-image {
		display: block;
		width: 100%;
		max-height: 420px;
		object-fit: contain;
		background: var(--color-surface-2);
	}
	.mc-note {
		padding: var(--space-md) var(--space-lg);
		color: var(--color-text-dim);
		font-size: 0.85rem;
	}

	/* The basemap is a light raster; in the (default) dark theme, invert it and tone it down
	   rather than needing a second, dark tile provider. */
	.mc-map :global(.leaflet-tile-pane) {
		filter: invert(1) hue-rotate(180deg) brightness(0.85) contrast(0.9) saturate(0.6);
	}
	:global(:root[data-theme='light']) .mc-map :global(.leaflet-tile-pane) {
		filter: none;
	}
	.mc-map :global(.leaflet-container) {
		font: inherit;
	}
	.mc-map :global(.leaflet-control-attribution) {
		background: color-mix(in srgb, var(--color-surface) 80%, transparent);
		color: var(--color-text-dim);
	}
	.mc-map :global(.leaflet-control-attribution a) {
		color: var(--color-accent-2);
	}

	.mc-map :global(path.mc-shape) {
		stroke: var(--color-accent-2);
		fill: var(--color-accent-2);
	}
	/* Open lines must not be filled (an SVG polyline's default fill would close the shape). */
	.mc-map :global(path.mc-shape.mc-line) {
		fill: none;
	}

	/* Leaflet injects tooltips and pins outside Svelte's scoping, hence :global. */
	.mc-map :global(.leaflet-tooltip) {
		background: var(--color-surface);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		box-shadow: none;
		font-size: 0.75rem;
		padding: 2px var(--space-sm);
	}
	.mc-map :global(.leaflet-tooltip-top::before) {
		border-top-color: var(--color-surface);
	}
	.mc-map :global(.leaflet-tooltip.mc-shape-label) {
		background: transparent;
		border: 0;
		color: var(--color-accent-2);
		font-weight: 600;
		text-shadow:
			0 0 3px #000,
			0 0 3px #000;
	}
	.labels-off .mc-map :global(.leaflet-tooltip) {
		display: none;
	}

	.mc-pin {
		display: grid;
		place-items: center;
		width: 28px;
		height: 28px;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		color: var(--color-bg);
		border: 2px solid var(--color-bg);
		font-size: 0.8rem;
		font-weight: 700;
		line-height: 1;
	}
	.mc-pin.landmark {
		background: var(--color-accent-2-strong);
	}
	.mc-pin.muted {
		background: var(--color-text-dim);
	}
	.mc-pin.static {
		flex: none;
	}
	.map-card :global(.mc-pin.sel) {
		transform: scale(1.25);
		box-shadow: 0 0 0 3px var(--color-accent-soft-strong);
	}

	.mc-chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		padding: var(--space-md) var(--space-lg);
	}
	.mc-chip {
		min-height: 44px;
		padding: 0 var(--space-lg);
		border: 0;
		border-radius: var(--radius-full);
		background: var(--color-surface-2);
		color: var(--color-text-dim);
		font: inherit;
		font-size: 0.85rem;
		cursor: pointer;
	}
	.mc-chip[aria-pressed='true'] {
		background: var(--color-accent-soft-strong);
		color: var(--color-accent);
	}

	.mc-foot {
		padding: var(--space-md) var(--space-lg);
		border-top: 1px solid var(--color-border);
		min-height: 64px;
	}
	.mc-foot-name {
		font-weight: 600;
		color: var(--color-text);
	}
	.mc-foot-detail {
		color: var(--color-text-dim);
		font-size: 0.85rem;
	}
	.mc-directions {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		margin-left: var(--space-sm);
		color: var(--color-accent-2);
	}

	.mc-list {
		display: grid;
		gap: var(--space-xs);
		padding: 0 var(--space-lg) var(--space-lg);
	}
	.mc-row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		min-height: 44px;
		padding: var(--space-sm) var(--space-md);
		border: 0;
		border-radius: var(--radius-md);
		background: var(--color-surface-2);
		color: var(--color-text);
		font: inherit;
		text-align: left;
		cursor: pointer;
	}
	.mc-row.sel {
		outline: 2px solid var(--color-accent);
	}
	.mc-row-text {
		display: flex;
		flex-direction: column;
	}
	.mc-row-detail {
		color: var(--color-text-dim);
		font-size: 0.8rem;
	}
</style>
