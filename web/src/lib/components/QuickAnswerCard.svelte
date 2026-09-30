<script lang="ts">
	import { Sparkles, Telescope } from '@lucide/svelte';
	import { searchState } from '$lib/search.svelte';
	import { domainOf } from '$lib/searchHelpers';

	// Atlas's Quick Answer panel: loading, error, or the streamed answer with its
	// source chips and a "Continue in Assistant" link. Reads the shared
	// searchState directly, since that's where the stream lands.
</script>

{#if searchState.quickAnswerLoading}
			<section class="quick-answer">
				<div class="qa-label"><Sparkles size={13} />Quick Answer</div>
				<p class="qa-loading">Thinking…</p>
			</section>
		{:else if searchState.quickAnswerError}
			<section class="quick-answer">
				<div class="qa-label"><Sparkles size={13} />Quick Answer</div>
				<p class="qa-loading">{searchState.quickAnswerError}</p>
			</section>
		{:else if searchState.quickAnswer}
			<section class="quick-answer">
				<div class="qa-label"><Sparkles size={13} />Quick Answer</div>
				<p class="qa-text">{searchState.quickAnswer.text}</p>
				{#if searchState.quickAnswer.citations.length > 0}
					<div class="qa-sources">
						{#each searchState.quickAnswer.citations as c, i (c.url)}
							<a class="qa-source" href={c.url} target="_blank" rel="noreferrer">
								<span class="qa-source-n">{i + 1}</span>
								{c.site_name || domainOf(c.url)}
							</a>
						{/each}
					</div>
				{/if}
				{#if searchState.quickAnswer.threadId}
					<a class="qa-continue" href="/t/{searchState.quickAnswer.threadId}">
						<Telescope size={13} />
						Continue in Assistant
					</a>
				{/if}
			</section>
{/if}

<style>
	.quick-answer {
		background: var(--paper-raised);
		border: 1px solid var(--line);
		border-radius: var(--radius-md);
		padding: var(--space-lg) var(--space-xl) var(--space-lg);
		margin-bottom: var(--space-2xl);
	}

	.qa-label {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 11.5px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--accent);
		margin-bottom: var(--space-md);
	}

	.qa-loading {
		margin: 0;
		font-size: 14px;
		color: var(--ink-faint);
	}

	.qa-text {
		font-family: ui-serif, Georgia, serif;
		font-size: 16px;
		line-height: 1.6;
		color: var(--ink);
		margin: 0 0 var(--space-lg);
		max-width: 68ch;
		white-space: pre-wrap;
	}

	.qa-sources {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		padding-top: var(--space-md);
		border-top: 1px solid var(--line);
	}

	.qa-source {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: 12px;
		color: var(--ink-muted);
		background: var(--paper-sunken);
		border: 1px solid var(--line);
		border-radius: var(--radius-full);
		padding: var(--space-xs) var(--space-md) var(--space-xs) var(--space-sm);
		text-decoration: none;
	}

	.qa-source:hover {
		border-color: var(--line-strong);
		color: var(--ink);
	}

	.qa-source-n {
		font-size: 10px;
		font-weight: 700;
		color: var(--accent);
		background: var(--accent-soft);
		border-radius: var(--radius-full);
		width: 15px;
		height: 15px;
		display: flex;
		align-items: center;
		justify-content: center;
		flex: none;
	}

	.qa-continue {
		display: inline-flex;
		align-items: center;
		gap: var(--space-sm);
		margin-top: var(--space-lg);
		font-size: 12.5px;
		font-weight: 600;
		color: var(--accent);
		text-decoration: none;
	}

	.qa-continue:hover {
		text-decoration: underline;
	}
</style>
