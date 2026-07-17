<script lang="ts">
	import type { ChatTurn } from '$lib/types';
	import ToolEvent from './ToolEvent.svelte';
	import { marked } from 'marked';
	import DOMPurify from 'dompurify';

	let { turn }: { turn: ChatTurn } = $props();

	// Content can originate from fetched web pages (via web_read) as well
	// as the model itself, so sanitize before injecting as HTML — treat
	// it the same as any other untrusted input.
	let renderedHtml = $derived(DOMPurify.sanitize(marked.parse(turn.content || '') as string));

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
		<div class="bubble bubble-user">{turn.content}</div>
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

			{#if turn.costUsd !== undefined}
				<div class="turn-cost">${turn.costUsd.toFixed(5)}</div>
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

	.bubble {
		max-width: 640px;
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

	.turn-cost {
		margin-top: 4px;
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
</style>
