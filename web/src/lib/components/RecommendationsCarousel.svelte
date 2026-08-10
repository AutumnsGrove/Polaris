<script lang="ts">
	import type { Card } from '$lib/types';

	let { cards }: { cards: Card[] } = $props();
</script>

<div class="carousel">
	{#each cards as card (card.url)}
		<a class="card" href={card.url} target="_blank" rel="noreferrer" title={card.title}>
			{#if card.image_url}
				<img class="card-image" src={card.image_url} alt="" loading="lazy" />
			{:else}
				<div class="card-image card-image-placeholder"></div>
			{/if}
			<span class="card-title">{card.title}</span>
			{#if card.subtitle}
				<span class="card-subtitle">{card.subtitle}</span>
			{/if}
		</a>
	{/each}
</div>

<style>
	/* Horizontal scroll, not a wrap — a "carousel" reads as something you
	   swipe through, and snap-mandatory gives it the same considered,
	   deliberate feel the rest of this app's motion has (see the
	   swipeToDismiss action) rather than a loose scrollable div. */
	.carousel {
		display: flex;
		gap: 10px;
		margin-top: 10px;
		padding-bottom: 4px;
		overflow-x: auto;
		scroll-snap-type: x proximity;
		-webkit-overflow-scrolling: touch;
	}

	.carousel::-webkit-scrollbar {
		height: 4px;
	}

	.carousel::-webkit-scrollbar-thumb {
		background: var(--color-surface-3);
		border-radius: 999px;
	}

	.card {
		flex-shrink: 0;
		scroll-snap-align: start;
		display: flex;
		flex-direction: column;
		width: 108px;
		gap: 5px;
		text-decoration: none;
		color: inherit;
	}

	.card-image {
		width: 108px;
		height: 108px;
		border-radius: var(--radius-md);
		object-fit: cover;
		box-shadow: var(--shadow-sm);
		background: var(--color-surface-2);
		transition: box-shadow 0.15s var(--ease-out-expo), transform 0.15s var(--ease-out-expo);
	}

	.card:hover .card-image {
		box-shadow: var(--shadow-md);
		transform: translateY(-1px);
	}

	.card-image-placeholder {
		box-shadow: var(--shadow-well);
	}

	.card-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text);
		line-height: 1.3;
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	.card-subtitle {
		font-size: 11px;
		color: var(--color-text-dim);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
