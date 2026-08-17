<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { searchState } from '$lib/search.svelte';
	import { appState } from '$lib/state.svelte';
	import ModeToggle from '$lib/components/ModeToggle.svelte';
	import { Search as SearchIcon, SlidersHorizontal, Globe, X, Sparkles, Telescope } from '@lucide/svelte';
	import type { SearchResult, RankState } from '$lib/types';

	// Tracks the query the address bar has already been synced to (either
	// by us, via submitSearch's goto below, or by whatever put it there
	// before this page loaded — a sidebar click, a pasted/reloaded URL).
	// The $effect only acts on a query that's NEW relative to this, so a
	// submit's own goto() doesn't loop back around and re-trigger itself.
	let syncedQuery = $state('');

	$effect(() => {
		const q = page.url.searchParams.get('q') ?? '';
		if (q && q !== syncedQuery) {
			syncedQuery = q;
			searchState.query = q;
			runQuery(q);
		}
	});

	// Shared by the $effect above (sidebar clicks, pasted/reloaded URLs)
	// and submitSearch below — a trailing "?" triggers Quick Answer (per
	// the plan's Kagi-matching omnibox convention) in parallel with the
	// regular results search, and is stripped before either request so
	// the literal "?" character never becomes part of the query itself.
	function runQuery(q: string) {
		const wantsQuickAnswer = q.endsWith('?');
		const bare = wantsQuickAnswer ? q.slice(0, -1).trim() : q;
		if (!bare) return;

		void searchState.search(bare);
		if (wantsQuickAnswer) {
			void searchState.askQuickAnswer(bare);
		} else {
			// Also bump quickAnswerSeq (not just clear the visible fields) so
			// a still-in-flight askQuickAnswer() from a *previous* "?" query
			// can't flip quickAnswerLoading back to true after this plain
			// query already cleared it — that reappeared as a stale
			// "Thinking…" panel for a query that never asked for one.
			searchState.discardQuickAnswer();
		}
	}

	let openPopoverFor = $state<string | null>(null);
	// Optimistic: reflects a click immediately, then persists via
	// setDomainRanking below — keyed by URL (not domain) purely so
	// rankStateOf can look it up alongside a SearchResult by the same key
	// it's already indexed by; the actual persisted state is per-domain.
	// Reverted if the write fails, so the UI never quietly disagrees with
	// what's actually on disk.
	let localRankOverrides = $state<Record<string, RankState>>({});

	function rankStateOf(r: SearchResult): RankState {
		return localRankOverrides[r.url] ?? (r.rank_state as RankState) ?? 'default';
	}

	function domainOf(url: string): string {
		try {
			return new URL(url).hostname.replace(/^www\./, '');
		} catch {
			return url;
		}
	}

	// Deterministic per-domain color for the favicon monogram — same spirit
	// as the mockup's hand-picked colors, but generated so every domain
	// gets one instead of just the handful in the sample data.
	function faviconHue(domain: string): number {
		let h = 0;
		for (let i = 0; i < domain.length; i++) h = (h * 31 + domain.charCodeAt(i)) % 360;
		return h;
	}

	function togglePopover(url: string) {
		openPopoverFor = openPopoverFor === url ? null : url;
	}

	async function setRank(r: SearchResult, domain: string, state: RankState) {
		const previous = rankStateOf(r);
		localRankOverrides = { ...localRankOverrides, [r.url]: state };

		const ok = await searchState.setDomainRanking(domain, state);
		if (!ok) {
			// Revert — don't leave the UI showing a state that isn't actually
			// persisted, since the whole point of this control is that it
			// applies everywhere search happens, not just visually here.
			localRankOverrides = { ...localRankOverrides, [r.url]: previous };
			appState.showToast("Couldn't save that ranking — try again");
		}
	}

	function submitSearch(e: Event) {
		e.preventDefault();
		const q = searchState.query.trim();
		if (!q) return;
		syncedQuery = q; // see the $effect above — prevents this goto from looping back
		runQuery(q);
		void goto(`/search?q=${encodeURIComponent(q)}`, { replaceState: true, keepFocus: true, noScroll: true });
	}

	onMount(() => {
		function closeOnOutsideClick(e: MouseEvent) {
			if (!(e.target as HTMLElement)?.closest('.result-actions, .rank-popover')) {
				openPopoverFor = null;
			}
		}
		document.addEventListener('click', closeOnOutsideClick);
		return () => document.removeEventListener('click', closeOnOutsideClick);
	});

	const rankLabels: Record<RankState, string> = {
		block: 'Block',
		lower: 'Lower',
		default: 'Default',
		raise: 'Raise',
		pin: 'Pin',
		'': 'Default'
	};
</script>

<svelte:head>
	<title>Atlas{searchState.lastQuery ? ` — ${searchState.lastQuery}` : ''}</title>
</svelte:head>

<div class="atlas-page">
	<header class="top">
		<div class="top-inner">
			<div class="brand-row">
				<div class="wordmark">
					<span class="mark"></span>
					<span class="name">Atlas<span class="sub">self-hosted, over SearXNG</span></span>
				</div>
				<div class="header-actions">
					<ModeToggle mode="search" />
				</div>
			</div>

			<form class="omnibox" onsubmit={submitSearch}>
				<SearchIcon size={16} class="icon-search" />
				<input
					type="text"
					bind:value={searchState.query}
					placeholder="Search the web"
					spellcheck="false"
				/>
				<span class="hint">? for answer</span>
			</form>

			{#if searchState.lastQuery}
				<div class="meta-line">
					{searchState.results.length} result{searchState.results.length === 1 ? '' : 's'} for
					<b>{searchState.lastQuery}</b>
				</div>
			{/if}
		</div>
	</header>

	<main>
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

		{#if searchState.loading}
			<p class="status-line">Searching…</p>
		{:else if searchState.error}
			<p class="status-line error">{searchState.error}</p>
		{:else if searchState.lastQuery && searchState.results.length === 0}
			<p class="status-line">No results for "{searchState.lastQuery}".</p>
		{/if}

		{#if searchState.results.length > 0}
			<h2 class="results-heading">Web results</h2>
			<ol class="results">
				{#each searchState.results as r (r.url)}
					{@const domain = domainOf(r.url)}
					{@const hue = faviconHue(domain)}
					{@const state = rankStateOf(r)}
					<li class="result">
						<div class="result-top">
							<span class="favicon" style="background: hsl({hue} 45% 45%)">
								{domain.charAt(0).toUpperCase()}
							</span>
							<span class="result-url">{domain}</span>
						</div>
						<div class="result-title-row">
							<h3><a href={r.url} target="_blank" rel="noreferrer">{r.title}</a></h3>
							<div class="result-actions">
								<button
									class="tune-btn"
									class:adjusted={state !== 'default'}
									type="button"
									aria-label={`Adjust ranking for ${domain}`}
									onclick={(e) => {
										e.stopPropagation();
										togglePopover(r.url);
									}}
								>
									<SlidersHorizontal size={15} />
								</button>
								{#if openPopoverFor === r.url}
									<div class="rank-popover" role="dialog" aria-label="Domain ranking">
										<div class="popover-head">
											<span class="popover-domain"><Globe size={13} />{domain}</span>
											<button
												class="popover-close"
												type="button"
												aria-label="Close"
												onclick={() => (openPopoverFor = null)}
											>
												<X size={14} />
											</button>
										</div>
										<div class="rank-group">
											{#each ['block', 'lower', 'default', 'raise', 'pin'] as opt (opt)}
												<button
													type="button"
													class="rank-option"
													class:selected={state === opt}
													data-state={opt}
													onclick={() => setRank(r, domain, opt as RankState)}
												>
													{rankLabels[opt as RankState]}
												</button>
											{/each}
										</div>
										<p class="popover-help">
											{#if state === 'block'}
												This domain will be <b>excluded</b> from results.
											{:else}
												This domain ranks <b>{rankLabels[state].toLowerCase()}</b>.
											{/if}
										</p>
										{#if r.engines && r.engines.length > 0}
											<div class="popover-section">
												<p class="popover-label">Found via</p>
												<div class="engine-chips">
													{#each r.engines as engine (engine)}
														<span class="engine-chip">{engine}</span>
													{/each}
												</div>
											</div>
										{/if}
									</div>
								{/if}
							</div>
						</div>
						<p class="snippet">{r.content}</p>
					</li>
				{/each}
			</ol>
		{/if}
	</main>
</div>

<style>
	/* Dark is the base rule and light is the attribute override — matching
	   app.css's own convention exactly (Polaris defaults to dark at :root,
	   [data-theme='light'] overrides it), not the reverse. Atlas used to
	   default the other way when it had its own independent toggle; now
	   that it follows the settings panel's global theme instead, disagreeing
	   about which state is the "no attribute yet" default would show the
	   wrong palette for a moment before settings.load() resolves. */
	.atlas-page {
		--paper: oklch(21% 0.014 75);
		--paper-raised: oklch(25% 0.015 75);
		--paper-sunken: oklch(18% 0.013 75);
		--ink: oklch(93% 0.008 75);
		--ink-muted: oklch(72% 0.012 75);
		--ink-faint: oklch(52% 0.012 75);
		--line: oklch(32% 0.014 75);
		--line-strong: oklch(40% 0.016 75);
		--accent: oklch(74% 0.12 48);
		--accent-soft: oklch(30% 0.05 48);
		--accent-soft-line: oklch(42% 0.08 48);
		--rank-block: oklch(68% 0.15 25);
		--rank-block-soft: oklch(30% 0.06 25);
		--rank-lower: oklch(68% 0.014 75);
		--rank-lower-soft: oklch(28% 0.014 75);
		--rank-default: oklch(72% 0.012 75);
		--rank-default-soft: oklch(28% 0.014 75);
		--rank-raise: oklch(72% 0.09 155);
		--rank-raise-soft: oklch(28% 0.045 155);
		--rank-pin: oklch(76% 0.1 85);
		--rank-pin-soft: oklch(30% 0.05 85);
		--shadow-ambient: oklch(0% 0 0 / 0.28);

		background: var(--paper);
		color: var(--ink);
		font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;

		/* +layout.svelte's .main wraps every route with overflow: hidden,
		   expecting each page to own its own scroll region (see ChatView's
		   .messages) rather than relying on document-level scrolling —
		   without height: 100% + overflow-y: auto here, tall result lists
		   just clip instead of scrolling. */
		height: 100%;
		overflow-y: auto;
	}

	/* Follows the settings panel's global theme (document.documentElement's
	   data-theme, set by settings.svelte.ts) rather than owning a separate
	   toggle — Atlas's palette is still its own (--paper/--ink, distinct
	   from Polaris's --color-* tokens), just switched by the one theme
	   control the app already has instead of a second, competing one. */
	:global([data-theme='light']) .atlas-page {
		--paper: oklch(97.3% 0.011 75);
		--paper-raised: oklch(99.2% 0.006 75);
		--paper-sunken: oklch(95% 0.014 75);
		--ink: oklch(23% 0.018 75);
		--ink-muted: oklch(46% 0.016 75);
		--ink-faint: oklch(62% 0.012 75);
		--line: oklch(88.5% 0.013 75);
		--line-strong: oklch(80% 0.016 75);
		--accent: oklch(53% 0.135 42);
		--accent-soft: oklch(93% 0.035 42);
		--accent-soft-line: oklch(83% 0.06 42);
		--rank-block: oklch(54% 0.16 25);
		--rank-block-soft: oklch(93% 0.035 25);
		--rank-lower: oklch(58% 0.02 75);
		--rank-lower-soft: oklch(91% 0.012 75);
		--rank-default: oklch(46% 0.016 75);
		--rank-default-soft: oklch(95% 0.014 75);
		--rank-raise: oklch(52% 0.1 155);
		--rank-raise-soft: oklch(92% 0.035 155);
		--rank-pin: oklch(58% 0.12 85);
		--rank-pin-soft: oklch(92% 0.045 85);
		--shadow-ambient: oklch(23% 0.018 75 / 0.05);
	}

	.top {
		position: sticky;
		top: 0;
		z-index: 10;
		border-bottom: 1px solid var(--line);
		background: var(--paper);
	}

	.top-inner {
		max-width: 780px;
		margin: 0 auto;
		padding: 14px 24px 16px;
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	.brand-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.wordmark {
		display: flex;
		align-items: baseline;
		gap: 9px;
	}

	.wordmark .mark {
		width: 20px;
		height: 20px;
		border-radius: 5px;
		background: var(--ink);
		flex: none;
	}

	.wordmark .name {
		font-family: ui-serif, Georgia, serif;
		font-size: 17px;
		font-weight: 600;
	}

	.wordmark .name .sub {
		font-family: inherit;
		font-weight: 400;
		font-size: 12px;
		color: var(--ink-faint);
		margin-left: 6px;
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: 10px;
	}

	.omnibox {
		display: flex;
		align-items: center;
		gap: 10px;
		background: var(--paper-raised);
		border: 1px solid var(--line-strong);
		border-radius: 10px;
		padding: 11px 14px;
	}

	.omnibox:focus-within {
		border-color: var(--accent);
		box-shadow: 0 0 0 3px var(--accent-soft);
	}

	.omnibox :global(.icon-search) {
		flex: none;
		color: var(--ink-faint);
	}

	.omnibox input {
		flex: 1;
		border: none;
		outline: none;
		background: transparent;
		font-size: 16px;
		color: var(--ink);
		min-width: 0;
	}

	.omnibox .hint {
		flex: none;
		font-size: 11px;
		color: var(--ink-faint);
		background: var(--paper-sunken);
		border: 1px solid var(--line);
		border-radius: 5px;
		padding: 3px 6px;
		white-space: nowrap;
	}

	.meta-line {
		font-size: 12px;
		color: var(--ink-faint);
		padding-left: 2px;
	}

	.meta-line b {
		color: var(--ink-muted);
		font-weight: 600;
	}

	main {
		max-width: 780px;
		margin: 0 auto;
		padding: 28px 24px 80px;
	}

	.status-line {
		font-size: 14px;
		color: var(--ink-faint);
		padding: 8px 2px;
	}

	.status-line.error {
		color: var(--rank-block);
	}

	.quick-answer {
		background: var(--paper-raised);
		border: 1px solid var(--line);
		border-radius: 10px;
		padding: 18px 20px 16px;
		margin-bottom: 28px;
	}

	.qa-label {
		display: flex;
		align-items: center;
		gap: 7px;
		font-size: 11.5px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--accent);
		margin-bottom: 12px;
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
		margin: 0 0 14px;
		max-width: 68ch;
		white-space: pre-wrap;
	}

	.qa-sources {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		padding-top: 12px;
		border-top: 1px solid var(--line);
	}

	.qa-source {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--ink-muted);
		background: var(--paper-sunken);
		border: 1px solid var(--line);
		border-radius: 999px;
		padding: 5px 10px 5px 7px;
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
		border-radius: 999px;
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
		gap: 6px;
		margin-top: 14px;
		font-size: 12.5px;
		font-weight: 600;
		color: var(--accent);
		text-decoration: none;
	}

	.qa-continue:hover {
		text-decoration: underline;
	}

	.results-heading {
		font-size: 11.5px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--ink-faint);
		margin: 0 0 6px;
	}

	.results {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	.result {
		padding: 18px 2px;
		border-bottom: 1px solid var(--line);
	}

	.result:first-of-type {
		padding-top: 8px;
	}

	.result-top {
		display: flex;
		align-items: center;
		gap: 9px;
		margin-bottom: 6px;
	}

	.favicon {
		width: 20px;
		height: 20px;
		border-radius: 5px;
		flex: none;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 10.5px;
		font-weight: 700;
		color: white;
	}

	.result-url {
		font-size: 12.5px;
		color: var(--ink-muted);
	}

	.result-title-row {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 10px;
		margin-bottom: 6px;
		position: relative;
	}

	.result h3 {
		margin: 0;
		font-family: ui-serif, Georgia, serif;
		font-size: 18px;
		font-weight: 600;
		line-height: 1.35;
	}

	.result h3 a {
		color: inherit;
		text-decoration: none;
	}

	.result h3 a:hover {
		color: var(--accent);
		text-decoration: underline;
	}

	.result-actions {
		position: relative;
		flex: none;
	}

	.tune-btn {
		appearance: none;
		border: 1px solid transparent;
		background: transparent;
		border-radius: 7px;
		width: 28px;
		height: 28px;
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--ink-faint);
		cursor: pointer;
	}

	.tune-btn:hover {
		background: var(--paper-sunken);
		color: var(--ink-muted);
	}

	.tune-btn.adjusted {
		color: var(--accent);
	}

	.rank-popover {
		position: absolute;
		top: 34px;
		right: 0;
		width: 280px;
		background: var(--paper-raised);
		border: 1px solid var(--line);
		border-radius: 13px;
		box-shadow:
			0 18px 40px var(--shadow-ambient),
			0 3px 10px var(--shadow-ambient);
		z-index: 30;
		padding: 16px 16px 17px;
	}

	.popover-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 14px;
	}

	.popover-domain {
		display: flex;
		align-items: center;
		gap: 7px;
		font-size: 14px;
		font-weight: 600;
		color: var(--ink);
	}

	.popover-domain :global(svg) {
		color: var(--ink-faint);
	}

	.popover-close {
		appearance: none;
		border: none;
		background: transparent;
		color: var(--ink-faint);
		cursor: pointer;
		width: 24px;
		height: 24px;
		border-radius: 6px;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.popover-close:hover {
		background: var(--paper-sunken);
	}

	.rank-group {
		display: grid;
		grid-template-columns: repeat(5, 1fr);
		gap: 5px;
		margin-bottom: 12px;
	}

	.rank-option {
		appearance: none;
		font-size: 10px;
		font-weight: 600;
		padding: 8px 2px;
		border-radius: 6px;
		border: 1px solid var(--line);
		background: var(--paper);
		color: var(--ink-muted);
		cursor: pointer;
		text-align: center;
	}

	.rank-option[data-state='block'].selected {
		background: var(--rank-block-soft);
		color: var(--rank-block);
		border-color: var(--line-strong);
	}

	.rank-option[data-state='lower'].selected {
		background: var(--rank-lower-soft);
		color: var(--ink);
		border-color: var(--line-strong);
	}

	.rank-option[data-state='default'].selected {
		background: var(--rank-default-soft);
		color: var(--ink);
		border-color: var(--line-strong);
	}

	.rank-option[data-state='raise'].selected {
		background: var(--rank-raise-soft);
		color: var(--rank-raise);
		border-color: var(--line-strong);
	}

	.rank-option[data-state='pin'].selected {
		background: var(--rank-pin-soft);
		color: var(--rank-pin);
		border-color: var(--line-strong);
	}

	.popover-help {
		font-size: 12px;
		line-height: 1.5;
		color: var(--ink-faint);
		margin: 0 0 10px;
	}

	.popover-help b {
		color: var(--ink-muted);
		font-weight: 600;
	}

	.popover-section {
		padding-top: 10px;
		border-top: 1px solid var(--line);
	}

	.popover-label {
		font-size: 10.5px;
		font-weight: 600;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: var(--ink-faint);
		margin: 0 0 8px;
	}

	.engine-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.engine-chip {
		font-size: 11.5px;
		font-weight: 600;
		color: var(--ink-muted);
		background: var(--paper-sunken);
		border: 1px solid var(--line);
		border-radius: 999px;
		padding: 4px 10px;
	}

	.snippet {
		margin: 0;
		font-size: 13.5px;
		line-height: 1.55;
		color: var(--ink-muted);
		max-width: 68ch;
	}

	@media (max-width: 640px) {
		.top-inner {
			padding: 12px 16px 14px;
		}
		.wordmark .name .sub {
			display: none;
		}
		.omnibox .hint {
			display: none;
		}
		main {
			padding: 22px 16px 64px;
		}
		.result h3 {
			font-size: 16.5px;
		}
		.rank-popover {
			position: fixed;
			left: 16px;
			right: 16px;
			top: auto;
			bottom: 16px;
			width: auto;
		}
	}
</style>
