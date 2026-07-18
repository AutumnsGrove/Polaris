<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import SettingsPanel from '$lib/components/SettingsPanel.svelte';

	let { children } = $props();

	onMount(() => {
		appState.connect();
		void appState.loadModels();
		void appState.loadThreads();
		void appState.loadSettings();

		// Start collapsed on phones (the primary use case) so the chat is
		// what you see first, not a full-screen thread list.
		if (window.innerWidth < 768) {
			appState.sidebarOpen = false;
		}
	});
</script>

<div class="shell">
	<Sidebar />
	{#if appState.sidebarOpen}
		<button
			class="backdrop"
			onclick={() => appState.closeSidebar()}
			aria-label="Close sidebar"
		></button>
	{/if}
	<main class="main">
		{@render children()}
	</main>
</div>

{#if appState.settingsOpen}
	<SettingsPanel />
{/if}

<style>
	.shell {
		display: flex;
		height: 100vh;
		width: 100vw;
		overflow: hidden;
		position: relative;
	}

	.main {
		display: flex;
		flex: 1;
		flex-direction: column;
		overflow: hidden;
		min-width: 0;
	}

	/* Backdrop only exists visually on narrow viewports, where the
	   sidebar becomes an overlay drawer instead of an inline column. */
	.backdrop {
		display: none;
		border: none;
		padding: 0;
		cursor: default;
	}

	@media (max-width: 768px) {
		.backdrop {
			display: block;
			position: fixed;
			inset: 0;
			background: rgba(0, 0, 0, 0.5);
			z-index: 40;
		}
	}
</style>
