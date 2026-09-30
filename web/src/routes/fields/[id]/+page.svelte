<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { fieldsState, fieldColorVar, FIELD_COLORS } from '$lib/fields.svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { FOCUS_MODES } from '$lib/focusModes';
	import ConfirmModal from '$lib/components/ConfirmModal.svelte';
	import FieldChips from '$lib/components/FieldChips.svelte';
	import MemoryManager from '$lib/components/MemoryManager.svelte';
	import { FieldMemorySource } from '$lib/fieldMemory.svelte';
	import Switch from '$lib/components/Switch.svelte';
	import WizardButton from '$lib/components/WizardButton.svelte';
	import WizardOverlay from '$lib/components/WizardOverlay.svelte';
	import {
		PanelLeft,
		ChevronLeft,
		Star,
		Trash2,
		ArrowUp,
		MessageCirclePlus,
		Upload,
		FileText,
		Lock,
		Brain,
		Cpu,
		Orbit,
		SearchSlash,
		SlidersHorizontal,
		Palette
	} from '@lucide/svelte';
	import type { Field, FieldDetail, FieldFile } from '$lib/types';

	// Mirrors gateway/settings.go's maxCustomInstructionsChars — the server is
	// the real limit; this just lets the counter turn red before a save 400s.
	const MAX_INSTRUCTIONS = 4000;

	// The four memory modes (issue #133). "Global" is the stored value
	// 'default' — today's behavior, so existing Fields need no migration.
	const MEMORY_MODES: { id: Field['memory_mode']; label: string; hint: string }[] = [
		{ id: 'default', label: 'Global', hint: 'Uses your regular memories, like any other chat.' },
		{ id: 'field_only', label: 'Field', hint: 'Uses only memories saved inside this Field. Your regular memories stay out of it.' },
		{ id: 'both', label: 'Both', hint: 'Reads this Field’s memories and your regular ones, but only ever saves to this Field.' },
		{ id: 'none', label: 'None', hint: 'No memory at all in this Field.' }
	];
	// Settings → Memory off overrides a Field's mode: it never turns your
	// regular memories back on, so the hint says what is actually in effect.
	let globalMemoryOff = $derived(!appState.settings.memoryEnabled);

	let detail = $state<FieldDetail | null>(null);
	// Derived from the id alone (a string, compared by value) so a save —
	// which replaces the whole field object — doesn't recreate the source and
	// drop the memory list's loaded state; it only changes when navigating
	// to a different Field.
	let fieldId = $derived(detail?.field.id ?? '');
	let memorySource = $derived(fieldId ? new FieldMemorySource(fieldId) : null);
	let notFound = $state(false);
	let loadError = $state('');

	// Local edit buffers for the text fields: bound to inputs, written back
	// only on an explicit save/blur so a half-typed value is never PATCHed.
	let name = $state('');
	let description = $state('');
	let instructions = $state('');

	let prompt = $state('');
	let savingInstructions = $state(false);
	let uploading = $state(false);
	let confirming = $state<null | { kind: 'field' } | { kind: 'file'; file: FieldFile }>(null);
	let fileInput: HTMLInputElement | undefined = $state();

	// Day-to-day use is "open a conversation", so that's the default tab and
	// setup (instructions/files/settings) sits one tap away instead of above
	// the work. A brand-new field is sent here with ?tab=instructions (see
	// the hub's create()) so the fill-it-in flow still leads for a new one.
	type Tab = 'conversations' | 'files' | 'instructions' | 'settings';
	const TABS: { id: Tab; label: string }[] = [
		{ id: 'conversations', label: 'Conversations' },
		{ id: 'files', label: 'Files' },
		{ id: 'instructions', label: 'Instructions' },
		{ id: 'settings', label: 'Settings' }
	];
	function initialTab(): Tab {
		const t = page.url.searchParams.get('tab');
		return TABS.some((x) => x.id === t) ? (t as Tab) : 'conversations';
	}
	let tab = $state<Tab>(initialTab());
	function selectTab(t: Tab) {
		tab = t;
		// Mirrored into the URL (replaceState via goto, so Back still leaves the
		// field rather than stepping through tabs) so a refresh or a shared
		// link lands on the same tab.
		const url = new URL(page.url);
		if (t === 'conversations') url.searchParams.delete('tab');
		else url.searchParams.set('tab', t);
		void goto(url, { replaceState: true, noScroll: true, keepFocus: true });
	}

	onMount(() => {
		// Needed only to grey out the Constellation toggle when the feature is
		// off globally — cheap, and null until loaded.
		if (!constellationState.config) void constellationState.loadConfig();
		if (!appState.settings.loaded) void appState.settings.load();
		// The model picker below reads appState.models, which the layout loads
		// once at startup — a single fetch that, if it lands while the backend
		// is restarting, is never retried, leaving the picker with no models.
		// Ensure it here too. Guarded on empty because loadModels() also
		// resets the composer's selectedModel to the default, which must not
		// clobber a choice already made.
		if (appState.models.length === 0) void appState.loadModels();
	});

	// Re-runs on a route-to-route navigation between two fields (the sidebar's
	// pinned shortcuts), which reuses this component instance.
	$effect(() => {
		const id = page.params.id;
		if (!id) return;
		detail = null;
		notFound = false;
		loadError = '';
		void load(id);
	});

	function adopt(d: FieldDetail) {
		detail = d;
		name = d.field.name;
		description = d.field.description;
		instructions = d.field.custom_instructions;
	}

	async function load(id: string) {
		const res = await fieldsState.loadDetail(id);
		if (id !== page.params.id) return; // navigated to another field mid-request
		if (res.ok) {
			adopt(res.data);
		} else if (res.error.includes('404') || res.error.toLowerCase().includes('not found')) {
			notFound = true;
		} else {
			loadError = res.error;
		}
	}

	// One PATCH path for every setting: applies the server's answer (not the
	// optimistic value) so a rejected field snaps back, and toasts the reason.
	async function patch(fields: Partial<Field>): Promise<boolean> {
		if (!detail) return false;
		const res = await fieldsState.update(detail.field.id, fields);
		if (!res.ok) {
			appState.showToast(res.error);
			// Re-sync the buffers so a rejected edit doesn't linger in an input.
			name = detail.field.name;
			description = detail.field.description;
			return false;
		}
		detail.field = res.data;
		return true;
	}

	async function saveName() {
		if (!detail || name.trim() === detail.field.name) return;
		await patch({ name });
	}

	async function saveDescription() {
		if (!detail || description === detail.field.description) return;
		await patch({ description });
	}

	async function saveInstructions() {
		if (savingInstructions) return;
		savingInstructions = true;
		await patch({ custom_instructions: instructions });
		savingInstructions = false;
	}

	// The "Help me write this" interview for the instructions textarea (see
	// WizardOverlay / gateway/wizard.go's field_instructions target).
	// Accepting only fills the textarea — it never saves, so the draft goes
	// through the same explicit Save (and 4000-char cap) as hand-typed text
	// and can still be edited or discarded first.
	let showInstructionsWizard = $state(false);
	function acceptWizardInstructions(text: string) {
		instructions = text;
	}

	// The omnibox creates the thread AND opens it already mid-turn, rather
	// than a separate "create, then type" step. Focus mode is resolved here
	// (field default, else the global one) because send()'s own focus
	// argument is what the very first turn uses — ChatView's config effect
	// only seeds the composer for the turns after it.
	async function startWithPrompt() {
		const text = prompt.trim();
		if (!text || !detail || appState.busy) return;
		const fieldFocus = detail.field.default_focus_mode;
		const focus = fieldFocus ? fieldFocus : appState.settings.defaultFocusMode;
		appState.startThreadInField(detail.field.id);
		prompt = '';
		await goto('/');
		appState.send(text, undefined, focus && focus !== 'off' ? focus : undefined);
	}

	async function startBlank() {
		if (!detail) return;
		appState.startThreadInField(detail.field.id);
		await goto('/');
	}

	function onPromptKeydown(e: KeyboardEvent) {
		// Enter sends, Shift+Enter is a newline — same as the chat composer.
		if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
			e.preventDefault();
			void startWithPrompt();
		}
	}

	async function uploadFiles(list: FileList | null) {
		if (!list || !detail) return;
		uploading = true;
		for (const file of Array.from(list)) {
			const res = await fieldsState.uploadFile(detail.field.id, file);
			if (!res.ok) appState.showToast(`${file.name}: ${res.error}`);
		}
		uploading = false;
		if (fileInput) fileInput.value = '';
		await refreshDetail();
	}

	// Reload without clobbering an in-progress edit in the text buffers.
	async function refreshDetail() {
		if (!detail) return;
		const res = await fieldsState.loadDetail(detail.field.id);
		if (res.ok) detail = res.data;
	}

	async function confirmed() {
		const target = confirming;
		confirming = null;
		if (!target || !detail) return;
		if (target.kind === 'file') {
			const res = await fieldsState.deleteFile(detail.field.id, target.file.name);
			if (!res.ok) appState.showToast(res.error);
			await refreshDetail();
		} else {
			const res = await fieldsState.remove(detail.field.id);
			if (!res.ok) {
				appState.showToast(res.error);
				return;
			}
			// Threads survive, ungrouped — the sidebar list has no field
			// affordance to refresh, but the thread rows' own state does.
			void appState.loadThreads();
			void goto('/fields');
		}
	}

	function formatSize(bytes: number): string {
		if (bytes < 1024) return `${bytes} B`;
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
		return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
	}

	// A saved default model that's no longer in the live list (removed from
	// config since) must still show as the current value, not silently read as
	// "use my global default" while the server keeps the old id.
	let modelMissing = $derived(
		!!detail?.field.default_model && !appState.models.some((m) => m.id === detail!.field.default_model)
	);

	function relativeDay(iso: string): string {
		const d = new Date(iso);
		if (Number.isNaN(d.getTime())) return '';
		const days = Math.floor((Date.now() - d.getTime()) / 86_400_000);
		if (days <= 0) return 'Today';
		if (days === 1) return 'Yesterday';
		if (days < 7) return `${days} days ago`;
		return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
	}

	let constellationOff = $derived(!!constellationState.config && !constellationState.config.enabled);
</script>

<svelte:head>
	<title>{detail ? `${detail.field.name} — Fields` : 'Field'} — Polaris</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		<button class="icon-btn" onclick={() => goto('/fields')} title="All Fields" aria-label="All Fields">
			<ChevronLeft size={18} />
		</button>
		{#if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<h1 class="page-title">{detail?.field.name ?? ''}</h1>
	</div>
	{#if detail}
		<div class="header-right">
			<button
				class="icon-btn"
				class:pinned={detail.field.favorite}
				onclick={() => patch({ favorite: !detail!.field.favorite })}
				title={detail.field.favorite ? 'Unpin from sidebar' : 'Pin to sidebar'}
				aria-label={detail.field.favorite ? 'Unpin from sidebar' : 'Pin to sidebar'}
				aria-pressed={detail.field.favorite}
			>
				<Star size={17} fill={detail.field.favorite ? 'currentColor' : 'none'} />
			</button>
			<button
				class="icon-btn"
				onclick={() => (confirming = { kind: 'field' })}
				title="Delete Field"
				aria-label="Delete Field"
			>
				<Trash2 size={17} />
			</button>
		</div>
	{/if}
</header>

<div class="content">
	{#if notFound}
		<p class="empty">That <span class="wordmark">Field</span> doesn't exist anymore.</p>
	{:else if loadError}
		<p class="empty">Couldn't load this <span class="wordmark">Field</span>: {loadError}</p>
	{:else if detail}
		{@const field = detail.field}
		<div class="column">
			<div class="summary">
				{#if field.description}<p class="summary-desc">{field.description}</p>{/if}
				<FieldChips {field} />
			</div>

			<!-- Omnibox: typing a prompt and sending creates a thread in this
			     field AND opens it mid-turn. The plain button beside it is the
			     other case — a blank, unsent composer scoped to the field. -->
			<section class="omnibox">
				<textarea
					bind:value={prompt}
					onkeydown={onPromptKeydown}
					rows="2"
					placeholder="Start a conversation in {field.name}…"
					aria-label="Start a conversation in this Field"
				></textarea>
				<div class="omnibox-actions">
					<button class="btn" onclick={startBlank}>
						<MessageCirclePlus size={16} />
						New thread
					</button>
					<button
						class="btn btn-accent send"
						onclick={startWithPrompt}
						disabled={!prompt.trim() || appState.busy}
						aria-label="Send"
					>
						<ArrowUp size={16} />
					</button>
				</div>
			</section>

			<div class="tabs" role="tablist" aria-label="Field sections">
				{#each TABS as t (t.id)}
					<button
						class="tab"
						class:selected={tab === t.id}
						role="tab"
						id="tab-{t.id}"
						aria-selected={tab === t.id}
						aria-controls="panel-{t.id}"
						onclick={() => selectTab(t.id)}
					>
						{t.label}
						{#if t.id === 'conversations' && detail.threads.length > 0}<span class="count">{detail.threads.length}</span>{/if}
						{#if t.id === 'files' && detail.files.length > 0}<span class="count">{detail.files.length}</span>{/if}
					</button>
				{/each}
			</div>

			{#if tab === 'conversations'}
				<div class="panel" role="tabpanel" id="panel-conversations" aria-labelledby="tab-conversations">
					{#if detail.threads.length === 0}
						<p class="muted">
							No conversations yet — start one above.
							{#if !field.custom_instructions && detail.files.length === 0}
								<button class="link" onclick={() => selectTab('instructions')}>Set up this <span class="wordmark">Field</span></button>
								with instructions or reference files first, if you like.
							{/if}
						</p>
					{/if}
					<ul class="threads">
						{#each detail.threads as thread (thread.id)}
							<li>
								<button class="thread-row" onclick={() => goto(`/t/${thread.id}`)}>
									<span class="thread-title">{thread.title || 'Untitled'}</span>
									<span class="thread-when">{relativeDay(thread.updated_at)}</span>
								</button>
							</li>
						{/each}
					</ul>
				</div>
			{:else if tab === 'files'}
				<div class="panel" role="tabpanel" id="panel-files" aria-labelledby="tab-files">
					<p class="hint">
						<Lock size={12} /> Every conversation in this <span class="wordmark">Field</span> can read these, but not change them — an
						edit is saved as a copy in that conversation's own workspace.
					</p>
					{#if detail.files.length === 0}
						<p class="muted">No shared files yet.</p>
					{/if}
					<ul class="files">
						{#each detail.files as file (file.name)}
							<li class="file">
								<FileText size={14} />
								<a
									class="file-name"
									href="/api/workspace/{field.id}/{encodeURIComponent(file.name)}"
									target="_blank"
									rel="noopener"
								>
									{file.name}
								</a>
								<span class="file-size">{formatSize(file.size_bytes)}</span>
								<button
									class="icon-btn"
									onclick={() => (confirming = { kind: 'file', file })}
									title="Remove file"
									aria-label="Remove {file.name}"
								>
									<Trash2 size={14} />
								</button>
							</li>
						{/each}
					</ul>
					<input
						bind:this={fileInput}
						type="file"
						multiple
						accept="image/*,.pdf,.md,.txt,.json,.csv,.yaml,.yml,.xml"
						hidden
						onchange={(e) => uploadFiles(e.currentTarget.files)}
					/>
					<button class="btn" onclick={() => fileInput?.click()} disabled={uploading}>
						<Upload size={16} />
						{uploading ? 'Uploading…' : 'Add files'}
					</button>
				</div>
			{:else if tab === 'instructions'}
				<div class="panel" role="tabpanel" id="panel-instructions" aria-labelledby="tab-instructions">
					<p class="hint">
						Added to every conversation in this <span class="wordmark">Field</span>, after your global custom instructions — never
						instead of them.
					</p>
					<div class="wizard-row">
						<WizardButton onclick={() => (showInstructionsWizard = true)} />
					</div>
					<textarea
						class="instructions"
						bind:value={instructions}
						rows="10"
						placeholder="How should Polaris behave in this Field? Context, tone, constraints…"
						aria-label="Field instructions"
					></textarea>
					<div class="instructions-foot">
						<span class="counter" class:over={instructions.length > MAX_INSTRUCTIONS}>
							{instructions.length} / {MAX_INSTRUCTIONS}
						</span>
						<button
							class="btn btn-accent"
							onclick={saveInstructions}
							disabled={savingInstructions || instructions === field.custom_instructions || instructions.length > MAX_INSTRUCTIONS}
						>
							{savingInstructions ? 'Saving…' : 'Save'}
						</button>
					</div>
				</div>
			{:else}
				<div class="panel" role="tabpanel" id="panel-settings" aria-labelledby="tab-settings">
					<h2 class="panel-title">About</h2>
					<input class="text-input" bind:value={name} onblur={saveName} maxlength="100" aria-label="Field name" />
					<input
						class="text-input"
						bind:value={description}
						onblur={saveDescription}
						maxlength="300"
						placeholder="One-line description (shown on the hub)"
						aria-label="Field description"
					/>
					<div class="setting-row">
						<span class="row-label"><Palette size={15} /> Color</span>
						<div class="swatches" role="radiogroup" aria-label="Color tag">
							<button
								class="swatch none"
								class:selected={field.color === ''}
								onclick={() => patch({ color: '' })}
								role="radio"
								aria-checked={field.color === ''}
								aria-label="No color"
								title="No color"
							></button>
							{#each FIELD_COLORS as c (c)}
								<button
									class="swatch"
									class:selected={field.color === c}
									style:background={fieldColorVar(c)}
									onclick={() => patch({ color: c })}
									role="radio"
									aria-checked={field.color === c}
									aria-label={c.replace(/-/g, ' ')}
									title={c.replace(/-/g, ' ')}
								></button>
							{/each}
						</div>
					</div>

					<h2 class="panel-title spaced">Defaults</h2>

					<div class="setting-row">
						<span class="row-label"><SlidersHorizontal size={15} /> Default focus mode</span>
						<select
							value={field.default_focus_mode}
							onchange={(e) => patch({ default_focus_mode: e.currentTarget.value as Field['default_focus_mode'] })}
							aria-label="Default focus mode"
						>
							<option value="">Use my global default</option>
							<option value="off">None</option>
							{#each FOCUS_MODES as mode (mode.id)}
								<option value={mode.id}>{mode.label}</option>
							{/each}
						</select>
					</div>

					<div class="setting-row">
						<span class="row-label"><Cpu size={15} /> Default model</span>
						<select
							value={field.default_model}
							onchange={(e) => patch({ default_model: e.currentTarget.value })}
							aria-label="Default model"
						>
							<option value="">Use my global default</option>
							{#if modelMissing}
								<option value={field.default_model}>{field.default_model} (unavailable)</option>
							{/if}
							{#each appState.models as m (m.id)}
								<option value={m.id}>{m.name}</option>
							{/each}
						</select>
					</div>

					<div class="setting-row">
						<span class="row-label"><Brain size={15} /> Memory</span>
						<div class="segmented" role="radiogroup" aria-label="Memory mode">
							{#each MEMORY_MODES as m (m.id)}
								<button
									class:selected={field.memory_mode === m.id}
									onclick={() => patch({ memory_mode: m.id })}
									role="radio"
									aria-checked={field.memory_mode === m.id}>{m.label}</button
								>
							{/each}
						</div>
					</div>
					<p class="hint">
						{MEMORY_MODES.find((m) => m.id === field.memory_mode)?.hint}
						{#if globalMemoryOff && (field.memory_mode === 'default' || field.memory_mode === 'both')}
							Memory is off in Settings, so {field.memory_mode === 'both' ? 'only this Field’s own memories are used' : 'nothing is used'}.
						{/if}
					</p>

					{#if (field.memory_mode === 'field_only' || field.memory_mode === 'both') && memorySource}
						{#key fieldId}
							<div class="field-memories">
								<MemoryManager
									source={memorySource}
									subject="in this Field"
									placeholder="e.g. The project codename is ORCA-7"
									emptyText="Nothing saved yet — it'll remember things worth carrying forward from this Field's chats."
								/>
							</div>
						{/key}
					{/if}

					<h2 class="panel-title spaced">Privacy</h2>

					<div class="setting-row">
						<span class="row-label">
							<Orbit size={15} /> Visible to Constellation
							{#if constellationOff}<span class="row-note">(Constellation is off)</span>{/if}
						</span>
						<Switch
							checked={field.constellation_visible}
							disabled={constellationOff}
							label="Visible to Constellation"
							onchange={(v) => patch({ constellation_visible: v })}
						/>
					</div>

					<div class="setting-row">
						<span class="row-label"><SearchSlash size={15} /> Exclude from chat search</span>
						<Switch
							checked={field.exclude_from_chat_search}
							label="Exclude from chat search"
							onchange={(v) => patch({ exclude_from_chat_search: v })}
						/>
					</div>
				</div>
			{/if}
		</div>
	{/if}
</div>

{#if showInstructionsWizard}
	<WizardOverlay
		target={{ kind: 'field_instructions', label: name }}
		seed={instructions}
		onClose={() => (showInstructionsWizard = false)}
		onAccept={acceptWizardInstructions}
	/>
{/if}

{#if confirming}
	<ConfirmModal
		heading={confirming.kind === 'field' ? 'Delete this Field?' : 'Remove this file?'}
		message={confirming.kind === 'field'
			? 'Its conversations are kept — they just become ordinary, ungrouped threads. The shared files are deleted.'
			: `"${confirming.file.name}" will be removed for every conversation in this Field.`}
		confirmLabel={confirming.kind === 'field' ? 'Delete Field' : 'Remove'}
		onConfirm={confirmed}
		onCancel={() => (confirming = null)}
	/>
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: max(var(--space-lg), env(safe-area-inset-top)) var(--space-lg) var(--space-lg);
		box-shadow: var(--shadow-well);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		min-width: 0;
	}

	.header-right {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.page-title {
		margin: 0;
		font-size: 20px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.icon-btn.pinned {
		color: var(--color-accent);
	}

	.content {
		flex: 1;
		overflow-y: auto;
		padding: var(--space-lg);
	}

	.empty {
		max-width: 46ch;
		margin: var(--space-2xl) auto;
		text-align: center;
		font-size: 13.5px;
		line-height: 1.6;
		color: var(--color-text-dim);
	}

	.column {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
		max-width: 720px;
		margin: 0 auto;
		padding-bottom: var(--space-4xl);
	}

	.panel,
	.omnibox {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
		padding: var(--space-lg);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
		box-shadow: var(--shadow-sm), var(--shadow-glass-edge);
	}

	.panel-title {
		margin: 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--color-text-dim);
	}

	.field-memories {
		margin-top: var(--space-lg);
	}

	.hint {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin: 0;
		font-size: 12.5px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.panel-title.spaced {
		margin-top: var(--space-md);
	}

	.summary {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.summary:empty {
		display: none;
	}

	.summary-desc {
		margin: 0;
		font-size: 13.5px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.tabs {
		display: flex;
		gap: var(--space-xs);
		overflow-x: auto;
		/* Scrolls sideways on a narrow phone rather than wrapping to two rows. */
		scrollbar-width: none;
	}

	.tab {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		flex-shrink: 0;
		padding: var(--space-sm) var(--space-md);
		border: none;
		border-radius: var(--radius-full);
		background: transparent;
		font: inherit;
		font-size: 13.5px;
		color: var(--color-text-dim);
		cursor: pointer;
		transition:
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo);
	}

	.tab:hover {
		background: var(--color-surface-2);
	}

	.tab.selected {
		background: var(--color-accent-soft);
		color: var(--color-text);
		font-weight: 600;
	}

	.count {
		font-size: 11.5px;
		font-variant-numeric: tabular-nums;
		color: var(--color-text-dim);
	}

	.link {
		border: none;
		background: transparent;
		padding: 0;
		font: inherit;
		color: var(--color-accent);
		cursor: pointer;
		text-decoration: underline;
	}

	.muted {
		margin: 0;
		font-size: 13px;
		color: var(--color-text-dim);
	}

	textarea,
	.text-input,
	select {
		width: 100%;
		box-sizing: border-box;
		padding: var(--space-md);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-surface-2);
		color: var(--color-text);
		font: inherit;
		font-size: 14px;
	}

	textarea {
		resize: vertical;
		line-height: 1.5;
	}

	textarea:focus,
	.text-input:focus,
	select:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.omnibox-actions {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm);
	}

	.send {
		width: 40px;
		padding-left: 0;
		padding-right: 0;
	}

	/* Right-aligned launcher above the textarea, in the spot a field's
	   label row would put it elsewhere (Pulsar's routine form) — this tab
	   has no label, just the hint line above. WizardButton carries its own
	   bottom margin, so this row adds none. */
	.wizard-row {
		display: flex;
		justify-content: flex-end;
	}

	.instructions-foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.counter {
		font-size: 12px;
		color: var(--color-text-dim);
		font-variant-numeric: tabular-nums;
	}

	.counter.over {
		color: var(--color-danger);
	}

	.setting-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
	}

	.row-label {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 13.5px;
	}

	.row-note {
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.setting-row select {
		width: auto;
		max-width: 55%;
	}

	.swatches {
		display: flex;
		gap: var(--space-sm);
	}

	.swatch {
		width: 22px;
		height: 22px;
		padding: 0;
		border: 2px solid transparent;
		border-radius: var(--radius-full);
		cursor: pointer;
	}

	.swatch.none {
		border: 1px dashed var(--color-border-strong);
		background: transparent;
	}

	.swatch.selected {
		border-color: var(--color-text);
	}

	.segmented {
		display: inline-flex;
		border-radius: var(--radius-full);
		background: var(--color-surface-2);
		padding: 2px;
	}

	.segmented button {
		border: none;
		border-radius: var(--radius-full);
		background: transparent;
		padding: var(--space-xs) var(--space-md);
		font: inherit;
		font-size: 12.5px;
		color: var(--color-text-dim);
		cursor: pointer;
	}

	.segmented button.selected {
		background: var(--color-accent-soft);
		color: var(--color-text);
	}

	.segmented button:disabled {
		opacity: 0.45;
		cursor: default;
	}

	.files,
	.threads {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
	}

	.file {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-xs) 0;
		color: var(--color-text-dim);
	}

	.file-name {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: 13.5px;
		color: var(--color-text);
		text-decoration: none;
	}

	.file-name:hover {
		text-decoration: underline;
	}

	.file-size {
		font-size: 12px;
		font-variant-numeric: tabular-nums;
	}

	.thread-row {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: var(--space-md);
		width: 100%;
		border: none;
		border-radius: var(--radius-md);
		background: transparent;
		padding: var(--space-md);
		font: inherit;
		font-size: 14px;
		text-align: left;
		color: var(--color-text);
		cursor: pointer;
		transition: background-color 0.15s var(--ease-out-expo);
	}

	.thread-title {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.thread-when {
		flex-shrink: 0;
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.thread-row:hover {
		background: var(--color-surface-2);
	}

	/* Reserved brand-face treatment (see app.css's --font-wordmark) — every
	   in-copy mention of "Field" as the feature's name, same as "Pulsar". */
	.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		letter-spacing: 0.02em;
	}
</style>
