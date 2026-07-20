<script lang="ts">
	import type { ChatTurn } from '$lib/types';
	import { appState } from '$lib/state.svelte';
	import ToolEvent from './ToolEvent.svelte';
	import { marked } from 'marked';
	import DOMPurify from 'dompurify';
	import { Pencil, RotateCcw, Check, X, Volume2, Loader2, Square, ChevronRight } from '@lucide/svelte';

	let { turn, index }: { turn: ChatTurn; index: number } = $props();

	// Sources start collapsed — a 15-result answer was burying the actual
	// answer under a wall of full-width pills. Count-only toggle up front,
	// full list is one click away instead of forced on every reader.
	let sourcesOpen = $state(false);

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
				<div class="sources">
					<button class="sources-toggle" onclick={() => (sourcesOpen = !sourcesOpen)}>
						<span class="sources-count">{turn.citations.length}</span>
						<span>{turn.citations.length === 1 ? 'Source' : 'Sources'}</span>
						<ChevronRight size={12} class={sourcesOpen ? 'chevron open' : 'chevron'} />
					</button>
					{#if sourcesOpen}
						<div class="citations">
							{#each turn.citations as c, i (c.url)}
								<a
									class="source-chip"
									href={c.url}
									target="_blank"
									rel="noreferrer"
									title={c.title || c.url}
								>
									<span class="source-index">{i + 1}</span>
									<span class="source-text">
										<span class="source-title">{c.title || hostname(c.url)}</span>
										<span class="source-domain">{hostname(c.url)}</span>
									</span>
								</a>
							{/each}
						</div>
					{/if}
				</div>
			{/if}

			{#if !turn.streaming}
				<div class="turn-footer">
					{#if appState.settings.showPrices && turn.costUsd !== undefined}
						<span class="turn-cost">${turn.costUsd.toFixed(5)}</span>
					{/if}
					<button
						class="icon-btn"
						onclick={() => appState.readAloud(index)}
						title={appState.audio.speakingIndex === index
							? appState.audio.isPlaying
								? 'Stop'
								: 'Loading…'
							: 'Read aloud'}
					>
						{#if appState.audio.speakingIndex === index}
							{#if appState.audio.isPlaying}
								<Square size={13} fill="currentColor" />
							{:else}
								<Loader2 size={13} class="spin" />
							{/if}
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
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		padding: 10px 14px;
		color: var(--color-text);
		white-space: pre-wrap;
		word-break: break-word;
	}

	.bubble-assistant {
		width: 100%;
		max-width: 680px;
		font-size: 15px;
		line-height: 1.65;
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

	.sources {
		margin-top: 10px;
	}

	.sources-toggle {
		display: inline-flex;
		align-items: center;
		gap: 5px;
		border: none;
		background: transparent;
		padding: 2px 0;
		font-size: 12px;
		color: var(--color-text-dim);
		transition: color 0.15s var(--ease-out-expo);
	}

	.sources-toggle:hover {
		color: var(--color-text);
	}

	.sources-count {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 16px;
		height: 16px;
		padding: 0 4px;
		border-radius: 999px;
		background: var(--color-surface-3);
		font-size: 10px;
		font-variant-numeric: tabular-nums;
		color: var(--color-text-dim);
	}

	.citations {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-top: 8px;
	}

	/* Fixed max-width + ellipsis is the whole fix — a 90-character arXiv
	   title no longer forces its own pill to the width of the page. Index
	   badge gives a stable visual anchor since these aren't referenced by
	   number anywhere else in the answer text (the model just hyperlinks
	   inline); it's a scan aid, not a citation marker. */
	.source-chip {
		display: flex;
		align-items: center;
		gap: 7px;
		max-width: 220px;
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: 5px 9px;
		text-decoration: none;
		transition: border-color 0.15s var(--ease-out-expo), background-color 0.15s var(--ease-out-expo);
	}

	.source-chip:hover {
		border-color: var(--color-accent-2);
		background: var(--color-surface-3);
	}

	.source-index {
		flex-shrink: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		width: 15px;
		height: 15px;
		border-radius: 50%;
		background: color-mix(in srgb, var(--color-accent-2) 20%, transparent);
		color: var(--color-accent-2);
		font-size: 9.5px;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
	}

	.source-text {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}

	.source-title {
		font-size: 12px;
		color: var(--color-text);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.source-domain {
		font-size: 10px;
		color: var(--color-text-dim);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.turn-footer {
		display: flex;
		align-items: center;
		gap: 4px;
		margin-top: 6px;
	}

	.turn-cost {
		font-size: 11px;
		color: var(--color-text-dim);
		margin-right: 4px;
	}

	.prose :global(p) {
		margin: 0 0 12px 0;
	}

	.prose :global(p:last-child) {
		margin-bottom: 0;
	}

	/* Weight contrast is the whole game here — serif headings at 700
	   against Lexend body copy at 400 creates real hierarchy without
	   raising the body-text floor. Tighter tracking on the biggest
	   heading; H3 stays sans + uppercase-caps feel via letter-spacing
	   so three levels of hierarchy actually feel distinct. */
	.prose :global(h1),
	.prose :global(h2) {
		font-family: var(--font-serif);
		font-weight: 700;
		line-height: 1.2;
		letter-spacing: -0.01em;
		margin: 22px 0 8px;
		color: var(--color-text);
	}

	.prose :global(h3) {
		font-family: var(--font-serif);
		font-weight: 700;
		line-height: 1.3;
		margin: 18px 0 6px;
		color: var(--color-text);
	}

	.prose :global(h1) {
		font-size: 24px;
		letter-spacing: -0.015em;
	}

	.prose :global(h2) {
		font-size: 20px;
	}

	.prose :global(h3) {
		font-size: 16px;
	}

	.prose :global(h1:first-child),
	.prose :global(h2:first-child),
	.prose :global(h3:first-child) {
		margin-top: 0;
	}

	.prose :global(ul),
	.prose :global(ol) {
		margin: 0 0 12px 0;
		padding-left: 22px;
	}

	.prose :global(li) {
		margin-bottom: 4px;
	}

	.prose :global(blockquote) {
		margin: 12px 0;
		padding: 2px 0 2px 14px;
		border-left: 2px solid var(--color-border-strong);
		color: var(--color-text-dim);
		font-style: italic;
	}

	.prose :global(pre) {
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 10px 12px;
		overflow-x: auto;
		font-size: 13px;
		line-height: 1.5;
	}

	.prose :global(pre code) {
		background: transparent;
		padding: 0;
		font-size: inherit;
	}

	.prose :global(code) {
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		padding: 1px 5px;
		font-size: 13px;
	}

	.prose :global(a) {
		color: var(--color-accent-2);
	}

	.prose :global(a:hover) {
		color: var(--color-accent-2-strong);
	}

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	:global(.chevron) {
		transition: transform 0.15s ease;
	}

	:global(.chevron.open) {
		transform: rotate(90deg);
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
