<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { fly } from 'svelte/transition';
</script>

<div class="toast-stack">
	{#each appState.toasts as toast (toast.id)}
		<div class="toast" in:fly={{ y: 6, duration: 200 }} out:fly={{ y: -6, duration: 150 }}>
			{toast.message}
		</div>
	{/each}
</div>

<style>
	.toast-stack {
		position: fixed;
		left: 50%;
		bottom: calc(24px + env(safe-area-inset-bottom));
		transform: translateX(-50%);
		z-index: var(--z-toast);
		display: flex;
		flex-direction: column-reverse;
		align-items: center;
		gap: var(--space-sm);
		pointer-events: none;
	}

	.toast {
		background: var(--color-surface-3);
		color: var(--color-text);
		border: none;
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-lg);
		font-size: 13px;
		box-shadow:
			0 12px 32px -12px rgba(0, 0, 0, 0.45),
			0 4px 12px -4px rgba(0, 0, 0, 0.3),
			var(--shadow-glass-edge);
		white-space: nowrap;
	}
</style>
