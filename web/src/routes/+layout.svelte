<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import SettingsPanel from '$lib/components/SettingsPanel.svelte';
	import Toast from '$lib/components/Toast.svelte';

	let { children } = $props();

	// iOS Safari's dvh support has been inconsistent across versions and
	// doesn't always update as the toolbar animates open/closed — the
	// reliable fix is tracking window.visualViewport directly and
	// exposing it as a CSS variable, which .shell falls back to dvh
	// without (desktop browsers, or before this runs).
	//
	// height alone isn't enough: when the on-screen keyboard opens, iOS
	// also shifts visualViewport.offsetTop as it scrolls the focused
	// input into view — and that scroll happens at the visual-viewport
	// layer, independent of the page's own DOM scroll position, so
	// body's position:fixed (which only stops DOM scrolling) does
	// nothing to prevent it. The composer ended up pinned near the top
	// of the screen with a big blank gap above it: .shell was correctly
	// *sized* to the shrunk height, but never *repositioned* to follow
	// where iOS had scrolled the visible area to. Exposing offsetTop as
	// a second variable and applying it as a translateY below cancels
	// that shift out.
	function updateViewportHeight() {
		const vv = window.visualViewport;
		const height = vv ? vv.height : window.innerHeight;
		const offsetTop = vv ? vv.offsetTop : 0;
		document.documentElement.style.setProperty('--app-height', `${height}px`);
		document.documentElement.style.setProperty('--app-offset-top', `${offsetTop}px`);

		// iOS has a known bug where offsetTop can fail to reset to 0 once
		// the keyboard fully closes (height back to ~the full window
		// height) — nudging the scroll position by 1px and back forces a
		// recompute. Gated on the keyboard actually being closed so this
		// never fights a still-open one.
		if (vv && offsetTop !== 0 && height >= window.innerHeight - 1) {
			window.scrollBy(0, -1);
			window.scrollBy(0, 1);
		}
	}

	onMount(() => {
		appState.connect();
		void appState.loadModels();
		void appState.loadThreads();
		void appState.settings.load();
		void appState.settings.checkUpdateStatus(() => appState.busy);

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

{#if appState.settings.open}
	<SettingsPanel />
{/if}

<Toast />

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
		/* --app-offset-top compensates for visualViewport.offsetTop — iOS
		   scrolls the focused input into view at the visual-viewport
		   layer when the keyboard opens, which is independent of the
		   page's own DOM scroll position (body's position:fixed only
		   stops the latter). Without this, .shell was sized correctly to
		   the shrunk visible height but never repositioned to follow
		   where iOS had scrolled the visible area to, leaving the
		   composer pinned near the screen's top with a blank gap above
		   it. translateY re-aligns .shell with wherever iOS actually put
		   the visible region. */
		transform: translateY(var(--app-offset-top, 0px));
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
			backdrop-filter: blur(4px);
			-webkit-backdrop-filter: blur(4px);
			z-index: var(--z-backdrop);
		}

		/* The sidebar's opening edge-swipe (see edgeSwipeSidebar.ts) starts
		   its first touch here, on the main chat view, not on the sidebar
		   itself — it's off-screen until the drag pulls it in. Restricting
		   horizontal panning keeps that initial touch from being read as a
		   native scroll gesture instead. */
		.shell {
			touch-action: pan-y;
		}
	}
</style>
