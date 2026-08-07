<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { Plus, Trash2, Pencil, Check, X, PanelLeftClose, Settings, TriangleAlert } from '@lucide/svelte';
	import { edgeSwipeSidebar } from '$lib/actions/edgeSwipeSidebar';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';

	function formatCost(c: number) {
		return c < 1 ? `$${c.toFixed(4)}` : `$${c.toFixed(2)}`;
	}

	// Delete is a two-step confirm, not a single click — a bare trash icon
	// right next to rename in a hover-only strip was too easy to fire by
	// accident. Same shape as renaming: a per-id flag swaps just that row
	// into a confirm state, everything else in the list stays interactive.
	let confirmingDeleteId = $state<string | null>(null);

	function askDelete(e: MouseEvent, id: string) {
		e.stopPropagation();
		renamingId = null;
		confirmingDeleteId = id;
	}

	function cancelDelete(e?: MouseEvent) {
		e?.stopPropagation();
		confirmingDeleteId = null;
	}

	function confirmDelete(e: MouseEvent, id: string) {
		e.stopPropagation();
		confirmingDeleteId = null;
		void appState.deleteThread(id);
	}

	// Rename is inline, mirroring how editing a user message works in the
	// chat itself (ChatTurnView) rather than a separate modal/prompt().
	let renamingId = $state<string | null>(null);
	let renameValue = $state('');

	function startRename(e: MouseEvent, id: string, currentTitle: string) {
		e.stopPropagation();
		confirmingDeleteId = null;
		renamingId = id;
		renameValue = currentTitle;
	}

	function cancelRename(e?: MouseEvent) {
		e?.stopPropagation();
		renamingId = null;
	}

	function saveRename(e?: MouseEvent) {
		e?.stopPropagation();
		if (renamingId) void appState.renameThread(renamingId, renameValue);
		renamingId = null;
	}

	function onRenameKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter') {
			e.preventDefault();
			saveRename();
		} else if (e.key === 'Escape') {
			cancelRename();
		}
	}

	// Focuses and selects the rename input the instant it's mounted, so
	// typing a new title doesn't need an extra click first.
	function focusOnMount(node: HTMLInputElement) {
		node.focus();
		node.select();
	}
</script>

<aside class="sidebar" class:open={appState.sidebarOpen} use:edgeSwipeSidebar>
	<div class="brand">
		<img class="brand-mark" src="/apple-touch-icon.png" alt="" width="22" height="22" />
		<span class="wordmark">Polaris</span>
		<button class="icon-btn collapse-btn" onclick={() => appState.toggleSidebar()} title="Collapse sidebar">
			<PanelLeftClose size={16} />
		</button>
	</div>

	<button class="btn btn-accent new-thread" onclick={() => appState.newThread()}>
		<Plus size={16} />
		New thread
	</button>

	<div class="thread-list">
		{#if appState.threads.length === 0}
			<p class="thread-empty">No threads yet. Ask something to start.</p>
		{/if}
		{#each appState.threads as thread, i (thread.id)}
			{#if renamingId === thread.id}
				<div class="thread-item renaming">
					<span class="thread-dot" aria-hidden="true"></span>
					<input
						class="thread-rename-input"
						bind:value={renameValue}
						onkeydown={onRenameKeydown}
						onclick={(e) => e.stopPropagation()}
						use:focusOnMount
					/>
					<button class="icon-btn" onclick={cancelRename} title="Cancel"><X size={13} /></button>
					<button class="icon-btn" onclick={saveRename} title="Save"><Check size={13} /></button>
				</div>
			{:else if confirmingDeleteId === thread.id}
				<div class="thread-item confirming-delete">
					<TriangleAlert size={14} />
					<span class="confirm-delete-text">Delete this thread?</span>
					<button class="icon-btn" onclick={cancelDelete} title="Cancel"><X size={13} /></button>
					<button
						class="icon-btn confirm-delete-btn"
						onclick={(e) => confirmDelete(e, thread.id)}
						title="Confirm delete"
					>
						<Check size={13} />
					</button>
				</div>
			{:else}
				<div
					class="thread-item"
					class:active={appState.currentThreadId === thread.id}
					onclick={() => appState.openThread(thread.id)}
					onkeydown={(e) => e.key === 'Enter' && appState.openThread(thread.id)}
					role="button"
					tabindex="0"
					in:fly={{ y: 8, duration: 220, delay: Math.min(i, 10) * 22, easing: quintOut }}
				>
					<span class="thread-dot" aria-hidden="true"></span>
					<div class="thread-meta">
						<div class="thread-title">{thread.title || 'Untitled'}</div>
						{#if appState.settings.showPrices}
							<div class="thread-cost">{formatCost(thread.cost_usd)}</div>
						{/if}
					</div>
					<button
						class="icon-btn rename-btn"
						onclick={(e) => startRename(e, thread.id, thread.title)}
						title="Rename"
					>
						<Pencil size={13} />
					</button>
					<button class="icon-btn delete-btn" onclick={(e) => askDelete(e, thread.id)} title="Delete">
						<Trash2 size={14} />
					</button>
				</div>
			{/if}
		{/each}
	</div>

	<div class="status">
		<span class="dot" class:connected={appState.connected}></span>
		<span class="status-text">{appState.connected ? 'connected' : 'reconnecting…'}</span>
		<button class="icon-btn settings-btn" onclick={() => appState.settings.toggle()} title="Settings">
			<Settings size={15} />
		</button>
	</div>
</aside>

<style>
	.sidebar {
		display: flex;
		width: 260px;
		flex-shrink: 0;
		flex-direction: column;
		background: var(--color-surface);
		/* A directional shadow reading as "this panel sits above the main
		   content" instead of a hairline drawn between two flat fills —
		   same light-source logic as the header/composer shadows in
		   ChatView.svelte, just cast rightward since the sidebar is the
		   elevated element here. */
		box-shadow: 6px 0 24px -16px rgba(0, 0, 0, 0.45);
		overflow: hidden;
		transition: width 0.2s ease;
	}

	/* Desktop: collapsing shrinks the column to nothing, main content
	   expands to fill — no overlay needed since there's room to spare. */
	.sidebar:not(.open) {
		width: 0;
		box-shadow: none;
	}

	.brand {
		display: flex;
		align-items: center;
		gap: 10px;
		/* On mobile the sidebar becomes a fixed, full-height overlay (see the
		   media query below) starting at the true viewport top, same as
		   .header in +page.svelte — needs the same safe-area-inset-top
		   clearance so the collapse button isn't under the iOS status bar
		   in standalone PWA mode. max() collapses to the plain 16px
		   everywhere else, where env() is 0. */
		padding: max(16px, env(safe-area-inset-top)) 16px 16px;
		white-space: nowrap;
	}

	.brand-mark {
		width: 22px;
		height: 22px;
		border-radius: 6px;
		flex-shrink: 0;
		box-shadow: var(--shadow-sm);
	}

	.wordmark {
		font-family: var(--font-wordmark);
		font-size: 18px;
		font-weight: 400;
		letter-spacing: 0.04em;
		/* Lexend body sits at 400 — the wordmark's single available weight
		   is also 400, so contrast comes from the display face itself
		   plus a hair more tracking, not from raising weight. */
	}

	.collapse-btn {
		margin-left: auto;
	}

	.new-thread {
		margin: 12px;
		white-space: nowrap;
	}

	.thread-list {
		flex: 1;
		overflow-y: auto;
		padding: 4px 8px 8px;
	}

	.thread-empty {
		margin: 12px 8px;
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.thread-item {
		display: flex;
		align-items: center;
		gap: 8px;
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		margin-bottom: 2px;
		cursor: pointer;
		transition:
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo);
	}

	.thread-item:hover {
		background: var(--color-surface-2);
	}

	/* Small leading dot that only lights up for the current thread.
	   Reads as a "you are here" pin rather than a decorative side rule. */
	.thread-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: transparent;
		flex-shrink: 0;
		transition:
			background-color 0.15s var(--ease-out-expo),
			box-shadow 0.15s var(--ease-out-expo);
	}

	/* Active state: filled accent-soft ground + bolder title weight +
	   the leading dot lit. No side stripe, no gradient — just a clearly
	   selected surface with a real weight contrast against the rest of
	   the list (400 dim titles vs. 600 lit title). */
	.thread-item.active {
		background: var(--color-accent-soft);
	}

	.thread-item.active .thread-dot {
		background: var(--color-accent);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 22%, transparent);
	}

	.thread-item.active .thread-title {
		font-weight: 600;
		color: var(--color-text);
	}

	.thread-meta {
		flex: 1;
		min-width: 0;
	}

	.thread-title {
		font-size: 13px;
		font-weight: 400;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.thread-cost {
		font-size: 11px;
		color: var(--color-text-dim);
	}

	.delete-btn {
		opacity: 0;
	}

	.thread-item:hover .delete-btn {
		opacity: 1;
	}

	.delete-btn:hover {
		color: var(--color-danger);
	}

	.rename-btn {
		opacity: 0;
	}

	.thread-item:hover .rename-btn {
		opacity: 1;
	}

	.rename-btn:hover {
		color: var(--color-text);
	}

	/* Renaming state swaps the whole row for an inline input — same
	   shape/padding as the normal row so nothing shifts, just not
	   clickable like the rest of the list. */
	.thread-item.renaming {
		cursor: default;
	}

	.thread-rename-input {
		flex: 1;
		min-width: 0;
		border: 1px solid var(--color-accent-2);
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: 3px 6px;
		font-size: 13px;
		font-family: inherit;
		color: var(--color-text);
		outline: none;
	}

	/* Same shape/padding as a normal row, same reasoning as .renaming —
	   swaps in place rather than opening a separate confirm dialog, since
	   this is a single low-stakes action (the thread is only disabled,
	   not actually erased) that doesn't warrant a full modal interruption. */
	.thread-item.confirming-delete {
		cursor: default;
		color: var(--color-danger);
	}

	.thread-item.confirming-delete :global(svg:first-child) {
		flex-shrink: 0;
	}

	.confirm-delete-text {
		flex: 1;
		min-width: 0;
		font-size: 12.5px;
		color: var(--color-text);
	}

	.confirm-delete-btn:hover {
		color: var(--color-danger);
	}

	.status {
		display: flex;
		align-items: center;
		gap: 8px;
		/* Recessed strip rather than a border line — the whole footer reads
		   as a shallow well the connection dot and settings button sit in,
		   consistent with the "carved, not drawn" treatment used on inputs
		   and readouts elsewhere (see --shadow-well in app.css). */
		box-shadow: var(--shadow-well);
		padding: 12px;
		font-size: 12px;
		color: var(--color-text-dim);
		white-space: nowrap;
	}

	.status-text {
		flex: 1;
	}

	.settings-btn {
		flex-shrink: 0;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-danger);
		flex-shrink: 0;
		transition: box-shadow 0.2s var(--ease-out-expo), background-color 0.2s var(--ease-out-expo);
	}

	.dot.connected {
		background: var(--color-accent);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 20%, transparent);
	}

	/* Mobile: the sidebar becomes a fixed-position overlay drawer that
	   slides in over the content instead of squeezing it — collapsing
	   the chat down to a sliver on a phone-width screen looks broken. */
	@media (max-width: 768px) {
		.sidebar {
			position: fixed;
			inset: 0 auto 0 0;
			width: 280px;
			z-index: 50;
			transform: translateX(-100%);
			transition: transform 0.2s ease;
			box-shadow: 2px 0 16px rgba(0, 0, 0, 0.4);
			/* Vertical scrolling of the thread list stays native; horizontal
			   panning is owned entirely by edgeSwipeSidebar (see the action
			   import above) — without this, the browser's own touch
			   handling can fight the JS-driven drag transform mid-gesture. */
			touch-action: pan-y;
		}

		.sidebar.open {
			width: 280px;
			transform: translateX(0);
		}

		.sidebar:not(.open) {
			width: 280px;
			border-right: 1px solid var(--color-border);
			transform: translateX(-100%);
		}
	}
</style>
