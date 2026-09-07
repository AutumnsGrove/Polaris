<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { Copy, Check, LoaderPinwheel, Send, Paperclip } from '@lucide/svelte';
	import { copyToClipboard } from '$lib/clipboard';
	import { autoResize } from '$lib/actions/autoResize';

	// Re-fetched every time this subpage mounts — see
	// SettingsState.loadExportPrompt's doc comment for why there's no
	// separate "already loaded" flag to skip this on a second visit.
	void appState.settings.loadExportPrompt();

	let dump = $state('');
	let copied = $state(false);
	let fileInput: HTMLInputElement | undefined = $state();
	let attachedFilename = $state('');

	// Reads a picked .txt/.md file straight into the same dump field a
	// paste would fill — there's no separate "import from file" code path
	// server-side, this is purely a client-side convenience for a dump
	// that's easier to save-and-attach than to select-all-and-copy (a long
	// reply on mobile, especially). FileReader.readAsText, not an actual
	// upload — the whole point is this never needs a multipart request or
	// server-side attachment handling the way the composer's PDF/image
	// attachments do (see tools.Context.AttachmentData); it's just a
	// different way to fill a plain string field.
	function handleFileChange(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		const reader = new FileReader();
		reader.onload = () => {
			dump = String(reader.result ?? '');
			attachedFilename = file.name;
		};
		reader.onerror = () => {
			appState.showToast("Couldn't read that file — try pasting the text instead");
		};
		reader.readAsText(file);
		// Clears the input's own value so picking the exact same file again
		// (e.g. after editing it and re-saving) still fires a change event —
		// browsers don't fire one for a no-op selection otherwise.
		input.value = '';
	}

	async function copyPrompt() {
		try {
			await copyToClipboard(appState.settings.exportPrompt);
			copied = true;
			setTimeout(() => (copied = false), 1500);
			appState.showToast('Copied — paste it into the other AI');
		} catch {
			appState.showToast('Copy failed — clipboard access was blocked');
		}
	}

	async function submitImport() {
		const text = dump.trim();
		if (!text || appState.settings.importBusy) return;
		await appState.settings.importMemories(text);
		// Cleared only on success (importMessage set, dump survives a
		// failed attempt) — losing a long pasted dump to a network hiccup
		// would mean re-copying it from wherever it came from all over
		// again.
		if (appState.settings.importMessage) {
			dump = '';
			attachedFilename = '';
		}
	}
</script>

<section class="import-step">
	<h3>1. Copy this into the other AI</h3>
	<p class="hint">
		Paste this prompt into claude.ai, ChatGPT, or any other assistant you've talked to a lot — it
		asks that assistant to write out everything it remembers about you.
	</p>
	<div class="prompt-box">
		<p class="prompt-text">{appState.settings.exportPrompt || 'Loading…'}</p>
		<button
			class="btn copy-btn"
			onclick={copyPrompt}
			disabled={!appState.settings.exportPrompt}
		>
			{#if copied}
				<Check size={14} /> Copied
			{:else}
				<Copy size={14} /> Copy prompt
			{/if}
		</button>
	</div>
</section>

<section class="import-step">
	<h3>2. Paste what it says back</h3>
	<p class="hint">
		Paste that assistant's whole reply below, code block and all — <span class="wordmark"
			>Polaris</span
		> will read through it and save what's worth keeping, editing anything that contradicts what it
		already knows instead of duplicating it.
	</p>
	<textarea
		rows="6"
		placeholder="Paste the other assistant's reply here"
		bind:value={dump}
		use:autoResize={{ value: dump, maxHeight: 320 }}
		disabled={appState.settings.importBusy}
	></textarea>
	<input
		type="file"
		accept=".txt,.md,text/plain,text/markdown"
		bind:this={fileInput}
		onchange={handleFileChange}
		hidden
	/>
	<button
		class="btn attach-btn"
		onclick={() => fileInput?.click()}
		disabled={appState.settings.importBusy}
	>
		<Paperclip size={13} />
		{attachedFilename || 'Attach a .txt file instead'}
	</button>
	<button
		class="btn import-btn"
		onclick={submitImport}
		disabled={!dump.trim() || appState.settings.importBusy}
	>
		{#if appState.settings.importBusy}
			<LoaderPinwheel size={14} class="spin" /> Reading…
		{:else}
			<Send size={14} /> Import
		{/if}
	</button>
	{#if appState.settings.importMessage}
		<p class="hint import-confirmation">{appState.settings.importMessage}</p>
	{/if}
</section>

<style>
	.hint {
		font-size: 12px;
		color: var(--color-text-dim);
		margin: var(--space-sm) 0 0 0;
		line-height: 1.5;
	}

	.import-step {
		margin-bottom: var(--space-xl);
	}

	.import-step:last-child {
		margin-bottom: 0;
	}

	.import-step h3 {
		margin: 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.12em;
		color: var(--color-text);
	}

	.prompt-box {
		margin-top: var(--space-sm);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-md);
	}

	.prompt-text {
		margin: 0 0 var(--space-md) 0;
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-dim);
		white-space: pre-wrap;
		/* Long, but not a page-filling wall — this is here so the user
		   knows roughly what they're pasting elsewhere, not to be read
		   closely inline; the copy button is the actual point. */
		max-height: 160px;
		overflow-y: auto;
	}

	.btn {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		border: none;
		background: var(--color-surface-3);
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-md);
		font-size: 13px;
		color: var(--color-text);
	}

	.btn:disabled {
		opacity: 0.5;
	}

	.copy-btn {
		width: 100%;
		justify-content: center;
	}

	textarea {
		width: 100%;
		margin-top: var(--space-sm);
		resize: none;
		border: none;
		background: var(--color-surface-2);
		box-shadow: var(--shadow-well);
		border-radius: var(--radius-md);
		font-family: inherit;
		font-size: 13px;
		color: var(--color-text);
		line-height: 1.4;
		padding: var(--space-sm) var(--space-md);
	}

	textarea::placeholder {
		color: var(--color-text-dim);
	}

	textarea:disabled {
		opacity: 0.6;
	}

	.attach-btn {
		width: 100%;
		justify-content: center;
		margin-top: var(--space-sm);
		background: transparent;
		box-shadow: none;
		font-size: 12px;
		color: var(--color-text-dim);
		/* Long filename doesn't blow out the row width on a narrow phone
		   screen — same overflow treatment as MemorySettings.svelte's
		   .memory-name. */
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}

	.attach-btn:hover {
		color: var(--color-text);
	}

	.import-btn {
		width: 100%;
		justify-content: center;
		margin-top: var(--space-sm);
	}

	.import-confirmation {
		text-align: center;
	}

	/* Same reserved-brand-face treatment as MemorySettings.svelte/
	   SettingsPanel.svelte's own .wordmark — duplicated, not shared, since
	   Svelte scopes component styles per-file (see SettingsPanel's doc
	   comment on this same duplication). */
	.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-size: 1.05em;
		letter-spacing: 0.02em;
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
