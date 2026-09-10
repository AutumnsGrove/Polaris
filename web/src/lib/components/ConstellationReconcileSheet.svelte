<script lang="ts">
	import { X, Send } from '@lucide/svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { autoResize } from '$lib/actions/autoResize';

	// ConstellationReconcileSheet backs both Edit star (mockup screen 4) and
	// Refine (screen 7) — same free-text, LLM-reconciled shape per the plan
	// doc's "Reviewing and editing a star": neither is a form/field editor,
	// both are "tell Weaver what's wrong in your own words." mode only
	// swaps copy; the caller decides what submitting actually calls
	// (editStar vs. reviewStar(..., 'refine', ...)).
	let {
		mode,
		starTitle,
		onSubmit,
		onClose
	}: {
		mode: 'edit' | 'refine';
		starTitle: string;
		onSubmit: (text: string) => Promise<{ error: string }>;
		onClose: () => void;
	} = $props();

	let text = $state('');
	let sending = $state(false);
	let error = $state('');

	const COPY = {
		edit: {
			title: 'Edit star',
			hint: "Tell it what's wrong or what to add, in your own words — Weaver folds it into this star. No fields to fill in.",
			placeholder: "e.g. \"actually we moved off this a while back — wasn't the right fit after all\"",
			footnote: 'Reads like a note to Constellation, not a form.'
		},
		refine: {
			title: 'Refine star',
			hint: "Tell it what it got right and what's off — Weaver reworks the star from there instead of starting over.",
			placeholder: 'e.g. "yeah, been on a real kick lately" or "not really, I was just asking one for a friend"',
			footnote: "Sending this also settles the review — no separate approve step after."
		}
	};
	// $derived, not a plain const off mode — mode is a prop, so a plain
	// destructure at module-init time would only ever reflect its initial
	// value instead of reacting if the caller ever swaps mode on an
	// already-mounted instance.
	const copy = $derived(COPY[mode]);

	async function submit() {
		const trimmed = text.trim();
		if (!trimmed || sending) return;
		sending = true;
		error = '';
		const result = await onSubmit(trimmed);
		sending = false;
		if (result.error) {
			error = result.error;
			return;
		}
		onClose();
	}
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label={copy.title}>
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<div>
				<h2>{copy.title}</h2>
				<p class="sheet-sub">{starTitle}</p>
			</div>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		<p class="sheet-hint">{copy.hint}</p>

		<div class="composer">
			<textarea
				bind:value={text}
				rows="1"
				placeholder={copy.placeholder}
				use:autoResize={{ value: text, maxHeight: 160 }}
				onkeydown={(e) => {
					if (e.key === 'Enter' && !e.shiftKey) {
						e.preventDefault();
						void submit();
					}
				}}
			></textarea>
			<button
				class="composer-send"
				onclick={submit}
				disabled={sending || !text.trim()}
				aria-label="Send"
			>
				<Send size={15} />
			</button>
		</div>

		{#if error}
			<p class="error-text">{error}</p>
		{/if}

		<p class="sheet-footnote">{copy.footnote}</p>
	</div>
</div>

<style>
	.sheet-sub {
		margin: 2px 0 0;
		font-size: 12px;
		color: var(--color-text-dim);
	}
	.sheet-hint {
		font-size: 12.5px;
		line-height: 1.55;
		color: var(--color-text-dim);
		margin: 0 0 var(--space-lg);
	}
	.composer {
		display: flex;
		align-items: flex-end;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-sm) var(--space-sm) var(--space-lg);
		border-radius: var(--radius-xl);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
	}
	.composer textarea {
		flex: 1;
		resize: none;
		border: none;
		background: none;
		font: inherit;
		font-size: 14px;
		line-height: 1.5;
		color: var(--color-text);
		padding: var(--space-xs) 0;
	}
	.composer textarea:focus {
		outline: none;
	}
	.composer-send {
		flex-shrink: 0;
		width: 34px;
		height: 34px;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		color: var(--color-bg);
		display: flex;
		align-items: center;
		justify-content: center;
		border: none;
		cursor: pointer;
	}
	.composer-send:disabled {
		opacity: 0.5;
		cursor: default;
	}
	.error-text {
		margin: var(--space-sm) 0 0;
		font-size: 12.5px;
		color: var(--color-danger);
	}
	.sheet-footnote {
		font-size: 11px;
		color: var(--color-text-dim);
		text-align: center;
		margin: var(--space-md) 0 0;
	}
</style>
