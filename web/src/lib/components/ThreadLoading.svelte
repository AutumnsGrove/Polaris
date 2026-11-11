<script lang="ts">
	import { Loader2 } from '@lucide/svelte';
</script>

<!-- Shown while openThread() is fetching a thread the user just asked for
     (see AppState.threadLoading). Deliberately its own state, not the
     WelcomeScreen: an in-flight load has no composer to offer and no "ask
     anything" framing, and showing the landing view here was exactly what
     made opening a slow thread read as a bounce back to the homescreen. -->
<div class="thread-loading" role="status" aria-live="polite">
	<Loader2 size={20} class="spin" />
	<span>Opening thread…</span>
</div>

<style>
	.thread-loading {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-md);
		padding: var(--space-4xl) var(--space-xl);
		color: var(--color-text-dim);
		font-size: 13.5px;
	}

	.thread-loading :global(svg) {
		color: var(--color-accent);
	}

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
