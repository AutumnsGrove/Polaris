<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';

	let { children } = $props();

	onMount(() => {
		appState.connect();
		void appState.loadModels();
		void appState.loadThreads();
	});
</script>

<div class="shell">
	<Sidebar />
	<main class="main">
		{@render children()}
	</main>
</div>

<style>
	.shell {
		display: flex;
		height: 100vh;
		width: 100vw;
		overflow: hidden;
	}

	.main {
		display: flex;
		flex: 1;
		flex-direction: column;
		overflow: hidden;
	}
</style>
