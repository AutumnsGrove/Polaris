<script lang="ts">
	import type { Card } from '$lib/types';
	import ImageLightbox from './ImageLightbox.svelte';

	let { cards }: { cards: Card[] } = $props();

	// Tapping a tile opens the lightbox (full-screen preview, zoomable by
	// virtue of just being bigger) instead of navigating straight to the
	// source page — the source link moves into the lightbox itself
	// (floating over the bottom of the image) so both actions stay
	// available without one silently replacing the other.
	let previewCard = $state<Card | null>(null);
</script>

<div class="gallery">
	{#each cards as card (card.url)}
		<button class="tile" onclick={() => (previewCard = card)} title={card.title}>
			<img class="tile-image" src={card.image_url} alt={card.title} loading="lazy" />
			{#if card.subtitle}
				<span class="tile-source">{card.subtitle}</span>
			{/if}
		</button>
	{/each}
</div>

{#if previewCard}
	<ImageLightbox card={previewCard} onClose={() => (previewCard = null)} />
{/if}

<style>
	/* CSS multi-column, not a uniform grid — each tile keeps its image's
	   natural aspect ratio (no forced square crop) instead of being
	   cropped to fit a fixed cell, so tile heights genuinely vary
	   top-to-bottom within a column, the masonry/bento look a fixed-size
	   grid can't produce. Bigger base column width than the original
	   96px grid tiles, per live feedback that the square-crop grid read
	   as too small/uniform for a photo gallery. */
	.gallery {
		columns: 2 180px;
		column-gap: var(--space-sm);
		margin-top: var(--space-md);
		max-width: 480px;
	}

	.tile {
		display: block;
		width: 100%;
		break-inside: avoid;
		margin-bottom: var(--space-sm);
		position: relative;
		border: none;
		padding: 0;
		font: inherit;
		cursor: pointer;
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
		height: auto;
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
