<script lang="ts">
	import { page } from '$app/state';
	import { appState } from '$lib/state.svelte';
	import ChatView from '$lib/components/ChatView.svelte';

	// Reopens the thread named by the URL — the fix for refresh/reconnect
	// dropping back to the homescreen with no way to tell which thread was
	// open. Guarded against the id we're already showing: AppState pushes
	// this exact URL itself (see syncURL in state.svelte.ts) whenever a
	// thread it already loaded becomes current, which would otherwise
	// re-trigger openThread here and refetch/clobber an in-flight stream.
	$effect(() => {
		const id = page.params.id;
		if (id && id !== appState.currentThreadId) {
			void appState.openThread(id);
		}
	});
</script>

<ChatView />
