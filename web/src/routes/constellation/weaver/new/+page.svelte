<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import ChatView from '$lib/components/ChatView.svelte';

	// Starts from a clean slate (in case some other thread was already
	// open before navigating here — e.g. via the browser back button) and
	// flags ChatView.svelte's stripped Weaver composer for the pre-thread
	// window, before appState.currentThread exists for it to read
	// .source off of (see appState.startingWeaverThread's own doc
	// comment). model is Constellation's own configured model (empty
	// means "use the default," same convention shooting stars follow),
	// carried over to ChatView's submit() via pendingWeaverModel since
	// ChatView takes no props.
	onMount(() => {
		appState.newThread();
		appState.startingWeaverThread = true;
		void constellationState.loadConfig().then(() => {
			appState.pendingWeaverModel = constellationState.config?.model || undefined;
		});
	});

	// Only resets if no thread ever got created here — sending the first
	// message moves the URL to /t/<id> via syncURL's raw replaceState
	// (deliberately not a real SvelteKit navigation, so this component
	// stays mounted and this never fires), so a real onDestroy only means
	// the person navigated away without ever sending anything.
	onDestroy(() => {
		if (!appState.currentThreadId) {
			appState.startingWeaverThread = false;
			appState.pendingWeaverModel = undefined;
		}
	});
</script>

<ChatView />
