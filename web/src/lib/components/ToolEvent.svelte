<script lang="ts">
	import { untrack } from 'svelte';
	import { marked } from '$lib/markdown';
	import DOMPurify from 'dompurify';
	import hljs from '$lib/highlightjs';
	import type { TimelineItem, Card } from '$lib/types';
	import ImageLightbox from './ImageLightbox.svelte';
	import ArtifactViewer from './ArtifactViewer.svelte';
	import {
		Search,
		FileText,
		Brain,
		Archive,
		Loader2,
		ChevronRight,
		Cloud,
		BookOpen,
		Image,
		MessageCircleQuestion,
		Users,
		NotebookText,
		Lightbulb,
		Calculator,
		FileSearch,
		MapPin,
		Captions,
		FolderGit,
		GitCommit,
		BookA,
		Music,
		Book,
		Film,
		SquareTerminal,
		CloudDownload,
		Images,
		ScanEye,
		Highlighter,
		Paperclip,
		History,
		Download
	} from '@lucide/svelte';
	import ShootingStar from './icons/ShootingStar.svelte';

	let { item }: { item: TimelineItem } = $props();

	// show renders as a large inline embed rather than the generic
	// collapsible chip every other tool gets — see docs/plans/show.md:
	// the whole point is displaying the artifact right where the call
	// happened, not tucking it behind a toggle. Own local state since
	// each ToolEvent instance is exactly one timeline item.
	let showLightboxOpen = $state(false);
	let artifactViewerOpen = $state(false);
	function showCard(showItem: Extract<TimelineItem, { kind: 'tool' }>): Card {
		return {
			title: showItem.caption || (showItem.args?.path as string) || 'Artifact',
			subtitle: showItem.caption,
			image_url: showItem.url ?? '',
			full_image_url: showItem.url,
			url: showItem.url ?? ''
		};
	}

	// Document tier vs. visual tier — see docs/plans/artifacts.md. show's
	// tool_result carries a path/URL but no content-type, so this decides
	// by extension rather than adding new backend metadata plumbing (the
	// route already sniffs content-type itself when actually serving the
	// bytes; this only needs a cheap up-front guess to pick a rendering
	// mode, not a source of truth). Unrecognized/no extension defaults to
	// the document card — safer than guessing "image" and showing a
	// broken-image icon for something like a report with no extension.
	const imageExtensions = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'avif']);
	function isLikelyImage(path: string | undefined): boolean {
		if (!path) return false;
		const ext = path.split('.').pop()?.toLowerCase();
		return ext ? imageExtensions.has(ext) : false;
	}
	function artifactFilename(showItem: Extract<TimelineItem, { kind: 'tool' }>): string {
		return (showItem.args?.path as string) || 'artifact';
	}
	function artifactTypeLabel(filename: string): string {
		const ext = filename.split('.').pop()?.toUpperCase();
		return ext && ext !== filename.toUpperCase() ? `Document · ${ext}` : 'Document';
	}
	// Tool calls start collapsed (their result is secondary detail) but a
	// reasoning block starts open — the whole point is watching it happen
	// live, so the user can tell "still thinking" apart from "stuck". Read
	// once via untrack: each TimelineItem is a stable, never-replaced
	// object for the lifetime of this component instance (keyed by index
	// in the {#each} above), so there's no later prop change to react to.
	let open = $state(untrack(() => item.kind === 'reasoning'));

	// web_search's provider key ("searxng"/"brave"/"parallel"/"tavily") is
	// a stable machine value (see types.ts's doc comment) — this maps it
	// to what a user should actually read, distinct from the tool-call
	// label above (which just shows the query, not who answered it).
	const providerLabels: Record<string, string> = {
		searxng: 'SearXNG',
		brave: 'Brave',
		parallel: 'Parallel',
		tavily: 'Tavily'
	};

	function label(item: Extract<TimelineItem, { kind: 'tool' }>): string {
		if (item.tool === 'web_search') return `Searching: ${item.args?.query ?? ''}`;
		if (item.tool === 'web_read') return `Reading: ${item.args?.url ?? ''}`;
		if (item.tool === 'weather') return `Weather: ${item.args?.location ?? ''}`;
		if (item.tool === 'reference_lookup') return `Looking up: ${item.args?.query ?? ''}`;
		if (item.tool === 'spawn_researchers') {
			const count = item.args?.task_count;
			return count ? `Spawned ${count} researcher${count === 1 ? '' : 's'}` : 'Spawning researchers…';
		}
		// Synthetic — not a real agent tool call. resolveAttachment
		// (gateway/attachments.go) emits this pair itself, before agent.Run
		// even starts, so an uploaded photo's vision-model description shows
		// up on the timeline instead of leaving the screen blank while it runs.
		if (item.tool === 'describe_image') return `Looking at: ${item.args?.filename ?? 'image'}`;
		if (item.tool === 'read_attachment') {
			const query = item.args?.query;
			if (query) return `Searching attachment: ${query}`;
			return `Reading attachment: page ${item.args?.page ?? 1}`;
		}
		// The real interactive UI for this lives in AskUserQuestionCard.svelte
		// (the options list + resolved/picked state) — this chip is just a
		// one-line timeline marker for it, so it names the question rather
		// than the literal tool name or ask_user_question.go's stale
		// "waiting for a reply" placeholder result (accurate only at the
		// instant the call happened, not after it's been answered).
		if (item.tool === 'ask_user_question') return `Asked: ${item.args?.question ?? ''}`;
		if (item.tool === 'memory') {
			// Mirrors tools/memory.go's handleMemory, which only ever logs
			// {action, name} (never description/content — those can be
			// long, and this label is meant to be a one-line summary, not
			// a place to read the full memory).
			const name = item.args?.name as string | undefined;
			switch (item.args?.action) {
				case 'write':
					return `Remembering: ${name ?? '…'}`;
				case 'edit':
					return `Updating memory: ${name ?? '…'}`;
				case 'forget':
					return `Forgetting: ${name ?? '…'}`;
				case 'view':
					return name ? `Checking memory: ${name}` : 'Reviewing memories';
				default:
					return 'Memory';
			}
		}
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

	// The exact Python source a code_exec call ran — tools/code_exec.go's
	// handleCodeExec already emits it as a "tool_call" event's args.code,
	// same as web_search's query, but this component only ever rendered
	// item.result (stdout/stderr/exit code) for a generic tool chip, so
	// the actual executed code was invisible even though the server was
	// never hiding it. hljs.highlight() already HTML-escapes its input
	// (see markdown.ts's code renderer, which trusts it the same way
	// without a separate DOMPurify pass), so this is safe for @html.
	let codeHtml = $derived(
		item.kind === 'tool' && item.tool === 'code_exec' && typeof item.args?.code === 'string'
			? hljs.highlight(item.args.code, { language: 'python' }).value
			: ''
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
{:else if item.tool === 'show'}
	<div class="show-artifact">
		{#if !item.done}
			<div class="show-loading">
				<Loader2 size={14} color="var(--color-text-dim)" class="spin" />
				<span>Preparing artifact…</span>
			</div>
		{:else if item.url && isLikelyImage(item.args?.path as string)}
			<button class="show-image-button" onclick={() => (showLightboxOpen = true)}>
				<img class="show-image" src={item.url} alt={item.caption || 'artifact'} />
			</button>
			{#if item.caption}
				<div class="show-caption">{item.caption}</div>
			{/if}
			{#if showLightboxOpen}
				<ImageLightbox card={showCard(item)} onClose={() => (showLightboxOpen = false)} />
			{/if}
		{:else if item.url}
			<button class="artifact-card" onclick={() => (artifactViewerOpen = true)}>
				<div class="artifact-card-icon"><FileText size={18} /></div>
				<div class="artifact-card-meta">
					<div class="artifact-card-title">{item.caption || artifactFilename(item)}</div>
					<div class="artifact-card-type">{artifactTypeLabel(artifactFilename(item))}</div>
				</div>
				<a
					class="artifact-card-download"
					href={item.url}
					download={artifactFilename(item)}
					onclick={(e) => e.stopPropagation()}
				>
					<Download size={12} />
					Download
				</a>
			</button>
			{#if artifactViewerOpen}
				<ArtifactViewer
					url={item.url}
					filename={artifactFilename(item)}
					caption={item.caption}
					onClose={() => (artifactViewerOpen = false)}
				/>
			{/if}
		{:else}
			<div class="show-error">{item.result}</div>
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
			{:else if item.tool === 'memory'}
				<NotebookText size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'ask_user_question'}
				<MessageCircleQuestion size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'spawn_researchers'}
				<Users size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'think'}
				<Lightbulb size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'calculator'}
				<Calculator size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'web_read'}
				<FileSearch size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'nearby_search'}
				<MapPin size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'youtube_transcript'}
				<Captions size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'github_repo'}
				<FolderGit size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'github_activity'}
				<GitCommit size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'dictionary'}
				<BookA size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'music'}
				<Music size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'books'}
				<Book size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'movies'}
				<Film size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'code_exec'}
				<SquareTerminal size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'fetch_url'}
				<CloudDownload size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'image_search'}
				<Images size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'view_image'}
				<ScanEye size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'highlight'}
				<Highlighter size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'read_attachment'}
				<Paperclip size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'search_chats'}
				<History size={13} color="var(--color-accent-2)" />
			{:else if item.tool === 'stars'}
				<ShootingStar size={13} color="var(--color-accent-2)" />
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
			{#if item.tool === 'web_search' && item.provider}
				<div class="provider-badge">Provided by {providerLabels[item.provider] ?? item.provider}</div>
			{/if}
			{#if item.tool === 'code_exec' && codeHtml}
				<div class="provider-badge">Code</div>
				<pre class="tool-code"><code class="hljs language-python">{@html codeHtml}</code></pre>
			{/if}
			<pre class="tool-result">{item.result}</pre>
		{/if}
	</div>
{/if}

<style>
	.thinking {
		background: color-mix(in srgb, var(--color-surface-2) 55%, transparent);
		border-radius: var(--radius-sm);
		padding: var(--space-sm) var(--space-md);
		margin-bottom: var(--space-xs);
		font-size: 12px;
		font-style: italic;
		color: var(--color-text-dim);
		line-height: 1.5;
	}

	/* Genuine reply prose, not a boxed/collapsible aside like .tool-event
	   or .thinking — it reads as part of the answer, just positioned
	   where it actually happened rather than pushed to the end. */
	.commentary {
		margin-bottom: var(--space-sm);
		font-size: 14px;
		line-height: 1.6;
		color: var(--color-text);
	}

	.commentary :global(p) {
		margin: 0 0 var(--space-sm) 0;
	}

	.commentary :global(p:last-child) {
		margin-bottom: 0;
	}

	.commentary :global(a) {
		color: var(--color-accent);
	}

	.commentary :global(code) {
		font-family: var(--font-mono);
		font-size: 0.9em;
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: var(--space-xs) var(--space-xs);
	}

	/* Same treatment as ChatTurnView.svelte's .prose :global(pre) — kept in
	   sync by eye since scoped styles can't be shared directly, see the
	   doc comment on commentaryHtml above for why this duplicates. */
	.commentary :global(pre) {
		background: var(--color-surface-2);
		border: none;
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-well);
		padding: var(--space-md) var(--space-md);
		overflow-x: auto;
		font-family: var(--font-mono);
		font-size: 13px;
		line-height: 1.5;
		margin: 0 0 var(--space-sm) 0;
	}

	.commentary :global(pre code) {
		background: transparent;
		padding: 0;
		font-size: inherit;
	}

	.tool-event {
		border: none;
		background: color-mix(in srgb, var(--color-surface-2) 55%, transparent);
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-xs);
		margin-bottom: var(--space-xs);
		font-size: 12px;
		overflow: hidden;
	}

	.tool-header {
		display: flex;
		width: 100%;
		align-items: center;
		gap: var(--space-sm);
		border: none;
		background: transparent;
		padding: var(--space-sm) var(--space-md);
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

	/* Sits directly above .tool-result, inside the same expanded panel —
	   a small, distinct label rather than relying on the "[via X]" line
	   buried as the first line of the raw result text below it. */
	.provider-badge {
		padding: var(--space-xs) var(--space-md) 0 var(--space-md);
		font-size: 10.5px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-accent-2);
	}

	/* Same recessed background as .tool-result, directly above it in the
	   same expanded panel — distinguished by holding hljs's own token
	   colors instead of plain dim text, since this is source code, not
	   a stdout/stderr transcript. */
	.tool-code {
		white-space: pre-wrap;
		word-break: break-word;
		background: color-mix(in srgb, black 12%, transparent);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		margin: 0;
		font-family: var(--font-mono);
		font-size: 11.5px;
		line-height: 1.5;
		max-height: 320px;
		overflow-y: auto;
	}

	.tool-result {
		white-space: pre-wrap;
		word-break: break-word;
		/* A tonal step down instead of a rule line — separates the result
		   from the header by being a visibly different depth, the same
		   "recessed" language used for inputs/readouts elsewhere. */
		background: color-mix(in srgb, black 12%, transparent);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		margin: 0;
		color: var(--color-text-dim);
		font-family: inherit;
		font-size: 11.5px;
		line-height: 1.5;
		max-height: 240px;
		overflow-y: auto;
	}

	/* show is deliberately bigger than ImageGallery's own tiles (480px
	   max-width) — see docs/plans/show.md: "one step above highlight" in
	   scale, not just placement. */
	.show-artifact {
		margin-bottom: var(--space-sm);
	}

	.show-loading {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		background: color-mix(in srgb, var(--color-surface-2) 55%, transparent);
		border-radius: var(--radius-sm);
		padding: var(--space-sm) var(--space-md);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.show-image-button {
		display: block;
		border: none;
		background: none;
		padding: 0;
		cursor: pointer;
		max-width: min(100%, 600px);
	}

	.show-image {
		display: block;
		width: 100%;
		height: auto;
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
	}

	.show-caption {
		margin-top: var(--space-xs);
		font-size: 12px;
		color: var(--color-text-dim);
		max-width: min(100%, 600px);
	}

	/* Document tier — see docs/plans/artifacts.md. Sized to sit inline in
	   normal message flow (unlike .show-image-button's deliberately large
	   scale), since a report/document artifact is announced by this card
	   rather than being the visual point of the message itself. */
	.artifact-card {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		width: 100%;
		max-width: 420px;
		background: color-mix(in srgb, var(--color-surface-2) 55%, transparent);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-lg);
		padding: var(--space-md) var(--space-lg);
		cursor: pointer;
		text-align: left;
		transition: border-color 0.15s var(--ease-out-expo), background-color 0.15s var(--ease-out-expo);
	}

	.artifact-card:hover {
		border-color: var(--color-accent);
		background: var(--color-surface-2);
	}

	.artifact-card-icon {
		flex-shrink: 0;
		width: 34px;
		height: 34px;
		border-radius: var(--radius-md);
		background: var(--color-accent-soft);
		color: var(--color-accent);
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.artifact-card-meta {
		flex: 1;
		min-width: 0;
	}

	.artifact-card-title {
		font-size: 13px;
		font-weight: 500;
		color: var(--color-text);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.artifact-card-type {
		font-size: 11.5px;
		color: var(--color-text-dim);
	}

	.artifact-card-download {
		flex-shrink: 0;
		display: flex;
		align-items: center;
		gap: 6px;
		background: var(--color-surface-3);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-md);
		padding: 6px 10px;
		font-size: 12px;
		color: var(--color-text);
		text-decoration: none;
	}

	.artifact-card-download:hover {
		background: var(--color-surface);
	}

	.show-error {
		background: color-mix(in srgb, var(--color-surface-2) 55%, transparent);
		border-radius: var(--radius-sm);
		padding: var(--space-sm) var(--space-md);
		font-size: 12px;
		color: var(--color-text-dim);
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
