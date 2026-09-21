<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { constellationState } from '$lib/constellation.svelte';
	import { ArrowLeft } from '@lucide/svelte';

	onMount(() => {
		void constellationState.loadWeaverThreads();
	});

	// Same relative-time convention as /constellation/week's own list.
	function relative(iso: string): string {
		const days = Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);
		if (days <= 0) return 'today';
		if (days === 1) return '1d';
		return `${days}d`;
	}
</script>

<svelte:head>
	<title>Weaver sessions — Constellation</title>
</svelte:head>

<header class="header">
	<button class="icon-btn" onclick={() => goto('/constellation')} aria-label="Back">
		<ArrowLeft size={18} />
	</button>
	<h1 class="page-title">Weaver sessions</h1>
</header>

<div class="content">
	{#if !constellationState.weaverThreadsLoaded}
		<p class="constellation-empty">Loading…</p>
	{:else if constellationState.weaverThreadsError && constellationState.weaverThreads.length === 0}
		<p class="constellation-empty">Couldn't load Weaver sessions — check your connection and try again.</p>
	{:else if constellationState.weaverThreads.length === 0}
		<p class="constellation-empty">
			No Weaver sessions yet — start one from Constellation, or wait for the next shooting star.
		</p>
	{:else}
		<div class="session-list">
			{#each constellationState.weaverThreads as thread (thread.id)}
				<button class="session-row" onclick={() => goto(`/t/${thread.id}`)}>
					<div class="session-text">
						<span class="session-title">{thread.title || 'Untitled session'}</span>
						<span class="session-meta">{relative(thread.updated_at)}</span>
					</div>
					<span class="badge" class:badge-auto={thread.is_automatic}>
						{thread.is_automatic ? 'auto' : 'manual'}
					</span>
				</button>
			{/each}
		</div>
	{/if}
</div>

<style>
	.header {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: max(var(--space-lg), env(safe-area-inset-top)) var(--space-lg) var(--space-xs);
		box-shadow: var(--shadow-well);
	}
	.page-title {
		margin: 0;
		font-family: var(--font-serif);
		font-size: 19px;
		font-weight: 700;
	}
	.content {
		flex: 1;
		overflow-y: auto;
		padding: var(--space-md) var(--space-lg) var(--space-xl);
	}
	.session-list {
		display: flex;
		flex-direction: column;
	}
	.session-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-xs);
		border: none;
		border-bottom: 1px solid var(--color-border);
		background: transparent;
		color: inherit;
		font-family: inherit;
		text-align: left;
		cursor: pointer;
	}
	.session-row:first-child {
		border-top: 1px solid var(--color-border);
	}
	.session-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}
	.session-title {
		font-size: 13.5px;
		font-weight: 500;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.session-meta {
		font-size: 11.5px;
		color: var(--color-text-dim);
	}
	.badge {
		flex-shrink: 0;
		font-size: 10px;
		font-weight: 600;
		padding: 3px var(--space-sm);
		border-radius: var(--radius-full);
		background: var(--color-accent-soft);
		color: var(--color-accent);
		white-space: nowrap;
	}
	.badge-auto {
		background: var(--color-surface-3);
		color: var(--color-text-dim);
	}
</style>
