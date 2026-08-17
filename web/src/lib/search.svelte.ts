import type { SearchResult, SearchHistoryEntry, RankState } from './types';

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

	async search(query: string) {
		const trimmed = query.trim();
		if (!trimmed) return;

		this.loading = true;
		this.error = '';
		const seq = ++this.searchSeq;

		try {
			const res = await fetch(`/api/search?q=${encodeURIComponent(trimmed)}`);
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
			if (seq !== this.searchSeq) return;
			this.error = "Couldn't reach the search backend.";
			this.results = [];
		} finally {
			if (seq === this.searchSeq) this.loading = false;
		}
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
}

export const searchState = new SearchState();
