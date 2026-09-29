import type { MessageSearchResult } from './types';

export class ThreadSearchState {
	// Sidebar's "search past chats" box — see searchThreads' doc comment.
	// Separate from `threads` (the plain recency list) rather than
	// filtering it client-side, since this searches every message's full
	// content via the server's FTS5 index, not just what's already loaded
	// here.
	query = $state('');
	results = $state<MessageSearchResult[]>([]);
	loading = $state(false);

	// search's debounce timer/cancellation state: both the timer and the
	// seq/AbortController pair are needed together (see search's doc comment).
	private seq = 0;
	private controller: AbortController | null = null;
	private debounce: ReturnType<typeof setTimeout> | null = null;

	// Debounced, cancellable full-text search over past chat content
	// (GET /api/threads/search) — called on every keystroke in the
	// sidebar's search box, so both a client-side debounce (this doesn't
	// fire a request per character) and the seq/AbortController guard
	// (a slow response for an earlier keystroke can't clobber a faster
	// one for a later keystroke) matter here, same reasoning as
	// SearchState.search() in search.svelte.ts.
	search(query: string) {
		this.query = query;
		if (this.debounce !== null) clearTimeout(this.debounce);
	
		const trimmed = query.trim();
		if (!trimmed) {
			this.controller?.abort();
			this.results = [];
			this.loading = false;
			return;
		}
	
		this.loading = true;
		this.debounce = setTimeout(() => void this.run(trimmed), 250);
	}
	
	private async run(query: string) {
		this.controller?.abort();
		const controller = new AbortController();
		this.controller = controller;
		const seq = ++this.seq;
	
		try {
			const res = await fetch(`/api/threads/search?q=${encodeURIComponent(query)}`, {
				signal: controller.signal
			});
			if (seq !== this.seq) return; // superseded by a newer keystroke
			this.results = res.ok ? ((await res.json()) ?? []) : [];
		} catch {
			if (seq !== this.seq) return; // includes our own abort() above
			this.results = [];
		} finally {
			if (seq === this.seq) this.loading = false;
		}
	}
	
	// Clears the search box back to the plain recency-ordered thread list —
	// called by the box's own clear button and when a result is clicked
	// (opening a thread shouldn't leave a stale search sitting above it).
	clear() {
		if (this.debounce !== null) clearTimeout(this.debounce);
		this.controller?.abort();
		this.query = '';
		this.results = [];
		this.loading = false;
		++this.seq;
	}
}
