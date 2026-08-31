<script lang="ts">
	import { goto } from '$app/navigation';
	import { Compass, Telescope } from '@lucide/svelte';

	let { mode }: { mode: 'assistant' | 'search' } = $props();
</script>

<!--
	Shared between ChatView's homepage header and Atlas's own header — one
	small piece of consistent chrome so switching between the two doesn't
	feel like switching apps, same reasoning as Sidebar.svelte being shared.
	Uses app.css's global --color-* tokens directly rather than either
	page's own palette, so it renders identically regardless of which side
	it's on. Icons echo each product's own identity: Compass for Atlas (the
	reference you consult), Telescope for Polaris (what you point at the
	night sky to find your fixed point by) — not arbitrary glyphs.
-->
<div class="mode-toggle" role="tablist" aria-label="Switch between Atlas and Polaris">
	<button
		type="button"
		class:active={mode === 'assistant'}
		onclick={() => goto('/')}
		role="tab"
		aria-selected={mode === 'assistant'}
	>
		<Telescope size={13} />
		<span class="wordmark">Polaris</span>
	</button>
	<button
		type="button"
		class:active={mode === 'search'}
		onclick={() => goto('/search')}
		role="tab"
		aria-selected={mode === 'search'}
	>
		<Compass size={13} />
		Atlas
	</button>
</div>

<style>
	.mode-toggle {
		display: flex;
		flex-shrink: 0;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-full);
		padding: var(--space-xs);
		gap: var(--space-xs);
	}

	.mode-toggle button {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		appearance: none;
		border: none;
		background: transparent;
		font-family: var(--font-sans);
		font-size: 12.5px;
		font-weight: 500;
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-full);
		color: var(--color-text-dim);
		cursor: pointer;
		transition:
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo);
	}

	.mode-toggle button :global(svg) {
		flex: none;
	}

	/* Same reserved-brand-face treatment as ChatView.svelte's
	   .welcome-heading .wordmark and Sidebar.svelte's own switcher label —
	   the literal word "Polaris" always renders in Asimovian, everywhere
	   it appears as a name rather than incidental prose. */
	.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-size: 1.05em;
		letter-spacing: 0.02em;
	}

	.mode-toggle button:hover:not(.active) {
		color: var(--color-text);
	}

	.mode-toggle button.active {
		background: var(--color-accent);
		color: var(--color-bg);
	}
</style>
