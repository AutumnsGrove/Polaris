<script lang="ts">
	import { onMount } from 'svelte';
	import { pulsarState } from '$lib/pulsar.svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { X } from '@lucide/svelte';

	let { onClose }: { onClose: () => void } = $props();

	onMount(() => {
		void pulsarState.loadStats(30);
	});

	const s = $derived(pulsarState.stats);

	// Pulsar routines can call the same full tool catalog a live chat
	// turn can (unlike Weaver's fixed six tools — see
	// ConstellationUsageModal, which lists a hardcoded set) — so this is
	// a dynamic, count-sorted list rather than a fixed row per tool,
	// same reasoning as SettingsPanel.svelte's own searchProviderCounts.
	const byTool = $derived(
		s ? Object.entries(s.tool_call_counts).sort((a, b) => b[1] - a[1]) : []
	);
	const failureRate = $derived(s && s.pulse_count > 0 ? (s.failed_pulse_count / s.pulse_count) * 100 : 0);
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Pulsar Usage">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>Pulsar Usage</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		{#if !pulsarState.statsLoaded}
			<p class="empty">Loading…</p>
		{:else if pulsarState.statsError || !s}
			<p class="empty">Couldn't load usage stats — check your connection and try again.</p>
		{:else}
			<div class="usage-big-cost">
				<div class="amount">${s.period_cost_usd.toFixed(2)}</div>
				<div class="caption">last {s.period_days} days &middot; ${s.total_cost_usd.toFixed(2)} all-time</div>
			</div>

			<div class="usage-section-label">Activity</div>
			<div class="usage-stat-group">
				<div class="usage-stat-row">
					<span class="label">Active / archived routines</span>
					<span class="value">{s.active_routine_count} / {s.archived_routine_count}</span>
				</div>
				<div class="usage-stat-row">
					<span class="label">Pulses fired</span>
					<span class="value">{s.pulse_count}</span>
				</div>
				<div class="usage-stat-row" class:warn={s.failed_pulse_count > 0}>
					<span class="label">Failed</span>
					<span class="value">{s.failed_pulse_count} ({failureRate.toFixed(1)}%)</span>
				</div>
			</div>

			<div class="usage-section-label">Tool calls</div>
			<div class="usage-stat-group">
				{#if byTool.length === 0}
					<div class="usage-stat-row"><span class="label">None yet</span></div>
				{:else}
					{#each byTool as [tool, count] (tool)}
						<div class="usage-stat-row">
							<span class="label">{tool}</span>
							<span class="value">{count} ({s.tool_error_counts[tool] ?? 0} errored)</span>
						</div>
					{/each}
				{/if}
			</div>

			<div class="usage-section-label">Research loop steering</div>
			<div class="usage-stat-group">
				<div class="usage-stat-row warn">
					<span class="label">Ran out of turn budget</span>
					<span class="value">{s.max_turns_wrapup_count}</span>
				</div>
				<div class="usage-stat-row">
					<span class="label">Check-in nudges</span>
					<span class="value">{s.check_in_count}</span>
				</div>
				<div class="usage-stat-row">
					<span class="label">Stale-streak warnings</span>
					<span class="value">{s.stale_streak_count}</span>
				</div>
			</div>
		{/if}
	</div>
</div>

<style>
	/* .usage-big-cost/.usage-section-label/.usage-stat-group/.usage-stat-row
	   live in app.css — same shared visual language ConstellationUsageModal
	   and SettingsPanel's own Usage section already use. */
	.empty {
		text-align: center;
		font-size: 13.5px;
		color: var(--color-text-dim);
		padding: var(--space-2xl) 0;
	}
</style>
