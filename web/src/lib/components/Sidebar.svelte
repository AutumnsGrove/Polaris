<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { Plus, Trash2, PanelLeftClose, Settings } from '@lucide/svelte';

	function formatCost(c: number) {
		return c < 1 ? `$${c.toFixed(4)}` : `$${c.toFixed(2)}`;
	}

	function handleDelete(e: MouseEvent, id: string) {
		e.stopPropagation();
		void appState.deleteThread(id);
	}
</script>

<aside class="sidebar" class:open={appState.sidebarOpen}>
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
		{#each appState.threads as thread (thread.id)}
			<div
				class="thread-item"
				class:active={appState.currentThreadId === thread.id}
				onclick={() => appState.openThread(thread.id)}
				onkeydown={(e) => e.key === 'Enter' && appState.openThread(thread.id)}
				role="button"
				tabindex="0"
			>
				<div class="thread-meta">
					<div class="thread-title">{thread.title || 'Untitled'}</div>
					{#if appState.showPrices}
						<div class="thread-cost">{formatCost(thread.cost_usd)}</div>
					{/if}
				</div>
				<button class="icon-btn delete-btn" onclick={(e) => handleDelete(e, thread.id)}>
					<Trash2 size={14} />
				</button>
			</div>
		{/each}
	</div>

	<div class="status">
		<span class="dot" class:connected={appState.connected}></span>
		<span class="status-text">{appState.connected ? 'connected' : 'reconnecting…'}</span>
		<button class="icon-btn settings-btn" onclick={() => appState.toggleSettings()} title="Settings">
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
		border-right: 1px solid var(--color-border);
		background: var(--color-surface);
		overflow: hidden;
		transition: width 0.2s ease;
	}

	/* Desktop: collapsing shrinks the column to nothing, main content
	   expands to fill — no overlay needed since there's room to spare. */
	.sidebar:not(.open) {
		width: 0;
		border-right: none;
	}

	.brand {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 16px;
		border-bottom: 1px solid var(--color-border);
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
		font-size: 17px;
		font-weight: 400;
		letter-spacing: 0.02em;
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
		padding: 8px;
		margin-bottom: 2px;
		cursor: pointer;
		transition: background-color 0.15s var(--ease-out-expo);
	}

	.thread-item:hover {
		background: var(--color-surface-2);
	}

	.thread-item.active {
		background: var(--color-surface-2);
		box-shadow: inset 2px 0 0 var(--color-accent);
	}

	.thread-meta {
		flex: 1;
		min-width: 0;
	}

	.thread-title {
		font-size: 13px;
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

	.status {
		display: flex;
		align-items: center;
		gap: 8px;
		border-top: 1px solid var(--color-border);
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
