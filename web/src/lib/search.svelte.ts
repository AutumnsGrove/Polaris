import type { SearchResult, SearchHistoryEntry, RankState, Citation, ServerEvent } from './types';

export interface QuickAnswer {
	text: string;
	citations: Citation[];
	threadId: string;
	costUsd: number;
}

// Atlas's own reactive state — deliberately separate from AppState
// (state.svelte.ts), not a mode bolted onto it. Chat's state model is
// built entirely around one persistent, streaming conversation; a search
// is a one-shot query -> ranked list with no equivalent shape to share.
export class SearchState {
	query = $state('');
	results = $state<SearchResult[]>([]);
	loading = $state(false);
	error = $state('');
	lastQuery = $state('');

	// Which SearXNG results page lastQuery is currently showing, and
	// whether a next one might exist — see gateway/search.go's has_more
	// doc comment for why that's a heuristic, not a promise. Reset to
	// page 1 whenever a genuinely new query runs (search() with no page
	// in opts); only goToPage-style navigation changes it otherwise.
	page = $state(1);
	hasMore = $state(false);

	// The first page (if any) that's actually confirmed empty for
	// lastQuery — null until one is. +page.svelte's bottom page-picker
	// shows a full run of 10 "a"s up front, optimistically, before any of
	// them are confirmed (see its own doc comment on that trade-off); once
	// a direct jump or Next actually lands on nothing, this remembers it
	// so that specific dead end (and everything past it) isn't offered
	// again for the rest of this query's session, instead of the same
	// surprise landing on every revisit.
	deadEndPage = $state<number | null>(null);

	// Sidebar's "Recent searches"/Favorites data — see store.SearchHistoryEntry.
	// Loaded independently of a live search (Sidebar needs it even before
	// the user has searched anything this session) and refreshed after
	// every successful search, since handleSearch records it server-side.
	history = $state<SearchHistoryEntry[]>([]);

	quickAnswer = $state<QuickAnswer | null>(null);
	quickAnswerLoading = $state(false);
	quickAnswerError = $state('');

	// record: false for reopening a search from the sidebar's history list
	// (see +page.svelte's $effect) — it should just show the same results
	// again, not bump that entry back to the top of its own list. There's
	// no way to "follow up" on a one-shot search, unlike a chat thread, so
	// merely revisiting one should never move it — same as opening a
	// thread never touches its position either.
	//
	// page: which SearXNG page to fetch (default 1, a genuinely new
	// query). Pagination within the *same* query (goToPage in
	// +page.svelte) calls this again with a higher page and record:
	// false — turning the page isn't a new search, so it shouldn't touch
	// history either.
	//
	// Pages are cached per query (see pageCache below) and the *next*
	// page is speculatively prefetched in the background right after this
	// one lands — so by the time a user actually reads the results and
	// clicks "Next", that page either loads instantly from cache or,
	// better, we already know for certain whether it's empty and can just
	// disable the button instead of letting them click into a dead end.
	// gateway/search.go's has_more is only a same-page heuristic (full
	// page in ⇒ maybe more, short page in ⇒ definitely not); the
	// prefetch's own has_more replaces that guess with the real answer as
	// soon as it resolves.
	async search(query: string, opts: { record: boolean; page?: number } = { record: true }) {
		const trimmed = query.trim();
		if (!trimmed) return;
		const page = opts.page ?? 1;

		if (trimmed !== this.lastQuery) {
			// A genuinely different query — yesterday's page 2 has nothing
			// to do with today's, and a stale hit here would silently show
			// the wrong query's results under a "page N" label.
			this.pageCache.clear();
			this.pagePrefetches.clear();
			this.deadEndPage = null;
		}

		const cached = this.pageCache.get(page);
		if (cached) {
			this.results = cached.results;
			this.lastQuery = trimmed;
			this.page = page;
			this.hasMore = cached.hasMore;
			this.markIfDeadEnd(page, cached.results.length);
			this.prefetchNextPage(trimmed, page);
			return;
		}

		// Cancels a still-outstanding previous search's actual network
		// request (not just its effect on state) — the seq guards below
		// already stopped it from clobbering newer state, but without this
		// it kept running server-side to completion regardless, wasting a
		// live connection every time a query was retyped or the user
		// navigated away mid-search.
		this.searchController?.abort();
		const controller = new AbortController();
		this.searchController = controller;

		this.loading = true;
		this.error = '';
		const seq = ++this.searchSeq;

		try {
			// A prefetch for this exact page may already be in flight from
			// an earlier page's own lookahead — ride it instead of firing a
			// second, redundant SearXNG request for the same page.
			const inFlight = this.pagePrefetches.get(page);
			const data = inFlight ? await inFlight : await this.fetchPage(trimmed, page, opts.record, controller.signal);
			if (seq !== this.searchSeq) return; // superseded by a newer search
			if (!data) {
				this.error = 'Search failed — try again.';
				this.results = [];
				return;
			}
			this.results = data.results;
			this.lastQuery = trimmed;
			this.page = data.page;
			this.hasMore = data.hasMore;
			this.pageCache.set(data.page, { results: data.results, hasMore: data.hasMore });
			this.markIfDeadEnd(data.page, data.results.length);
			if (opts.record) void this.loadHistory();
			this.prefetchNextPage(trimmed, data.page);
		} catch {
			if (seq !== this.searchSeq) return; // includes our own abort() above
			this.error = "Couldn't reach the search backend.";
			this.results = [];
		} finally {
			if (seq === this.searchSeq) this.loading = false;
		}
	}

	// Shared by search()'s own fetch and prefetchNextPage's background
	// one — same request, same response shape, different caller. Returns
	// null (not throwing) on a non-OK response so a failed *background*
	// prefetch doesn't need its own try/catch at every call site; a failed
	// *foreground* fetch is turned back into the user-visible error by
	// search() itself.
	private async fetchPage(
		query: string,
		page: number,
		record: boolean,
		signal?: AbortSignal
	): Promise<{ results: SearchResult[]; page: number; hasMore: boolean } | null> {
		const params = new URLSearchParams({ q: query });
		if (!record) params.set('record', '0');
		if (page > 1) params.set('page', String(page));
		const res = await fetch(`/api/search?${params}`, { signal });
		if (!res.ok) return null;
		const data = await res.json();
		return { results: data.results ?? [], page: data.page ?? page, hasMore: data.has_more ?? false };
	}

	// Fire-and-forget: fetches fromPage + 1 in the background and caches
	// it, so goToPage's own search() call either serves it instantly from
	// cache or (if this hasn't resolved yet) rides this same in-flight
	// request rather than starting a duplicate one. Also corrects
	// this.hasMore live if the user is still sitting on fromPage when this
	// resolves and it turns out to be empty — the "Next" button disables
	// itself before they ever click into the dead end, which is the
	// actual point of prefetching rather than just caching.
	private prefetchNextPage(query: string, fromPage: number) {
		// this.hasMore reflects fromPage (the caller just set it, from
		// either a cache hit or a fresh fetch) — a page that already came
		// back short is essentially guaranteed to have nothing after it,
		// so there's nothing worth confirming ahead of time.
		if (!this.hasMore) return;
		const next = fromPage + 1;
		if (this.pageCache.has(next) || this.pagePrefetches.has(next)) return;

		// .catch here, not just in search()'s own try/catch — this promise
		// may never be awaited at all (the user might never click "Next"),
		// and an unawaited rejection is an unhandled-rejection console
		// error, not a quiet no-op. A network failure just means the
		// eventual goToPage falls through to fetchPage's normal foreground
		// path and surfaces the error there instead, same as if no
		// prefetch had ever been attempted.
		const promise = this.fetchPage(query, next, false)
			.then((data) => {
				this.pagePrefetches.delete(next);
				if (!data || query !== this.lastQuery) return null;
				this.pageCache.set(next, { results: data.results, hasMore: data.hasMore });
				this.markIfDeadEnd(next, data.results.length);
				if (this.page === fromPage && this.lastQuery === query) {
					this.hasMore = data.results.length > 0;
				}
				return data;
			})
			.catch(() => {
				this.pagePrefetches.delete(next);
				return null;
			});
		this.pagePrefetches.set(next, promise);
	}

	// Records the first page that's actually confirmed empty for the
	// current query — see deadEndPage's own doc comment. Keeps the
	// smallest one found so far: jumping straight to page 9 and finding
	// it empty, then later checking page 5 and finding it fine, should
	// still leave the boundary at 9, not get confused by check order.
	private markIfDeadEnd(page: number, resultCount: number) {
		if (page <= 1 || resultCount > 0) return;
		if (this.deadEndPage === null || page < this.deadEndPage) {
			this.deadEndPage = page;
		}
	}

	// Quick Answer, "?"-triggered per the plan — deliberately the full
	// agent pipeline via POST /api/ask/stream (gateway/ask.go), not a
	// separate lightweight synthesis path: it runs the same web_search/
	// web_read tool-calling loop and multi-source verification the chat
	// assistant uses, and persists a real, revisitable thread, so a Quick
	// Answer can grow into a full conversation via "Continue in
	// Assistant" instead of being a dead end. quick_mode: true (tools.
	// Context.QuickMode) is one behavior difference — it skips web_read's
	// optional per-page filter LLM call, trading some precision for fewer
	// sequential round-trips, since "quick" is the whole point here.
	//
	// Streamed (NDJSON over a flushed response body, same wire shape /ws
	// sends), not a single blocking request — waiting out a full agent
	// turn in total silence before anything appears is exactly the
	// "nothing then everything at once" problem this fixes. Event
	// handling below mirrors state.svelte.ts's handleEvent as closely as
	// this one-shot (non-thread-following) context allows, most
	// importantly the "commentary" reset: text streamed as "token" before
	// a tool call is preamble, not the real answer, and must not survive
	// into what's shown.
	async askQuickAnswer(query: string) {
		const trimmed = query.trim();
		if (!trimmed) return;

		// See search()'s identical comment — same reasoning, same fix.
		this.quickAnswerController?.abort();
		const controller = new AbortController();
		this.quickAnswerController = controller;

		this.quickAnswerLoading = true;
		this.quickAnswerError = '';
		this.quickAnswer = { text: '', citations: [], threadId: '', costUsd: 0 };
		const seq = ++this.quickAnswerSeq;

		try {
			const res = await fetch('/api/ask/stream', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ content: trimmed, source: 'atlas', quick_mode: true }),
				signal: controller.signal
			});
			if (seq !== this.quickAnswerSeq) return;
			if (!res.ok || !res.body) {
				this.quickAnswerError = 'Quick Answer failed — try again.';
				this.quickAnswer = null;
				return;
			}

			const reader = res.body.getReader();
			const decoder = new TextDecoder();
			let buffered = '';

			// NDJSON: each line is a complete JSON value, but a single chunk
			// read from the stream can split a line across two reads (or
			// contain several) — same buffering shape as synthesizeStream's
			// /api/speak/stream consumer in speech.ts.
			while (true) {
				const { done, value } = await reader.read();
				if (done) break;
				if (seq !== this.quickAnswerSeq) {
					void reader.cancel();
					return;
				}
				buffered += decoder.decode(value, { stream: true });

				let newlineIndex: number;
				while ((newlineIndex = buffered.indexOf('\n')) !== -1) {
					const line = buffered.slice(0, newlineIndex).trim();
					buffered = buffered.slice(newlineIndex + 1);
					if (!line) continue;
					this.handleQuickAnswerEvent(JSON.parse(line));
					if (this.quickAnswerLoading) this.quickAnswerLoading = false;
				}
			}
		} catch {
			if (seq !== this.quickAnswerSeq) return; // includes our own abort() above
			this.quickAnswerError = "Couldn't reach the assistant.";
			this.quickAnswer = null;
		} finally {
			if (seq === this.quickAnswerSeq) this.quickAnswerLoading = false;
		}
	}

	private handleQuickAnswerEvent(evt: ServerEvent) {
		if (!this.quickAnswer) return;
		switch (evt.type) {
			case 'token':
				this.quickAnswer.text += evt.content;
				break;
			case 'commentary':
				// See askQuickAnswer's doc comment — discard, don't append.
				this.quickAnswer.text = '';
				break;
			case 'done':
				this.quickAnswer.citations = evt.citations ?? [];
				this.quickAnswer.threadId = evt.thread_id;
				this.quickAnswer.costUsd = evt.cost_usd ?? 0;
				break;
			case 'error':
				// Same fallback rule as state.svelte.ts's 'error' case: keep
				// whatever text already streamed in if there is any, only
				// surface the error message when there's genuinely nothing
				// to show instead.
				if (!this.quickAnswer.text) this.quickAnswerError = evt.message || 'Quick Answer failed.';
				break;
		}
	}

	// Clears Quick Answer's three fields together, and — critically — bumps
	// quickAnswerSeq so a still-in-flight askQuickAnswer() call from a
	// *previous* "?" query can't write into quickAnswerLoading/quickAnswer
	// after this call already cleared them (that's what askQuickAnswer's
	// own seq guard exists to prevent, but only if the seq is actually
	// bumped here too, not just the visible fields reset).
	discardQuickAnswer() {
		this.quickAnswerController?.abort();
		this.quickAnswer = null;
		this.quickAnswerLoading = false;
		this.quickAnswerError = '';
		++this.quickAnswerSeq;
	}

	// Clears every field tied to "the current search" in one place —
	// query/results plus Quick Answer's three fields — so a caller (e.g.
	// Sidebar's "New search" button) can't reset one half and leave the
	// other stale. A prior version of "New search" reset only
	// query/results/lastQuery, leaving a previous Quick Answer card
	// visibly attached to nothing on a blank search page.
	reset() {
		this.searchController?.abort();
		this.query = '';
		this.results = [];
		this.lastQuery = '';
		this.error = '';
		this.page = 1;
		this.hasMore = false;
		this.deadEndPage = null;
		this.pageCache.clear();
		this.pagePrefetches.clear();
		++this.searchSeq; // discard any in-flight search() response
		this.discardQuickAnswer();
	}

	async loadHistory() {
		const res = await fetch('/api/search-history');
		if (!res.ok) return;
		this.history = (await res.json()) ?? [];
	}

	async favoriteSearch(id: number, favorite: boolean) {
		await fetch(`/api/search-history/${id}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ favorite })
		});
		await this.loadHistory();
	}

	// Persists a ranking popover choice to domain_rankings.yaml
	// (search.SetDomainRanking, via PUT /api/domain-rankings) — applies to
	// every future search, Atlas's or the assistant's web_search tool's,
	// not just the results currently on screen. Returns whether it
	// succeeded so the caller can decide how to handle a failure (e.g. an
	// optimistic UI update that needs reverting).
	async setDomainRanking(domain: string, state: RankState): Promise<boolean> {
		const res = await fetch('/api/domain-rankings', {
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ domain, state })
		});
		return res.ok;
	}

	// Bumped on every search() call; a fetch that resolves after a newer
	// one has already started discards its own result — same pattern as
	// AppState.openThreadSeq, for the same reason (a fast retype
	// shouldn't let an earlier, slower response clobber a later one).
	private searchSeq = 0;
	private quickAnswerSeq = 0;
	private searchController: AbortController | null = null;
	private quickAnswerController: AbortController | null = null;

	// Per-query page cache/in-flight-prefetch tracking — see search()'s
	// doc comment. Both cleared together whenever the query itself
	// changes (a different query's page 2 means nothing here).
	private pageCache = new Map<number, { results: SearchResult[]; hasMore: boolean }>();
	private pagePrefetches = new Map<
		number,
		Promise<{ results: SearchResult[]; page: number; hasMore: boolean } | null>
	>();
}

export const searchState = new SearchState();
