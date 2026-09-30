<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import ComposerMenu from '$lib/components/ComposerMenu.svelte';
	import VoiceButton from '$lib/components/VoiceButton.svelte';
	import { Send, Square, Paperclip, X, Loader2, MicAudioLines } from '@lucide/svelte';
	import { autoResize } from '$lib/actions/autoResize';
	import type { FocusMode } from '$lib/types';

	// The message composer: textarea, attachment chips, and the toolbar (menu,
	// call, voice, send/stop). Rendered by ChatView on both the welcome screen
	// and under the timeline. The composer-wide state (text, focus/research
	// toggles, voice cost) is owned by ChatView and bound through here, since
	// submit()/retry logic elsewhere in ChatView reads and resets it.
	let {
		input = $bindable(),
		focusMode = $bindable(),
		focusModeManual = $bindable(),
		deepResearch = $bindable(),
		research = $bindable(),
		voiceCostUsd = $bindable(),
		ghostMode,
		attachedFiles,
		uploading,
		isWeaverThread,
		busyElsewhere,
		onSubmit,
		onKeydown,
		onPaste,
		onAttach,
		onRemoveAttachment,
		onCall
	}: {
		input: string;
		focusMode: FocusMode;
		focusModeManual: boolean;
		deepResearch: boolean;
		research: boolean;
		voiceCostUsd: number | undefined;
		ghostMode: boolean;
		attachedFiles: File[];
		uploading: boolean;
		isWeaverThread: boolean;
		busyElsewhere: boolean;
		onSubmit: () => void;
		onKeydown: (e: KeyboardEvent) => void;
		onPaste: (e: ClipboardEvent) => void;
		onAttach: (files: File[]) => void;
		onRemoveAttachment: (index: number) => void;
		onCall: () => void;
	} = $props();
</script>

<form
	class="composer"
	onsubmit={(e) => {
		e.preventDefault();
		onSubmit();
	}}
>
	<div class="textarea-wrap">
		{#if !input}
			<!-- A native placeholder attribute can't mix fonts within its
			     text, so the "Polaris" wordmark treatment used everywhere
			     else (see .welcome-heading .wordmark) needs this overlay
			     instead — invisible to interaction (pointer-events: none)
			     and hidden the instant there's real input, so it never
			     competes with what's actually being typed. Skipped
			     entirely for a Weaver session — "Ask Polaris" would be
			     actively misleading for a turn that never reaches the
			     main assistant at all. -->
			{#if !isWeaverThread}
				<div class="fake-placeholder" aria-hidden="true">
					Ask <span class="wordmark">Polaris</span>…
				</div>
			{/if}
		{/if}
		<textarea
			rows="1"
			bind:value={input}
			onkeydown={onKeydown}
			onpaste={onPaste}
			use:autoResize={{ value: input, maxHeight: 200 }}
			placeholder={isWeaverThread ? 'Tell Weaver what to fix…' : undefined}
			aria-label={isWeaverThread ? 'Tell Weaver what to fix' : 'Ask Polaris'}
		></textarea>
	</div>

	{#if attachedFiles.length > 0}
		<div class="attachment-chips">
			{#each attachedFiles as file, i (file.name + i)}
				<div class="attachment-chip">
					<Paperclip size={13} />
					<span class="attachment-name">{file.name}</span>
					<button
						type="button"
						onclick={() => onRemoveAttachment(i)}
						disabled={uploading}
						aria-label="Remove attachment"
					>
						<X size={13} />
					</button>
				</div>
			{/each}
		</div>
	{/if}

	<div class="composer-toolbar">
		{#if !isWeaverThread}
			<ComposerMenu
				bind:focusMode
				bind:focusModeManual
				bind:deepResearch
				bind:research
				{ghostMode}
				{onAttach}
			/>
		{/if}
		<div class="toolbar-spacer"></div>
		{#if !isWeaverThread}
			<button
				type="button"
				class="call-btn"
				disabled={busyElsewhere}
				title="Talk to Polaris"
				onclick={onCall}
			>
				<MicAudioLines size={16} />
			</button>
			<VoiceButton bind:value={input} bind:sttCostUsd={voiceCostUsd} />
		{/if}
		<button
			type={appState.busyOnCurrentThread ? 'button' : 'submit'}
			class="send-btn"
			class:stop={appState.busyOnCurrentThread}
			disabled={uploading || busyElsewhere || (!appState.busyOnCurrentThread && !input.trim())}
			title={appState.busyOnCurrentThread
				? 'Stop generating'
				: busyElsewhere
					? 'A response is still generating in another thread'
					: uploading
						? 'Uploading…'
						: 'Send'}
			onclick={() => {
				if (appState.busyOnCurrentThread) appState.stopGeneration();
			}}
		>
			{#if appState.busyOnCurrentThread}
				<Square size={14} fill="currentColor" />
			{:else if uploading}
				<Loader2 size={14} class="spin" />
			{:else}
				<Send size={16} />
			{/if}
		</button>
	</div>
</form>

<style>
	/* Column now, not a single row — the textarea sits on its own line
	   with room to breathe, the model/focus/attach controls that used to
	   crowd it live in .composer-toolbar underneath instead (see
	   ComposerMenu's "+" sheet, which absorbed the old inline model
	   selector — a row of 4-5 small controls doesn't survive phone
	   width, one thumb-reachable entry point does). */
	.composer {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		box-shadow: 0 -8px 16px -14px rgba(0, 0, 0, 0.5);
		padding: var(--space-lg);
		/* Clears iOS Safari's bottom toolbar / home-indicator area — falls
		   back to the plain 12px on browsers without safe-area support. */
		padding-bottom: max(var(--space-md), env(safe-area-inset-bottom));
	}

	.composer-toolbar {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.toolbar-spacer {
		flex: 1;
	}

	/* Mirrors VoiceButton's own .mic-btn treatment (that component's style
	   is scoped and unreachable from here) — same recedes-until-relevant
	   secondary-control look as the rest of the toolbar. */
	.call-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		border: 1px solid transparent;
		background: transparent;
		border-radius: var(--radius-md);
		width: 38px;
		height: 38px;
		color: var(--color-text-dim);
		flex-shrink: 0;
		transition:
			border-color 0.18s var(--ease-out-expo),
			background-color 0.18s var(--ease-out-expo),
			color 0.18s var(--ease-out-expo),
			transform 0.18s var(--ease-out-expo);
	}

	.call-btn:hover:not(:disabled) {
		border-color: var(--color-border);
		background: var(--color-surface-2);
		color: var(--color-text);
		transform: translateY(-1px);
	}

	.call-btn:disabled {
		opacity: 0.4;
		cursor: default;
	}

	.attachment-chips {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
	}

	.attachment-chip {
		display: inline-flex;
		align-items: center;
		gap: var(--space-sm);
		align-self: flex-start;
		max-width: 100%;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-full);
		padding: var(--space-xs) var(--space-sm) var(--space-xs) var(--space-md);
		font-size: 12.5px;
		color: var(--color-text-dim);
		box-shadow: var(--shadow-xs);
	}

	.attachment-chip .attachment-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.attachment-chip button {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		border-radius: var(--radius-full);
		flex-shrink: 0;
		color: var(--color-text-dim);
	}

	.attachment-chip button:hover {
		background: var(--color-surface-3);
		color: var(--color-text);
	}

	.textarea-wrap {
		position: relative;
		display: flex;
	}

	.fake-placeholder {
		position: absolute;
		inset: 0;
		/* Not display: flex — a flex container treats a whitespace-only
		   text node between two inline elements as display: none (per the
		   flexbox spec's anonymous-item handling), which silently ate the
		   space between "Ask" and the "Polaris" span. Padding alone
		   already centers a single line of text the same height as the
		   textarea's own single row, so flex's vertical centering was
		   never actually needed here. */
		padding: var(--space-lg) var(--space-lg);
		font-size: 16px;
		line-height: 1.5;
		font-family: var(--font-sans);
		color: var(--color-text-dim);
		pointer-events: none;
		white-space: nowrap;
		/* Horizontal-only: still truncates on narrow screens the same as
		   before. Vertical clipping is what was cutting the wordmark span
		   below down to a thin sliver — see .fake-placeholder .wordmark. */
		overflow-x: hidden;
		overflow-y: visible;
	}

	.fake-placeholder .wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		/* Asimovian's glyph metrics run taller than Lexend's at the same
		   font-size — inherited from the 1.5 line-height above, this
		   overflowed the placeholder's fixed-height box and, combined with
		   overflow: hidden, rendered as a squashed sliver instead of full
		   letterforms. A tighter line-height here (this span only — the
		   welcome heading's much larger wordmark instance never needed
		   this, it already has plenty of room) keeps it within the box
		   without needing the vertical clip that caused this at all. */
		line-height: 1.2;
	}

	textarea {
		width: 100%;
		resize: none;
		/* Carved-in well at rest instead of a flat hairline box — the accent
		   border only appears on focus (below), so idle the composer reads
		   as a soft trough in the surface, not a form field. */
		border: 1px solid transparent;
		background: var(--color-surface-2);
		box-shadow: var(--shadow-well);
		border-radius: var(--radius-lg);
		padding: var(--space-lg) var(--space-lg);
		/* 16px, not 14 — anything smaller makes iOS Safari zoom the whole
		   page in on focus (it does this for any input/textarea under
		   16px), which is what was pushing the send button out of the
		   viewport. autoResize (see the action import above) handles
		   height, growing with content up to its maxHeight before
		   scrolling — same shape as Claude's composer, instead of a
		   fixed single row that just scrolls its own content out of view. */
		font-size: 16px;
		line-height: 1.5;
		font-family: var(--font-sans);
		color: var(--color-text);
		outline: none;
		/* A taller resting height than the bare single-row minimum — the
		   composer now carries its own toolbar underneath instead of
		   cramming everything onto one line, so it can afford to feel
		   like a real writing surface instead of a thin search bar. */
		min-height: 56px;
		max-height: 200px;
		overflow-y: auto;
		transition:
			border-color 0.15s var(--ease-out-expo),
			background-color 0.15s var(--ease-out-expo),
			box-shadow 0.15s var(--ease-out-expo);
	}

	textarea:hover {
		background: var(--color-surface-3);
	}

	textarea:focus {
		border-color: var(--color-accent);
		background: var(--color-surface);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 16%, transparent);
	}

	.send-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		border: 1px solid transparent;
		background: var(--color-accent);
		color: oklch(18% 0.02 75);
		border-radius: var(--radius-md);
		width: 38px;
		height: 38px;
		box-shadow: 0 1px 2px rgba(15, 10, 5, 0.25);
		transition:
			background-color 0.18s var(--ease-out-expo),
			transform 0.18s var(--ease-out-expo),
			box-shadow 0.18s var(--ease-out-expo),
			opacity 0.15s var(--ease-out-expo);
	}

	:root[data-theme='light'] .send-btn {
		color: oklch(98% 0.005 80);
		box-shadow: 0 1px 2px rgba(60, 48, 32, 0.14);
	}

	.send-btn:hover:not(:disabled) {
		background: var(--color-accent-strong);
		transform: translateY(-1px);
		box-shadow:
			0 6px 16px -6px color-mix(in srgb, var(--color-accent) 55%, transparent),
			0 2px 4px rgba(15, 10, 5, 0.3);
	}

	.send-btn:active:not(:disabled) {
		transform: translateY(0);
		box-shadow: 0 1px 2px rgba(15, 10, 5, 0.25);
	}

	.send-btn:disabled {
		opacity: 0.35;
		cursor: default;
		box-shadow: none;
	}

	/* Stop mode: deliberately not the accent gold — that's reserved for
	   the primary "send" action, and a stop control shouldn't read as
	   another CTA competing with it. A quiet neutral chip that stays
	   legible without stealing attention from the streaming answer. */
	.send-btn.stop {
		background: var(--color-surface-3);
		color: var(--color-text);
		box-shadow: none;
	}

	.send-btn.stop:hover {
		background: var(--color-border-strong);
		transform: none;
		box-shadow: none;
	}
</style>
