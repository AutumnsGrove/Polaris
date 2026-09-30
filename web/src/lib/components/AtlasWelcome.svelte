<script lang="ts">
	import type { Snippet } from 'svelte';
	import { fade } from 'svelte/transition';
	import { Earth } from '@lucide/svelte';

	// Atlas's start screen: branding plus the omnibox (passed in as children).
	// Fades out when the first search runs.
	let { children }: { children: Snippet } = $props();
</script>

<div class="welcome" out:fade={{ duration: 150 }}>
	<!-- The plain lucide Earth outline, not the desk-globe photo
	     (atlas-touch-icon.png) used everywhere else — that one
	     has a stand/arm molded into the artwork, which spins
	     along with the sphere and reads as broken, not
	     delightful. A bare sphere has no "wrong way up" to
	     violate. -->
	<Earth size={44} class="welcome-mark" aria-hidden="true" />
	<h1 class="welcome-heading">Atlas</h1>
	<p class="welcome-tagline">Point it anywhere.</p>
	<div class="welcome-omnibox">
		{@render children()}
	</div>
</div>

<style>
	/* Start screen — centered branding + the omnibox itself, unified with
	   ChatView's own welcome state (same idea: a floating composer/search
	   bar before the first turn, pinned to the header once there's a
	   reason to pin it). Fills most of the space below the sticky header
	   so it reads as an actual landing moment, not a stray banner. */
	.welcome {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		text-align: center;
		gap: var(--space-sm);
		min-height: min(60vh, 520px);
		isolation: isolate;
	}

	.welcome::before {
		content: '';
		position: absolute;
		inset: 0;
		z-index: var(--z-behind);
		background: radial-gradient(
			ellipse 55% 45% at 50% 32%,
			var(--accent-soft) 0%,
			color-mix(in srgb, var(--accent-soft) 55%, transparent) 40%,
			transparent 72%
		);
		pointer-events: none;
	}

	/* A globe that turns — the one piece of motion this screen gets, slow
	   and continuous enough to read as ambient rather than attention-
	   seeking (a full turn takes longer than anyone spends looking at an
	   empty search page). Pauses on hover so it doesn't fight a click,
	   and drops out entirely under reduced motion. :global() because the
	   class lands on the <svg> Earth's own component renders, not on
	   anything this file draws directly — same pattern as .icon-search
	   below. */
	.welcome :global(.welcome-mark) {
		color: var(--ink-faint);
		margin-bottom: var(--space-xs);
		animation: welcome-mark-spin 34s linear infinite;
	}

	.welcome :global(.welcome-mark:hover) {
		animation-play-state: paused;
		color: var(--accent);
	}

	@keyframes welcome-mark-spin {
		to {
			transform: rotate(360deg);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.welcome :global(.welcome-mark) {
			animation: none;
		}
	}

	/* Same hero treatment ChatView.svelte gives "Polaris" in its own
	   welcome heading — the app's name set in the Asimovian display face
	   at a scale nothing else on this page uses. */
	.welcome-heading {
		margin: 0;
		font-family: var(--font-wordmark);
		font-size: clamp(34px, 6vw, 52px);
		font-weight: 400;
		letter-spacing: 0.01em;
		color: var(--ink);
	}

	.welcome-tagline {
		margin: 0 0 var(--space-xl);
		font-family: ui-serif, Georgia, serif;
		font-style: italic;
		font-size: 15px;
		color: var(--ink-faint);
	}

	.welcome-omnibox {
		width: 100%;
		max-width: 560px;
	}

	/* A touch more presence than the pinned-header version — same idea as
	   ChatView's .welcome-composer focus ring — since this instance is
	   the whole point of the screen, not a secondary control up top. */
	.welcome-omnibox :global(.omnibox) {
		padding: var(--space-lg) var(--space-lg);
	}

	.welcome-omnibox :global(.omnibox:focus-within) {
		box-shadow: 0 0 0 4px var(--accent-soft);
	}
</style>
