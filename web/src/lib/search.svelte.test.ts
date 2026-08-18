import { describe, it, expect, vi, beforeEach } from 'vitest';
import { SearchState } from './search.svelte';

function fakeFetch(data: unknown, ok = true) {
	return vi.fn((_url: string) => Promise.resolve({ ok, json: async () => data }));
}

describe('SearchState.search — record option', () => {
	let state: SearchState;

	beforeEach(() => {
		state = new SearchState();
	});

	// Reopening a search from the sidebar's history list must not bump it
	// back to the top of that same list — see +page.svelte's $effect and
	// this method's own doc comment. record: false is how that's signaled
	// down to the /api/search request.
	it('appends &record=0 and skips loadHistory when record is false', async () => {
		const fetchSpy = fakeFetch({ results: [] });
		vi.stubGlobal('fetch', fetchSpy);

		await state.search('rust async runtime', { record: false });

		const url = fetchSpy.mock.calls[0][0] as string;
		expect(url).toContain('record=0');
		// loadHistory would be a second fetch call — there should be exactly one.
		expect(fetchSpy).toHaveBeenCalledTimes(1);
	});

	it('omits record=0 and calls loadHistory when record is true (the default)', async () => {
		const fetchSpy = fakeFetch({ results: [] });
		vi.stubGlobal('fetch', fetchSpy);

		await state.search('rust async runtime');

		const url = fetchSpy.mock.calls[0][0] as string;
		expect(url).not.toContain('record=0');
		// loadHistory fires as a second, unawaited fetch call.
		await Promise.resolve();
		expect(fetchSpy.mock.calls.some((c) => (c[0] as string).includes('/api/search-history'))).toBe(
			true
		);
	});
});

describe('SearchState.search — page option', () => {
	let state: SearchState;

	beforeEach(() => {
		state = new SearchState();
	});

	it('omits &page for the default (page 1) and reads page/hasMore back from the response', async () => {
		const fetchSpy = fakeFetch({ results: [], page: 1, has_more: true });
		vi.stubGlobal('fetch', fetchSpy);

		await state.search('rust async runtime');

		const url = fetchSpy.mock.calls[0][0] as string;
		expect(url).not.toContain('page=');
		expect(state.page).toBe(1);
		expect(state.hasMore).toBe(true);
	});

	it('appends &page=N for page > 1 and updates state.page from it', async () => {
		const fetchSpy = fakeFetch({ results: [], page: 3, has_more: false });
		vi.stubGlobal('fetch', fetchSpy);

		await state.search('rust async runtime', { record: false, page: 3 });

		const url = fetchSpy.mock.calls[0][0] as string;
		expect(url).toContain('page=3');
		expect(state.page).toBe(3);
		expect(state.hasMore).toBe(false);
	});

	it('reset() puts page back to 1 and clears hasMore', async () => {
		const fetchSpy = fakeFetch({ results: [], page: 4, has_more: true });
		vi.stubGlobal('fetch', fetchSpy);
		await state.search('rust async runtime', { record: false, page: 4 });
		expect(state.page).toBe(4);

		state.reset();

		expect(state.page).toBe(1);
		expect(state.hasMore).toBe(false);
	});
});

// The actual point of prefetching: know a "Next" click is a dead end
// *before* the user clicks it, instead of them landing on an empty page.
describe('SearchState.search — next-page prefetch', () => {
	let state: SearchState;

	beforeEach(() => {
		state = new SearchState();
	});

	function fakeFetchByPage(byPage: Record<number, { results: unknown[]; has_more: boolean }>) {
		return vi.fn((url: string) => {
			if (url.includes('/api/search-history')) {
				return Promise.resolve({ ok: true, json: async () => [] });
			}
			const match = /[?&]page=(\d+)/.exec(url);
			const page = match ? Number(match[1]) : 1;
			const body = byPage[page] ?? { results: [], has_more: false };
			return Promise.resolve({ ok: true, json: async () => ({ ...body, page }) });
		});
	}

	it('self-corrects hasMore to false once a prefetched empty next page resolves', async () => {
		const fetchSpy = fakeFetchByPage({
			1: { results: [{ url: 'https://a.com' }], has_more: true }, // heuristic: page 1 came back "full"
			2: { results: [], has_more: false } // ground truth: nothing after it
		});
		vi.stubGlobal('fetch', fetchSpy);

		await state.search('rust async runtime');
		expect(state.hasMore).toBe(true); // same-page heuristic, before the prefetch lands

		// Let the background prefetch's microtasks (fetch -> .json() -> .then) run.
		await new Promise((r) => setTimeout(r, 0));
		await new Promise((r) => setTimeout(r, 0));

		expect(state.hasMore).toBe(false); // corrected — "Next" should now be disabled
	});

	it('goToPage onto an already-prefetched page serves from cache with no extra request', async () => {
		const fetchSpy = fakeFetchByPage({
			1: { results: [{ url: 'https://a.com' }], has_more: true },
			2: { results: [{ url: 'https://b.com' }], has_more: false }
		});
		vi.stubGlobal('fetch', fetchSpy);

		await state.search('rust async runtime');
		await new Promise((r) => setTimeout(r, 0));
		await new Promise((r) => setTimeout(r, 0));
		const callsAfterPrefetch = fetchSpy.mock.calls.length;
		expect(callsAfterPrefetch).toBeGreaterThanOrEqual(2); // page 1 + the page 2 prefetch

		await state.search('rust async runtime', { record: false, page: 2 });

		expect(state.page).toBe(2);
		expect(state.results).toEqual([{ url: 'https://b.com' }]);
		expect(fetchSpy.mock.calls.length).toBe(callsAfterPrefetch); // no new request — served from cache
	});
});
