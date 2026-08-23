<script lang="ts">
	import { untrack } from 'svelte';
	import { X, Check } from '@lucide/svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { autoResize } from '$lib/actions/autoResize';

	// General-purpose "edit this text" modal — same centered/bottom-sheet
	// treatment as SettingsPanel/ComposerMenu (see app.css's .modal-*
	// rules), but sized for a single field instead of a whole settings
	// list. Built because the thread-rename box used to be a ~150px input
	// squeezed into ThreadMenu's dropdown — unusable on mobile, where you
	// could see maybe a word of the title at a time. This gives any
	// "rename X" or "edit Y" flow a real, full-width writing surface
	// (same font-size/line-height as the composer's own textarea) instead
	// of a cramped one-off input.
	let {
		heading,
		initialValue = '',
		placeholder = '',
		maxLength,
		saveLabel = 'Save',
		onSave,
		onCancel
	}: {
		heading: string;
		initialValue?: string;
		placeholder?: string;
		maxLength?: number;
		saveLabel?: string;
		onSave: (value: string) => void;
		onCancel: () => void;
	} = $props();

	// The component is mounted fresh each time a caller opens it (an {#if}
	// around it, same as SettingsPanel/ComposerMenu), so seeding from
	// initialValue once is exactly right — untrack tells Svelte that's
	// intentional rather than a missed reactive dependency.
	let value = $state(untrack(() => initialValue));

	function save() {
		const trimmed = value.trim();
		if (trimmed) onSave(trimmed);
	}

	// Plain Enter saves (titles/single-field edits are the common case,
	// matching the old inline input's behavior); Shift+Enter still gets a
	// newline for whenever this is reused on something that wants one.
	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			save();
		} else if (e.key === 'Escape') {
			onCancel();
		}
	}

	function focusAndSelect(node: HTMLTextAreaElement) {
		node.focus();
		node.select();
	}
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onCancel} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label={heading}>
		<div class="sheet-handle" use:swipeToDismiss={onCancel} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>{heading}</h2>
			<button class="icon-btn" onclick={onCancel} title="Close"><X size={18} /></button>
		</div>

		<textarea
			class="edit-field"
			bind:value
			{placeholder}
			maxlength={maxLength}
			rows="1"
			use:autoResize={{ value, maxHeight: 320 }}
			use:focusAndSelect
			onkeydown={onKeydown}
		></textarea>

		<div class="modal-actions">
			<button class="btn" onclick={onCancel}>Cancel</button>
			<button class="btn btn-accent" onclick={save} disabled={!value.trim()}>
				<Check size={15} />
				{saveLabel}
			</button>
		</div>
	</div>
</div>

<style>
	/* Same recipe as ChatView's composer textarea — 16px stops iOS Safari
	   from zooming the page on focus, and autoResize grows it with content
	   instead of leaving long titles to scroll inside a fixed box. */
	.edit-field {
		display: block;
		width: 100%;
		resize: none;
		border: 1px solid var(--color-accent-2);
		background: var(--color-surface-2);
		box-shadow: var(--shadow-well);
		border-radius: var(--radius-lg);
		padding: var(--space-lg) var(--space-lg);
		font-size: 16px;
		line-height: 1.5;
		font-family: var(--font-sans);
		color: var(--color-text);
		outline: none;
		min-height: 56px;
	}

	.modal-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-sm);
		margin-top: var(--space-lg);
	}

	.modal-actions .btn-accent {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}
</style>
