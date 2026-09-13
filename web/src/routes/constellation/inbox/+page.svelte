<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { constellationState } from '$lib/constellation.svelte';
	import StarCard from '$lib/components/StarCard.svelte';
	import { ArrowLeft } from '@lucide/svelte';

	onMount(() => {
		void constellationState.loadInbox();
	});
</script>

<svelte:head>
	<title>Inbox — Constellation</title>
</svelte:head>

<header class="header">
	<button class="icon-btn" onclick={() => goto('/constellation')} aria-label="Back">
		<ArrowLeft size={18} />
	</button>
	<h1 class="page-title">Inbox</h1>
	<span class="count">{constellationState.inboxStars.length} proposed</span>
</header>

<p class="lede">
	Weaver wasn't confident enough to save these on its own — a quick look settles them.
</p>

<div class="content">
	{#if !constellationState.inboxLoaded}
		<p class="empty">Loading…</p>
	{:else if constellationState.inboxError && constellationState.inboxStars.length === 0}
		<p class="empty">Couldn't load the inbox — check your connection and try again.</p>
	{:else if constellationState.inboxStars.length === 0}
		<p class="empty">Nothing waiting on review right now.</p>
	{:else}
		<div class="card-list">
			{#each constellationState.inboxStars as star (star.id)}
				<StarCard {star} reason={star.reasoning} onclick={() => goto(`/constellation/inbox/${star.id}`)} />
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
	.count {
		font-size: 12px;
		color: var(--color-text-dim);
		margin-left: auto;
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
	.card-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}
</style>
