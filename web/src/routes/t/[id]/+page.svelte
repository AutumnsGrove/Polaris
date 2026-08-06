<script lang="ts">
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { appState } from '$lib/state.svelte';
	import ChatView from '$lib/components/ChatView.svelte';

	// Reopens the thread named by the URL — the fix for refresh/reconnect
	// dropping back to the homescreen with no way to tell which thread was
	// open. Only actually re-runs when page.params.id itself changes (a
	// real navigation to this route, e.g. a hard reload) — appState.
	// currentThreadId is read via untrack() specifically so it does NOT
	// become a dependency of this effect. Without that, switching threads
	// from the sidebar (which changes currentThreadId but never touches
	// page.params, since AppState.syncURL updates the address bar via raw
	// history.replaceState, not a real SvelteKit navigation — see its doc
	// comment) would re-run this effect with the same stale id and
	// immediately call openThread(id) again, snapping the view straight
	// back to whatever thread this route was first loaded with.
	$effect(() => {
		const id = page.params.id;
		if (id && untrack(() => id !== appState.currentThreadId)) {
			void appState.openThread(id);
		}
	});
</script>

<ChatView />
