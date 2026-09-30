<script lang="ts">
	import type { Snippet } from 'svelte';
	import NightSky from '$lib/components/NightSky.svelte';
	import { appState } from '$lib/state.svelte';
	import type { fieldsState } from '$lib/fields.svelte';

	// The empty-thread landing view: heading, subtitle, and the composer floated
	// centered (passed in as children so ChatView keeps owning composer state).
	let {
		isWeaverThread,
		activeField,
		children
	}: {
		isWeaverThread: boolean;
		activeField: ReturnType<typeof fieldsState.byId>;
		children: Snippet;
	} = $props();
</script>

<div class="welcome">
	<NightSky />
	{#if isWeaverThread}
		<h1 class="welcome-heading">Talk to <span class="wordmark">Weaver</span></h1>
		<p class="subtitle wordmark">
			Tell it what's wrong across your stars — it can search, read, merge, and update them.
		</p>
	{:else}
		<h1 class="welcome-heading">Ask <span class="wordmark">Polaris</span> anything</h1>
		{#if activeField && !appState.isGhostThread}
			<p class="subtitle wordmark">
				Working in {activeField.name} — its instructions and shared files come along.
			</p>
		{:else}
			<p class="subtitle wordmark">Your questions, answered with sources from the web.</p>
		{/if}
	{/if}
	<div class="welcome-composer">
		{@render children()}
	</div>
</div>

<style>
	/* The welcome state is the ONE screen in the app allowed a committed
	   color treatment — a subtle off-center radial wash of the starlight
	   accent behind the heading. Not a card, not glass, not a gradient
	   applied to text. Just a soft distant-sun cast on the ground the
	   composer sits on. Positioned above/left of center so it feels
	   observed rather than staged. */
	.welcome {
		position: relative;
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-sm);
		padding: var(--space-4xl) var(--space-xl);
		text-align: center;
		/* A first attempt here removed this entirely to stop mobile
		   browsers from auto-scrolling this container to bring a
		   newly-focused input into view — but that just traded one bug
		   for another: with nowhere to scroll, the keyboard shrinking
		   available height clipped the heading/composer instead of
		   scrolling them, a squashed/"crunched" look. The real fix for
		   the page-jumping was locking body itself (see app.css) — once
		   the *page* can't scroll, a local scrollable container here is
		   exactly as safe as .timeline-scroll already is in the
		   conversation view, which never had this problem. */
		overflow-y: auto;
		isolation: isolate;
	}

	.welcome::before {
		content: '';
		position: absolute;
		inset: 0;
		z-index: var(--z-behind);
		background:
			radial-gradient(
				ellipse 60% 45% at 38% 34%,
				color-mix(in srgb, var(--color-accent) 22%, transparent) 0%,
				color-mix(in srgb, var(--color-accent) 8%, transparent) 35%,
				transparent 70%
			);
		pointer-events: none;
	}

	:root[data-theme='light'] .welcome::before {
		background:
			radial-gradient(
				ellipse 60% 45% at 38% 34%,
				color-mix(in srgb, var(--color-accent) 14%, transparent) 0%,
				color-mix(in srgb, var(--color-accent) 5%, transparent) 40%,
				transparent 70%
			);
	}

	.welcome-heading {
		margin: 0 0 var(--space-xs) 0;
		font-family: var(--font-serif);
		/* Real hero scale — this is the one heading in the app allowed to
		   run large, since there's no competing content on this screen. */
		font-size: clamp(36px, 6vw, 56px);
		font-weight: 700;
		letter-spacing: -0.02em;
		line-height: 1.1;
		color: var(--color-text);
	}

	.welcome-heading .wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-size: 0.88em;
		letter-spacing: 0.01em;
	}

	.welcome .subtitle {
		margin: var(--space-md) 0 var(--space-3xl) 0;
		color: var(--color-text-dim);
		line-height: 1.5;
	}

	.welcome .subtitle.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-style: italic;
		font-size: 17px;
		letter-spacing: 0.01em;
	}

	.welcome-composer {
		width: 100%;
		max-width: 640px;
	}

	.welcome-composer :global(.composer) {
		border-top: none;
		padding: 0 0 var(--space-md) 0;
	}

	/* Composer inside the welcome state gets a touch more presence —
	   a soft accent ring on focus that ties back to the hero glow.
	   Regular in-conversation composer stays plain. */
	.welcome-composer :global(textarea:focus) {
		box-shadow: 0 0 0 4px color-mix(in srgb, var(--color-accent) 18%, transparent);
	}
</style>
