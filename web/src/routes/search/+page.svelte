<script lang="ts">
	import { onMount } from 'svelte';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { searchState } from '$lib/search.svelte';
	import { appState } from '$lib/state.svelte';
	import ModeToggle from '$lib/components/ModeToggle.svelte';
	import AtlasOmnibox from '$lib/components/AtlasOmnibox.svelte';
	import AtlasWelcome from '$lib/components/AtlasWelcome.svelte';
	import QuickAnswerCard from '$lib/components/QuickAnswerCard.svelte';
	import SearchResultItem from '$lib/components/SearchResultItem.svelte';
	import PageScrubber from '$lib/components/PageScrubber.svelte';
	import { PanelLeft } from '@lucide/svelte';
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
			// A sidebar click (see Sidebar.svelte's openSearch) tags its own
			// navigation with &from=history so reopening a past search
			// doesn't bump it back to the top of that same list — same
			// "viewing isn't activity" rule ListThreads already follows for
			// chat threads (opening one doesn't touch its position either).
			// There's no way to "follow up" on a one-shot search the way a
			// thread can be continued, so revisiting one should never move it.
			const fromHistory = page.url.searchParams.get('from') === 'history';
			// &page=N (see goToPage) means a reload or a pasted/shared link
			// lands back on the page it was on instead of always resetting
			// to 1 — the whole point of putting it in the URL at all.
			const urlPage = Number(page.url.searchParams.get('page')) || 1;
			runQuery(q, { record: !fromHistory, page: urlPage });
		}
	});

	// Shared by the $effect above (sidebar clicks, pasted/reloaded URLs)
	// and submitSearch below — a trailing "?" triggers Quick Answer (per
	// the plan's Kagi-matching omnibox convention) in parallel with the
	// regular results search, and is stripped before either request so
	// the literal "?" character never becomes part of the query itself.
	function runQuery(q: string, opts: { record: boolean; page?: number } = { record: true }) {
		const wantsQuickAnswer = q.endsWith('?');
		const bare = wantsQuickAnswer ? q.slice(0, -1).trim() : q;
		if (!bare) return;

		void searchState.search(bare, opts);
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

	function togglePopover(url: string) {
		openPopoverFor = openPopoverFor === url ? null : url;
	}

	async function setRank(r: SearchResult, domain: string, state: RankState) {
		const previous = rankStateOf(r);
		localRankOverrides = { ...localRankOverrides, [r.url]: state };

		const ok = await searchState.setDomainRanking(domain, state);
		// Only revert if nothing newer (a second click on this same result
		// before this request resolved) has already moved the override past
		// what this call set — otherwise a slow, now-stale failure would
		// stomp a later, possibly-already-succeeded choice.
		if (!ok && localRankOverrides[r.url] === state) {
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

	let atlasPageEl = $state<HTMLDivElement | undefined>(undefined);

	// Turning the page isn't a new search (record: false — same reasoning
	// as reopening a sidebar history entry) and it doesn't touch Quick
	// Answer, which is tied to the original query, not to which page of
	// web results is showing. lastQuery, not searchState.query, since the
	// omnibox's live value could differ from what's actually on screen if
	// the user's since typed something without submitting it.
	function goToPage(n: number) {
		if (n < 1 || n === searchState.page || searchState.loading) return;
		void searchState.search(searchState.lastQuery, { record: false, page: n });
		atlasPageEl?.scrollTo({ top: 0, behavior: 'smooth' });

		// &page=N in the address bar so a reload, a shared link, or just
		// glancing at the URL bar (instead of scrolling all the way back
		// down to the picker to check) all reflect which page is actually
		// showing. replaceState, not a real navigation — page 4 isn't a
		// distinct history entry the back button should stop on, same
		// reasoning as submitSearch's own goto() below.
		const params = new URLSearchParams(page.url.searchParams);
		if (n > 1) params.set('page', String(n));
		else params.delete('page');
		void goto(`/search?${params}`, { replaceState: true, keepFocus: true, noScroll: true });
	}

	// Google stretches the "o"s in its own logo into clickable page
	// links — this is Atlas's version, shown at the bottom of the results
	// (see .page-wordmark). Deliberately not grown one letter at a time
	// as you actually reach each page: the moment page 1 comes back with
	// more results waiting (hasMore), it shows the full run of 10 a's
	// right away, optimistically, the same way Google's own footer
	// doesn't wait to confirm page 10 exists before drawing it. Once a
	// jump actually lands on an empty page, though, searchState.deadEndPage
	// remembers it and this stops re-offering that page (and anything past
	// it) for the rest of this query's session — the "optimistic 10 up
	// front" trade only applies to *undiscovered* territory, not to a spot
	// already known to be a dead end.
	let pageLetterCount = $derived(
		searchState.deadEndPage !== null
			? Math.max(searchState.deadEndPage - 1, 1)
			: searchState.hasMore || searchState.page > 1
				? 10
				: 1
	);

	onMount(() => {
		function closeOnOutsideClick(e: MouseEvent) {
			if (!(e.target as HTMLElement)?.closest('.result-actions, .rank-popover')) {
				openPopoverFor = null;
			}
		}
		document.addEventListener('click', closeOnOutsideClick);
		return () => document.removeEventListener('click', closeOnOutsideClick);
	});

	// True before any search has actually run — drives which layout the
	// omnibox lives in (see the markup below): centered and prominent here,
	// or compact and pinned in the header once there's something to show
	// underneath it. Same shape as ChatView.svelte's `appState.turns.length
	// === 0` check for its own welcome state.
	let isStartScreen = $derived(
		!searchState.lastQuery && !searchState.loading && !searchState.error
	);
</script>

<svelte:head>
	<title>Atlas{searchState.lastQuery ? ` — ${searchState.lastQuery}` : ''}</title>
</svelte:head>

{#snippet omniboxForm()}
	<AtlasOmnibox bind:value={searchState.query} onSubmit={submitSearch} />
{/snippet}

<div class="atlas-page" bind:this={atlasPageEl}>
	<header class="top">
		<div class="top-inner">
			<div class="brand-row">
				<div class="wordmark">
					{#if !appState.sidebarOpen}
						<!-- The sidebar shrinks to width: 0 when collapsed (see
						     Sidebar.svelte) and takes its own collapse button
						     with it — without a way to reopen it from here, a
						     collapsed sidebar was a dead end on this page.
						     Same reopen affordance ChatView.svelte's header
						     already has for the assistant side. -->
						<button
							class="sidebar-toggle"
							type="button"
							onclick={() => appState.toggleSidebar()}
							title="Open sidebar"
							aria-label="Open sidebar"
						>
							<PanelLeft size={17} />
						</button>
					{/if}
					<img class="mark" src="/atlas-touch-icon.png" alt="" width="20" height="20" />
					<span class="name">Atlas<span class="sub">Search the web</span></span>
				</div>
				<div class="header-actions">
					<ModeToggle mode="search" />
				</div>
			</div>

			<!-- The omnibox itself only lives here once there's something
			     for it to sit above — before a first search it's centered
			     in the empty canvas below instead (see .welcome), same
			     shape as ChatView's composer floating for its own welcome
			     state rather than pinned at the bottom from the start. -->
			{#if !isStartScreen}
				<div in:fly={{ y: -14, duration: 320, easing: quintOut }}>
					{@render omniboxForm()}
				</div>
			{/if}

			{#if searchState.lastQuery}
				<div class="meta-line">
					{searchState.results.length} result{searchState.results.length === 1 ? '' : 's'} for
					<b>{searchState.lastQuery}</b>
				</div>
			{/if}
		</div>
	</header>

	<main>
		{#if isStartScreen}
			<!-- Start screen: the omnibox lives centered here, right below
			     the branding, rather than pinned in the header — unified
			     with ChatView's own empty state, which floats its composer
			     the same way before the first message. Once a search runs,
			     this whole block gives way to the compact header version
			     above (see the fly transition on it) instead of staying put. -->
			<AtlasWelcome>
				{@render omniboxForm()}
			</AtlasWelcome>
		{:else}
			<QuickAnswerCard />
		{/if}

		{#if searchState.loading}
			<p class="status-line">Searching…</p>
		{:else if searchState.error}
			<p class="status-line error">{searchState.error}</p>
		{:else if searchState.lastQuery && searchState.results.length === 0 && searchState.page > 1}
			<!-- Distinct from the page-1-empty case below: this is "you paged
			     past the end", not "this query has no results at all" — the
			     bottom page-picker offering all 10 letters up front (see
			     pageLetterCount) means landing here is expected to happen
			     sometimes, not a failure, so it gets its own reassuring
			     copy and a one-click way back instead of a bare dead end. -->
			<p class="status-line">
				That's the end of the results — page {searchState.page - 1} was the last one with anything on
				it.
				<button type="button" class="status-line-link" onclick={() => goToPage(searchState.page - 1)}>
					Back to page {searchState.page - 1}
				</button>
			</p>
		{:else if searchState.lastQuery && searchState.results.length === 0}
			<p class="status-line">No results for "{searchState.lastQuery}".</p>
		{/if}

		{#if searchState.results.length > 0}
			<h2 class="results-heading">Web results</h2>
			<ol class="results">
				{#each searchState.results as r (r.url)}
					<SearchResultItem
						result={r}
						state={rankStateOf(r)}
						popoverOpen={openPopoverFor === r.url}
						onTogglePopover={() => togglePopover(r.url)}
						onClosePopover={() => (openPopoverFor = null)}
						onSetRank={setRank}
					/>
				{/each}
			</ol>

			<!-- A real page-scrubber, not a "load more" — each click is its
			     own independent SearXNG page fetch (see search.svelte.ts's
			     page option), not appended to what's already showing.
			     hasMore starts as gateway/search.go's same-page heuristic
			     (SearXNG never reports a total count) but search.svelte.ts
			     prefetches the next page in the background and corrects it
			     to the real answer once that lands — usually well before
			     anyone actually reads this far and clicks, so "Next"
			     disables itself instead of leading into a dead end. -->
			{#if searchState.page > 1 || searchState.hasMore}
				<PageScrubber page={searchState.page} hasMore={searchState.hasMore} letterCount={pageLetterCount} onGoTo={goToPage} />
			{/if}
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
		z-index: var(--z-sticky);
		border-bottom: 1px solid var(--line);
		background: var(--paper);
	}

	/* Full-bleed, not a centered fixed-width column — same choice
	   ChatView.svelte's .header/.timeline-scroll make (no max-width at
	   all there). A capped, centered column here meant collapsing the
	   sidebar just grew the margins on both sides instead of giving this
	   page any more room, unlike the assistant side. */
	.top-inner {
		padding: var(--space-lg) var(--space-xl) var(--space-lg);
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	.brand-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.wordmark {
		display: flex;
		align-items: baseline;
		gap: var(--space-sm);
	}

	/* Atlas's own palette (--ink/--paper), not the global .icon-btn's
	   --color-* tokens — same reasoning as .tune-btn just below. */
	.sidebar-toggle {
		appearance: none;
		border: none;
		background: transparent;
		border-radius: var(--radius-sm);
		width: 28px;
		height: 28px;
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--ink-faint);
		cursor: pointer;
		flex: none;
		align-self: center;
	}

	.sidebar-toggle:hover {
		background: var(--paper-sunken);
		color: var(--ink-muted);
	}

	.wordmark .mark {
		width: 20px;
		height: 20px;
		border-radius: var(--radius-sm);
		flex: none;
		box-shadow: var(--shadow-ambient) 0 1px 3px;
	}

	.wordmark .name {
		font-family: var(--font-wordmark);
		font-size: 18px;
		font-weight: 400;
		letter-spacing: 0.03em;
	}

	.wordmark .name .sub {
		/* Not inherit — Asimovian is a display face, too heavy-handed to
		   read well this small. Falls back to .atlas-page's own base
		   sans stack instead, same as the omnibox's plain UI text. */
		font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
		font-weight: 400;
		font-size: 12px;
		letter-spacing: normal;
		color: var(--ink-faint);
		margin-left: var(--space-sm);
	}

	.header-actions {
		display: flex;
		align-items: center;
		gap: var(--space-md);
	}

	.meta-line {
		font-size: 12px;
		color: var(--ink-faint);
		padding-left: var(--space-xs);
	}

	.meta-line b {
		color: var(--ink-muted);
		font-weight: 600;
	}

	main {
		padding: var(--space-2xl) var(--space-xl) var(--space-6xl);
	}

	.status-line {
		font-size: 14px;
		color: var(--ink-faint);
		padding: var(--space-sm) var(--space-xs);
	}

	.status-line.error {
		color: var(--rank-block);
	}

	.status-line-link {
		appearance: none;
		border: none;
		background: transparent;
		padding: 0;
		margin-left: var(--space-xs);
		font: inherit;
		font-weight: 600;
		color: var(--accent);
		cursor: pointer;
		text-decoration: underline;
	}

	.results-heading {
		font-size: 11.5px;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--ink-faint);
		margin: 0 0 var(--space-sm);
	}

	.results {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	@media (max-width: 640px) {
		.top-inner {
			padding: var(--space-md) var(--space-lg) var(--space-lg);
		}
		.wordmark .name .sub {
			display: none;
		}
		main {
			padding: var(--space-xl) var(--space-lg) var(--space-5xl);
		}
	}
</style>
