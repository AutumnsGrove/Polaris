<script lang="ts">
	import { appState } from '$lib/state.svelte';
</script>

<div class="toast-stack">
	{#each appState.toasts as toast (toast.id)}
		<div class="toast">{toast.message}</div>
	{/each}
</div>

<style>
	.toast-stack {
		position: fixed;
		left: 50%;
		bottom: calc(24px + env(safe-area-inset-bottom));
		transform: translateX(-50%);
		z-index: 200;
		display: flex;
		flex-direction: column-reverse;
		align-items: center;
		gap: 8px;
		pointer-events: none;
	}

	.toast {
		background: var(--color-surface-3);
		color: var(--color-text);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-md);
		padding: 9px 16px;
		font-size: 13px;
		box-shadow:
			0 12px 32px -12px rgba(0, 0, 0, 0.45),
			0 4px 12px -4px rgba(0, 0, 0, 0.3);
		animation: toast-in 0.2s var(--ease-out-expo);
		white-space: nowrap;
	}

	@keyframes toast-in {
		from {
			opacity: 0;
			transform: translateY(6px);
		}
		to {
			opacity: 1;
			transform: translateY(0);
		}
	}
</style>
