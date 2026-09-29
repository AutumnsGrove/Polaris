<script lang="ts">
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { constellationState, type ConstellationConfigInput } from '$lib/constellation.svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { X, ChevronRight, Info, History } from '@lucide/svelte';
	import ConstellationUsageModal from './ConstellationUsageModal.svelte';

	let { onClose }: { onClose: () => void } = $props();

	onMount(() => {
		if (!constellationState.config) void constellationState.loadConfig();
	});

	const POLL_INTERVALS = [15, 30, 60, 120, 240];

	// The on/off switch lives in the main Settings panel now (so the sidebar
	// entry can hide when it's off) — this modal just carries the saved value
	// through its own full-overwrite save.
	let enabled = $state(constellationState.config?.enabled ?? false);
	let pollInterval = $state(constellationState.config?.poll_interval_minutes ?? 60);
	let model = $state(constellationState.config?.model ?? '');
	let saving = $state(false);
	let error = $state('');
	let showUsage = $state(false);

	// Config loads async (see onMount above) — this syncs the local editable
	// fields once it lands, same one-shot "seed from the store, then edit
	// locally" pattern PulsarDailyConfigModal uses for its own cfg-derived
	// $state fields.
	$effect(() => {
		if (constellationState.config) {
			enabled = constellationState.config.enabled;
			pollInterval = constellationState.config.poll_interval_minutes;
			model = constellationState.config.model;
		}
	});

	async function save() {
		saving = true;
		error = '';
		const input: ConstellationConfigInput = {
			enabled,
			poll_interval_minutes: pollInterval,
			model
		};
		const result = await constellationState.updateConfig(input);
		saving = false;
		if (result.error) {
			error = result.error;
			return;
		}
		onClose();
	}
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Constellation settings">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>Constellation</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		{#if constellationState.configError && !constellationState.config}
			<p class="error-text">
				Couldn't load current settings — the toggles below may not reflect what's actually saved.
				Try closing and reopening this panel.
			</p>
		{/if}

		<div class="settings-group">
			<div class="settings-row">
				<span class="row-label">Check every</span>
				<select bind:value={pollInterval}>
					{#each POLL_INTERVALS as mins (mins)}
						<option value={mins}>{mins} min</option>
					{/each}
				</select>
			</div>
			<div class="settings-row">
				<span class="row-label">Model</span>
				<select bind:value={model}>
					<option value="">Same as chat (default)</option>
					{#each appState.models as m (m.id)}
						<option value={m.id}>{m.name}</option>
					{/each}
				</select>
			</div>
		</div>

		<div class="section-head"><History size={15} /><span class="section-title">History</span></div>
		<div class="settings-group">
			<button class="usage-link" onclick={() => (showUsage = true)}>
				<Info size={16} class="usage-icon" />
				<span class="label">Constellation usage</span>
				<ChevronRight size={14} class="chev" />
			</button>
		</div>

		{#if error}
			<p class="error-text">{error}</p>
		{/if}

		<button class="btn btn-accent save-btn" onclick={save} disabled={saving}>
			{saving ? 'Saving…' : 'Save'}
		</button>
	</div>
</div>

{#if showUsage}
	<ConstellationUsageModal onClose={() => (showUsage = false)} />
{/if}

<style>
	.settings-group {
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		overflow: hidden;
		/* Tight on purpose — see SettingsPanel.svelte's identical comment. */
		margin-bottom: var(--space-xs);
	}
	.settings-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}
	.settings-row:last-child {
		border-bottom: none;
	}
	.row-label {
		font-size: 14px;
		font-weight: 500;
	}
	.settings-row select {
		font: inherit;
		font-size: 13px;
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		padding: var(--space-xs) var(--space-sm);
	}

	/* See SettingsPanel.svelte's .section-head comment — same gold-icon
	   + rule treatment, replacing the old all-dim .section-label. */
	.section-head {
		display: flex;
		align-items: center;
		gap: 9px;
		margin-top: var(--space-2xl);
		margin-bottom: var(--space-xs);
	}
	.section-head :global(svg) {
		color: var(--color-accent);
		flex-shrink: 0;
	}
	.section-title {
		font-size: 15px;
		font-weight: 600;
		color: var(--color-text);
	}
	.usage-link {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		width: 100%;
		background: none;
		border: none;
		cursor: pointer;
		font: inherit;
		color: inherit;
		text-align: left;
	}
	.usage-link :global(.usage-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
	}
	.usage-link .label {
		flex: 1;
		font-size: 14px;
		font-weight: 500;
	}
	.usage-link :global(.chev) {
		color: var(--color-text-dim);
	}

	.error-text {
		margin: 0 0 var(--space-md);
		font-size: 12.5px;
		color: var(--color-danger);
	}
	.save-btn {
		width: 100%;
		justify-content: center;
	}
</style>
