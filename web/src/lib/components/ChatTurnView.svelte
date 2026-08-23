<script lang="ts">
	import type { ChatTurn } from '$lib/types';
	import { appState } from '$lib/state.svelte';
	import ToolEvent from './ToolEvent.svelte';
	import RecommendationsCarousel from './RecommendationsCarousel.svelte';
	import { marked } from '$lib/markdown';
	import DOMPurify from 'dompurify';
	import { Pencil, RotateCcw, Check, X, Volume2, Loader2, Square, ChevronRight, ChevronLeft, Copy, Link2, Paperclip } from '@lucide/svelte';
	import { copyToClipboard } from '$lib/clipboard';
	import { autoResize } from '$lib/actions/autoResize';
	import { renderInlineCitations } from '$lib/citations';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';

	let { turn, index }: { turn: ChatTurn; index: number } = $props();

	// Editing/regenerating always replaces starting at the preceding user
	// message's position (see gateway/turn.go's ForkThread call) — so an
	// assistant reply's variant group, if it has one, is keyed one index
	// back from its own. appState.variants has no entry at all for a
	// position that's never been touched, which is exactly what keeps the
	// switcher hidden on an ordinary, never-edited reply.
	let variantGroup = $derived(turn.role === 'assistant' ? appState.variants[index - 1] : undefined);
	let variantPosition = $derived(variantGroup ? variantGroup.ids.indexOf(variantGroup.active) : -1);

	function browseVariant(delta: number) {
		if (!variantGroup) return;
		const next = variantPosition + delta;
		if (next < 0 || next >= variantGroup.ids.length) return;
		void appState.swapVariant(variantGroup.ids[next]);
	}

	// Sources start collapsed — a 15-result answer was burying the actual
	// answer under a wall of full-width pills. Count-only toggle up front,
	// full list is one click away for anyone who wants to skim every
	// source at once; inline citation chips (see renderInlineCitations)
	// already open a source directly, so this isn't the only way in.
	let sourcesOpen = $state(false);

	// Content can originate from fetched web pages (via web_read) as well
	// as the model itself, so sanitize before injecting as HTML — treat
	// it the same as any other untrusted input. renderInlineCitations runs
	// AFTER sanitize, turning the model's inline [Title](URL) links into
	// named source chips (e.g. "The Hollywood Reporter") for exactly the
	// claims that actually cite one of this turn's tracked sources — each
	// chip keeps its real href and opens the source directly, so there's
	// no detour through the source list below to find out what a bare
	// number pointed at.
	let renderedHtml = $derived(
		renderInlineCitations(DOMPurify.sanitize(marked.parse(turn.content || '') as string), turn.citations ?? [])
	);

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

	function formatDuration(ms: number): string {
		const seconds = ms / 1000;
		if (seconds < 1) return `${Math.round(ms)}ms`;
		if (seconds < 60) return `${seconds.toFixed(1)}s`;
		const minutes = Math.floor(seconds / 60);
		return `${minutes}m ${Math.round(seconds % 60)}s`;
	}

	// Brief per-button checkmark confirmation after a successful copy —
	// local, unlike updateState in settings.svelte.ts, since there's
	// nothing to preserve across a remount: this turn's copy buttons
	// don't need to "still show progress" if you navigate away mid-copy,
	// the clipboard write is already synchronous and done.
	let copied = $state<'answer' | 'withSources' | null>(null);

	function flashCopied(which: 'answer' | 'withSources') {
		copied = which;
		setTimeout(() => {
			if (copied === which) copied = null;
		}, 1500);
	}

	async function copyAnswer() {
		try {
			await copyToClipboard(turn.content);
			flashCopied('answer');
			appState.showToast('Copied answer');
		} catch (err) {
			appState.showToast('Copy failed — clipboard access was blocked');
		}
	}

	async function copyAnswerWithSources() {
		const sources = (turn.citations ?? [])
			.map((c, i) => `${i + 1}. ${c.title || hostname(c.url)} — ${c.url}`)
			.join('\n');
		const text = sources ? `${turn.content}\n\nSources:\n${sources}` : turn.content;
		try {
			await copyToClipboard(text);
			flashCopied('withSources');
			appState.showToast('Copied answer with sources');
		} catch (err) {
			appState.showToast('Copy failed — clipboard access was blocked');
		}
	}
</script>

{#if turn.role === 'user'}
	<div class="row row-user" in:fly={{ y: 10, duration: 260, easing: quintOut }}>
		{#if turn.attachmentFilename && !editing}
			<div class="attachment-chip">
				<Paperclip size={12} />
				<span>{turn.attachmentFilename}</span>
			</div>
		{/if}
		<div class="user-block" class:editing>
			{#if editing}
				<div class="edit-box">
					<textarea
						bind:value={editValue}
						onkeydown={onEditKeydown}
						rows="2"
						use:autoResize={{ value: editValue, maxHeight: 320 }}
					></textarea>
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
	<div class="row row-assistant" in:fly={{ y: 10, duration: 260, easing: quintOut }}>
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

			{#if turn.cards?.length}
				<RecommendationsCarousel cards={turn.cards} />
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
									{#if c.image_url}
										<img class="source-thumb" src={c.image_url} alt="" loading="lazy" />
									{:else}
										<span class="source-index">{i + 1}</span>
									{/if}
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
					{#if variantGroup && variantGroup.ids.length > 1}
						<div class="variant-switcher">
							<button
								class="icon-btn"
								onclick={() => browseVariant(-1)}
								disabled={variantPosition <= 0}
								title="Previous response"
							>
								<ChevronLeft size={13} />
							</button>
							<span class="variant-position">{variantPosition + 1}/{variantGroup.ids.length}</span>
							<button
								class="icon-btn"
								onclick={() => browseVariant(1)}
								disabled={variantPosition >= variantGroup.ids.length - 1}
								title="Next response"
							>
								<ChevronRight size={13} />
							</button>
						</div>
					{/if}
					{#if turn.costUsd !== undefined}
						<span class="turn-cost">${turn.costUsd.toFixed(5)}</span>
					{/if}
					{#if turn.durationMs !== undefined}
						<span class="turn-duration">{formatDuration(turn.durationMs)}</span>
					{/if}
					<button class="icon-btn" onclick={copyAnswer} title="Copy answer">
						{#if copied === 'answer'}
							<Check size={13} />
						{:else}
							<Copy size={13} />
						{/if}
					</button>
					{#if turn.citations?.length}
						<button class="icon-btn" onclick={copyAnswerWithSources} title="Copy answer with sources">
							{#if copied === 'withSources'}
								<Check size={13} />
							{:else}
								<Link2 size={13} />
							{/if}
						</button>
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
		flex-direction: column;
		align-items: flex-end;
		gap: var(--space-sm);
	}

	.row-user .attachment-chip {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		max-width: 640px;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-full);
		padding: var(--space-xs) var(--space-md);
		font-size: 12px;
		color: var(--color-text-dim);
		box-shadow: var(--shadow-xs);
	}

	.row-user .attachment-chip span {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.row-assistant {
		justify-content: flex-start;
	}

	.user-block {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		max-width: 640px;
	}

	/* Editing needs real room to type in, not the width of whatever short
	   bubble it's replacing — a one-line "what's the capital of france"
	   would otherwise hand the edit box a cramped ~250px, the opposite of
	   Claude.ai's full-width editor. Widens to the same max-width the main
	   composer uses instead of shrink-wrapping to the original message. */
	.user-block.editing {
		width: 100%;
		max-width: 640px;
		align-items: flex-start;
	}

	.bubble {
		font-size: 14px;
		line-height: 1.5;
	}

	.bubble-user {
		background: var(--color-surface-2);
		border: none;
		border-radius: var(--radius-lg);
		/* A real lift instead of a hairline — this is the one bubble shape
		   in the timeline, so it can afford to read as a small floating
		   card rather than a bordered box. Padding grown a notch too; the
		   original 10/14 read tight enough to feel like a form field. */
		box-shadow: var(--shadow-sm);
		padding: var(--space-md) var(--space-lg);
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
		gap: var(--space-sm);
		width: 100%;
	}

	.edit-box textarea {
		resize: vertical;
		border: 1px solid var(--color-accent-2);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: var(--space-md) var(--space-md);
		/* 16px, matching the main composer — anything smaller triggers
		   iOS Safari's zoom-on-focus. autoResize (see the action import
		   above) grows this with content instead of squeezing multi-line
		   text into a fixed 2-row box. */
		font-size: 16px;
		line-height: 1.5;
		font-family: inherit;
		color: var(--color-text);
		outline: none;
		min-height: 60px;
		max-height: 320px;
		overflow-y: auto;
	}

	.edit-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-xs);
	}

	.timeline {
		margin-bottom: var(--space-sm);
	}

	/* A static "…" reads as stalled, not working — a slow, low-amplitude
	   breathing fade (not a spinner; nothing here should look busy or
	   mechanical) is enough to signal "still here" during the gap before
	   the first token lands. */
	.pending {
		color: var(--color-text-dim);
		animation: pending-breathe 1.6s ease-in-out infinite;
	}

	@keyframes pending-breathe {
		0%, 100% {
			opacity: 0.4;
		}
		50% {
			opacity: 1;
		}
	}

	.sources {
		margin-top: var(--space-md);
	}

	.sources-toggle {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		border: none;
		background: transparent;
		padding: var(--space-xs) 0;
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
		padding: 0 var(--space-xs);
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		font-size: 10px;
		font-variant-numeric: tabular-nums;
		color: var(--color-text-dim);
	}

	.citations {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin-top: var(--space-sm);
	}

	/* Fixed max-width + ellipsis is the whole fix — a 90-character arXiv
	   title no longer forces its own pill to the width of the page. Index
	   badge gives a stable visual anchor since these aren't referenced by
	   number anywhere else in the answer text (the model just hyperlinks
	   inline); it's a scan aid, not a citation marker. */
	.source-chip {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		max-width: 220px;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: var(--space-xs) var(--space-sm);
		text-decoration: none;
		box-shadow: var(--shadow-xs);
		transition: background-color 0.15s var(--ease-out-expo), box-shadow 0.15s var(--ease-out-expo);
	}

	.source-chip:hover {
		background: var(--color-surface-3);
		box-shadow: var(--shadow-sm);
	}

	/* Named inline citation chips — the model's [Title](URL) links land
	   here already sanitized, and renderInlineCitations (see citations.ts)
	   swaps in a real site name for a subset of them. Sits low and quiet
	   in the text flow (dim, small, no underline) so it reads as
	   attribution rather than a normal hyperlink; opens the source
	   directly on tap since the label already says what it is — no more
	   detour through the source list below to decode a bare number.
	   :global since these live inside {@html}-injected content, not
	   Svelte-templated markup. */
	.prose :global(.citation-chip) {
		display: inline-flex;
		align-items: center;
		max-width: 180px;
		margin: 0 0 0 var(--space-xs);
		padding: var(--space-xs) var(--space-sm);
		border: none;
		border-radius: var(--radius-full);
		background: var(--color-surface-2);
		color: var(--color-text-dim);
		font-size: 11.5px;
		font-weight: 500;
		text-decoration: none;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		vertical-align: middle;
		transform: translateY(-1px);
		cursor: pointer;
		box-shadow: var(--shadow-xs);
		transition: background-color 0.15s var(--ease-out-expo), color 0.15s var(--ease-out-expo), box-shadow 0.15s var(--ease-out-expo);
	}

	.prose :global(.citation-chip:hover) {
		background: var(--color-surface-3);
		color: var(--color-text);
		box-shadow: var(--shadow-sm);
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

	/* Takes over from .source-index whenever a citation carries a real
	   thumbnail (see tools/registry.go's Citation.ImageURL) — a slightly
	   rounded square (not a circle) since it's showing real art (album
	   covers, etc.), not an abstract badge. Kept small and compressed on
	   purpose, per the same "calm over clever" brief every other bit of
	   chrome in this app follows — it should read as a recognizable
	   thumbnail at a glance, not a decorative hero image. */
	.source-thumb {
		flex-shrink: 0;
		width: 28px;
		height: 28px;
		border-radius: var(--radius-sm);
		object-fit: cover;
		box-shadow: var(--shadow-xs);
	}

	.source-text {
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
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
		gap: var(--space-xs);
		margin-top: var(--space-sm);
	}

	.turn-cost,
	.turn-duration {
		font-size: 11px;
		color: var(--color-text-dim);
		margin-right: var(--space-xs);
		font-variant-numeric: tabular-nums;
	}

	/* Leads the footer, not tucked in with the utility icons — browsing
	   past replies is a real navigation action, not a minor aside like
	   copy/read-aloud. The position readout sits in a shallow well (same
	   "carved, not drawn" language as inputs/readouts elsewhere) between
	   its two arrows so it reads as one compact control. */
	.variant-switcher {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin-right: var(--space-sm);
	}

	.variant-position {
		min-width: 28px;
		padding: var(--space-xs) var(--space-xs);
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-well);
		text-align: center;
		font-size: 11px;
		color: var(--color-text-dim);
		font-variant-numeric: tabular-nums;
	}

	.prose :global(p) {
		margin: 0 0 var(--space-md) 0;
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
		margin: var(--space-xl) 0 var(--space-sm);
		color: var(--color-text);
	}

	.prose :global(h3) {
		font-family: var(--font-serif);
		font-weight: 700;
		line-height: 1.3;
		margin: var(--space-lg) 0 var(--space-sm);
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
		margin: 0 0 var(--space-md) 0;
		padding-left: var(--space-xl);
	}

	.prose :global(li) {
		margin-bottom: var(--space-xs);
	}

	.prose :global(blockquote) {
		margin: var(--space-md) 0;
		padding: var(--space-xs) 0 var(--space-xs) var(--space-lg);
		border-left: 2px solid var(--color-border-strong);
		color: var(--color-text-dim);
		font-style: italic;
	}

	.prose :global(pre) {
		background: var(--color-surface-2);
		border: none;
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-well);
		padding: var(--space-md) var(--space-md);
		overflow-x: auto;
		font-family: var(--font-mono);
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
		border: none;
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-well);
		padding: var(--space-xs) var(--space-xs);
		font-family: var(--font-mono);
		font-size: 13px;
	}

	/* GitHub's table-scroll trick: display:block on the <table> itself (not
	   a wrapper div) turns it into a scrollable block box while its
	   tbody/tr/td children still get anonymous table boxes from the
	   browser, so the grid layout is untouched — width:max-content lets it
	   size to its natural content width, capped by max-width so overflow-x
	   kicks in instead of the table squeezing columns or blowing past the
	   bubble edge. */
	.prose :global(table) {
		display: block;
		width: max-content;
		max-width: 100%;
		overflow-x: auto;
		border-collapse: collapse;
		margin: 0 0 var(--space-md) 0;
		font-size: 13.5px;
	}

	.prose :global(th),
	.prose :global(td) {
		border: 1px solid var(--color-border);
		padding: var(--space-sm) var(--space-md);
		text-align: left;
		vertical-align: top;
	}

	.prose :global(th) {
		background: var(--color-surface-2);
		font-weight: 600;
		white-space: nowrap;
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
