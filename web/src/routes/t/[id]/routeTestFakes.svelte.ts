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

export const fakePage = {
	get params() {
		return { id: threadId };
	}
};

export function setPageId(id: string) {
	threadId = id;
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
}
