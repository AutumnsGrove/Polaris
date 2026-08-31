<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { Trash2, TriangleAlert, Send, Brain } from '@lucide/svelte';

	// Re-fetched every time this mounts (the panel unmounts this entirely
	// on close, same as the rest of SettingsPanel's sub-pages) rather than
	// once at app startup — a memory added mid-conversation elsewhere
	// shouldn't require a full page reload to show up here.
	void appState.settings.loadMemories();

	// The generic add box up top — "what should it remember" in general,
	// with no particular existing memory in mind.
	let addText = $state('');
	// The scoped box shown once a specific row is expanded — "adjust THIS
	// one" rather than a fresh, untargeted instruction. Kept as its own
	// field (not reusing addText) so expanding a different row while
	// something's half-typed doesn't leak text between the two contexts.
	let adjustText = $state('');
	let expandedName = $state<string | null>(null);
	let confirmingDeleteName = $state<string | null>(null);

	function toggleExpanded(name: string) {
		expandedName = expandedName === name ? null : name;
		adjustText = '';
	}

	async function submitAdd() {
		const text = addText.trim();
		if (!text || appState.settings.memoryChatBusy) return;
		addText = '';
		await appState.settings.sendMemoryInstruction(text);
	}

	// Prefixing with the memory's own name is enough to disambiguate —
	// handleMemoryChat's system prompt (see prompts.yaml's
	// memory_chat_system) already gets the full current index every call,
	// so the model can match "the 'user-timezone' memory" back to that
	// exact row rather than guessing from the instruction text alone.
	async function submitAdjust(name: string) {
		const text = adjustText.trim();
		if (!text || appState.settings.memoryChatBusy) return;
		adjustText = '';
		await appState.settings.sendMemoryInstruction(`Regarding the memory named "${name}": ${text}`);
	}

	function handleKeydown(e: KeyboardEvent, onSubmit: () => void) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			onSubmit();
		}
	}

	async function confirmDelete(name: string) {
		confirmingDeleteName = null;
		if (expandedName === name) expandedName = null;
		await appState.settings.deleteMemory(name);
	}

	const typeLabels: Record<string, string> = {
		user: 'User',
		feedback: 'Feedback',
		project: 'Project',
		reference: 'Reference'
	};

	// gateway's JSON already sends full RFC3339 (store.Store's SQLite driver
	// formats DATETIME columns that way even scanned into a plain Go
	// string — see store/memory.go's Memory struct) — no manual 'T'/'Z'
	// stitching needed, just parse it directly.
	function formatDate(iso: string): string {
		const d = new Date(iso);
		if (Number.isNaN(d.getTime())) return iso;
		return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
	}
</script>

<section class="memory-instruction">
	<h3 class="add-heading">
		What would you like <span class="wordmark">Polaris</span> to remember about you?
	</h3>
	<div class="instruction-bar">
		<input
			type="text"
			placeholder="e.g. I prefer metric units, or I'm a backend engineer"
			bind:value={addText}
			onkeydown={(e) => handleKeydown(e, submitAdd)}
			disabled={appState.settings.memoryChatBusy}
		/>
		<button
			class="icon-btn send-btn"
			onclick={submitAdd}
			disabled={!addText.trim() || appState.settings.memoryChatBusy}
			title="Send"
			aria-label="Send"
		>
			<Send size={16} class={appState.settings.memoryChatBusy ? 'spin' : ''} />
		</button>
	</div>
	<p class="hint">
		General, unprompted additions — for changing or forgetting a specific one, open it below.
	</p>
	{#if appState.settings.memoryChatMessage}
		<p class="hint chat-confirmation">{appState.settings.memoryChatMessage}</p>
	{/if}
</section>

<section class="memory-list">
	{#if !appState.settings.memoriesLoaded}
		<p class="hint">Loading…</p>
	{:else if appState.settings.memories.length === 0}
		<div class="empty-state">
			<Brain size={20} />
			<p class="hint">Nothing saved yet — it'll remember things worth carrying forward as you talk.</p>
		</div>
	{:else}
		{#each appState.settings.memories as memory (memory.name)}
			<div class="memory-row">
				<!-- svelte-ignore a11y_click_events_have_key_events -->
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div class="memory-row-main" onclick={() => toggleExpanded(memory.name)}>
					<div class="memory-row-header">
						<span class="type-badge">{typeLabels[memory.type] ?? memory.type}</span>
						<span class="memory-name">{memory.name}</span>
						<span class="memory-date">{formatDate(memory.updated_at)}</span>
					</div>
					<p class="memory-description">{memory.description}</p>
				</div>
				{#if confirmingDeleteName === memory.name}
					<div class="row-confirm">
						<div class="row-confirm-message">
							<TriangleAlert size={13} />
							<span>Forget this memory?</span>
						</div>
						<div class="row-confirm-actions">
							<button class="text-btn" onclick={() => (confirmingDeleteName = null)}>Cancel</button>
							<button class="text-btn danger" onclick={() => confirmDelete(memory.name)}>Forget</button>
						</div>
					</div>
				{:else}
					<button
						class="icon-btn delete-btn"
						onclick={() => (confirmingDeleteName = memory.name)}
						title="Forget this memory"
						aria-label="Forget this memory"
					>
						<Trash2 size={14} />
					</button>
				{/if}
				{#if expandedName === memory.name}
					<p class="memory-content">{memory.content}</p>
					<div class="instruction-bar adjust-bar">
						<input
							type="text"
							placeholder="Tell it what to change about this memory"
							bind:value={adjustText}
							onkeydown={(e) => handleKeydown(e, () => submitAdjust(memory.name))}
							disabled={appState.settings.memoryChatBusy}
						/>
						<button
							class="icon-btn send-btn"
							onclick={() => submitAdjust(memory.name)}
							disabled={!adjustText.trim() || appState.settings.memoryChatBusy}
							title="Send"
							aria-label="Send"
						>
							<Send size={14} class={appState.settings.memoryChatBusy ? 'spin' : ''} />
						</button>
					</div>
				{/if}
			</div>
		{/each}
	{/if}
</section>

<style>
	/* Not shared with SettingsPanel.svelte's own .hint — Svelte's
	   per-component style scoping means that rule doesn't reach elements
	   rendered by this component, even nested inside the same modal. */
	.hint {
		font-size: 12px;
		color: var(--color-text-dim);
		margin: var(--space-sm) 0 0 0;
	}

	.memory-instruction {
		margin-bottom: var(--space-lg);
	}

	.add-heading {
		margin: 0 0 var(--space-sm) 0;
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text);
		line-height: 1.4;
	}

	/* Same treatment as ChatView.svelte's .welcome-heading .wordmark —
	   Asimovian is the brand's single reserved display face, used only
	   for the literal word "Polaris" wherever it appears in copy. */
	.add-heading .wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		font-size: 0.95em;
		letter-spacing: 0.01em;
	}

	.instruction-bar {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-xs) var(--space-xs) var(--space-xs) var(--space-md);
	}

	.instruction-bar input {
		flex: 1;
		border: none;
		background: transparent;
		font-size: 13px;
		color: var(--color-text);
		padding: var(--space-sm) 0;
	}

	.instruction-bar input::placeholder {
		color: var(--color-text-dim);
	}

	.instruction-bar input:disabled {
		opacity: 0.6;
	}

	.send-btn {
		flex-shrink: 0;
		background: var(--color-surface-3);
	}

	.send-btn:disabled {
		opacity: 0.4;
	}

	.chat-confirmation {
		padding: 0 var(--space-sm);
	}

	.memory-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-xl) 0;
		color: var(--color-text-dim);
		text-align: center;
	}

	.empty-state .hint {
		margin: 0;
		max-width: 240px;
	}

	.memory-row {
		position: relative;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-md);
	}

	.memory-row-main {
		cursor: pointer;
		padding-right: var(--space-xl);
	}

	.memory-row-header {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.type-badge {
		flex-shrink: 0;
		font-size: 10px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--color-text-dim);
		background: var(--color-surface-3);
		border-radius: var(--radius-sm);
		padding: 2px var(--space-xs);
	}

	.memory-name {
		flex: 1;
		min-width: 0;
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.memory-date {
		flex-shrink: 0;
		font-size: 11px;
		color: var(--color-text-dim);
	}

	.memory-description {
		margin: var(--space-xs) 0 0 0;
		font-size: 12.5px;
		color: var(--color-text-dim);
		line-height: 1.4;
	}

	.memory-content {
		margin: var(--space-sm) 0 0 0;
		padding-top: var(--space-sm);
		border-top: none;
		box-shadow: inset 0 1px 0 color-mix(in srgb, var(--color-border) 60%, transparent);
		font-size: 12.5px;
		color: var(--color-text);
		line-height: 1.5;
		white-space: pre-wrap;
	}

	.adjust-bar {
		margin-top: var(--space-sm);
		background: var(--color-surface-3);
	}

	.adjust-bar input {
		font-size: 12.5px;
	}

	.delete-btn {
		position: absolute;
		top: var(--space-sm);
		right: var(--space-sm);
		color: var(--color-text-dim);
	}

	.delete-btn:hover {
		color: var(--color-danger);
	}

	.row-confirm {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
		padding-top: var(--space-sm);
		box-shadow: inset 0 1px 0 color-mix(in srgb, var(--color-border) 60%, transparent);
	}

	.row-confirm-message {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		font-size: 12.5px;
		color: var(--color-text);
	}

	.row-confirm-message :global(svg) {
		color: var(--color-danger);
		flex-shrink: 0;
	}

	.row-confirm-actions {
		display: flex;
		gap: var(--space-sm);
		flex-shrink: 0;
	}

	.text-btn {
		border: none;
		background: transparent;
		font-size: 12.5px;
		font-family: inherit;
		color: var(--color-text-dim);
		padding: var(--space-xs) var(--space-sm);
		border-radius: var(--radius-sm);
	}

	.text-btn:hover {
		background: var(--color-surface-3);
		color: var(--color-text);
	}

	.text-btn.danger {
		color: var(--color-danger);
	}

	.text-btn.danger:hover {
		background: var(--color-danger-bg);
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
