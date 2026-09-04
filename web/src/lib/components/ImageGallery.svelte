<script lang="ts">
	import type { Card } from '$lib/types';

	let { cards }: { cards: Card[] } = $props();
</script>

<div class="gallery">
	{#each cards as card (card.url)}
		<a class="tile" href={card.url} target="_blank" rel="noreferrer" title={card.title}>
			<img class="tile-image" src={card.image_url} alt={card.title} loading="lazy" />
			{#if card.subtitle}
				<span class="tile-source">{card.subtitle}</span>
			{/if}
		</a>
	{/each}
</div>

<style>
	/* A grid, not a carousel — a raw multi-source photo gallery reads as
	   something you scan at a glance, unlike RecommendationsCarousel's
	   curated single-file strip. */
	.gallery {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(96px, 1fr));
		gap: var(--space-sm);
		margin-top: var(--space-md);
		max-width: 420px;
	}

	.tile {
		position: relative;
		aspect-ratio: 1;
		border-radius: var(--radius-md);
		overflow: hidden;
		background: var(--color-surface-2);
		box-shadow: var(--shadow-sm);
		transition: box-shadow 0.15s var(--ease-out-expo), transform 0.15s var(--ease-out-expo);
	}

	.tile:hover {
		box-shadow: var(--shadow-md);
		transform: translateY(-1px);
	}

	.tile-image {
		width: 100%;
		height: 100%;
		object-fit: cover;
		display: block;
	}

	.tile-source {
		position: absolute;
		left: var(--space-xs);
		bottom: var(--space-xs);
		padding: 2px 6px;
		border-radius: var(--radius-full);
		background: color-mix(in srgb, black 55%, transparent);
		color: white;
		font-size: 10px;
		max-width: calc(100% - var(--space-md));
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
</style>
