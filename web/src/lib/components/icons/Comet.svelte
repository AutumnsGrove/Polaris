<!--
  Vendored from Majesticons (MIT) — https://majesticons.com, icon "comet".
  Chosen over Tabler's and Hugeicons' comet glyphs (see
  docs/plans/comet.md's "Naming" section and mockups/comet-icon-options.html):
  a radiating diagonal burst with a solid nucleus, built on the exact same
  conventions Lucide uses (24x24, stroke-width 2, round linecap/linejoin),
  so it sits visually consistent next to every other icon in the app.
  Lucide has no comet/meteor/shooting-star icon at all (verified against
  1.46.0 and lucide-lab), so this is one local component rather than a new
  icon-library dependency — same treatment ShootingStar.svelte got.
-->
<script lang="ts">
	let { size = 24, color = 'currentColor', ...rest }: { size?: number | string; color?: string; [key: string]: unknown } = $props();
</script>

<svg
	xmlns="http://www.w3.org/2000/svg"
	width={size}
	height={size}
	viewBox="0 0 24 24"
	fill="none"
	stroke={color}
	stroke-width="2"
	stroke-linecap="round"
	stroke-linejoin="round"
	{...rest}
>
	<path d="m9.879 5.636l.707-.707a6 6 0 0 1 8.485 8.485l-2.121 2.122M7.757 7.757L5.636 9.88M3.515 12l-.707.707m12.02 4.95l-1.414 1.414m-.707-4.95l-.707.707M9.879 16.95l-2.122 2.12m2.122-7.778l-6.364 6.364" />
	<circle cx="14.828" cy="9.172" r="2" fill={color} transform="rotate(45 14.828 9.172)" />
</svg>
