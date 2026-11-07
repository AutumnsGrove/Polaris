<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { X, TriangleAlert } from '@lucide/svelte';

	let { onClose }: { onClose: () => void } = $props();

	const status = $derived(appState.settings.providers);
	const go = $derived(status?.opencode_go);
	const usage = $derived(appState.settings.usage);

	function fmtReset(iso?: string): string {
		if (!iso) return 'unknown';
		const d = new Date(iso);
		return isNaN(d.getTime()) ? iso : d.toLocaleString();
	}
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="OpenCode Go Usage">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>OpenCode Go</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		{#if !appState.settings.providersLoaded}
			<p class="empty">Loading…</p>
		{:else if appState.settings.providersError || !status}
			<p class="empty">Couldn't load provider status — check your connection and try again.</p>
		{:else if !go?.configured}
			<p class="empty">
				OpenCode Go isn't configured — set <code>providers.opencode_go.api_key</code> in config.yaml and
				switch routing on (see <code>config.yaml.example</code>). Everything keeps running on
				OpenRouter until then.
			</p>
		{:else}
			<div class="usage-big-cost">
				<div class="amount">${go.polaris_share_allowance_usd.toFixed(2)}</div>
				<div class="caption">Polaris's share this month &middot; allowance dollars</div>
			</div>

			<!-- The meters are account-wide (other clients count too), so the
			     panel says so — a number that silently mixes in pi/OpenCode
			     usage looks wrong next to Polaris's own ledger below. -->
			<div class="usage-section-label">Allowance windows <span class="usage-section-sublabel">(account-wide)</span></div>
			{#if go.error}
				<p class="empty">Meters unavailable: {go.error}</p>
			{:else}
				<div class="usage-stat-group">
					{#each go.meters ?? [] as m (m.kind)}
						<div class="meter-row">
							<div class="meter-head">
								<span class="label">{m.label}</span>
								<span class="value"
									>${m.used_usd.toFixed(2)} / ${m.limit_usd.toFixed(2)} ({m.percent.toFixed(0)}%)</span
								>
							</div>
							<div class="meter-bar" aria-hidden="true">
								<i style="width:{Math.min(100, m.percent)}%"></i>
							</div>
							<div class="meter-reset">resets {fmtReset(m.resets_at)}</div>
						</div>
					{/each}
				</div>
				{#if go.period_end}
					<p class="hint">Plan period ends {fmtReset(go.period_end)}.</p>
				{/if}
			{/if}

			{#if usage?.billed_to_balance && usage.billed_to_balance.period_cost_usd > 0}
				<!-- balance billing is a misconfiguration (Extra Usage is on), never a
				     routing outcome — loud, not a line item. -->
				<div class="balance-warning" role="alert">
					<TriangleAlert size={16} />
					<span
						>${usage.billed_to_balance.period_cost_usd.toFixed(2)} billed to console balance in the
						last 30 days — Extra Usage is on. Turn it off in the OpenCode console; the
						subscription should cover these calls.</span
					>
				</div>
			{/if}

			<div class="usage-section-label">Policy</div>
			<div class="usage-stat-group">
				<div class="usage-stat-row">
					<span class="label">Default backend</span>
					<span class="value">{status.policy.default_backend}</span>
				</div>
				<div class="usage-stat-row">
					<span class="label">Failover</span>
					<span class="value">{status.policy.failover}</span>
				</div>
				{#if status.policy.pinned}
					<div class="usage-stat-row">
						<span class="label">Kill switch</span>
						<span class="value">pinned to OpenRouter</span>
					</div>
				{/if}
			</div>
		{/if}
	</div>
</div>

<style>
	.meter-row {
		padding: var(--space-sm) 0;
	}
	.meter-row + .meter-row {
		border-top: 1px solid color-mix(in srgb, currentColor 10%, transparent);
	}
	.meter-head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
	}
	.meter-head .label {
		color: var(--color-text);
	}
	.meter-head .value {
		font-variant-numeric: tabular-nums;
		color: var(--color-text-dim);
	}
	.meter-bar {
		height: 6px;
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, currentColor 12%, transparent);
		margin-top: var(--space-xs);
		overflow: hidden;
	}
	.meter-bar i {
		display: block;
		height: 100%;
		background: var(--color-accent-2);
	}
	.meter-reset {
		font-size: 11.5px;
		color: var(--color-text-dim);
		margin-top: 2px;
	}
	.balance-warning {
		display: flex;
		gap: var(--space-sm);
		align-items: flex-start;
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-md);
		border-radius: var(--radius-md);
		background: color-mix(in srgb, red 12%, transparent);
		color: var(--color-text);
		font-size: 13px;
	}
	.hint {
		font-size: 12px;
		color: var(--color-text-dim);
		margin: var(--space-sm) 0 0;
	}
	.empty {
		color: var(--color-text-dim);
	}
	code {
		font-family: var(--font-mono);
		font-size: 0.92em;
	}
</style>
