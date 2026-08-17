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

	// Sidebar's "Recent searches"/Favorites data — see store.SearchHistoryEntry.
	// Loaded independently of a live search (Sidebar needs it even before
	// the user has searched anything this session) and refreshed after
	// every successful search, since handleSearch records it server-side.
	history = $state<SearchHistoryEntry[]>([]);

	quickAnswer = $state<QuickAnswer | null>(null);
	quickAnswerLoading = $state(false);
	quickAnswerError = $state('');

	async search(query: string) {
		const trimmed = query.trim();
		if (!trimmed) return;

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
			const res = await fetch(`/api/search?q=${encodeURIComponent(trimmed)}`, {
				signal: controller.signal
			});
			if (seq !== this.searchSeq) return; // superseded by a newer search
			if (!res.ok) {
				this.error = 'Search failed — try again.';
				this.results = [];
				return;
			}
			const data = await res.json();
			if (seq !== this.searchSeq) return;
			this.results = data.results ?? [];
			this.lastQuery = trimmed;
			void this.loadHistory();
		} catch {
			if (seq !== this.searchSeq) return; // includes our own abort() above
			this.error = "Couldn't reach the search backend.";
			this.results = [];
		} finally {
			if (seq === this.searchSeq) this.loading = false;
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
}

export const searchState = new SearchState();
