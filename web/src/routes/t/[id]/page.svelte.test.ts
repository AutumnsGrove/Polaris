import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, waitFor } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { fakeAppState, fakePage, setPageId, openThreadCalls, resetRouteTestFakes } from './routeTestFakes.svelte';

// vi.mock factories run before regular imports resolve (they're hoisted
// above them), so they can't safely close over routeTestFakes' bindings
// directly — a dynamic import() defers resolution past that hoisting and
// lands on the same singleton module instance the top-level import above
// already has, so both sides share state.
vi.mock('$app/state', async () => {
	const fakes = await import('./routeTestFakes.svelte');
	return { page: fakes.fakePage };
});
vi.mock('$lib/state.svelte', async () => {
	const fakes = await import('./routeTestFakes.svelte');
	return { appState: fakes.fakeAppState, debugBeacon: () => {} };
});
vi.mock('$lib/components/ChatView.svelte', () => import('./ChatViewStub.svelte'));

const { default: PageComponent } = await import('./+page.svelte');

describe('t/[id]/+page.svelte', () => {
	beforeEach(() => {
		resetRouteTestFakes();
	});

	it('opens the thread named by the URL on mount', async () => {
		render(PageComponent);
		await waitFor(() => expect(openThreadCalls).toEqual(['A']));
	});

	// Regression test for the snap-back bug: the effect used to read
	// appState.currentThreadId directly (a tracked dependency), so
	// switching threads from the sidebar — which changes currentThreadId
	// but never touches the URL, since AppState.syncURL updates the
	// address bar via raw history.replaceState rather than a real
	// SvelteKit navigation — re-ran this effect against the same stale
	// page.params.id and immediately reopened the original thread,
	// undoing the switch. The fix wraps that read in untrack() so only a
	// genuine change to page.params.id can trigger a reopen.
	it('does not re-open the thread when currentThreadId changes without a URL navigation', async () => {
		render(PageComponent);
		await waitFor(() => expect(openThreadCalls).toEqual(['A']));

		fakeAppState.currentThreadId = 'B'; // simulates a sidebar-triggered switch
		flushSync();

		expect(openThreadCalls).toEqual(['A']); // still just the once
	});

	it('re-opens when the URL itself navigates to a different thread', async () => {
		render(PageComponent);
		await waitFor(() => expect(openThreadCalls).toEqual(['A']));

		setPageId('C'); // a real navigation changes page.params.id
		flushSync();

		await waitFor(() => expect(openThreadCalls).toEqual(['A', 'C']));
	});
});
