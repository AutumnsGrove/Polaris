<script lang="ts">
	// Oracle mode's own icon (docs/plans/oracle-mode.md) — not in
	// @lucide/svelte, so this mirrors that library's minimal external shape
	// (size + passthrough props onto the <svg>) rather than introducing a
	// different icon API just for one glyph. Unlike a generic lucide icon,
	// the path/circle geometry here is fixed art (four stars of different
	// sizes joined by two line segments), not meant to be recolored via
	// strokeWidth — only `size` and `class` are expected to vary at call
	// sites (see mockups/oracle-mode.html's `#i-asterism` this was lifted
	// from).
	let { size = 24, ...rest }: { size?: number; [key: string]: unknown } = $props();
</script>

<svg
	xmlns="http://www.w3.org/2000/svg"
	width={size}
	height={size}
	viewBox="0 0 24 24"
	fill="none"
	{...rest}
>
	<path
		d="M5 18 9.5 8.5 19 5M9.5 8.5 16 16.5"
		fill="none"
		stroke="currentColor"
		stroke-width="1.5"
		stroke-linecap="round"
		stroke-linejoin="round"
		opacity=".7"
	/>
	<circle cx="5" cy="18" r="2.1" fill="currentColor" />
	<circle cx="9.5" cy="8.5" r="2.4" fill="currentColor" />
	<circle cx="19" cy="5" r="1.8" fill="currentColor" />
	<circle cx="16" cy="16.5" r="2.1" fill="currentColor" />
</svg>
