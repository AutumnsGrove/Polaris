<script lang="ts">
	import { marked } from '$lib/markdown';
	import DOMPurify from 'dompurify';
	import { Eye, Code2, Copy, Check, Download, X, Loader2 } from '@lucide/svelte';

	// Document-tier artifact viewer — see docs/plans/artifacts.md. Wide
	// viewports get a side panel docked to the right edge; narrow
	// (phone-class, this app's primary real usage per CLAUDE.md) get a
	// full-screen sheet. Both reuse .modal-backdrop-close for the scrim,
	// same convention ImageLightbox.svelte already established, rather
	// than inventing new overlay chrome. Doesn't actually resize the chat
	// column underneath on wide viewports (that would need restructuring
	// ChatView's own layout, which nothing in the app currently does) —
	// a deliberate v1 simplification over the reference screenshots' true
	// split-pane, revisit if it reads as wrong in practice.
	let {
		url,
		filename,
		caption,
		onClose
	}: { url: string; filename: string; caption?: string; onClose: () => void } = $props();

	let raw = $state('');
	let loading = $state(true);
	let loadError = $state('');
	let mode: 'rendered' | 'raw' = $state('rendered');
	let copied = $state(false);

	$effect(() => {
		let cancelled = false;
		fetch(url)
			.then((res) => {
				if (!res.ok) throw new Error(`fetch failed: ${res.status}`);
				return res.text();
			})
			.then((text) => {
				if (!cancelled) raw = text;
			})
			.catch((err) => {
				if (!cancelled) loadError = err instanceof Error ? err.message : 'failed to load artifact';
			})
			.finally(() => {
				if (!cancelled) loading = false;
			});
		return () => {
			cancelled = true;
		};
	});

	let renderedHtml = $derived(raw ? DOMPurify.sanitize(marked.parse(raw) as string) : '');

	async function copyRaw() {
		await navigator.clipboard.writeText(raw);
		copied = true;
		setTimeout(() => (copied = false), 1500);
	}

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') onClose();
	}
</script>

<svelte:window onkeydown={onKeydown} />

<div class="modal-backdrop artifact-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="artifact-panel">
		<div class="panel-head">
			<div class="toggle-group">
				<button
					class={mode === 'rendered' ? 'active' : ''}
					onclick={() => (mode = 'rendered')}
					title="Rendered"
					aria-label="Rendered view"
				>
					<Eye size={14} />
				</button>
				<button
					class={mode === 'raw' ? 'active' : ''}
					onclick={() => (mode = 'raw')}
					title="Raw source"
					aria-label="Raw source view"
				>
					<Code2 size={14} />
				</button>
			</div>
			<div class="title-block">
				<div class="t">{caption || filename}</div>
				<div class="s">{filename}</div>
			</div>
			<div class="actions">
				<button onclick={copyRaw} title="Copy" aria-label="Copy raw content" disabled={!raw}>
					{#if copied}<Check size={14} />{:else}<Copy size={14} />{/if}
				</button>
				<a class="action-link" href={url} download={filename} title="Download" aria-label="Download">
					<Download size={14} />
				</a>
				<button onclick={onClose} title="Close" aria-label="Close">
					<X size={14} />
				</button>
			</div>
		</div>
		<div class="panel-body">
			{#if loading}
				<div class="panel-loading"><Loader2 size={16} class="spin" /> Loading…</div>
			{:else if loadError}
				<div class="panel-loading">{loadError}</div>
			{:else if mode === 'rendered'}
				<div class="prose">{@html renderedHtml}</div>
			{:else}
				<pre class="raw">{raw}</pre>
			{/if}
		</div>
	</div>
</div>

<style>
	/* Wide viewport: dock right, don't fill the whole scrim — reads as a
	   panel, not a centered dialog. Narrow: full-screen sheet, padding
	   collapses to 0 so it truly fills the phone viewport. */
	.artifact-backdrop {
		justify-content: flex-end;
		padding: 0;
	}

	.artifact-panel {
		position: relative;
		width: min(480px, 100vw);
		height: 100%;
		background: var(--color-surface);
		border-left: 1px solid var(--color-border-strong);
		box-shadow: var(--shadow-lg);
		display: flex;
		flex-direction: column;
		animation: artifact-panel-in 0.2s var(--ease-out-expo);
	}

	@keyframes artifact-panel-in {
		from {
			transform: translateX(24px);
			opacity: 0;
		}
	}

	@media (max-width: 768px) {
		.artifact-panel {
			width: 100vw;
			border-left: none;
		}
	}

	.panel-head {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border);
		flex-shrink: 0;
	}

	.toggle-group {
		display: flex;
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: 2px;
		gap: 2px;
		flex-shrink: 0;
	}

	.toggle-group button {
		border: none;
		background: none;
		color: var(--color-text-dim);
		padding: 5px 7px;
		border-radius: 5px;
		cursor: pointer;
		display: flex;
	}

	.toggle-group button.active {
		background: var(--color-surface-3);
		color: var(--color-text);
	}

	.title-block {
		flex: 1;
		min-width: 0;
	}

	.title-block .t {
		font-size: 13px;
		font-weight: 500;
		color: var(--color-text);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.title-block .s {
		font-size: 11px;
		color: var(--color-text-dim);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.actions {
		display: flex;
		gap: var(--space-xs);
		flex-shrink: 0;
	}

	.actions button,
	.action-link {
		border: 1px solid var(--color-border-strong);
		background: var(--color-surface-2);
		color: var(--color-text-dim);
		width: 28px;
		height: 28px;
		border-radius: var(--radius-sm);
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
	}

	.actions button:hover,
	.action-link:hover {
		color: var(--color-text);
		background: var(--color-surface-3);
	}

	.actions button:disabled {
		opacity: 0.4;
		cursor: default;
	}

	.panel-body {
		flex: 1;
		overflow-y: auto;
		padding: var(--space-xl);
	}

	.panel-loading {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		color: var(--color-text-dim);
		font-size: 13px;
	}

	.raw {
		white-space: pre-wrap;
		word-break: break-word;
		font-family: var(--font-mono);
		font-size: 12.5px;
		line-height: 1.6;
		color: var(--color-text-dim);
		margin: 0;
	}

	.prose {
		font-size: 14px;
		line-height: 1.65;
		color: var(--color-text);
	}

	.prose :global(h1) {
		font-size: 1.2rem;
		margin: 0 0 var(--space-md);
	}

	.prose :global(h2) {
		font-size: 1rem;
		color: var(--color-accent);
		margin: var(--space-lg) 0 var(--space-xs);
	}

	.prose :global(h2:first-child) {
		margin-top: 0;
	}

	.prose :global(p) {
		margin: 0 0 var(--space-sm);
	}

	.prose :global(a) {
		color: var(--color-accent);
	}

	.prose :global(pre) {
		background: var(--color-surface-2);
		border: none;
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow-well);
		padding: var(--space-md);
		overflow-x: auto;
		font-family: var(--font-mono);
		font-size: 13px;
		line-height: 1.5;
		margin: 0 0 var(--space-sm) 0;
	}

	.prose :global(code) {
		font-family: var(--font-mono);
		font-size: 0.9em;
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: 1px 4px;
	}

	.prose :global(pre code) {
		background: transparent;
		padding: 0;
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
