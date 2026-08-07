<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { MoreHorizontal, Pencil, Trash2, Check, X } from '@lucide/svelte';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';

	// Header-level equivalent of the Sidebar's per-row rename/delete —
	// same two actions, surfaced here for whoever's already looking at the
	// thread instead of hunting for its row in the list (especially once
	// the sidebar is collapsed, where there's no row to hover at all).
	let { threadId, threadTitle }: { threadId: string; threadTitle: string } = $props();

	let open = $state(false);
	let renaming = $state(false);
	let renameValue = $state('');
	let rootEl: HTMLDivElement | undefined = $state();

	function toggle() {
		open = !open;
		renaming = false;
	}

	function close() {
		open = false;
		renaming = false;
	}

	function startRename() {
		renameValue = threadTitle;
		renaming = true;
	}

	function saveRename() {
		if (renameValue.trim()) void appState.renameThread(threadId, renameValue);
		close();
	}

	function onRenameKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter') {
			e.preventDefault();
			saveRename();
		} else if (e.key === 'Escape') {
			close();
		}
	}

	function handleDelete() {
		void appState.deleteThread(threadId);
		close();
	}

	function focusOnMount(node: HTMLInputElement) {
		node.focus();
		node.select();
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

<div class="thread-menu" bind:this={rootEl}>
	<button class="icon-btn" onclick={toggle} title="Thread options" aria-label="Thread options" aria-haspopup="menu" aria-expanded={open}>
		<MoreHorizontal size={17} />
	</button>
	{#if open}
		<div class="dropdown" role="menu" in:fly={{ y: -6, duration: 150, easing: quintOut }}>
			{#if renaming}
				<div class="rename-row">
					<input
						class="rename-input"
						bind:value={renameValue}
						onkeydown={onRenameKeydown}
						use:focusOnMount
					/>
					<button class="icon-btn" onclick={close} title="Cancel"><X size={13} /></button>
					<button class="icon-btn" onclick={saveRename} title="Save"><Check size={13} /></button>
				</div>
			{:else}
				<button class="dropdown-item" onclick={startRename} role="menuitem">
					<Pencil size={14} />
					<span>Rename</span>
				</button>
				<button class="dropdown-item danger" onclick={handleDelete} role="menuitem">
					<Trash2 size={14} />
					<span>Delete</span>
				</button>
			{/if}
		</div>
	{/if}
</div>

<style>
	.thread-menu {
		position: relative;
		flex-shrink: 0;
	}

	.dropdown {
		position: absolute;
		top: calc(100% + 8px);
		right: 0;
		z-index: 60;
		min-width: 160px;
		background: var(--color-surface-3);
		border-radius: var(--radius-md);
		padding: 6px;
		box-shadow: var(--shadow-md), var(--shadow-glass-edge);
	}

	.dropdown-item {
		display: flex;
		align-items: center;
		gap: 9px;
		width: 100%;
		border: none;
		background: transparent;
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		font-size: 13px;
		font-family: inherit;
		text-align: left;
		color: var(--color-text);
		transition: background-color 0.12s var(--ease-out-expo);
	}

	.dropdown-item:hover {
		background: var(--color-surface-2);
	}

	.dropdown-item :global(svg) {
		flex-shrink: 0;
		color: var(--color-text-dim);
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

	.rename-row {
		display: flex;
		align-items: center;
		gap: 4px;
		padding: 2px;
	}

	.rename-input {
		flex: 1;
		min-width: 0;
		border: 1px solid var(--color-accent-2);
		background: var(--color-surface-2);
		border-radius: var(--radius-sm);
		padding: 6px 8px;
		font-size: 13px;
		font-family: inherit;
		color: var(--color-text);
		outline: none;
	}
</style>
