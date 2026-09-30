<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { goto } from '$app/navigation';
	import { PanelLeft, ChevronLeft, Ghost, BookmarkPlus, MessageCirclePlus } from '@lucide/svelte';
	import ThreadMenu from '$lib/components/ThreadMenu.svelte';
	import FieldPill from '$lib/components/FieldPill.svelte';
	import ModeToggle from '$lib/components/ModeToggle.svelte';
	import type { fieldsState } from '$lib/fields.svelte';
	import type { Thread } from '$lib/types';

	// The chat page header: back/sidebar control, thread title and Field pill on
	// the left; ghost toggle, new-thread/promote and the thread menu on the right.
	// ghostMode is bound because the toggle here decides how the next new thread
	// starts (ChatView sends it with the first message).
	let {
		ghostMode = $bindable(),
		pulsarBackRoutineId,
		isWeaverThread,
		currentThreadTitle,
		currentThread,
		activeField
	}: {
		ghostMode: boolean;
		pulsarBackRoutineId: string | null | undefined;
		isWeaverThread: boolean;
		currentThreadTitle: string;
		currentThread: Thread | null;
		activeField: ReturnType<typeof fieldsState.byId>;
	} = $props();
</script>

<header class="header">
	<div class="header-left">
		{#if pulsarBackRoutineId}
			<button
				class="icon-btn"
				onclick={() => goto(`/pulsar/${pulsarBackRoutineId}`)}
				title="Back to routine"
			>
				<ChevronLeft size={18} />
			</button>
		{:else if isWeaverThread}
			<button class="icon-btn" onclick={() => goto('/constellation')} title="Back to Constellation">
				<ChevronLeft size={18} />
			</button>
		{:else if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		{#if currentThreadTitle}
			<h1 class="thread-title" title={currentThreadTitle}>{currentThreadTitle}</h1>
		{/if}
		{#if activeField && !appState.isGhostThread}
			<FieldPill field={activeField} />
		{/if}
	</div>
	<div class="header-right">
		{#if appState.turns.length === 0 && !isWeaverThread}
			<!-- Homepage only — gated on turns.length, not
			     !appState.currentThreadId: a ghost thread DOES get a real
			     currentThreadId now (see state.svelte.ts's isGhostThread doc
			     comment), same as any other thread, so that check alone
			     would let this row and the New-thread/ThreadMenu controls
			     below both try to render at once for a ghost session's
			     first turn onward. turns.length is what actually means
			     "still the empty-composer moment", same as a normal thread.
			     Also excluded for a Weaver session (issue #94) — ghost
			     mode/model switching are both main-assistant concerns that
			     don't apply to a tool-driven Weaver turn. -->
			<button
				type="button"
				class="icon-btn"
				onclick={() => (ghostMode = !ghostMode)}
				title={ghostMode
					? 'Ghost mode is on — nothing about this chat will be saved'
					: 'Start a ghost chat — no history, no memory, no personalization'}
				aria-label="Toggle ghost mode"
				aria-pressed={ghostMode}
			>
				<Ghost size={17} class={ghostMode ? 'ghost-filled' : ''} />
			</button>
			<ModeToggle mode="assistant" />
		{/if}
		{#if appState.currentThreadId && appState.isGhostThread}
			<!-- Still ghost: no ThreadMenu (nothing to rename/favorite/
			     delete on a thread the sidebar doesn't even show yet — see
			     store.go's ghost schema comment) — just the one action that
			     matters, keeping this conversation for good. -->
			<button
				class="icon-btn"
				onclick={() => appState.promote()}
				title="Save this chat permanently"
				aria-label="Save this chat permanently"
			>
				<BookmarkPlus size={17} />
			</button>
		{:else if appState.currentThreadId}
			<button
				class="icon-btn"
				onclick={() => appState.newThread()}
				title="New thread"
				aria-label="New thread"
			>
				<MessageCirclePlus size={17} />
			</button>
			<ThreadMenu
				threadId={appState.currentThreadId}
				threadTitle={currentThreadTitle}
				favorite={currentThread?.favorite ?? false}
				createdAt={currentThread?.created_at}
				updatedAt={currentThread?.updated_at}
			/>
		{/if}
	</div>
</header>

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		/* Directional shadow instead of a rule — the header floats a hair
		   above the timeline scrolling underneath it, same light-source
		   logic as the sidebar's own right-edge shadow. */
		box-shadow: 0 8px 16px -14px rgba(0, 0, 0, 0.5);
		background: color-mix(in srgb, var(--color-surface) 60%, transparent);
		/* Installed as a standalone PWA (apple-mobile-web-app-status-bar-style:
		   black-translucent), iOS draws the status bar over the page instead
		   of pushing content down like ordinary Safari does — without this,
		   the status bar's clock/battery area sits directly on top of the
		   sidebar toggle button, making it untappable. Falls back to the
		   plain 12px on browsers without safe-area support, same pattern as
		   the composer's safe-area-inset-bottom handling below. */
		padding: max(var(--space-md), env(safe-area-inset-top)) var(--space-lg) var(--space-md);
		gap: var(--space-md);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		min-width: 0;
		flex: 1;
	}

	/* Replaces the model selector, which moved into the composer's "+"
	   sheet — clamped to 3 lines since generated titles ("Debugging a
	   Go goroutine leak in the SearXNG client") routinely run past what
	   fits on one line at a readable size, and a regenerated title
	   (drawing on the whole thread instead of just the opening message)
	   only makes that more likely, not less. */
	.thread-title {
		margin: 0;
		min-width: 0;
		font-family: var(--font-serif);
		font-size: 15px;
		font-weight: 600;
		line-height: 1.3;
		color: var(--color-text);
		display: -webkit-box;
		-webkit-line-clamp: 3;
		line-clamp: 3;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	.header-right {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		flex-shrink: 0;
	}

	/* A filled amber background here (first attempt) read as shouting for
	   a header action that isn't actually the primary one — plain
	   .icon-btn, same quiet treatment as the sidebar toggle and the "..."
	   trigger right next to it, so the icon's shape alone communicates
	   what it does instead of a competing pill of color. */

	/* Ghost mode's "on" state tints the glyph itself in the accent color —
	   no fill (unlike ThreadMenu's favorited star): the Ghost icon's eyes
	   are their own separate paths, and setting fill="currentColor" on the
	   whole icon fills them in solid along with the body, erasing the
	   detail that makes it read as a ghost at all. A plain color change
	   keeps the outline intact and still clearly reads as "on". */
	.icon-btn :global(svg.ghost-filled) {
		color: var(--color-accent);
	}
</style>
