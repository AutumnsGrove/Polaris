<script lang="ts">
	import { Icon } from '@lucide/svelte';

	// The one place Fields' icon is chosen — the sidebar nav row and pinned
	// header, the hub cards, the chat-header pill, and the composer picker
	// all render this instead of importing a glyph themselves, so swapping
	// the icon is a one-file change (options were compared in
	// mockups/field-icons.html).
	//
	// "Deep Field, two corners": a patch of sky marked out by just two
	// opposing corner brackets, with one sparkle and a few points of
	// different sizes inside it — after the Hubble Deep Field, where staring
	// at one small region turns up far more than expected. Deliberately not
	// a closed box: every other nav-column icon (Galaxy, Orbit, Sunrise,
	// Compass) is an open shape, and a full frame read as the odd one out.
	// Drawn on Lucide's 24px grid at its default 2px round stroke, with the
	// stars as fills, so it renders through the same Icon component as the
	// rest and inherits currentColor.
	let { size = 16, class: className = '' }: { size?: number; class?: string } = $props();

	const STAR = { fill: 'currentColor', stroke: 'none' };
	const node: [string, Record<string, string>][] = [
		['path', { d: 'M3 9V6a3 3 0 0 1 3-3h3' }],
		['path', { d: 'M21 15v3a3 3 0 0 1-3 3h-3' }],
		[
			'path',
			{
				d: 'M10 6.4Q11.008 8.992 13.6 10Q11.008 11.008 10 13.6Q8.992 11.008 6.4 10Q8.992 8.992 10 6.4Z',
				...STAR
			}
		],
		['circle', { cx: '16.6', cy: '7.6', r: '1.1', ...STAR }],
		['circle', { cx: '15.4', cy: '15.6', r: '1.4', ...STAR }],
		['circle', { cx: '7.4', cy: '16.6', r: '0.9', ...STAR }],
		['circle', { cx: '18', cy: '12.2', r: '0.7', ...STAR }]
	];
</script>

<Icon iconNode={node as never} {size} class={className} />
