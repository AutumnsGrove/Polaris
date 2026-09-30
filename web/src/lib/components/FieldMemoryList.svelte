<script lang="ts">
	import { Trash2, Pencil } from '@lucide/svelte';
	import type { Memory } from '$lib/settings.svelte';
	import ConfirmModal from './ConfirmModal.svelte';

	// A Field's own memories (issue #133), shown on its page when the memory
	// mode is Field or Both. View/edit/forget only — there's deliberately no
	// "add" box: entries appear by the model saving them during a chat in the
	// Field. Not MemorySettings.svelte: that one is wired to the global
	// settings state and its "tell it what to change" chat endpoint.
	let { fieldId }: { fieldId: string } = $props();

	let memories = $state<Memory[]>([]);
	let loaded = $state(false);
	let error = $state('');
	let editingName = $state<string | null>(null);
	let draftDescription = $state('');
	let draftContent = $state('');
	let confirmingName = $state<string | null>(null);

	const base = $derived(`/api/fields/${fieldId}/memories`);

	async function load() {
		try {
			const res = await fetch(base);
			if (!res.ok) throw new Error(await res.text());
			memories = (await res.json()) as Memory[];
			error = '';
		} catch {
			error = 'Couldn’t load this Field’s memories.';
		}
		loaded = true;
	}
	// Re-fetched on mount (and when switching Fields), not cached: the model
	// can add one mid-chat in another tab.
	$effect(() => {
		void fieldId;
		void load();
	});

	function startEdit(m: Memory) {
		editingName = m.name;
		draftDescription = m.description;
		draftContent = m.content;
	}

	async function saveEdit() {
		const name = editingName;
		if (!name) return;
		const res = await fetch(`${base}/${encodeURIComponent(name)}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ description: draftDescription.trim(), content: draftContent.trim() })
		});
		if (!res.ok) {
			error = await res.text();
			return;
		}
		editingName = null;
		await load();
	}

	async function confirmForget() {
		const name = confirmingName;
		confirmingName = null;
		if (!name) return;
		const res = await fetch(`${base}/${encodeURIComponent(name)}`, { method: 'DELETE' });
		if (!res.ok) {
			error = 'Couldn’t forget that memory — try again.';
			return;
		}
		await load();
	}
</script>

<div class="field-memories">
	<h2 class="panel-title spaced">Field memories</h2>
	{#if error}<p class="error">{error}</p>{/if}
	{#if loaded && memories.length === 0 && !error}
		<p class="empty">Nothing saved yet. Memories Polaris saves in this Field’s chats show up here.</p>
	{/if}
	{#each memories as m (m.name)}
		<div class="memory">
			<div class="memory-head">
				<span class="memory-name">{m.name}</span>
				<span class="memory-type">{m.type}</span>
				<button class="icon-btn" title="Edit" aria-label="Edit {m.name}" onclick={() => startEdit(m)}>
					<Pencil size={14} />
				</button>
				<button class="icon-btn" title="Forget" aria-label="Forget {m.name}" onclick={() => (confirmingName = m.name)}>
					<Trash2 size={14} />
				</button>
			</div>
			{#if editingName === m.name}
				<input class="edit-input" bind:value={draftDescription} aria-label="Description" maxlength="2000" />
				<textarea class="edit-input" rows="4" bind:value={draftContent} aria-label="Content"></textarea>
				<div class="edit-actions">
					<button class="btn" onclick={() => (editingName = null)}>Cancel</button>
					<button class="btn btn-accent" onclick={saveEdit}>Save</button>
				</div>
			{:else}
				<p class="memory-description">{m.description}</p>
				<p class="memory-content">{m.content}</p>
			{/if}
		</div>
	{/each}
</div>

{#if confirmingName}
	<ConfirmModal
		heading="Forget this memory?"
		message="“{confirmingName}” will be removed from this Field’s memory."
		confirmLabel="Forget"
		onConfirm={confirmForget}
		onCancel={() => (confirmingName = null)}
	/>
{/if}

<style>
	.field-memories {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		margin-top: var(--space-md);
	}

	.memory {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		padding: var(--space-md);
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
	}

	.memory-head {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.memory-name {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
		font-size: 14px;
		font-weight: 500;
	}

	.memory-type {
		font-size: 11.5px;
		color: var(--color-text-dim);
	}

	.memory-description {
		margin: 0;
		font-size: 13px;
		color: var(--color-text);
	}

	.memory-content {
		margin: 0;
		font-size: 12.5px;
		line-height: 1.5;
		color: var(--color-text-dim);
		white-space: pre-wrap;
	}

	.edit-input {
		width: 100%;
		border: 1px solid var(--color-border);
		background: var(--color-surface-3);
		border-radius: var(--radius-md);
		padding: var(--space-sm) var(--space-md);
		font: inherit;
		/* 16px floor: anything smaller makes iOS Safari zoom on focus. */
		font-size: 16px;
		color: var(--color-text);
		resize: vertical;
	}

	.edit-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-sm);
	}

	.empty,
	.error {
		margin: 0;
		font-size: 12.5px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.error {
		color: var(--color-danger);
	}
</style>
