<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { searchState } from '$lib/search.svelte';
	import { pulsarState } from '$lib/pulsar.svelte';
	import PulsarUnreadBadge from './PulsarUnreadBadge.svelte';
	import { Plus, PanelLeftClose, Settings, Star, Search, X, Orbit } from '@lucide/svelte';
	import { edgeSwipeSidebar } from '$lib/actions/edgeSwipeSidebar';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import type { Thread, SearchHistoryEntry, MessageSearchResult } from '$lib/types';

	// The sidebar SHELL (this component, its layout/collapse/mobile-overlay
	// behavior) is shared between Atlas and the chat assistant — see
	// docs/plans/local-search-frontend.md's "Shared chrome" section — but
	// its CONTENT is mode-scoped: Atlas shows past searches, Assistant
	// shows chat threads. Two separate histories, not a merged one.
	let isAtlas = $derived(page.url.pathname.startsWith('/search'));

	// Pinned to the top, same shape as the Claude app — favorited threads
	// move out of the plain recency list into their own section instead of
	// just being flagged in place, so favoriting something actually
	// changes where it lives, not just how it looks.
	let favorites = $derived(appState.threads.filter((t) => t.favorite));
	let recents = $derived(appState.threads.filter((t) => !t.favorite));

	let searchFavorites = $derived(searchState.history.filter((h) => h.favorite));
	let searchRecents = $derived(searchState.history.filter((h) => !h.favorite));

	// Loaded once regardless of which mode is active on mount — same as
	// appState.loadThreads() in +layout.svelte — so switching into Atlas
	// for the first time doesn't show a flash of "no searches yet" while a
	// fetch that could've started earlier is still in flight. search()
	// itself refreshes this again after every completed search.
	onMount(() => {
		void searchState.loadHistory();
		// Loaded regardless of mode/route (not just when /pulsar is open)
		// so the sidebar's own Orbit-icon badge is accurate the moment the
		// app loads — same "don't wait for the relevant page to visit it
		// first" reasoning as loadThreads() in +layout.svelte.
		void pulsarState.loadUnreadCounts();
	});

	// appState.newThread() deliberately only touches history via
	// replaceState, not goto() — see its doc comment — since /t/[id] and /
	// both render the same ChatView and a real navigation there would
	// pointlessly remount it. But /pulsar and /pulsar/[id] render an
	// entirely different route component, which a raw replaceState can't
	// swap out (SvelteKit's router only reacts to its own goto()/link
	// navigations) — so "New thread" from inside Pulsar needs a real
	// navigation first, or the click silently does nothing visible.
	function startNewThread() {
		const onPulsar = page.url.pathname.startsWith('/pulsar');
		appState.newThread();
		if (onPulsar) void goto('/');
	}

	function openSearch(query: string) {
		// &from=history tells +page.svelte's $effect this is a revisit, not
		// a fresh search — see its comment on why that must not bump this
		// entry back to the top of the list it was just clicked from.
		void goto(`/search?q=${encodeURIComponent(query)}&from=history`);
	}

	function openMatch(result: MessageSearchResult) {
		appState.clearThreadSearch();
		appState.openThread(result.thread_id);
	}

	// Splits a snippet on the \x02/\x03 markers store.SearchMessages wraps
	// each matched term in (see MessageSearchResult's doc comment in
	// types.ts) into plain segments — rendered as real text nodes below via
	// {#if seg.match}<mark> / plain text otherwise, deliberately never
	// {@html}, so a search result built out of past user/assistant message
	// content can never be interpreted as markup.
	function snippetSegments(snippet: string): { text: string; match: boolean }[] {
		return snippet.split(/([\x02][^\x02\x03]*[\x03])/).map((part) => {
			if (part.startsWith('\x02') && part.endsWith('\x03')) {
				return { text: part.slice(1, -1), match: true };
			}
			return { text: part, match: false };
		});
	}
</script>

{#snippet threadRow(thread: Thread, i: number)}
	<div
		class="thread-item"
		class:active={appState.currentThreadId === thread.id}
		onclick={() => appState.openThread(thread.id)}
		onkeydown={(e) => e.key === 'Enter' && appState.openThread(thread.id)}
		role="button"
		tabindex="0"
		in:fly={{ y: 8, duration: 220, delay: Math.min(i, 10) * 22, easing: quintOut }}
	>
		<span class="thread-dot" aria-hidden="true"></span>
		<div class="thread-meta">
			<div class="thread-title">{thread.title || 'Untitled'}</div>
		</div>
	</div>
{/snippet}

{#snippet searchRow(entry: SearchHistoryEntry, i: number)}
	<div
		class="thread-item search-item"
		class:active={isAtlas && searchState.lastQuery === entry.query}
		onclick={() => openSearch(entry.query)}
		onkeydown={(e) => e.key === 'Enter' && openSearch(entry.query)}
		role="button"
		tabindex="0"
		in:fly={{ y: 8, duration: 220, delay: Math.min(i, 10) * 22, easing: quintOut }}
	>
		<span class="thread-dot" aria-hidden="true"></span>
		<div class="thread-meta">
			<div class="thread-title">{entry.query}</div>
		</div>
		<button
			class="favorite-btn"
			class:favorited={entry.favorite}
			type="button"
			aria-label={entry.favorite ? 'Remove from favorites' : 'Add to favorites'}
			onclick={(e) => {
				e.stopPropagation();
				void searchState.favoriteSearch(entry.id, !entry.favorite);
			}}
		>
			<Star size={13} fill={entry.favorite ? 'currentColor' : 'none'} />
		</button>
	</div>
{/snippet}

{#snippet matchRow(result: MessageSearchResult, i: number)}
	<div
		class="thread-item match-item"
		onclick={() => openMatch(result)}
		onkeydown={(e) => e.key === 'Enter' && openMatch(result)}
		role="button"
		tabindex="0"
		in:fly={{ y: 8, duration: 180, delay: Math.min(i, 10) * 18, easing: quintOut }}
	>
		<span class="thread-dot" aria-hidden="true"></span>
		<div class="thread-meta">
			<div class="thread-title">{result.thread_title || 'Untitled'}</div>
			<div class="match-snippet">
				{#each snippetSegments(result.snippet) as seg}
					{#if seg.match}<mark>{seg.text}</mark>{:else}{seg.text}{/if}
				{/each}
			</div>
		</div>
	</div>
{/snippet}

<aside class="sidebar" class:open={appState.sidebarOpen} use:edgeSwipeSidebar>
	<div class="brand">
		{#if isAtlas}
			<img class="brand-mark" src="/atlas-touch-icon.png" alt="" width="22" height="22" />
			<span class="wordmark">Atlas</span>
		{:else}
			<img class="brand-mark" src="/apple-touch-icon.png" alt="" width="22" height="22" />
			<span class="wordmark">Polaris</span>
		{/if}
		<button class="icon-btn collapse-btn" onclick={() => appState.toggleSidebar()} title="Collapse sidebar">
			<PanelLeftClose size={16} />
		</button>
	</div>

	{#if isAtlas}
		<button
			class="btn btn-accent new-thread"
			onclick={() => {
				searchState.reset();
				void goto('/search');
			}}
		>
			<Plus size={16} />
			New search
		</button>
	{:else}
		<button class="btn btn-accent new-thread" onclick={startNewThread}>
			<Plus size={16} />
			New thread
		</button>
		<button
			class="pulsar-entry"
			class:active={page.url.pathname.startsWith('/pulsar')}
			onclick={() => goto('/pulsar')}
		>
			<Orbit size={16} />
			<span class="pulsar-label">Pulsar</span>
			<PulsarUnreadBadge count={pulsarState.totalUnread} />
		</button>
		<div class="thread-search">
			<Search size={14} class="icon-search" aria-hidden="true" />
			<input
				type="text"
				value={appState.threadSearchQuery}
				oninput={(e) => appState.searchThreads(e.currentTarget.value)}
				placeholder="Search past chats"
				spellcheck="false"
			/>
			{#if appState.threadSearchQuery}
				<button class="icon-btn clear-btn" onclick={() => appState.clearThreadSearch()} aria-label="Clear search">
					<X size={13} />
				</button>
			{/if}
		</div>
	{/if}

	<div class="thread-list">
		{#if isAtlas}
			{#if searchState.history.length === 0}
				<p class="thread-empty">No searches yet. Try Atlas above.</p>
			{/if}
			{#if searchFavorites.length > 0}
				<div class="section-label">
					<Star size={11} fill="currentColor" />
					Favorites
				</div>
				{#each searchFavorites as entry, i (entry.id)}
					{@render searchRow(entry, i)}
				{/each}
				{#if searchRecents.length > 0}
					<div class="section-label">Recent searches</div>
				{/if}
			{/if}
			{#each searchRecents as entry, i (entry.id)}
				{@render searchRow(entry, i)}
			{/each}
		{:else if appState.threadSearchQuery}
			<!-- Live full-text search over every past message's content
			     (GET /api/threads/search) — see AppState.searchThreads' doc
			     comment. Replaces the normal Favorites/Recents view entirely
			     while a query is active, same as Atlas's own results list
			     replacing its default state. -->
			{#if appState.threadSearchLoading}
				<p class="thread-empty">Searching…</p>
			{:else if appState.threadSearchResults.length === 0}
				<p class="thread-empty">No matches for "{appState.threadSearchQuery}".</p>
			{:else}
				{#each appState.threadSearchResults as result, i (result.thread_id + '-' + i)}
					{@render matchRow(result, i)}
				{/each}
			{/if}
		{:else}
			{#if appState.threads.length === 0}
				<p class="thread-empty">No threads yet. Ask something to start.</p>
			{/if}
			<!-- Rename/delete used to live here as hover-revealed row icons —
			     moved to ThreadMenu.svelte (the "..." menu in the chat header)
			     since managing the thread you're actually looking at fits there
			     better than a list row whose whole job is just "open this". -->
			{#if favorites.length > 0}
				<div class="section-label">
					<Star size={11} fill="currentColor" />
					Favorites
				</div>
				{#each favorites as thread, i (thread.id)}
					{@render threadRow(thread, i)}
				{/each}
				{#if recents.length > 0}
					<div class="section-label">Recents</div>
				{/if}
			{/if}
			{#each recents as thread, i (thread.id)}
				{@render threadRow(thread, i)}
			{/each}
		{/if}
	</div>

	<div class="status">
		<span class="dot" class:connected={appState.connected}></span>
		<span class="status-text">{appState.connected ? 'connected' : 'reconnecting…'}</span>
		<button class="icon-btn settings-btn" onclick={() => appState.settings.toggle()} title="Settings">
			<Settings size={15} />
		</button>
	</div>
</aside>

<style>
	.sidebar {
		display: flex;
		width: 260px;
		flex-shrink: 0;
		flex-direction: column;
		background: var(--color-surface);
		/* A directional shadow reading as "this panel sits above the main
		   content" instead of a hairline drawn between two flat fills —
		   same light-source logic as the header/composer shadows in
		   ChatView.svelte, just cast rightward since the sidebar is the
		   elevated element here. */
		box-shadow: 6px 0 24px -16px rgba(0, 0, 0, 0.45);
		overflow: hidden;
		transition: width 0.2s ease;
	}

	/* Desktop: collapsing shrinks the column to nothing, main content
	   expands to fill — no overlay needed since there's room to spare. */
	.sidebar:not(.open) {
		width: 0;
		box-shadow: none;
	}

	.brand {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		/* On mobile the sidebar becomes a fixed, full-height overlay (see the
		   media query below) starting at the true viewport top, same as
		   .header in +page.svelte — needs the same safe-area-inset-top
		   clearance so the collapse button isn't under the iOS status bar
		   in standalone PWA mode. max() collapses to the plain 16px
		   everywhere else, where env() is 0. */
		padding: max(var(--space-lg), env(safe-area-inset-top)) var(--space-lg) var(--space-lg);
		white-space: nowrap;
	}

	.brand-mark {
		width: 22px;
		height: 22px;
		border-radius: var(--radius-sm);
		flex-shrink: 0;
		box-shadow: var(--shadow-sm);
	}

	.wordmark {
		font-family: var(--font-wordmark);
		font-size: 18px;
		font-weight: 400;
		letter-spacing: 0.04em;
		/* Lexend body sits at 400 — the wordmark's single available weight
		   is also 400, so contrast comes from the display face itself
		   plus a hair more tracking, not from raising weight. */
	}

	.collapse-btn {
		margin-left: auto;
	}

	.new-thread {
		margin: var(--space-md);
		white-space: nowrap;
	}

	/* A single entry point, not a scrolling row list — closer to how the
	   Settings gear below opens a dedicated panel than to this same
	   sidebar's own thread-list rows, per docs/plans/pulsar-routines.md's
	   "UI structure". */
	.pulsar-entry {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin: 0 var(--space-md) var(--space-md);
		padding: var(--space-sm) var(--space-md);
		border: none;
		background: transparent;
		border-radius: var(--radius-md);
		font: inherit;
		font-size: 13px;
		color: var(--color-text-dim);
		transition:
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo);
	}

	.pulsar-entry:hover {
		background: var(--color-surface-2);
		color: var(--color-text);
	}

	.pulsar-entry.active {
		background: var(--color-accent-soft);
		color: var(--color-text);
		font-weight: 600;
	}

	.pulsar-entry :global(svg) {
		flex-shrink: 0;
		color: var(--color-accent);
	}

	.pulsar-label {
		flex: 1;
		text-align: left;
	}

	.thread-search {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin: 0 var(--space-md) var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		transition: border-color 0.15s var(--ease-out-expo);
	}

	.thread-search:focus-within {
		border-color: var(--color-accent);
	}

	.thread-search :global(.icon-search) {
		flex-shrink: 0;
		color: var(--color-text-dim);
	}

	.thread-search input {
		flex: 1;
		min-width: 0;
		border: none;
		background: transparent;
		font: inherit;
		font-size: 13px;
		color: var(--color-text);
	}

	.thread-search input:focus {
		outline: none;
	}

	.thread-search input::placeholder {
		color: var(--color-text-dim);
	}

	.clear-btn {
		flex-shrink: 0;
		width: 20px;
		height: 20px;
	}

	.match-snippet {
		margin-top: 2px;
		font-size: 11.5px;
		line-height: 1.4;
		color: var(--color-text-dim);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.match-snippet mark {
		background: transparent;
		color: var(--color-accent);
		font-weight: 600;
	}

	.thread-list {
		flex: 1;
		overflow-y: auto;
		padding: var(--space-xs) var(--space-sm) var(--space-sm);
	}

	.thread-empty {
		margin: var(--space-md) var(--space-sm);
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	/* Small-caps section labels, same vocabulary as SettingsPanel's h3 —
	   text alone doing the organizing, no boxed header/pill. */
	.section-label {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin: var(--space-lg) var(--space-md) var(--space-sm);
		font-size: 10.5px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--color-text-dim);
	}

	.section-label:first-child {
		margin-top: var(--space-xs);
	}

	.section-label :global(svg) {
		color: var(--color-accent);
	}

	.thread-item {
		position: relative;
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		border-radius: var(--radius-sm);
		padding: var(--space-md) var(--space-md);
		cursor: pointer;
		transition:
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo);
	}

	.thread-item:hover {
		background: var(--color-surface-2);
	}

	/* Inset separator, not a boxed card — same idea as iOS/macOS list rows:
	   a hairline between items, indented past the leading dot rather than
	   spanning the full row, so it reads as "these are members of one
	   list" instead of "these are N separate bordered elements". Skipped
	   on the last row (nothing below it to separate from) and specifically
	   an ::after rather than border-bottom so it can be inset independent
	   of the row's own padding. */
	.thread-item:not(:last-child)::after {
		content: '';
		position: absolute;
		left: 24px;
		right: 10px;
		bottom: 0;
		height: 1px;
		background: color-mix(in srgb, var(--color-border) 55%, transparent);
	}

	/* Small leading dot that only lights up for the current thread.
	   Reads as a "you are here" pin rather than a decorative side rule. */
	.thread-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: transparent;
		flex-shrink: 0;
		transition:
			background-color 0.15s var(--ease-out-expo),
			box-shadow 0.15s var(--ease-out-expo);
	}

	/* Active state: filled accent-soft ground + bolder title weight +
	   the leading dot lit. No side stripe, no gradient — just a clearly
	   selected surface with a real weight contrast against the rest of
	   the list (400 dim titles vs. 600 lit title). */
	.thread-item.active {
		background: var(--color-accent-soft);
	}

	.thread-item.active .thread-dot {
		background: var(--color-accent);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 22%, transparent);
	}

	.thread-item.active .thread-title {
		font-weight: 600;
		color: var(--color-text);
	}

	.thread-meta {
		flex: 1;
		min-width: 0;
	}

	/* Hover-revealed, same idea as the star affordance ThreadMenu.svelte
	   exposes for chat threads — search history rows have no equivalent
	   "..." menu (there's no per-search page header to hang one off), so
	   this lives directly on the row instead. */
	.favorite-btn {
		appearance: none;
		border: none;
		background: transparent;
		flex-shrink: 0;
		width: 22px;
		height: 22px;
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: var(--radius-sm);
		color: var(--color-text-dim);
		cursor: pointer;
		opacity: 0;
		transition: opacity 0.15s var(--ease-out-expo), color 0.15s var(--ease-out-expo);
	}

	.search-item:hover .favorite-btn,
	.favorite-btn.favorited {
		opacity: 1;
	}

	.favorite-btn:hover {
		color: var(--color-accent);
	}

	.favorite-btn.favorited {
		color: var(--color-accent);
	}

	.thread-title {
		font-size: 13px;
		font-weight: 400;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.status {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		/* Recessed strip rather than a border line — the whole footer reads
		   as a shallow well the connection dot and settings button sit in,
		   consistent with the "carved, not drawn" treatment used on inputs
		   and readouts elsewhere (see --shadow-well in app.css). */
		box-shadow: var(--shadow-well);
		padding: var(--space-md);
		font-size: 12px;
		color: var(--color-text-dim);
		white-space: nowrap;
	}

	.status-text {
		flex: 1;
	}

	.settings-btn {
		flex-shrink: 0;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-danger);
		flex-shrink: 0;
		transition: box-shadow 0.2s var(--ease-out-expo), background-color 0.2s var(--ease-out-expo);
	}

	.dot.connected {
		background: var(--color-accent);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 20%, transparent);
	}

	/* Mobile: the sidebar becomes a fixed-position overlay drawer that
	   slides in over the content instead of squeezing it — collapsing
	   the chat down to a sliver on a phone-width screen looks broken. */
	@media (max-width: 768px) {
		.sidebar {
			position: fixed;
			inset: 0 auto 0 0;
			width: 280px;
			z-index: var(--z-sidebar);
			transform: translateX(-100%);
			transition: transform 0.2s ease;
			box-shadow: 2px 0 16px rgba(0, 0, 0, 0.4);
			/* Vertical scrolling of the thread list stays native; horizontal
			   panning is owned entirely by edgeSwipeSidebar (see the action
			   import above) — without this, the browser's own touch
			   handling can fight the JS-driven drag transform mid-gesture. */
			touch-action: pan-y;
		}

		.sidebar.open {
			width: 280px;
			transform: translateX(0);
		}

		.sidebar:not(.open) {
			width: 280px;
			border-right: 1px solid var(--color-border);
			transform: translateX(-100%);
		}
	}
</style>
