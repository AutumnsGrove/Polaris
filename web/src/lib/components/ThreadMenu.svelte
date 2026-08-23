<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { MoreHorizontal, Pencil, RefreshCw, Trash2, TriangleAlert, Gauge, Coins, Star } from '@lucide/svelte';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import EditTextModal from './EditTextModal.svelte';

	// Header-level equivalent of the Sidebar's per-row rename/delete —
	// same two actions, surfaced here for whoever's already looking at the
	// thread instead of hunting for its row in the list (especially once
	// the sidebar is collapsed, where there's no row to hover at all).
	// Cost/context also live here now, not as always-on header chrome —
	// they're useful to check, not useful to stare at constantly.
	let { threadId, threadTitle, favorite }: { threadId: string; threadTitle: string; favorite: boolean } =
		$props();

	function toggleFavorite() {
		void appState.favoriteThread(threadId, !favorite);
		close();
	}

	// Same threshold the backend auto-compacts at, so this doubles as a
	// warning before that happens.
	let contextPercent = $derived(
		appState.settings.contextWindowTokens > 0
			? Math.min(100, Math.round((appState.contextTokens / appState.settings.contextWindowTokens) * 100))
			: 0
	);

	let open = $state(false);
	let renaming = $state(false);
	let confirmingDelete = $state(false);
	let regeneratingTitle = $state(false);
	let rootEl: HTMLDivElement | undefined = $state();

	function toggle() {
		open = !open;
		renaming = false;
		confirmingDelete = false;
	}

	function close() {
		open = false;
		renaming = false;
		confirmingDelete = false;
	}

	// Rename opens the full EditTextModal instead of an inline dropdown
	// input — a title can run well past what the ~190px-wide dropdown
	// could ever show, especially on mobile. Closing the dropdown here
	// (not just leaving it open behind the modal) avoids stacking two
	// floating layers on top of each other.
	function startRename() {
		renaming = true;
		open = false;
	}

	function saveRename(newTitle: string) {
		void appState.renameThread(threadId, newTitle);
		close();
	}

	// Re-titles from the whole conversation instead of just the opening
	// question — left open (not close()'d) on failure so the item resets
	// to normal and the user can retry, instead of the menu just vanishing
	// with no explanation.
	async function regenerateTitle() {
		if (regeneratingTitle) return;
		regeneratingTitle = true;
		const ok = await appState.regenerateTitle(threadId);
		regeneratingTitle = false;
		if (ok) {
			close();
		} else {
			appState.showToast("Couldn't regenerate title");
		}
	}

	function askDelete() {
		confirmingDelete = true;
	}

	function confirmDelete() {
		void appState.deleteThread(threadId);
		close();
	}

	// Click-outside-to-close — the click that opened the menu also bubbles
	// to this same window listener, but rootEl wraps both the trigger
	// button and the dropdown, so a click landing anywhere inside it
	// (including the very click that just opened it) never closes itself.
	function handleWindowClick(e: MouseEvent) {
		if (open && rootEl && !rootEl.contains(e.target as Node)) close();
	}
</script>

<svelte:window onclick={handleWindowClick} />

<!-- stopPropagation here, not on each individual button inside — a click
     on e.g. "Delete" swaps the dropdown's content synchronously (renaming
     view -> confirm view), which can detach the very button that was
     clicked from the DOM before the event finishes bubbling. At that
     point rootEl.contains(e.target) sees a node no longer attached to
     anything and reports false, so handleWindowClick closed the menu on
     its own click before the confirm state ever had a chance to show.
     Stopping propagation at this single outer boundary means the click
     never reaches the window listener at all, regardless of what the
     click handler does to the DOM underneath it. -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="thread-menu" bind:this={rootEl} onclick={(e) => e.stopPropagation()}>
	<button class="icon-btn" onclick={toggle} title="Thread options" aria-label="Thread options" aria-haspopup="menu" aria-expanded={open}>
		<MoreHorizontal size={17} />
	</button>
	{#if open}
		<div class="dropdown" role="menu" in:fly={{ y: -6, duration: 150, easing: quintOut }}>
			{#if confirmingDelete}
				<div class="confirm">
					<div class="confirm-message">
						<TriangleAlert size={14} />
						<span>Delete this thread?</span>
					</div>
					<div class="confirm-actions">
						<button class="dropdown-item" onclick={close}>Cancel</button>
						<button class="dropdown-item danger" onclick={confirmDelete}>Delete</button>
					</div>
				</div>
			{:else}
				<button class="dropdown-item" onclick={toggleFavorite} role="menuitem">
					<Star size={14} fill={favorite ? 'currentColor' : 'none'} class={favorite ? 'favorited' : ''} />
					<span>{favorite ? 'Unfavorite' : 'Favorite'}</span>
				</button>
				<button class="dropdown-item" onclick={startRename} role="menuitem">
					<Pencil size={14} />
					<span>Rename</span>
				</button>
				<button
					class="dropdown-item"
					onclick={regenerateTitle}
					disabled={regeneratingTitle}
					role="menuitem"
				>
					<RefreshCw size={14} class={regeneratingTitle ? 'spin' : ''} />
					<span>{regeneratingTitle ? 'Regenerating…' : 'Regenerate title'}</span>
				</button>
				<button class="dropdown-item danger" onclick={askDelete} role="menuitem">
					<Trash2 size={14} />
					<span>Delete</span>
				</button>
				<div class="divider" role="separator"></div>
				<div class="info-row" class:hot={contextPercent >= 90}>
					<Gauge size={14} />
					<span>Context</span>
					<span class="info-value">{contextPercent}%</span>
				</div>
				<div class="info-row">
					<Coins size={14} />
					<span>Thread cost</span>
					<span class="info-value">${appState.totalCost.toFixed(4)}</span>
				</div>
			{/if}
		</div>
	{/if}
</div>
{#if renaming}
	<EditTextModal
		heading="Rename thread"
		initialValue={threadTitle}
		placeholder="Thread title"
		maxLength={200}
		onSave={saveRename}
		onCancel={close}
	/>
{/if}

<style>
	.thread-menu {
		position: relative;
		flex-shrink: 0;
	}

	.dropdown {
		position: absolute;
		top: calc(100% + 8px);
		right: 0;
		z-index: var(--z-dropdown);
		min-width: 190px;
		background: var(--color-surface-3);
		border-radius: var(--radius-md);
		padding: var(--space-sm);
		box-shadow: var(--shadow-md), var(--shadow-glass-edge);
	}

	.dropdown-item {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		width: 100%;
		border: none;
		background: transparent;
		border-radius: var(--radius-sm);
		padding: var(--space-sm) var(--space-md);
		font-size: 13px;
		font-family: inherit;
		text-align: left;
		color: var(--color-text);
		transition: background-color 0.12s var(--ease-out-expo);
	}

	.dropdown-item:hover {
		background: var(--color-surface-2);
	}

	.dropdown-item:disabled {
		opacity: 0.6;
		cursor: default;
	}

	.dropdown-item:disabled:hover {
		background: transparent;
	}

	:global(.dropdown-item .spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.dropdown-item :global(svg) {
		flex-shrink: 0;
		color: var(--color-text-dim);
	}

	.dropdown-item :global(svg.favorited) {
		color: var(--color-accent);
	}

	.dropdown-item.danger {
		color: var(--color-danger);
	}

	.dropdown-item.danger:hover {
		background: var(--color-danger-bg);
	}

	.dropdown-item.danger :global(svg) {
		color: var(--color-danger);
	}

	/* Whitespace-only rows below, not buttons — matches the "no rule
	   lines" treatment used everywhere else (see SettingsPanel.svelte's
	   section spacing), just a tonal step instead of a line. */
	.divider {
		height: 1px;
		margin: var(--space-sm) var(--space-xs);
		background: color-mix(in srgb, var(--color-border) 60%, transparent);
	}

	.info-row {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		font-size: 12.5px;
		color: var(--color-text-dim);
	}

	.info-row :global(svg) {
		flex-shrink: 0;
		color: var(--color-text-dim);
	}

	.info-row .info-value {
		margin-left: auto;
		color: var(--color-text);
		font-variant-numeric: tabular-nums;
	}

	/* Approaching the auto-compaction threshold — a quiet heads-up before
	   it fires, not an alarm; still just text weight/color, no icon change. */
	.info-row.hot .info-value {
		color: var(--color-danger);
		font-weight: 600;
	}

	.confirm {
		padding: var(--space-sm) var(--space-sm) var(--space-sm);
	}

	.confirm-message {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin-bottom: var(--space-md);
		font-size: 13px;
		color: var(--color-text);
	}

	.confirm-message :global(svg) {
		flex-shrink: 0;
		color: var(--color-danger);
	}

	.confirm-actions {
		display: flex;
		gap: var(--space-sm);
	}

	.confirm-actions .dropdown-item {
		width: auto;
		flex: 1;
		justify-content: center;
		background: var(--color-surface-2);
		box-shadow: var(--shadow-xs);
	}

	.confirm-actions .dropdown-item.danger:hover {
		background: var(--color-danger);
		color: var(--color-bg);
	}

	.confirm-actions .dropdown-item.danger:hover :global(svg) {
		color: var(--color-bg);
	}

</style>
