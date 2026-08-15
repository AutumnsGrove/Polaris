// $state needs the Svelte compiler's rune transform, which only applies to
// files matching the *.svelte.ts pattern — a plain object literal inside a
// vi.mock/vi.hoisted factory in the test file itself doesn't get that
// transform, and Svelte's reactivity is exactly what page.svelte.test.ts
// needs to distinguish "the fix works" from "nothing was ever tracked in
// the first place" (see that file's comments). Lives as its own module so
// both the test body and its vi.mock factories (which can only safely
// reference fakes via a deferred dynamic import(), not a closed-over
// binding — see Vitest's vi.mock hoisting docs) share the same instance.

let threadId = $state('A');

// Bumped to invalidate page state *without* changing the id. Real
// SvelteKit page state is a single reactive object: anything reading
// page.params re-runs whenever page state is updated at all, not only when
// the matched params happen to differ. Modelling that is what lets
// page.svelte.test.ts reproduce the bump-back bug, where the effect re-ran
// on its own while page.params.id was stale.
let pageVersion = $state(0);

export const fakePage = {
	get params() {
		pageVersion;
		return { id: threadId };
	}
};

export function setPageId(id: string) {
	threadId = id;
}

// A real SvelteKit navigation moves the address bar and page.params
// together; syncURL's raw history.replaceState moves only the address bar.
export function navigateTo(id: string) {
	window.history.replaceState(null, '', `/t/${id}`);
	threadId = id;
}

// What AppState.syncURL does: address bar only, page state left stale.
export function syncURLOnly(path: string) {
	window.history.replaceState(null, '', path);
}

// Page state churn with no navigation — forces effects reading page.params
// to re-run against an unchanged (and possibly stale) id.
export function invalidatePageState() {
	pageVersion++;
}

let currentThreadId = $state<string | null>(null);
export const openThreadCalls: string[] = [];

export const fakeAppState = {
	get currentThreadId() {
		return currentThreadId;
	},
	set currentThreadId(v: string | null) {
		currentThreadId = v;
	},
	async openThread(id: string) {
		openThreadCalls.push(id);
		currentThreadId = id;
	}
};

export function resetRouteTestFakes() {
	currentThreadId = null;
	openThreadCalls.length = 0;
	threadId = 'A';
	pageVersion = 0;
	window.history.replaceState(null, '', '/t/A');
}
