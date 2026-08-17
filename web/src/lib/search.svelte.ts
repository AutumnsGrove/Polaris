import type { SearchResult } from './types';

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
		} catch {
			if (seq !== this.searchSeq) return;
			this.error = "Couldn't reach the search backend.";
			this.results = [];
		} finally {
			if (seq === this.searchSeq) this.loading = false;
		}
	}

	// Bumped on every search() call; a fetch that resolves after a newer
	// one has already started discards its own result — same pattern as
	// AppState.openThreadSeq, for the same reason (a fast retype
	// shouldn't let an earlier, slower response clobber a later one).
	private searchSeq = 0;
}

export const searchState = new SearchState();
