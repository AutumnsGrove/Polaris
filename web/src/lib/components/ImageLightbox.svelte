<script lang="ts">
	import { X, ExternalLink } from '@lucide/svelte';
	import type { Card } from '$lib/types';

	// Full-screen preview for an ImageGallery tile — tapping a thumbnail
	// opens this instead of navigating away, so "see it bigger" and "open
	// the source" are two separate actions instead of one link doing both.
	// Reuses the app's existing .modal-backdrop/.modal-backdrop-close
	// (dim + blur + click-to-dismiss, see app.css) for the scrim, but not
	// .modal-panel — that's styled for a form/settings card, not an image
	// viewer, so the image and its floating controls are laid out here
	// instead.
	let { card, onClose }: { card: Card; onClose: () => void } = $props();

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') onClose();
	}
</script>

<svelte:window onkeydown={onKeydown} />

<div class="modal-backdrop lightbox-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="lightbox-content">
		<img class="lightbox-image" src={card.full_image_url || card.image_url} alt={card.title} />
		<button class="lightbox-close" onclick={onClose} aria-label="Close preview">
			<X size={20} />
		</button>
		<a class="lightbox-source" href={card.url} target="_blank" rel="noreferrer">
			<ExternalLink size={13} />
			<span>{card.subtitle || card.title}</span>
		</a>
	</div>
</div>

<style>
	/* z-index/backdrop/default centering already come from .modal-backdrop. */
	.lightbox-backdrop {
		padding: var(--space-lg);
	}

	/* display: inline-block (not flex) is deliberate — this needs to
	   shrink-wrap to the <img>'s actual rendered size so the close button
	   below, positioned relative to this element, lands on the image's
	   real corner instead of floating off in empty space at the edge of
	   the (much wider) max-width box. Live-tested: a tall/narrow image
	   with the previous flex layout put the button nowhere near the
	   image at all. */
	.lightbox-content {
		position: relative;
		display: inline-block;
		max-width: min(90vw, 900px);
		max-height: 85vh;
		line-height: 0;
	}

	.lightbox-image {
		max-width: 100%;
		max-height: 85vh;
		width: auto;
		height: auto;
		object-fit: contain;
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-lg);
	}

	/* Inset onto the image's own top-right corner, not floating above it
	   with a gap — matches .lightbox-source's frosted-glass treatment
	   (dark blur, not the app's ordinary --color-surface-3 panel-button
	   look) since this sits on top of a photo, not app chrome. */
	.lightbox-close {
		position: absolute;
		top: var(--space-sm);
		right: var(--space-sm);
		display: flex;
		align-items: center;
		justify-content: center;
		width: 32px;
		height: 32px;
		border: none;
		border-radius: var(--radius-full);
		background: color-mix(in srgb, black 55%, transparent);
		backdrop-filter: blur(8px);
		-webkit-backdrop-filter: blur(8px);
		color: white;
		cursor: pointer;
	}

	.lightbox-close:hover {
		background: color-mix(in srgb, black 70%, transparent);
	}

	.lightbox-source {
		position: absolute;
		left: 50%;
		bottom: var(--space-lg);
		transform: translateX(-50%);
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		max-width: calc(100% - var(--space-2xl));
		padding: var(--space-xs) var(--space-md);
		border-radius: var(--radius-full);
		background: color-mix(in srgb, black 60%, transparent);
		backdrop-filter: blur(8px);
		-webkit-backdrop-filter: blur(8px);
		color: white;
		font-size: 12px;
		text-decoration: none;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.lightbox-source:hover {
		background: color-mix(in srgb, black 75%, transparent);
	}

	.lightbox-source :global(svg) {
		flex-shrink: 0;
	}
</style>
