<script lang="ts">
	import { WifiOff, RotateCcw } from '@lucide/svelte';

	// Distinct from the generic "Error: <raw Go text>" bubble a
	// non-network turn failure still falls back to (see the
	// 'error' case in state.svelte.ts) — this is specifically the
	// "never even reached the provider" case (dropped wifi, DNS
	// failure, timeout), which deserves a plain "try again"
	// rather than surfacing text like "read tcp 10.0.0.5:1234->
	// ...: operation timed out" that means nothing to look at
	// and leaks a local IP besides. Mirrors ChatView.svelte's
	// .interrupted banner (same dangling-turn idea, different
	// trigger: that one is a turn with no error event at all,
	// this one got a real one).
	let { disabled, onRetry }: { disabled: boolean; onRetry: () => void } = $props();
</script>

<div class="network-error">
	<div class="network-error-message">
		<WifiOff size={15} />
		<span>Couldn't reach the AI provider — check your connection and try again.</span>
	</div>
	<button class="btn btn-accent" onclick={() => onRetry()} disabled={disabled}>
		<RotateCcw size={15} />
		Retry
	</button>
</div>

<style>
	/* Same layout as ChatView.svelte's .interrupted banner, but on the
	   danger palette instead of the neutral surface — this is a real
	   failure with a concrete cause (no response at all), not just "still
	   waiting"/"session dropped, nothing lost yet". */
	.network-error {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-lg);
		flex-wrap: wrap;
		background: var(--color-danger-bg);
		border-radius: var(--radius-lg);
		padding: var(--space-lg);
	}

	.network-error-message {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		flex: 1;
		min-width: 220px;
		font-size: 13.5px;
		line-height: 1.4;
		color: var(--color-text-dim);
	}

	.network-error-message :global(svg) {
		flex-shrink: 0;
		color: var(--color-danger);
	}
</style>
