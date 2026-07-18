<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import SettingsPanel from '$lib/components/SettingsPanel.svelte';

	let { children } = $props();

	// iOS Safari's dvh support has been inconsistent across versions and
	// doesn't always update as the toolbar animates open/closed — the
	// reliable fix is tracking window.visualViewport directly and
	// exposing it as a CSS variable, which .shell falls back to dvh
	// without (desktop browsers, or before this runs).
	function updateViewportHeight() {
		const vv = window.visualViewport;
		const height = vv ? vv.height : window.innerHeight;
		document.documentElement.style.setProperty('--app-height', `${height}px`);
	}

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

		updateViewportHeight();
		window.visualViewport?.addEventListener('resize', updateViewportHeight);
		window.visualViewport?.addEventListener('scroll', updateViewportHeight);
		window.addEventListener('orientationchange', updateViewportHeight);

		return () => {
			window.visualViewport?.removeEventListener('resize', updateViewportHeight);
			window.visualViewport?.removeEventListener('scroll', updateViewportHeight);
			window.removeEventListener('orientationchange', updateViewportHeight);
		};
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
		/* --app-height (set from window.visualViewport in +layout.svelte)
		   is the reliable source of truth on iOS Safari, where the
		   collapsing toolbar makes 100vh too tall and 100dvh support has
		   been inconsistent across versions. Falls back to dvh before
		   that JS runs, then to vh on anything without dvh support. */
		height: var(--app-height, 100dvh);
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
