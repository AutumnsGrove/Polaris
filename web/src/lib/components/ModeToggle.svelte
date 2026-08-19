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
		Polaris
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
		border-radius: 999px;
		padding: 3px;
		gap: 2px;
	}

	.mode-toggle button {
		display: flex;
		align-items: center;
		gap: 6px;
		appearance: none;
		border: none;
		background: transparent;
		font-family: var(--font-sans);
		font-size: 12.5px;
		font-weight: 500;
		padding: 6px 14px;
		border-radius: 999px;
		color: var(--color-text-dim);
		cursor: pointer;
		transition:
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo);
	}

	.mode-toggle button :global(svg) {
		flex: none;
	}

	.mode-toggle button:hover:not(.active) {
		color: var(--color-text);
	}

	.mode-toggle button.active {
		background: var(--color-accent);
		color: var(--color-bg);
	}
</style>
