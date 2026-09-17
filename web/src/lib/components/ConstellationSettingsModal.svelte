<script lang="ts">
	import { onMount } from 'svelte';
	import { appState } from '$lib/state.svelte';
	import { constellationState, type ConstellationConfigInput } from '$lib/constellation.svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import { X, ChevronRight, Info } from '@lucide/svelte';
	import ConstellationUsageModal from './ConstellationUsageModal.svelte';

	let { onClose }: { onClose: () => void } = $props();

	onMount(() => {
		if (!constellationState.config) void constellationState.loadConfig();
	});

	const POLL_INTERVALS = [15, 30, 60, 120, 240];

	// Preset pronoun choices the segmented control offers — 'custom' isn't a
	// real pronoun value, it's the toggle's fourth button that reveals the
	// free-text input below for anything not on this fixed list.
	const PRONOUN_PRESETS = ['he/him', 'she/her', 'they/them'];

	let enabled = $state(constellationState.config?.enabled ?? false);
	let pollInterval = $state(constellationState.config?.poll_interval_minutes ?? 60);
	let model = $state(constellationState.config?.model ?? '');
	let personName = $state(constellationState.config?.person_name ?? '');
	// pronounChoice is one of PRONOUN_PRESETS, 'custom', or '' (nothing
	// picked yet — no guidance saved). customPronouns only matters while
	// pronounChoice === 'custom'.
	let pronounChoice = $state('');
	let customPronouns = $state('');
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
			personName = constellationState.config.person_name;
			const savedPronouns = constellationState.config.person_pronouns;
			if (savedPronouns === '' || PRONOUN_PRESETS.includes(savedPronouns)) {
				pronounChoice = savedPronouns;
			} else {
				pronounChoice = 'custom';
				customPronouns = savedPronouns;
			}
		}
	});

	async function save() {
		saving = true;
		error = '';
		const input: ConstellationConfigInput = {
			enabled,
			poll_interval_minutes: pollInterval,
			model,
			person_name: personName.trim(),
			person_pronouns: pronounChoice === 'custom' ? customPronouns.trim() : pronounChoice
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
				<div>
					<div class="row-label">Enabled</div>
					<div class="row-hint">
						Weaver checks for new threads and builds your library automatically.
					</div>
				</div>
				<label class="switch">
					<input type="checkbox" bind:checked={enabled} />
					<span class="slider"></span>
				</label>
			</div>
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

		<div class="section-label">About you</div>
		<div class="settings-group">
			<div class="settings-row column-row">
				<div>
					<div class="row-label">Name</div>
					<div class="row-hint">So Weaver can use it instead of "they" where that reads better.</div>
				</div>
				<input
					type="text"
					class="text-input"
					placeholder="e.g. Alex"
					maxlength="80"
					bind:value={personName}
				/>
			</div>
			<div class="settings-row column-row">
				<div>
					<div class="row-label">Pronouns</div>
					<div class="row-hint">
						Weaver has no way to know this on its own — it'll keep guessing from context (and can
						guess wrong) unless you set it here.
					</div>
				</div>
				<div class="pronoun-toggle">
					{#each PRONOUN_PRESETS as preset (preset)}
						<button
							type="button"
							class:active={pronounChoice === preset}
							onclick={() => (pronounChoice = preset)}
						>
							{preset}
						</button>
					{/each}
					<button
						type="button"
						class:active={pronounChoice === 'custom'}
						onclick={() => (pronounChoice = 'custom')}
					>
						Custom
					</button>
				</div>
				{#if pronounChoice === 'custom'}
					<input
						type="text"
						class="text-input"
						placeholder="e.g. ze/zir"
						maxlength="40"
						bind:value={customPronouns}
					/>
				{/if}
			</div>
		</div>

		<div class="section-label">History</div>
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
	.switch {
		position: relative;
		display: inline-block;
		width: 36px;
		height: 20px;
		flex-shrink: 0;
	}
	.switch input {
		opacity: 0;
		width: 0;
		height: 0;
	}
	.slider {
		position: absolute;
		inset: 0;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-full);
		cursor: pointer;
		transition: background 0.15s ease;
	}
	.slider::before {
		content: '';
		position: absolute;
		width: 14px;
		height: 14px;
		left: 2px;
		top: 2px;
		background: var(--color-text-dim);
		border-radius: 50%;
		transition:
			transform 0.15s ease,
			background 0.15s ease;
	}
	.switch input:checked + .slider {
		background: color-mix(in srgb, var(--color-accent) 30%, transparent);
		border-color: var(--color-accent);
	}
	.switch input:checked + .slider::before {
		transform: translateX(16px);
		background: var(--color-accent);
	}

	.settings-group {
		border-radius: var(--radius-lg);
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		overflow: hidden;
		margin-bottom: var(--space-xl);
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
	.row-hint {
		font-size: 11.5px;
		color: var(--color-text-dim);
		margin-top: 2px;
		line-height: 1.4;
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

	/* column-row: label/hint on top, the control on its own full-width line
	   below — the plain switch/select settings-row layout (label left,
	   control right) doesn't have room for a text input or a four-button
	   pronoun toggle next to it, especially at phone width. */
	.settings-row.column-row {
		flex-direction: column;
		align-items: stretch;
		gap: var(--space-sm);
	}
	.text-input {
		font: inherit;
		font-size: 13px;
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		color: var(--color-text);
		padding: var(--space-xs) var(--space-sm);
		width: 100%;
	}
	.pronoun-toggle {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-xs);
	}
	.pronoun-toggle button {
		font: inherit;
		font-size: 13px;
		background: var(--color-surface-3);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-full);
		color: var(--color-text-dim);
		padding: var(--space-xs) var(--space-md);
		transition:
			background-color 0.15s ease,
			color 0.15s ease,
			border-color 0.15s ease;
	}
	.pronoun-toggle button.active {
		background: color-mix(in srgb, var(--color-accent) 20%, var(--color-surface-3));
		border-color: var(--color-accent);
		color: var(--color-text);
		font-weight: 600;
	}

	.section-label {
		font-size: 11px;
		font-weight: 600;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-text-dim);
		padding: 0 var(--space-xs) var(--space-sm);
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
