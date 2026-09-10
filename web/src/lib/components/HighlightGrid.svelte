<script lang="ts">
	import type { Card } from '$lib/types';

	let { cards }: { cards: Card[] } = $props();
</script>

<div class="grid">
	{#each cards as card (card.url)}
		<a class="tile" href={card.url} target="_blank" rel="noreferrer" title={card.title}>
			<div class="tile-image-wrap">
				{#if card.image_url}
					<img class="tile-image" src={card.image_url} alt="" loading="lazy" />
				{:else}
					<div class="tile-image tile-image-placeholder"></div>
				{/if}
				{#if card.price}
					<span class="tile-price">{card.price}</span>
				{/if}
			</div>
			<span class="tile-title">{card.title}</span>
		</a>
	{/each}
</div>

<style>
	/* Same CSS multi-column masonry as ImageGallery.svelte — real photos
	   vary in aspect ratio same as search-result photos do, so a uniform
	   grid would either crop or leave dead space. */
	.grid {
		columns: 2 180px;
		column-gap: var(--space-sm);
		margin-top: var(--space-md);
		max-width: 480px;
	}

	/* A real <a>, not a button-into-lightbox like ImageGallery's tiles —
	   there's nothing to preview full-screen here, the point is to leave
	   for the source, same as RecommendationsCarousel's cards. */
	.tile {
		display: block;
		width: 100%;
		break-inside: avoid;
		margin-bottom: var(--space-sm);
		border-radius: var(--radius-md);
		overflow: hidden;
		background: var(--color-surface-2);
		box-shadow: var(--shadow-sm);
		text-decoration: none;
		color: inherit;
		transition: box-shadow 0.15s var(--ease-out-expo), transform 0.15s var(--ease-out-expo);
	}

	.tile:hover {
		box-shadow: var(--shadow-md);
		transform: translateY(-1px);
	}

	/* Own stacking context for the price pill, separate from .tile as a
	   whole — the tile now has a title row below the image (unlike
	   ImageGallery's tiles, which are image-only), so the pill has to
	   anchor to just the image area or it floats over the title text
	   instead of the photo. */
	.tile-image-wrap {
		position: relative;
	}

	.tile-image {
		width: 100%;
		height: auto;
		display: block;
	}

	.tile-image-placeholder {
		aspect-ratio: 1;
		box-shadow: var(--shadow-well);
	}

	/* Same pill treatment as ImageGallery's .tile-source, but showing price
	   instead of a source domain — only rendered when the item actually
	   has one, so a non-shopping highlight call with no price just shows a
	   plain tile. */
	.tile-price {
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

	.tile-title {
		display: block;
		padding: var(--space-xs) var(--space-sm);
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text);
		line-height: 1.3;
	}
</style>
