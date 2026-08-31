<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { Trash2, TriangleAlert, Send, Brain } from '@lucide/svelte';

	// Re-fetched every time this mounts (the panel unmounts this entirely
	// on close, same as the rest of SettingsPanel's sub-pages) rather than
	// once at app startup — a memory added mid-conversation elsewhere
	// shouldn't require a full page reload to show up here.
	void appState.settings.loadMemories();

	let instruction = $state('');
	let expandedName = $state<string | null>(null);
	let confirmingDeleteName = $state<string | null>(null);

	function toggleExpanded(name: string) {
		expandedName = expandedName === name ? null : name;
	}

	async function submitInstruction() {
		const text = instruction.trim();
		if (!text || appState.settings.memoryChatBusy) return;
		instruction = '';
		await appState.settings.sendMemoryInstruction(text);
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			void submitInstruction();
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
	<div class="instruction-bar">
		<input
			type="text"
			placeholder="Tell it what to change or remove"
			bind:value={instruction}
			onkeydown={handleKeydown}
			disabled={appState.settings.memoryChatBusy}
		/>
		<button
			class="icon-btn send-btn"
			onclick={submitInstruction}
			disabled={!instruction.trim() || appState.settings.memoryChatBusy}
			title="Send"
			aria-label="Send"
		>
			<Send size={16} class={appState.settings.memoryChatBusy ? 'spin' : ''} />
		</button>
	</div>
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
