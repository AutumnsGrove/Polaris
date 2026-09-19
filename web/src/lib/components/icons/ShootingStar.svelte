<!--
  Vendored from Majesticons (MIT) — https://majesticons.com, icon "shooting-star".
  Not in @lucide/svelte (no comet/meteor/shooting-star icon exists there as of
  1.46.0 — checked directly), so this is one local component rather than a new
  icon-library dependency. Matches Lucide's own stroke conventions (24x24,
  stroke-width 2, round linecap/linejoin) so it sits visually consistent next
  to every other icon in the app. Used for the 'stars' tool card (ToolEvent.svelte)
  — a much stronger visual fit than the previous CircleStar than a plain circled star.
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
	<path
		d="M9 8c-1.667.667-5.4 2.7-7 5.5m9.5-2.5C9.167 12.333 4 16.4 2 22m10.5-7.5c-1.167 1.167-3.8 4.1-5 6.5m7.174-14.55l.673-3.285l2.225 2.51l3.027-.294l-1.768 3.062l1.743 2.639l-3.286-.673l-2.51 2.225l.19-3.156l-3.062-1.768z"
	/>
</svg>
