<script lang="ts">
	import { ArrowDown } from '@lucide/svelte';

	// Floating "scroll to latest" control shown while the timeline is scrolled up.
	// busy adds the pulsing dot while a turn is arriving.
	let { busy, onClick }: { busy: boolean; onClick: () => void } = $props();
</script>

<button
	class="jump-to-bottom"
	onclick={onClick}
	aria-label="Scroll to latest"
	title="Scroll to latest"
>
	<ArrowDown size={16} />
	{#if busy}<span class="jump-to-bottom-dot" aria-hidden="true"></span>{/if}
</button>

<style>
	/* Appears once the user scrolls away from the bottom (see
	   pinnedToBottom) — same "jump to latest" affordance Claude/ChatGPT
	   show while a reply is streaming, so scrolling up to read doesn't
	   mean losing your place once you're ready to catch up. The pulsing
	   dot only shows while a turn is actually in flight — otherwise this
	   is just "you're not at the bottom", not "something new is arriving". */
	.jump-to-bottom {
		position: absolute;
		bottom: 16px;
		left: 50%;
		transform: translateX(-50%);
		display: flex;
		align-items: center;
		justify-content: center;
		width: 36px;
		height: 36px;
		border-radius: var(--radius-full);
		border: none;
		background: var(--color-surface-2);
		color: var(--color-text);
		box-shadow: 0 4px 16px color-mix(in srgb, black 20%, transparent), var(--shadow-glass-edge);
		transition:
			background-color 0.15s var(--ease-out-expo),
			transform 0.15s var(--ease-out-expo);
	}

	.jump-to-bottom:hover {
		background: var(--color-surface-3);
		transform: translateX(-50%) translateY(-1px);
	}

	.jump-to-bottom-dot {
		position: absolute;
		top: -2px;
		right: -2px;
		width: 9px;
		height: 9px;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		animation: jump-to-bottom-pulse 1.4s ease-in-out infinite;
	}

	@keyframes jump-to-bottom-pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.4;
		}
	}
</style>
