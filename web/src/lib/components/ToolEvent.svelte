<script lang="ts">
	import { untrack } from 'svelte';
	import { marked } from 'marked';
	import DOMPurify from 'dompurify';
	import type { TimelineItem } from '$lib/types';
	import { Search, FileText, Brain, Archive, Loader2, ChevronRight, Cloud, BookOpen, Image } from '@lucide/svelte';

	let { item }: { item: TimelineItem } = $props();
	// Tool calls start collapsed (their result is secondary detail) but a
	// reasoning block starts open — the whole point is watching it happen
	// live, so the user can tell "still thinking" apart from "stuck". Read
	// once via untrack: each TimelineItem is a stable, never-replaced
	// object for the lifetime of this component instance (keyed by index
	// in the {#each} above), so there's no later prop change to react to.
	let open = $state(untrack(() => item.kind === 'reasoning'));

	function label(item: Extract<TimelineItem, { kind: 'tool' }>): string {
		if (item.tool === 'web_search') return `Searching: ${item.args?.query ?? ''}`;
		if (item.tool === 'web_read') return `Reading: ${item.args?.url ?? ''}`;
		if (item.tool === 'weather') return `Weather: ${item.args?.location ?? ''}`;
		if (item.tool === 'reference_lookup') return `Looking up: ${item.args?.query ?? ''}`;
		// Synthetic — not a real agent tool call. resolveAttachment
		// (gateway/attachments.go) emits this pair itself, before agent.Run
		// even starts, so an uploaded photo's vision-model description shows
		// up on the timeline instead of leaving the screen blank while it runs.
		if (item.tool === 'describe_image') return `Looking at: ${item.args?.filename ?? 'image'}`;
		return item.tool;
	}

	// Commentary is genuine assistant reply text (not private reasoning),
	// so it's rendered as real markdown — same sanitize-then-parse
	// treatment as the final answer in ChatTurnView.svelte, duplicated
	// here rather than shared since Svelte's per-component style scoping
	// means that component's .prose rules can't reach markup rendered by
	// this one regardless of class name.
	let commentaryHtml = $derived(
		item.kind === 'commentary' ? DOMPurify.sanitize(marked.parse(item.content) as string) : ''
	);
</script>

{#if item.kind === 'thinking'}
	<div class="thinking">{item.content}</div>
{:else if item.kind === 'commentary'}
	<div class="commentary">{@html commentaryHtml}</div>
{:else if item.kind === 'reasoning'}
	<div class="tool-event">
		<button class="tool-header" onclick={() => (open = !open)}>
			<Brain size={13} color="var(--color-accent-2)" />
			<span class="tool-label">{item.done ? 'Reasoned' : 'Reasoning…'}</span>
			{#if !item.done}
				<Loader2 size={13} color="var(--color-text-dim)" class="spin" />
			{:else}
				<ChevronRight size={13} color="var(--color-text-dim)" class={open ? 'chevron open' : 'chevron'} />
			{/if}
		</button>
		{#if open && item.content}
			<pre class="tool-result">{item.content}</pre>
		{/if}
	</div>
{:else if item.kind === 'compacted'}
	<div class="tool-event compacted">
		<button class="tool-header" onclick={() => (open = !open)}>
			<Archive size={13} color="var(--color-accent)" />
			<span class="tool-label">Compacted conversation to save context</span>
			<ChevronRight size={13} color="var(--color-text-dim)" class={open ? 'chevron open' : 'chevron'} />
		</button>
		{#if open}
			<pre class="tool-result">{item.summary}</pre>
		{/if}
	</div>
{:else}
	<div class="tool-event">
		<button class="tool-header" onclick={() => (open = !open)}>
			{#if item.tool === 'web_search'}
				<Search size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'weather'}
				<Cloud size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'reference_lookup'}
				<BookOpen size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'describe_image'}
				<Image size={13} color="var(--color-accent-2)" />
			{:else}
				<FileText size={13} color="var(--color-accent-2)" />
			{/if}
			<span class="tool-label">{label(item)}</span>
			{#if !item.done}
				<Loader2 size={13} color="var(--color-text-dim)" class="spin" />
			{:else}
				<ChevronRight size={13} color="var(--color-text-dim)" class={open ? 'chevron open' : 'chevron'} />
			{/if}
		</button>
		{#if open && item.result}
			<pre class="tool-result">{item.result}</pre>
		{/if}
	</div>
{/if}

<style>
	.thinking {
		background: color-mix(in srgb, var(--color-surface-2) 55%, transparent);
		border-radius: var(--radius-sm);
		padding: 6px 10px;
		margin-bottom: 4px;
		font-size: 12px;
		font-style: italic;
		color: var(--color-text-dim);
		line-height: 1.5;
	}

	/* Genuine reply prose, not a boxed/collapsible aside like .tool-event
	   or .thinking — it reads as part of the answer, just positioned
	   where it actually happened rather than pushed to the end. */
	.commentary {
		margin-bottom: 8px;
		font-size: 14px;
		line-height: 1.6;
		color: var(--color-text);
	}

	.commentary :global(p) {
		margin: 0 0 8px 0;
	}

	.commentary :global(p:last-child) {
		margin-bottom: 0;
	}

	.commentary :global(a) {
		color: var(--color-accent);
	}

	.commentary :global(code) {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 0.9em;
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: 1px 4px;
	}

	.tool-event {
		border: none;
		background: color-mix(in srgb, var(--color-surface-2) 55%, transparent);
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-xs);
		margin-bottom: 4px;
		font-size: 12px;
		overflow: hidden;
	}

	.tool-header {
		display: flex;
		width: 100%;
		align-items: center;
		gap: 8px;
		border: none;
		background: transparent;
		padding: 6px 10px;
		text-align: left;
		color: var(--color-text-dim);
		transition: background-color 0.15s var(--ease-out-expo), color 0.15s var(--ease-out-expo);
	}

	.tool-header:hover {
		background: var(--color-surface-2);
		color: var(--color-text);
	}

	/* Compaction is a system-level event, not a research step — a subtle
	   accent-tinted glow (in place of the old border tint, now that flat
	   borders are gone) sets it apart from ordinary tool chips. */
	.tool-event.compacted {
		box-shadow: 0 0 0 1px color-mix(in srgb, var(--color-accent) 30%, transparent), var(--shadow-xs);
	}

	.tool-label {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.tool-result {
		white-space: pre-wrap;
		word-break: break-word;
		/* A tonal step down instead of a rule line — separates the result
		   from the header by being a visibly different depth, the same
		   "recessed" language used for inputs/readouts elsewhere. */
		background: color-mix(in srgb, black 12%, transparent);
		box-shadow: var(--shadow-well);
		padding: 8px 10px;
		margin: 0;
		color: var(--color-text-dim);
		font-family: inherit;
		font-size: 11.5px;
		line-height: 1.5;
		max-height: 240px;
		overflow-y: auto;
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
