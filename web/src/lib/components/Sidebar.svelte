<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { Sparkles, Plus, Trash2 } from '@lucide/svelte';

	function formatCost(c: number) {
		return c < 1 ? `$${c.toFixed(4)}` : `$${c.toFixed(2)}`;
	}

	function handleDelete(e: MouseEvent, id: string) {
		e.stopPropagation();
		void appState.deleteThread(id);
	}
</script>

<aside class="sidebar">
	<div class="brand">
		<Sparkles size={18} color="var(--color-accent)" />
		<span>Polaris</span>
	</div>

	<button class="btn new-thread" onclick={() => appState.newThread()}>
		<Plus size={16} />
		New thread
	</button>

	<div class="thread-list">
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
					<div class="thread-cost">{formatCost(thread.cost_usd)}</div>
				</div>
				<button class="icon-btn delete-btn" onclick={(e) => handleDelete(e, thread.id)}>
					<Trash2 size={14} />
				</button>
			</div>
		{/each}
	</div>

	<div class="status">
		<span class="dot" class:connected={appState.connected}></span>
		{appState.connected ? 'connected' : 'reconnecting…'}
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
	}

	.brand {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 16px;
		border-bottom: 1px solid var(--color-border);
		font-weight: 600;
	}

	.new-thread {
		margin: 12px;
	}

	.thread-list {
		flex: 1;
		overflow-y: auto;
		padding: 0 8px;
	}

	.thread-item {
		display: flex;
		align-items: center;
		gap: 8px;
		border-radius: var(--radius-sm);
		padding: 8px;
		margin-bottom: 2px;
		cursor: pointer;
	}

	.thread-item:hover {
		background: var(--color-surface-2);
	}

	.thread-item.active {
		background: var(--color-surface-2);
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
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-danger);
	}

	.dot.connected {
		background: var(--color-accent);
	}
</style>
