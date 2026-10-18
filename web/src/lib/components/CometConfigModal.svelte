<script lang="ts">
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { cometState, type CometConfigInput } from '$lib/comet.svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { X } from '@lucide/svelte';
	import Switch from './Switch.svelte';

	let { onClose }: { onClose: () => void } = $props();

	onMount(() => {
		if (!cometState.config) void cometState.loadConfig();
		if (!appState.models.length) void appState.loadModels();
	});

	let enabled = $state(cometState.config?.enabled ?? false);
	let timeOfDay = $state(cometState.config?.time_of_day ?? '06:00');
	let model = $state(cometState.config?.model ?? '');
	let saving = $state(false);
	let error = $state('');

	// Config loads async — sync the editable fields once it lands, same
	// one-shot "seed from the store, then edit locally" pattern
	// ConstellationSettingsModal uses.
	$effect(() => {
		if (cometState.config) {
			enabled = cometState.config.enabled;
			timeOfDay = cometState.config.time_of_day;
			model = cometState.config.model;
		}
	});

	async function save() {
		saving = true;
		error = '';
		const input: CometConfigInput = { enabled, time_of_day: timeOfDay, model };
		const result = await cometState.updateConfig(input);
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
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Comet settings">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>Comet</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		<p class="modal-hint">
			A monthly retrospective built from what you already asked, searched, and starred. Once a month
			Comet classifies that month's conversations, then assembles the page.
		</p>

		<div class="settings-group">
			<div class="settings-row">
				<div>
					<div class="row-label">Enable Comet</div>
					<div class="row-sub">Generate a retrospective at the start of each month.</div>
				</div>
				<Switch checked={enabled} label="Enable Comet" onchange={(v) => (enabled = v)} />
			</div>
			<div class="settings-row">
				<span class="row-label">Generate at</span>
				<input type="time" bind:value={timeOfDay} />
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

		{#if error}
			<p class="error-text">{error}</p>
		{/if}

		<button class="btn btn-accent save-btn" onclick={save} disabled={saving}>
			{saving ? 'Saving…' : 'Save'}
		</button>
	</div>
</div>

<style>
	.modal-hint {
		color: var(--color-text-dim);
		font-size: 0.85rem;
		line-height: 1.5;
		margin: 0 0 var(--space-lg);
	}
	.settings-group {
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		overflow: hidden;
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
		font-size: 0.85rem;
	}
	.row-sub {
		font-size: 0.72rem;
		color: var(--color-text-dim);
		margin-top: var(--space-xs);
	}
	.settings-row select,
	.settings-row input[type='time'] {
		background: var(--color-surface-3);
		color: var(--color-text);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		padding: var(--space-xs) var(--space-sm);
		font: inherit;
		font-size: 0.82rem;
	}
	.error-text {
		color: var(--color-danger);
		font-size: 0.82rem;
		margin: var(--space-md) 0 0;
	}
	.save-btn {
		width: 100%;
		margin-top: var(--space-lg);
	}
</style>
