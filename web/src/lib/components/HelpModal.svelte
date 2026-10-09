<script lang="ts">
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { X } from '@lucide/svelte';

	let { onClose }: { onClose: () => void } = $props();

	// The one place new feature names get explained in plain words. Polaris
	// keeps growing astronomy-flavored names (Pulsar, Constellation, Weaver,
	// ...) that mean nothing to someone who just installed it — add a line
	// here whenever a new named concept ships. `means` is the plain-English
	// equivalent; `detail` is one sentence of what you actually do with it.
	const TERMS: { name: string; means: string; detail: string }[] = [
		{
			name: 'Polaris',
			means: 'Your search assistant',
			detail: 'Ask a question and it searches the web, reads pages, and answers with sources.'
		},
		{
			name: 'Atlas',
			means: 'Plain search results',
			detail: 'A classic results page (like a search engine) instead of a written answer.'
		},
		{
			name: 'Fields',
			means: 'Projects',
			detail: 'A folder of conversations that share instructions and reference files.'
		},
		{
			name: 'Constellation',
			means: 'What it knows about you',
			detail: 'An auto-built personal library of short facts, learned from your chats. Optional.'
		},
		{
			name: 'Stars',
			means: 'Individual saved facts',
			detail: 'Each card in your Constellation is one star.'
		},
		{
			name: 'Weaver',
			means: 'The Constellation builder',
			detail: 'A background helper that reads recent chats and writes the stars.'
		},
		{
			name: 'Shooting stars',
			means: 'Weaver’s per-chat passes',
			detail: 'One quick look through a single conversation for things worth remembering.'
		},
		{
			name: 'Pulsar',
			means: 'Routine searches',
			detail: 'A saved question that runs on a schedule and tells you only what’s new.'
		},
		{
			name: 'The Daily',
			means: 'Your morning newspaper',
			detail: 'One edition a day: weather, headlines, a quote, and any topics you add.'
		},
		{
			name: 'Oracle mode',
			means: 'Automatic answer tuning',
			detail: 'Reads each message first and quietly adjusts how it’s answered. You can undo it.'
		},
		{
			name: 'Prism',
			means: 'Comparisons and step lists',
			detail:
				'Lets an answer use a comparison, steps, a timeline, a checklist, a flow chart or tabs when that beats plain text. Set how often in Settings.'
		},
		{
			name: 'Ghost thread',
			means: 'Incognito chat',
			detail: 'Leaves no trace: no memory, hidden from the sidebar, deleted when it ends.'
		},
		{
			name: 'Memory',
			means: 'Things it remembers',
			detail: 'Short facts and preferences carried between conversations. Editable in Settings.'
		}
	];
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel help-panel" role="dialog" aria-modal="true" aria-label="What's what">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>What’s what</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		<p class="intro">
			Polaris is a private assistant that actually searches the web and shows its sources. The
			other names are optional extras built around that.
		</p>

		<dl class="terms">
			{#each TERMS as term (term.name)}
				<div class="term">
					<dt>
						<span class="name">{term.name}</span>
						<span class="equals" aria-hidden="true">=</span>
						<span class="means">{term.means}</span>
					</dt>
					<dd>{term.detail}</dd>
				</div>
			{/each}
		</dl>
	</div>
</div>

<style>
	.help-panel {
		max-height: min(80vh, 640px);
		overflow-y: auto;
	}

	.intro {
		margin: 0 0 var(--space-lg);
		font-size: 13px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.terms {
		margin: 0;
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		overflow: hidden;
	}

	.term {
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.term:last-child {
		border-bottom: none;
	}

	dt {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-sm);
		font-size: 14px;
	}

	.name {
		font-family: var(--font-wordmark);
		font-size: 1.05em;
		letter-spacing: 0.02em;
		color: var(--color-accent);
	}

	.equals {
		color: var(--color-text-dim);
	}

	.means {
		font-weight: 600;
	}

	dd {
		margin: 2px 0 0;
		font-size: 12.5px;
		line-height: 1.45;
		color: var(--color-text-dim);
	}
</style>
