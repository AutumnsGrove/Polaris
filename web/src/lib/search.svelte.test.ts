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
