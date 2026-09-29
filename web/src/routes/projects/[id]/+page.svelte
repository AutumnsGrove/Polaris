<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { projectsState, projectColorVar, PROJECT_COLORS } from '$lib/projects.svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { FOCUS_MODES } from '$lib/focusModes';
	import ConfirmModal from '$lib/components/ConfirmModal.svelte';
	import Switch from '$lib/components/Switch.svelte';
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
	import type { Project, ProjectDetail, ProjectFile } from '$lib/types';

	// Mirrors gateway/settings.go's maxCustomInstructionsChars — the server is
	// the real limit; this just lets the counter turn red before a save 400s.
	const MAX_INSTRUCTIONS = 4000;

	let detail = $state<ProjectDetail | null>(null);
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
	let confirming = $state<null | { kind: 'project' } | { kind: 'file'; file: ProjectFile }>(null);
	let fileInput: HTMLInputElement | undefined = $state();

	onMount(() => {
		// Needed only to grey out the Constellation toggle when the feature is
		// off globally — cheap, and null until loaded.
		if (!constellationState.config) void constellationState.loadConfig();
		if (!appState.settings.loaded) void appState.settings.load();
	});

	// Re-runs on a route-to-route navigation between two projects (the sidebar's
	// pinned shortcuts), which reuses this component instance.
	$effect(() => {
		const id = page.params.id;
		if (!id) return;
		detail = null;
		notFound = false;
		loadError = '';
		void load(id);
	});

	function adopt(d: ProjectDetail) {
		detail = d;
		name = d.project.name;
		description = d.project.description;
		instructions = d.project.custom_instructions;
	}

	async function load(id: string) {
		const res = await projectsState.loadDetail(id);
		if (id !== page.params.id) return; // navigated to another project mid-request
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
	async function patch(fields: Partial<Project>): Promise<boolean> {
		if (!detail) return false;
		const res = await projectsState.update(detail.project.id, fields);
		if (!res.ok) {
			appState.showToast(res.error);
			// Re-sync the buffers so a rejected edit doesn't linger in an input.
			name = detail.project.name;
			description = detail.project.description;
			return false;
		}
		detail.project = res.data;
		return true;
	}

	async function saveName() {
		if (!detail || name.trim() === detail.project.name) return;
		await patch({ name });
	}

	async function saveDescription() {
		if (!detail || description === detail.project.description) return;
		await patch({ description });
	}

	async function saveInstructions() {
		if (savingInstructions) return;
		savingInstructions = true;
		await patch({ custom_instructions: instructions });
		savingInstructions = false;
	}

	// The omnibox creates the thread AND opens it already mid-turn, rather
	// than a separate "create, then type" step. Focus mode is resolved here
	// (project default, else the global one) because send()'s own focus
	// argument is what the very first turn uses — ChatView's config effect
	// only seeds the composer for the turns after it.
	async function startWithPrompt() {
		const text = prompt.trim();
		if (!text || !detail || appState.busy) return;
		const projectFocus = detail.project.default_focus_mode;
		const focus = projectFocus ? projectFocus : appState.settings.defaultFocusMode;
		appState.startThreadInProject(detail.project.id);
		prompt = '';
		await goto('/');
		appState.send(text, undefined, focus && focus !== 'off' ? focus : undefined);
	}

	async function startBlank() {
		if (!detail) return;
		appState.startThreadInProject(detail.project.id);
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
			const res = await projectsState.uploadFile(detail.project.id, file);
			if (!res.ok) appState.showToast(`${file.name}: ${res.error}`);
		}
		uploading = false;
		if (fileInput) fileInput.value = '';
		await refreshDetail();
	}

	// Reload without clobbering an in-progress edit in the text buffers.
	async function refreshDetail() {
		if (!detail) return;
		const res = await projectsState.loadDetail(detail.project.id);
		if (res.ok) detail = res.data;
	}

	async function confirmed() {
		const target = confirming;
		confirming = null;
		if (!target || !detail) return;
		if (target.kind === 'file') {
			const res = await projectsState.deleteFile(detail.project.id, target.file.name);
			if (!res.ok) appState.showToast(res.error);
			await refreshDetail();
		} else {
			const res = await projectsState.remove(detail.project.id);
			if (!res.ok) {
				appState.showToast(res.error);
				return;
			}
			// Threads survive, ungrouped — the sidebar list has no project
			// affordance to refresh, but the thread rows' own state does.
			void appState.loadThreads();
			void goto('/projects');
		}
	}

	function formatSize(bytes: number): string {
		if (bytes < 1024) return `${bytes} B`;
		if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
		return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
	}

	let constellationOff = $derived(!!constellationState.config && !constellationState.config.enabled);
</script>

<svelte:head>
	<title>{detail ? `${detail.project.name} — Projects` : 'Project'} — Polaris</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		<button class="icon-btn" onclick={() => goto('/projects')} title="All projects" aria-label="All projects">
			<ChevronLeft size={18} />
		</button>
		{#if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<h1 class="page-title">{detail?.project.name ?? ''}</h1>
	</div>
	{#if detail}
		<div class="header-right">
			<button
				class="icon-btn"
				class:pinned={detail.project.favorite}
				onclick={() => patch({ favorite: !detail!.project.favorite })}
				title={detail.project.favorite ? 'Unpin from sidebar' : 'Pin to sidebar'}
				aria-label={detail.project.favorite ? 'Unpin from sidebar' : 'Pin to sidebar'}
				aria-pressed={detail.project.favorite}
			>
				<Star size={17} fill={detail.project.favorite ? 'currentColor' : 'none'} />
			</button>
			<button
				class="icon-btn"
				onclick={() => (confirming = { kind: 'project' })}
				title="Delete project"
				aria-label="Delete project"
			>
				<Trash2 size={17} />
			</button>
		</div>
	{/if}
</header>

<div class="content">
	{#if notFound}
		<p class="empty">That project doesn't exist anymore.</p>
	{:else if loadError}
		<p class="empty">Couldn't load this project: {loadError}</p>
	{:else if detail}
		{@const project = detail.project}
		<div class="column">
			<!-- Omnibox: typing a prompt and sending creates a thread in this
			     project AND opens it mid-turn. The plain button beside it is the
			     other case — a blank, unsent composer scoped to the project. -->
			<section class="omnibox">
				<textarea
					bind:value={prompt}
					onkeydown={onPromptKeydown}
					rows="3"
					placeholder="Start a conversation in {project.name}…"
					aria-label="Start a conversation in this project"
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

			<section class="card">
				<h2 class="card-title">About</h2>
				<input class="text-input" bind:value={name} onblur={saveName} maxlength="100" aria-label="Project name" />
				<input
					class="text-input"
					bind:value={description}
					onblur={saveDescription}
					maxlength="300"
					placeholder="One-line description (shown on the hub)"
					aria-label="Project description"
				/>
				<div class="setting-row">
					<span class="row-label"><Palette size={15} /> Color</span>
					<div class="swatches" role="radiogroup" aria-label="Color tag">
						<button
							class="swatch none"
							class:selected={project.color === ''}
							onclick={() => patch({ color: '' })}
							role="radio"
							aria-checked={project.color === ''}
							aria-label="No color"
							title="No color"
						></button>
						{#each PROJECT_COLORS as c (c)}
							<button
								class="swatch"
								class:selected={project.color === c}
								style:background={projectColorVar(c)}
								onclick={() => patch({ color: c })}
								role="radio"
								aria-checked={project.color === c}
								aria-label={c.replace(/-/g, ' ')}
								title={c.replace(/-/g, ' ')}
							></button>
						{/each}
					</div>
				</div>
			</section>

			<section class="card">
				<h2 class="card-title">Instructions</h2>
				<p class="hint">
					Added to every conversation in this project, after your global custom instructions — never
					instead of them.
				</p>
				<textarea
					class="instructions"
					bind:value={instructions}
					rows="6"
					placeholder="How should Polaris behave in this project? Context, tone, constraints…"
					aria-label="Project instructions"
				></textarea>
				<div class="instructions-foot">
					<span class="counter" class:over={instructions.length > MAX_INSTRUCTIONS}>
						{instructions.length} / {MAX_INSTRUCTIONS}
					</span>
					<button
						class="btn btn-accent"
						onclick={saveInstructions}
						disabled={savingInstructions || instructions === project.custom_instructions || instructions.length > MAX_INSTRUCTIONS}
					>
						{savingInstructions ? 'Saving…' : 'Save'}
					</button>
				</div>
			</section>

			<section class="card">
				<h2 class="card-title">Shared files</h2>
				<p class="hint">
					<Lock size={12} /> Every conversation in this project can read these, but not change them — an
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
								href="/api/workspace/{project.id}/{encodeURIComponent(file.name)}"
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
			</section>

			<section class="card">
				<h2 class="card-title">Settings</h2>

				<div class="setting-row">
					<span class="row-label"><SlidersHorizontal size={15} /> Default focus mode</span>
					<select
						value={project.default_focus_mode}
						onchange={(e) => patch({ default_focus_mode: e.currentTarget.value as Project['default_focus_mode'] })}
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
						value={project.default_model}
						onchange={(e) => patch({ default_model: e.currentTarget.value })}
						aria-label="Default model"
					>
						<option value="">Use my global default</option>
						{#each appState.models as m (m.id)}
							<option value={m.id}>{m.name}</option>
						{/each}
					</select>
				</div>

				<div class="setting-row">
					<span class="row-label"><Brain size={15} /> Memory</span>
					<div class="segmented" role="radiogroup" aria-label="Memory mode">
						<button
							class:selected={project.memory_mode === 'default'}
							onclick={() => patch({ memory_mode: 'default' })}
							role="radio"
							aria-checked={project.memory_mode === 'default'}>Default</button
						>
						<button
							class:selected={project.memory_mode === 'none'}
							onclick={() => patch({ memory_mode: 'none' })}
							role="radio"
							aria-checked={project.memory_mode === 'none'}>None</button
						>
						<!-- Reserved for the real per-project memory store (plan's
						     v2) — shown so the picker's shape doesn't change when it
						     lands, but not selectable until it does. -->
						<button disabled title="Coming later" role="radio" aria-checked="false">Project-scoped</button>
					</div>
				</div>

				<div class="setting-row">
					<span class="row-label">
						<Orbit size={15} /> Visible to Constellation
						{#if constellationOff}<span class="row-note">(Constellation is off)</span>{/if}
					</span>
					<Switch
						checked={project.constellation_visible}
						disabled={constellationOff}
						label="Visible to Constellation"
						onchange={(v) => patch({ constellation_visible: v })}
					/>
				</div>

				<div class="setting-row">
					<span class="row-label"><SearchSlash size={15} /> Exclude from chat search</span>
					<Switch
						checked={project.exclude_from_chat_search}
						label="Exclude from chat search"
						onchange={(v) => patch({ exclude_from_chat_search: v })}
					/>
				</div>
			</section>

			<section class="card">
				<h2 class="card-title">Conversations</h2>
				{#if detail.threads.length === 0}
					<p class="muted">No conversations yet. Start one above.</p>
				{/if}
				<ul class="threads">
					{#each detail.threads as thread (thread.id)}
						<li>
							<button class="thread-row" onclick={() => goto(`/t/${thread.id}`)}>
								{thread.title || 'Untitled'}
							</button>
						</li>
					{/each}
				</ul>
			</section>
		</div>
	{/if}
</div>

{#if confirming}
	<ConfirmModal
		heading={confirming.kind === 'project' ? 'Delete this project?' : 'Remove this file?'}
		message={confirming.kind === 'project'
			? 'Its conversations are kept — they just become ordinary, ungrouped threads. The shared files are deleted.'
			: `"${confirming.file.name}" will be removed for every conversation in this project.`}
		confirmLabel={confirming.kind === 'project' ? 'Delete project' : 'Remove'}
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

	.card,
	.omnibox {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
		padding: var(--space-lg);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
		box-shadow: var(--shadow-sm), var(--shadow-glass-edge);
	}

	.card-title {
		margin: 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--color-text-dim);
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

	.thread-row:hover {
		background: var(--color-surface-2);
	}
</style>
