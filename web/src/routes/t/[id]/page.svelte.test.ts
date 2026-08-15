import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, waitFor } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import {
	fakeAppState,
	fakePage,
	setPageId,
	navigateTo,
	syncURLOnly,
	invalidatePageState,
	openThreadCalls,
	resetRouteTestFakes
} from './routeTestFakes.svelte';

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

		navigateTo('C'); // a real navigation changes page.params.id
		flushSync();

		await waitFor(() => expect(openThreadCalls).toEqual(['A', 'C']));
	});

	// Regression test for the "thread bump-back" bug, the mechanism finally
	// confirmed from the live debug-log trace on 2026-08-15 (see the
	// project_thread_bump_back_root_cause memory).
	//
	// The user's own description: "I finish a thread at 2pm, update and
	// restart at 3pm, start a new thread at 5pm — and it bumps me back to
	// the 2pm thread. It's always the latest one before the most recent
	// restart." That is exactly this sequence: the restart's checkVersion
	// reload pins page.params.id to the 2pm thread, syncURL then moves the
	// address bar to the 5pm thread without page state noticing, and the
	// next re-run of the route effect reopens the stale 2pm id.
	//
	// The pre-existing untrack() fix does NOT cover this: the effect isn't
	// re-running because currentThreadId changed, it's re-running because
	// page state was invalidated for some unrelated reason.
	it('does not bump back to a stale page.params.id after syncURL moved the address bar', async () => {
		// Restart-triggered reload landed here on the 2pm thread.
		render(PageComponent);
		await waitFor(() => expect(openThreadCalls).toEqual(['A']));

		// 5pm: user starts a new thread. AppState.syncURL rewrites the
		// address bar only — page.params.id is still the 2pm thread 'A'.
		fakeAppState.currentThreadId = 'B';
		syncURLOnly('/t/B');

		// Any page-state churn re-runs the effect against the stale 'A'.
		invalidatePageState();
		flushSync();

		expect(openThreadCalls).toEqual(['A']); // must NOT have reopened 'A'
		expect(fakeAppState.currentThreadId).toBe('B'); // still on the new thread
	});

	// The same staleness guard must not break the homescreen case: newThread()
	// calls syncURL(null), leaving this route mounted with pathname '/'.
	it('does not reopen the stale thread after newThread() returns to /', async () => {
		render(PageComponent);
		await waitFor(() => expect(openThreadCalls).toEqual(['A']));

		fakeAppState.currentThreadId = null; // newThread()
		syncURLOnly('/');

		invalidatePageState();
		flushSync();

		expect(openThreadCalls).toEqual(['A']);
		expect(fakeAppState.currentThreadId).toBeNull();
	});

	// The staleness check keys on the address bar, so it must not suppress a
	// genuine back/forward navigation to a thread this document has already
	// shown once.
	it('re-opens on a real navigation back to a previously visited thread', async () => {
		render(PageComponent);
		await waitFor(() => expect(openThreadCalls).toEqual(['A']));

		navigateTo('C');
		flushSync();
		await waitFor(() => expect(openThreadCalls).toEqual(['A', 'C']));

		navigateTo('A'); // browser Back
		flushSync();
		await waitFor(() => expect(openThreadCalls).toEqual(['A', 'C', 'A']));
	});
});
