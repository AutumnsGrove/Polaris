<script lang="ts">
	import type { Card } from '$lib/types';
	import { ChevronLeft, ChevronRight, Sparkles } from '@lucide/svelte';

	let { cards }: { cards: Card[] } = $props();

	// One pick in focus at a time, with real prev/next + dot paging — not
	// scroll-snap — so a curated set of up to five reads as considered
	// rather than a wall of options (see docs/plans and issue #75). Index
	// resets whenever the card set itself changes (a new highlight call in
	// the same turn, or a different turn's cards replacing these) rather
	// than carrying over a position that may no longer exist.
	let index = $state(0);
	$effect(() => {
		cards;
		index = 0;
	});

	function prev() {
		if (index > 0) index -= 1;
	}
	function next() {
		if (index < cards.length - 1) index += 1;
	}

	function hostname(url: string): string {
		try {
			return new URL(url).hostname.replace(/^www\./, '');
		} catch {
			return url;
		}
	}

	// Same formula as app.css's --color-cat-* tokens: fixed lightness/chroma,
	// hue alone varies. Hashing the card's own domain (never a model-filled
	// field) means a given source always lands on the same hue, with no
	// network fetch — fetching a real favicon would leak which sites the
	// user is looking at to a third party, the exact leak this app's
	// self-hosted SearXNG exists to avoid.
	function hashHue(domain: string): number {
		let h = 0;
		for (let i = 0; i < domain.length; i++) {
			h = (h * 31 + domain.charCodeAt(i)) >>> 0;
		}
		return h % 360;
	}

	function fallbackBackground(url: string): string {
		const hue = hashHue(hostname(url));
		return `radial-gradient(120% 120% at 30% 20%, color-mix(in srgb, oklch(78% 0.15 ${hue}) 30%, transparent), transparent 60%), var(--color-surface-3)`;
	}

	let card = $derived(cards[index]);
</script>

<div class="carousel">
	<div class="stack">
		{#if cards.length > 2}
			<div class="depth depth-2"></div>
		{/if}
		{#if cards.length > 1}
			<div class="depth depth-1"></div>
		{/if}

		<div class="card">
			<div class="media">
				{#if card.image_url}
					<img src={card.image_url} alt="" loading="lazy" />
				{:else}
					<div class="fallback" style:background={fallbackBackground(card.url)}>
						<Sparkles size={38} />
					</div>
				{/if}
				{#if card.price}
					<span class="price">{card.price}</span>
				{/if}
			</div>
			<div class="content">
				<div class="title">{card.title}</div>
				{#if card.why}
					<div class="why">{card.why}</div>
				{/if}
				<a class="open" href={card.url} target="_blank" rel="noreferrer">Open ↗</a>
			</div>
		</div>
	</div>

	{#if cards.length > 1}
		<div class="nav">
			<button class="nav-btn" onclick={prev} disabled={index === 0} aria-label="Previous pick">
				<ChevronLeft size={18} />
			</button>
			<div class="dots">
				{#each cards as c, i (c.url)}
					<button
						class="dot"
						class:active={i === index}
						onclick={() => (index = i)}
						aria-label={`Go to pick ${i + 1} of ${cards.length}`}
					></button>
				{/each}
			</div>
			<button
				class="nav-btn"
				onclick={next}
				disabled={index === cards.length - 1}
				aria-label="Next pick"
			>
				<ChevronRight size={18} />
			</button>
		</div>
		<div class="position">{index + 1} of {cards.length}</div>
	{/if}
</div>

<style>
	.carousel {
		margin-top: var(--space-md);
		max-width: 342px;
	}

	/* .stack gets its height from .card (the only sibling in normal flow —
	   .depth-* are absolutely positioned) so the faux layers behind it
	   always match however tall .card ends up, including a card with an
	   unusually long why. */
	.stack {
		position: relative;
	}

	/* One step further from --color-bg than the card itself
	   (--color-surface-2) — in light theme, bg and surface-2 sit only 2
	   lightness points apart, so a depth layer at surface-2 all but
	   disappears there even though the same choice reads fine against
	   dark theme's wider gaps. */
	.depth {
		position: absolute;
		left: var(--space-md);
		right: calc(-1 * var(--space-md));
		bottom: 0;
		background: var(--color-surface-3);
		border-radius: var(--radius-xl);
	}

	.depth-1 {
		top: var(--space-sm);
		opacity: 0.75;
		transform: rotate(0.6deg);
	}

	.depth-2 {
		top: var(--space-lg);
		opacity: 0.5;
		transform: rotate(1.2deg);
	}

	.card {
		position: relative;
		background: var(--color-surface-2);
		border-radius: var(--radius-xl);
		box-shadow: var(--shadow-md);
		overflow: hidden;
	}

	.media {
		position: relative;
		width: 100%;
		height: 190px;
	}

	.media img {
		width: 100%;
		height: 100%;
		object-fit: cover;
		display: block;
	}

	.fallback {
		width: 100%;
		height: 100%;
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--color-text);
		opacity: 0.55;
	}

	.price {
		position: absolute;
		left: var(--space-md);
		bottom: var(--space-md);
		padding: 3px 9px;
		border-radius: var(--radius-full);
		background: color-mix(in srgb, black 55%, transparent);
		color: white;
		font-size: 12px;
		font-weight: 600;
	}

	.content {
		padding: var(--space-lg) var(--space-lg) var(--space-xl);
	}

	.title {
		font-size: 17px;
		font-weight: 700;
		line-height: 1.3;
		color: var(--color-text);
	}

	/* No clamp, no max-height — the old masonry grid needed roughly uniform
	   tile heights for its 2-column layout to hold together, so it hard-
	   clipped this text at 2 lines (see HighlightGrid's removal in this
	   commit). A single card in focus has nothing beside it to break, so it
	   can just be as tall as the model's why actually needs. */
	.why {
		font-size: 13.5px;
		line-height: 1.55;
		color: var(--color-text-dim);
		margin-top: var(--space-sm);
	}

	.open {
		display: inline-block;
		margin-top: var(--space-md);
		font-size: 13px;
		font-weight: 600;
		color: var(--color-accent-strong);
		text-decoration: none;
	}

	.open:hover {
		color: var(--color-accent);
	}

	.nav {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-md);
		margin-top: var(--space-lg);
	}

	/* Bigger and more visible than the app's ordinary .icon-btn — this is
	   the primary way to move through the set, not a secondary action like
	   copy/regenerate, so it needs to read as a real, tappable control on a
	   phone (>=44px touch target) rather than a quiet icon-only affordance. */
	.nav-btn {
		width: 40px;
		height: 40px;
		border-radius: var(--radius-full);
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		color: var(--color-text);
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		transition: background-color 0.15s var(--ease-out-expo);
	}

	.nav-btn:hover:not(:disabled) {
		background: var(--color-surface-3);
	}

	.nav-btn:disabled {
		opacity: 0.35;
		cursor: default;
	}

	.dots {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.dot {
		width: 6px;
		height: 6px;
		padding: 0;
		border: none;
		border-radius: var(--radius-full);
		background: var(--color-border-strong);
		cursor: pointer;
		transition: all 0.2s var(--ease-out-expo);
	}

	.dot.active {
		width: 16px;
		background: var(--color-accent);
	}

	.position {
		text-align: center;
		margin-top: var(--space-xs);
		font-size: 11px;
		color: var(--color-text-dim);
		font-variant-numeric: tabular-nums;
	}
</style>
