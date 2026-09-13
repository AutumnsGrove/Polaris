<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { constellationState } from '$lib/constellation.svelte';
	import { ArrowLeft } from '@lucide/svelte';

	onMount(() => {
		void constellationState.loadWeek();
	});

	function relative(iso: string): string {
		const days = Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);
		if (days <= 0) return 'today';
		if (days === 1) return '1d';
		return `${days}d`;
	}

	function typeLabel(kind: string): string {
		if (kind === 'new') return 'New';
		if (kind === 'updated') return 'Updated';
		return 'Linked';
	}
</script>

<svelte:head>
	<title>This week — Constellation</title>
</svelte:head>

<header class="header">
	<button class="icon-btn" onclick={() => goto('/constellation')} aria-label="Back">
		<ArrowLeft size={18} />
	</button>
	<h1 class="page-title">This week</h1>
</header>

<p class="lede">
	Everything created, updated, or linked in the last 7 days — the same activity the Library
	banner is counting.
</p>

<div class="content">
	{#if constellationState.weekItems.length === 0}
		<p class="empty">Nothing yet this week.</p>
	{:else}
		<div class="week-list">
			{#each constellationState.weekItems as item, i (item.kind + '-' + item.star_id + '-' + item.timestamp + '-' + i)}
				<button
					class="week-row"
					class:updated={item.kind === 'updated'}
					class:linked={item.kind === 'linked'}
					onclick={() => goto(`/constellation/star/${item.star_id}`)}
				>
					<span class="type-dot"></span>
					<div class="week-main">
						<div class="week-title">{item.title}{item.detail ? ` ↔ ${item.detail}` : ''}</div>
						<div class="week-type">{typeLabel(item.kind)}</div>
					</div>
					<span class="week-time">{relative(item.timestamp)}</span>
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
	.lede {
		padding: var(--space-sm) var(--space-lg) var(--space-md);
		font-size: 12.5px;
		line-height: 1.5;
		color: var(--color-text-dim);
		margin: 0;
	}
	.content {
		flex: 1;
		overflow-y: auto;
		padding: 0 var(--space-lg) var(--space-xl);
	}
	.empty {
		max-width: 46ch;
		margin: var(--space-2xl) auto;
		text-align: center;
		font-size: 13.5px;
		color: var(--color-text-dim);
	}
	.week-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}
	.week-row {
		display: flex;
		align-items: center;
		width: 100%;
		gap: var(--space-md);
		padding: var(--space-md);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
		border: 1px solid var(--color-border);
		font: inherit;
		color: inherit;
		text-align: left;
		cursor: pointer;
		transition:
			transform 0.15s ease,
			border-color 0.15s ease;
	}
	.week-row:hover,
	.week-row:focus-visible {
		transform: translateY(-1px);
		border-color: var(--color-border-strong);
	}
	.type-dot {
		width: 8px;
		height: 8px;
		border-radius: var(--radius-full);
		flex-shrink: 0;
		background: var(--color-accent);
	}
	.week-row.updated .type-dot {
		background: var(--color-text-dim);
	}
	.week-row.linked .type-dot {
		background: var(--color-accent-2);
	}
	.week-main {
		flex: 1;
		min-width: 0;
	}
	.week-title {
		font-size: 14px;
		font-weight: 600;
	}
	.week-type {
		font-size: 11px;
		color: var(--color-text-dim);
		margin-top: 2px;
	}
	.week-time {
		font-size: 11px;
		color: var(--color-text-dim);
		flex-shrink: 0;
	}
</style>
