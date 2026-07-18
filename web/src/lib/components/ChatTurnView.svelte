<script lang="ts">
	import type { ChatTurn } from '$lib/types';
	import { appState } from '$lib/state.svelte';
	import ToolEvent from './ToolEvent.svelte';
	import { marked } from 'marked';
	import DOMPurify from 'dompurify';
	import { Pencil, RotateCcw, Check, X, Volume2, Loader2 } from '@lucide/svelte';

	let { turn, index }: { turn: ChatTurn; index: number } = $props();

	// Content can originate from fetched web pages (via web_read) as well
	// as the model itself, so sanitize before injecting as HTML — treat
	// it the same as any other untrusted input.
	let renderedHtml = $derived(DOMPurify.sanitize(marked.parse(turn.content || '') as string));

	let editing = $state(false);
	let editValue = $state('');

	function startEdit() {
		editValue = turn.content;
		editing = true;
	}

	function cancelEdit() {
		editing = false;
	}

	function saveEdit() {
		editing = false;
		appState.editMessage(index, editValue);
	}

	function onEditKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			saveEdit();
		} else if (e.key === 'Escape') {
			cancelEdit();
		}
	}

	function hostname(url: string): string {
		try {
			return new URL(url).hostname;
		} catch {
			return url;
		}
	}
</script>

{#if turn.role === 'user'}
	<div class="row row-user">
		<div class="user-block">
			{#if editing}
				<div class="edit-box">
					<textarea bind:value={editValue} onkeydown={onEditKeydown} rows="2"></textarea>
					<div class="edit-actions">
						<button class="icon-btn" onclick={cancelEdit} title="Cancel"><X size={14} /></button>
						<button class="icon-btn" onclick={saveEdit} title="Save and re-run"><Check size={14} /></button>
					</div>
				</div>
			{:else}
				<div class="bubble bubble-user">{turn.content}</div>
				<button
					class="icon-btn edit-trigger"
					onclick={startEdit}
					disabled={turn.id === undefined || appState.busy}
					title="Edit and re-run"
				>
					<Pencil size={13} />
				</button>
			{/if}
		</div>
	</div>
{:else}
	<div class="row row-assistant">
		<div class="bubble bubble-assistant">
			{#if turn.timeline?.length}
				<div class="timeline">
					{#each turn.timeline as item, i (i)}
						<ToolEvent {item} />
					{/each}
				</div>
			{/if}

			{#if turn.content}
				<div class="prose">{@html renderedHtml}</div>
			{:else if turn.streaming}
				<div class="pending">…</div>
			{/if}

			{#if turn.citations?.length}
				<div class="citations">
					{#each turn.citations as c (c.url)}
						<a class="badge" href={c.url} target="_blank" rel="noreferrer">
							{c.title || hostname(c.url)}
						</a>
					{/each}
				</div>
			{/if}

			{#if !turn.streaming}
				<div class="turn-footer">
					{#if turn.costUsd !== undefined}
						<span class="turn-cost">${turn.costUsd.toFixed(5)}</span>
					{/if}
					<button
						class="icon-btn"
						onclick={() => appState.readAloud(index)}
						disabled={appState.speakingIndex !== null}
						title="Read aloud"
					>
						{#if appState.speakingIndex === index}
							<Loader2 size={13} class="spin" />
						{:else}
							<Volume2 size={13} />
						{/if}
					</button>
					<button
						class="icon-btn retry-btn"
						onclick={() => appState.retry(index)}
						disabled={appState.busy}
						title="Retry this turn"
					>
						<RotateCcw size={13} />
					</button>
				</div>
			{/if}
		</div>
	</div>
{/if}

<style>
	.row {
		display: flex;
	}

	.row-user {
		justify-content: flex-end;
	}

	.row-assistant {
		justify-content: flex-start;
	}

	.user-block {
		display: flex;
		align-items: center;
		gap: 6px;
		max-width: 640px;
	}

	.bubble {
		font-size: 14px;
		line-height: 1.5;
	}

	.bubble-user {
		background: color-mix(in srgb, var(--color-accent-2) 15%, transparent);
		border-radius: var(--radius-lg);
		padding: 10px 14px;
	}

	.bubble-assistant {
		width: 100%;
		max-width: 640px;
	}

	.edit-trigger {
		opacity: 0;
		flex-shrink: 0;
	}

	.user-block:hover .edit-trigger {
		opacity: 1;
	}

	.edit-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		width: 100%;
	}

	.edit-box textarea {
		resize: vertical;
		border: 1px solid var(--color-accent-2);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: 10px 12px;
		font-size: 14px;
		font-family: inherit;
		color: var(--color-text);
		outline: none;
	}

	.edit-actions {
		display: flex;
		justify-content: flex-end;
		gap: 4px;
	}

	.timeline {
		margin-bottom: 8px;
	}

	.pending {
		color: var(--color-text-dim);
	}

	.citations {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-top: 8px;
	}

	.turn-footer {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-top: 4px;
	}

	.turn-cost {
		font-size: 11px;
		color: var(--color-text-dim);
	}

	.prose :global(p) {
		margin: 0 0 10px 0;
	}

	.prose :global(pre) {
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 10px;
		overflow-x: auto;
	}

	.prose :global(code) {
		background: var(--color-surface-2);
		border-radius: 4px;
		padding: 1px 4px;
		font-size: 13px;
	}

	.prose :global(a) {
		color: var(--color-accent-2);
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
