<script lang="ts">
	import { X, TriangleAlert } from '@lucide/svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';

	// General-purpose "are you sure" modal — same modal-backdrop/modal-panel
	// shell as EditTextModal.svelte, for any destructive or hard-to-undo
	// action that deserves a real full-screen confirmation instead of a
	// dropdown's cramped inline Cancel/Delete pair. Not a replacement for
	// ThreadMenu.svelte's own inline confirm (that one's deliberately
	// lightweight for a menu item the user is already looking straight at)
	// — this is for actions triggered from somewhere with more room and
	// more at stake, like the Memory settings list.
	let {
		heading,
		message,
		confirmLabel = 'Delete',
		danger = true,
		onConfirm,
		onCancel
	}: {
		heading: string;
		message: string;
		confirmLabel?: string;
		danger?: boolean;
		onConfirm: () => void;
		onCancel: () => void;
	} = $props();

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') onCancel();
	}
</script>

<svelte:window onkeydown={onKeydown} />

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onCancel} aria-label="Close"></button>
	<div class="modal-panel confirm-panel" role="alertdialog" aria-modal="true" aria-label={heading}>
		<div class="sheet-handle" use:swipeToDismiss={onCancel} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>{heading}</h2>
			<button class="icon-btn" onclick={onCancel} title="Close"><X size={18} /></button>
		</div>

		<p class="confirm-message">
			{#if danger}
				<TriangleAlert size={16} />
			{/if}
			<span>{message}</span>
		</p>

		<div class="modal-actions">
			<button class="btn" onclick={onCancel}>Cancel</button>
			<button class="btn" class:btn-danger={danger} class:btn-accent={!danger} onclick={onConfirm}>
				{confirmLabel}
			</button>
		</div>
	</div>
</div>

<style>
	.confirm-message {
		display: flex;
		align-items: flex-start;
		gap: var(--space-sm);
		margin: 0;
		font-size: 14px;
		line-height: 1.5;
		color: var(--color-text);
	}

	.confirm-message :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
		color: var(--color-danger);
	}

	.modal-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-sm);
		margin-top: var(--space-lg);
	}

	.btn-danger {
		background: var(--color-danger-bg);
		color: var(--color-danger);
	}

	.btn-danger:hover {
		background: var(--color-danger);
		color: var(--color-bg);
	}
</style>
