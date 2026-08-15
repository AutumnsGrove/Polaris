<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { appState, debugBeacon } from '$lib/state.svelte';
	import ChatView from '$lib/components/ChatView.svelte';

	// The id this effect has already acted on. Distinguishes "page.params.id
	// genuinely changed" (a real navigation — always worth opening) from
	// "this effect re-ran against the same id" (which must be treated with
	// suspicion — see the staleness check below).
	let lastRouteId: string | null = null;

	// Reopens the thread named by the URL — the fix for refresh/reconnect
	// dropping back to the homescreen with no way to tell which thread was
	// open.
	//
	// page.params.id is a snapshot from the last *real* navigation, and it
	// goes stale: AppState.syncURL moves the address bar between threads
	// with raw history.replaceState (deliberately, so this route never
	// remounts mid-stream — see its doc comment), which SvelteKit's page
	// state never observes. So once a restart-triggered reload lands on
	// /t/<X>, page.params.id stays pinned to X for the entire life of the
	// document, even as the user starts new threads and moves elsewhere.
	//
	// untrack()ing currentThreadId stops it becoming a *dependency*, but
	// nothing stops this effect re-running for other reasons — page.params
	// reads the one reactive page-state object, so any update to it re-runs
	// this. Every such re-run found `stale id !== currentThreadId` true and
	// called openThread(X), yanking the user back to the thread they were
	// on before the last restart. That's the long-standing "thread
	// bump-back" bug; worse, when it fired mid-turn it also silently
	// discarded the in-flight answer, because handleEvent's stillWatching
	// check correctly concluded the user had navigated away.
	//
	// The address bar is the tiebreaker for the re-run case: syncURL keeps
	// it authoritative and in step with currentThreadId, so a page.params.id
	// that no longer matches window.location is by definition stale and must
	// not drive a navigation. A genuine navigation is exempt from that check
	// (it changes both together, and we don't want to depend on which of the
	// two SvelteKit updates first).
	$effect(() => {
		const id = page.params.id;
		const isRealNavigation = id !== lastRouteId;
		lastRouteId = id ?? null;

		const stale =
			!isRealNavigation &&
			untrack(() => typeof window !== 'undefined' && window.location.pathname !== `/t/${id}`);
		const willOpen = !!id && !stale && untrack(() => id !== appState.currentThreadId);
		// TEMPORARY — see project_thread_bump_back_root_cause memory.
		debugBeacon('route effect fired', {
			routeId: id,
			currentThreadId: untrack(() => appState.currentThreadId),
			pathname: untrack(() => (typeof window === 'undefined' ? '' : window.location.pathname)),
			stale,
			willOpen
		});
		if (willOpen && id) {
			void appState.openThread(id);
		}
	});
</script>

<ChatView />
