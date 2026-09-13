<script lang="ts">
	import { onMount } from 'svelte';
	import { constellationState } from '$lib/constellation.svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { X, Info } from '@lucide/svelte';

	let { onClose }: { onClose: () => void } = $props();

	onMount(() => {
		void constellationState.loadStats(30);
	});

	const s = $derived(constellationState.stats);
	const byStatus = $derived(s?.star_counts_by_status ?? {});
	const byTool = $derived(s?.tool_call_counts ?? {});
	const byReview = $derived(s?.review_action_counts ?? {});
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Constellation Usage">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>Constellation Usage</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		{#if !constellationState.statsLoaded}
			<p class="empty">Loading…</p>
		{:else if constellationState.statsError || !s}
			<p class="empty">Couldn't load usage stats — check your connection and try again.</p>
		{:else}
			<div class="usage-big-cost">
				<div class="amount">${s.period_cost_usd.toFixed(2)}</div>
				<div class="caption">last {s.period_days} days &middot; ${s.total_cost_usd.toFixed(2)} all-time</div>
			</div>

			<div class="usage-section-label">Activity</div>
			<div class="usage-stat-group">
				<div class="usage-stat-row"><span class="label">Shooting stars run</span><span class="value">{s.shooting_star_count}</span></div>
				<div class="usage-stat-row">
					<span class="label">Stars &mdash; auto / confirmed</span>
					<span class="value">{byStatus['auto'] ?? 0} / {byStatus['confirmed'] ?? 0}</span>
				</div>
				<div class="usage-stat-row">
					<span class="label">Stars &mdash; proposed / rejected</span>
					<span class="value">{byStatus['proposed'] ?? 0} / {byStatus['rejected'] ?? 0}</span>
				</div>
				<div class="usage-stat-row"><span class="label">Links created</span><span class="value">{s.links_created_count}</span></div>
			</div>

			<div class="usage-section-label">Tool calls</div>
			<div class="usage-stat-group">
				{#each ['search_stars', 'read_star', 'create_star', 'update_star', 'link_stars'] as tool (tool)}
					<div class="usage-stat-row"><span class="label">{tool}</span><span class="value">{byTool[tool] ?? 0}</span></div>
				{/each}
			</div>

			<div class="usage-section-label">Review &amp; health</div>
			<div class="usage-stat-group">
				<div class="usage-stat-row">
					<span class="label">Approved / Refined / Discarded</span>
					<span class="value">{byReview['approved'] ?? 0} / {byReview['refined'] ?? 0} / {byReview['discarded'] ?? 0}</span>
				</div>
				<div class="usage-stat-row warn">
					<span class="label">Ran out of turns (25 cap)</span>
					<span class="value">{s.max_turns_count}</span>
				</div>
				<div class="usage-stat-row" class:attention={s.needs_retry_count > 0}>
					<span class="label">Waiting on retry</span>
					<span class="value">{s.needs_retry_count}</span>
				</div>
			</div>

			<div class="separate-note">
				<Info size={15} class="note-icon" />
				<span>
					Separate from Polaris's own usage — this cost never appears in the main Usage panel's
					total, and vice versa.
				</span>
			</div>
		{/if}
	</div>
</div>

<style>
	/* .usage-big-cost/.usage-section-label/.usage-stat-group/.usage-stat-row
	   live in app.css — shared with SettingsPanel.svelte's own Usage
	   section, one visual treatment for "usage stats" instead of two
	   copies to keep in sync by hand. */
	.empty {
		text-align: center;
		font-size: 13.5px;
		color: var(--color-text-dim);
		padding: var(--space-2xl) 0;
	}
	.separate-note {
		display: flex;
		gap: var(--space-md);
		padding: var(--space-md);
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		font-size: 11.5px;
		line-height: 1.5;
		color: var(--color-text-dim);
		margin-top: var(--space-lg);
	}
	.separate-note :global(.note-icon) {
		color: var(--color-text-dim);
		flex-shrink: 0;
	}
</style>
