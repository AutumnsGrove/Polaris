<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { pulsarWizardState } from '$lib/pulsarWizard.svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { autoResize } from '$lib/actions/autoResize';
	import { X, Send, Loader2, Check } from '@lucide/svelte';

	// seed: whatever's currently typed into the routine form's prompt
	// field, if anything — passed straight through to
	// pulsarWizardState.start(). onAccept fires per "Use this prompt"
	// click, independent of whether it's the latest draft in the
	// transcript (see pulsarWizard.svelte.ts's applyResponse) — see
	// accept() below for why this also closes the wizard.
	let { seed, onClose, onAccept }: { seed: string; onClose: () => void; onAccept: (prompt: string, name?: string) => void } =
		$props();

	let freeform = $state('');

	// "Use this prompt" closes the wizard outright, same as picking a
	// value from any other picker — simpler than the alternative tried
	// first (a button that flips to an inert "added" state while the
	// wizard stays open): closing is the expected behavior for an accept
	// action, and it puts the now-filled form directly in view instead of
	// behind this modal's own backdrop.
	function accept(prompt: string, name?: string) {
		onAccept(prompt, name);
		close();
	}

	let transcriptEl: HTMLDivElement | undefined;

	// Always pins to the newest message — unlike ChatView's thread history
	// (long-lived, worth letting a user scroll back through mid-stream
	// without being yanked back down), a wizard session is short and
	// linear: there's never a reason to be looking at anything but the
	// latest question or draft.
	$effect(() => {
		void pulsarWizardState.transcript.length;
		void pulsarWizardState.loading;
		tick().then(() => {
			transcriptEl?.scrollTo({ top: transcriptEl.scrollHeight, behavior: 'smooth' });
		});
	});

	onMount(() => {
		// Detached, not awaited — onMount itself must stay synchronous (see
		// /pulsar/[id]/+page.svelte's identical note on why: an async
		// onMount callback's returned Promise isn't treated as a teardown
		// function by Svelte).
		void pulsarWizardState.start(seed);
	});

	function submitFreeform() {
		const text = freeform.trim();
		if (!text) return;
		freeform = '';
		void pulsarWizardState.answer(text);
	}

	// Same Enter-to-send / Shift+Enter-for-newline convention as the main
	// composer (ChatView.svelte's onKeydown) — this box grows with content
	// now (see autoResize below), so it needs the same real-newline
	// affordance a plain single-line input never did.
	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			submitFreeform();
		}
	}

	function pickOption(option: string) {
		void pulsarWizardState.answer(option);
	}

	function close() {
		pulsarWizardState.close();
		onClose();
	}
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={close} aria-label="Close"></button>
	<div class="modal-panel wizard-panel" role="dialog" aria-modal="true" aria-label="Help me write this prompt">
		<div class="sheet-handle" use:swipeToDismiss={close} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>Help me write this</h2>
			<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
		</div>

		<div class="transcript" bind:this={transcriptEl}>
			{#each pulsarWizardState.transcript as entry, i (i)}
				{#if entry.kind === 'user'}
					<div class="bubble user">{entry.text}</div>
				{:else if entry.kind === 'question'}
					<div class="bubble assistant">{entry.question.question}</div>
					<!-- Tappable options only on the still-unanswered last question —
					     once a reply is sent, pendingQuestion clears (see
					     pulsarWizard.svelte.ts's answer()), and every earlier question
					     in the transcript renders as plain history with no controls. -->
					{#if i === pulsarWizardState.transcript.length - 1 && pulsarWizardState.pendingQuestion?.options?.length}
						<div class="options">
							{#each pulsarWizardState.pendingQuestion.options as option, oi (option)}
								<button class="option-row" onclick={() => pickOption(option)} disabled={pulsarWizardState.loading}>
									<span class="option-index">{oi + 1}</span>
									<span class="option-text">{option}</span>
								</button>
							{/each}
						</div>
					{/if}
				{:else if entry.kind === 'text'}
					<div class="bubble assistant">{entry.text}</div>
				{:else if entry.kind === 'final'}
					<div class="draft">
						<pre class="draft-prompt">{entry.final.prompt}</pre>
						<button class="btn btn-accent use-btn" onclick={() => accept(entry.final.prompt, entry.final.name)}>
							<Check size={14} />
							Use this prompt
						</button>
					</div>
				{/if}
			{/each}

			{#if pulsarWizardState.loading}
				<div class="bubble assistant loading"><Loader2 size={14} class="spin" /></div>
			{/if}

			{#if pulsarWizardState.error}
				<p class="error">{pulsarWizardState.error}</p>
			{/if}
		</div>

		<form
			class="freeform"
			onsubmit={(e) => {
				e.preventDefault();
				submitFreeform();
			}}
		>
			<textarea
				class="freeform-input"
				rows="1"
				placeholder="Type your answer, or keep refining…"
				bind:value={freeform}
				onkeydown={onKeydown}
				use:autoResize={{ value: freeform, maxHeight: 140 }}
				disabled={pulsarWizardState.loading || !pulsarWizardState.sessionId}
			></textarea>
			<button
				class="freeform-send"
				type="submit"
				disabled={pulsarWizardState.loading || !freeform.trim() || !pulsarWizardState.sessionId}
			>
				<Send size={14} />
			</button>
		</form>
	</div>
</div>

<style>
	/* .modal-backdrop/.modal-panel/.modal-panel-header/.sheet-handle are
	   the app's real shared glass-modal system (app.css) — the same one
	   PulsarRoutineForm.svelte uses, complete with backdrop blur, the
	   layered elevation shadow stack, and the mobile bottom-sheet
	   treatment. Only .wizard-panel below adds this modal's own
	   chat-layout needs (a scrolling transcript with a pinned compose bar)
	   on top — everything about how it actually LOOKS comes from the
	   shared classes, not reinvented here. */
	.wizard-panel {
		display: flex;
		flex-direction: column;
		max-height: 78vh;
		/* The shared .modal-panel is a plain scrolling block (right for a
		   form); this needs the scrolling to happen inside .transcript
		   instead, with the header/compose bar pinned. */
		overflow: hidden;
		padding: 0;
	}

	.modal-panel-header {
		padding: var(--space-xl) var(--space-xl) var(--space-lg);
		margin-bottom: 0;
	}

	.transcript {
		flex: 1;
		/* Without this, a flex item defaults to min-height: auto — it
		   refuses to shrink below its own content's height, so it just
		   grows to fit the whole transcript instead of clipping to the
		   space actually available and scrolling internally. The overflow
		   then got cut off by .wizard-panel's own overflow: hidden with no
		   scrollbar to reach it at all — a real bug, not just a missing
		   affordance. */
		min-height: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
		padding: 0 var(--space-xl) var(--space-lg);
		overflow-y: auto;
	}

	.bubble {
		max-width: 88%;
		padding: var(--space-sm) var(--space-md);
		border-radius: var(--radius-md);
		font-size: 13.5px;
		line-height: 1.5;
	}

	.bubble.assistant {
		align-self: flex-start;
		background: var(--color-surface-2);
		box-shadow: var(--shadow-well);
	}

	.bubble.user {
		align-self: flex-end;
		background: var(--color-accent-soft);
	}

	.bubble.loading {
		display: flex;
		align-items: center;
	}

	.bubble.loading :global(.spin) {
		animation: wizard-spin 0.8s linear infinite;
	}

	@keyframes wizard-spin {
		to {
			transform: rotate(360deg);
		}
	}

	/* Same option-row vocabulary as AskUserQuestionCard.svelte — the
	   ask_user_question interaction already has an established look
	   elsewhere in the app; this reuses it rather than inventing a second
	   one for what's functionally the same control. */
	.options {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		overflow: hidden;
	}

	.option-row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		width: 100%;
		padding: var(--space-md);
		border: none;
		border-bottom: 1px solid var(--color-border);
		background: var(--color-surface);
		color: var(--color-text);
		font: inherit;
		font-size: 13.5px;
		text-align: left;
		transition: background-color 0.15s var(--ease-out-expo);
	}

	.option-row:last-child {
		border-bottom: none;
	}

	.option-row:hover:not(:disabled) {
		background: var(--color-surface-3);
	}

	.option-row:disabled {
		opacity: 0.5;
	}

	.option-index {
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		width: 20px;
		height: 20px;
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		color: var(--color-text-dim);
		font-size: 11px;
		font-weight: 600;
	}

	/* Plain text, monospace, select-all-copyable by hand — not just via
	   the button below. Per the user's explicit request: a code block,
	   not a styled card, so it reads as "text you're meant to copy". */
	.draft {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.draft-prompt {
		margin: 0;
		padding: var(--space-md);
		border-radius: var(--radius-md);
		background: var(--color-surface-2);
		box-shadow: var(--shadow-well);
		font-family: var(--font-mono);
		font-size: 12.5px;
		line-height: 1.6;
		white-space: pre-wrap;
		word-break: break-word;
	}

	.use-btn {
		align-self: flex-start;
	}

	.error {
		margin: 0;
		font-size: 12.5px;
		color: var(--color-danger);
	}

	.freeform {
		display: flex;
		align-items: flex-end;
		gap: var(--space-sm);
		padding: var(--space-md) var(--space-xl);
		box-shadow: var(--shadow-well);
	}

	/* Same carved-well, auto-growing treatment as the main composer's own
	   textarea (ChatView.svelte) — a fixed-height single-line input was
	   forcing a real answer's worth of typing to scroll sideways inside
	   itself instead of wrapping, which read as broken rather than just
	   cramped. */
	.freeform-input {
		flex: 1;
		resize: none;
		max-height: 140px;
		border: 1px solid transparent;
		background: var(--color-surface-2);
		box-shadow: var(--shadow-well);
		border-radius: var(--radius-lg);
		padding: var(--space-sm) var(--space-md);
		/* 16px, not smaller — anything under 16px makes iOS Safari zoom the
		   whole page on focus, same reasoning as the main composer. */
		font: inherit;
		font-size: 16px;
		line-height: 1.4;
		color: var(--color-text);
		outline: none;
		transition: border-color 0.15s var(--ease-out-expo);
	}

	.freeform-input:focus {
		border-color: var(--color-accent);
	}

	.freeform-send {
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		width: 32px;
		height: 32px;
		margin-bottom: 2px;
		border: none;
		border-radius: var(--radius-full);
		background: var(--color-accent);
		color: oklch(18% 0.02 75);
	}

	:root[data-theme='light'] .freeform-send {
		color: oklch(98% 0.005 80);
	}

	.freeform-send:disabled {
		opacity: 0.35;
	}

	@media (max-width: 768px) {
		.wizard-panel {
			max-height: 82vh;
		}
	}
</style>
