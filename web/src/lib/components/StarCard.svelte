<script lang="ts">
	import { BookOpen, Cpu, Globe2, User, ChevronRight } from '@lucide/svelte';
	import type { Star } from '$lib/types';

	let {
		star,
		reason,
		onclick
	}: {
		star: Star;
		// reason: shown instead of star.summary on the Inbox (screen 5) —
		// shooting_star_candidates.reasoning, "why Weaver flagged this"
		// rather than a confident-sounding summary of unconfirmed content.
		reason?: string;
		onclick: () => void;
	} = $props();

	// categoryIcon: category is free text with no fixed list (Weaver's own
	// choice, per the plan doc), so this is a small keyword heuristic with
	// a generic fallback — same shape as /daily's blockIcons lookup, not a
	// real enum mapping. is_personal overrides this entirely (a User icon
	// regardless of category), matching the mockup's personal tile.
	const iconFor = (category: string) => {
		const c = category.toLowerCase();
		if (c.includes('tech')) return Cpu;
		if (c.includes('book') || c.includes('idea')) return BookOpen;
		if (c.includes('science') || c.includes('history') || c.includes('space')) return Globe2;
		return BookOpen;
	};
	const Icon = $derived(star.is_personal ? User : iconFor(star.category));

	const relativeTime = $derived(formatRelative(star.updated_at));

	function formatRelative(iso: string): string {
		const diffMs = Date.now() - new Date(iso).getTime();
		const days = Math.floor(diffMs / 86_400_000);
		if (days <= 0) return 'today';
		if (days === 1) return '1d';
		if (days < 7) return `${days}d`;
		const weeks = Math.floor(days / 7);
		if (weeks < 5) return `${weeks}w`;
		return `${Math.floor(days / 30)}mo`;
	}
</script>

<div
	class="star-card"
	class:personal={star.is_personal}
	{onclick}
	onkeydown={(e) => e.key === 'Enter' && onclick()}
	role="button"
	tabindex="0"
>
	<div class="tile">
		<Icon size={18} />
	</div>
	<div class="card-main">
		<div class="card-top">
			<span class="card-title">{star.title}</span>
			{#if star.is_personal}
				<span class="badge-personal">Personal</span>
			{:else if !reason}
				<span class="card-time">{relativeTime}</span>
			{/if}
		</div>
		<div class="card-sub" class:reason>{reason ?? star.summary}</div>
		<div class="card-bottom">
			{#each star.tags.slice(0, 3) as tag (tag)}
				<span class="tag">{tag}</span>
			{/each}
			<span class="card-chev"><ChevronRight size={14} /></span>
		</div>
	</div>
</div>

<style>
	.star-card {
		display: flex;
		gap: var(--space-md);
		padding: var(--space-md);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
		border: 1px solid var(--color-border);
		cursor: pointer;
		text-align: left;
		transition:
			transform 0.15s ease,
			border-color 0.15s ease;
	}
	.star-card:hover,
	.star-card:focus-visible {
		transform: translateY(-1px);
		border-color: var(--color-border-strong);
	}

	.tile {
		width: 38px;
		height: 38px;
		border-radius: var(--radius-md);
		flex-shrink: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--color-accent-soft);
		color: var(--color-accent);
	}
	.star-card.personal .tile {
		background: color-mix(in srgb, var(--color-personal) 16%, transparent);
		color: var(--color-personal);
	}
	.star-card.personal {
		border-color: color-mix(in srgb, var(--color-personal) 32%, var(--color-border));
	}

	.card-main {
		flex: 1;
		min-width: 0;
	}
	.card-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm);
	}
	.card-title {
		font-size: 14px;
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.card-time {
		font-size: 11px;
		color: var(--color-text-dim);
		flex-shrink: 0;
	}
	.badge-personal {
		font-size: 10px;
		font-weight: 600;
		padding: 2px var(--space-sm);
		border-radius: var(--radius-full);
		background: color-mix(in srgb, var(--color-personal) 18%, transparent);
		color: var(--color-personal);
		flex-shrink: 0;
	}
	.card-sub {
		font-size: 12px;
		color: var(--color-text-dim);
		line-height: 1.4;
		margin: 3px 0 var(--space-sm);
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	.card-sub.reason {
		font-style: italic;
	}
	.card-bottom {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
	}
	.tag {
		font-size: 10px;
		padding: 2px var(--space-sm);
		border-radius: var(--radius-full);
		background: var(--color-surface-3);
		color: var(--color-text-dim);
	}
	.card-chev {
		margin-left: auto;
		color: var(--color-text-dim);
		display: flex;
		align-items: center;
	}
</style>
