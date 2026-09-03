<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { pulsarState, type PulsarRoutineInput } from '$lib/pulsar.svelte';
	import { FOCUS_MODES } from '$lib/focusModes';
	import type { FocusMode, PulsarRoutine } from '$lib/types';
	import { X } from '@lucide/svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';

	// One form doing double duty as both create and edit, per
	// docs/plans/pulsar-routines.md's "Routine lifecycle" — routine is
	// null for create, or the routine being edited (pre-filling every
	// field) otherwise. onClose always fires on cancel/success;
	// onSaved fires only on a successful save/archive/unarchive, so a
	// caller (the /pulsar and /pulsar/[id] routes) can refresh its own
	// list without this component needing to know which route it's in.
	let {
		routine = null,
		onClose,
		onSaved
	}: {
		routine?: PulsarRoutine | null;
		onClose: () => void;
		onSaved: () => void;
	} = $props();

	const weekdays = [
		{ value: 'sunday', label: 'Sunday' },
		{ value: 'monday', label: 'Monday' },
		{ value: 'tuesday', label: 'Tuesday' },
		{ value: 'wednesday', label: 'Wednesday' },
		{ value: 'thursday', label: 'Thursday' },
		{ value: 'friday', label: 'Friday' },
		{ value: 'saturday', label: 'Saturday' }
	];

	let name = $state(routine?.name ?? '');
	let prompt = $state(routine?.prompt ?? '');
	let model = $state(routine?.model ?? appState.selectedModel);
	// `|| 'off'`, not `?? 'off'` — a stored routine's focus_mode is '' for
	// "no focus mode" (see the normalization in submit() below), which
	// needs to map back to the select's 'off' option, same as
	// state.svelte.ts's openThread does for threads.
	let focusMode = $state<FocusMode>((routine?.focus_mode as FocusMode) || 'off');
	let deepResearch = $state(routine?.deep_research ?? false);
	let scheduleType = $state<'daily' | 'weekly' | 'monthly'>(routine?.schedule_type ?? 'daily');
	// Separate default per schedule type so switching the dropdown back
	// and forth doesn't leave a monthly day-of-month string sitting in a
	// weekly routine's schedule_params (or vice versa) — each type keeps
	// its own last-edited value, submitted as scheduleParams below only
	// for whichever type is actually selected.
	let weeklyParam = $state(routine?.schedule_type === 'weekly' ? routine.schedule_params : 'monday');
	let monthlyParam = $state(routine?.schedule_type === 'monthly' ? routine.schedule_params : '1');
	let timeOfDay = $state(routine?.time_of_day ?? '07:00');

	let saving = $state(false);
	let error = $state('');
	let confirmingDelete = $state(false);

	let scheduleParams = $derived(
		scheduleType === 'weekly' ? weeklyParam : scheduleType === 'monthly' ? monthlyParam : ''
	);

	async function submit(e: Event) {
		e.preventDefault();
		if (saving) return;
		saving = true;
		error = '';

		const input: PulsarRoutineInput = {
			name: name.trim(),
			prompt: prompt.trim(),
			model,
			// 'off' -> '' — see AppState.persistThreadConfig's identical
			// normalization for why: "no focus mode" is empty string
			// server-side, 'off' is only this form's own sentinel for it.
			focus_mode: focusMode === 'off' ? ('' as FocusMode) : focusMode,
			deep_research: deepResearch,
			schedule_type: scheduleType,
			schedule_params: scheduleParams,
			time_of_day: timeOfDay
		};

		const result = routine
			? await pulsarState.updateRoutine(routine.id, input)
			: await pulsarState.createRoutine(input);

		saving = false;
		if (!result.routine) {
			error = result.error || 'Something went wrong — try again.';
			return;
		}
		onSaved();
	}

	async function archive() {
		if (!routine) return;
		saving = true;
		const ok = await pulsarState.archiveRoutine(routine.id);
		saving = false;
		if (ok) onSaved();
		else error = "Couldn't archive this routine — try again.";
	}

	async function unarchive() {
		if (!routine) return;
		saving = true;
		const ok = await pulsarState.unarchiveRoutine(routine.id);
		saving = false;
		if (ok) onSaved();
		else error = "Couldn't restore this routine — try again.";
	}
</script>

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label={routine ? 'Edit routine' : 'New routine'}>
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>{routine ? 'Edit routine' : 'New Pulsar'}</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		<form onsubmit={submit}>
			<div class="field">
				<label for="pulsar-name">Name</label>
				<input id="pulsar-name" type="text" bind:value={name} placeholder="Daily news" required maxlength="120" />
			</div>

			<div class="field">
				<label for="pulsar-prompt">Prompt</label>
				<textarea
					id="pulsar-prompt"
					bind:value={prompt}
					placeholder="Give me a quick tech news rundown"
					rows="3"
					required
				></textarea>
			</div>

			<div class="row">
				<span>Model</span>
				<select bind:value={model}>
					{#each appState.models as m (m.id)}
						<option value={m.id}>{m.name}</option>
					{/each}
				</select>
			</div>

			<div class="row">
				<span>Focus mode</span>
				<select bind:value={focusMode}>
					<option value="off">Off</option>
					{#each FOCUS_MODES as mode (mode.id)}
						<option value={mode.id}>{mode.label}</option>
					{/each}
				</select>
			</div>

			<div class="row">
				<span>Deep research</span>
				<label class="switch">
					<input type="checkbox" bind:checked={deepResearch} />
					<span class="slider"></span>
				</label>
			</div>

			<h3>Schedule</h3>

			<div class="row">
				<span>Repeats</span>
				<select bind:value={scheduleType}>
					<option value="daily">Daily</option>
					<option value="weekly">Weekly</option>
					<option value="monthly">Monthly</option>
				</select>
			</div>

			{#if scheduleType === 'weekly'}
				<div class="row">
					<span>On</span>
					<select bind:value={weeklyParam}>
						{#each weekdays as wd (wd.value)}
							<option value={wd.value}>{wd.label}</option>
						{/each}
					</select>
				</div>
			{:else if scheduleType === 'monthly'}
				<div class="row">
					<span>On day</span>
					<input type="number" min="1" max="31" bind:value={monthlyParam} class="day-input" />
				</div>
			{/if}

			<div class="row">
				<span>At</span>
				<input type="time" bind:value={timeOfDay} />
			</div>
			<p class="hint">Server-local time — no timezone handling.</p>

			{#if error}
				<p class="error">{error}</p>
			{/if}

			<div class="actions">
				{#if routine && !routine.archived_at}
					{#if confirmingDelete}
						<button type="button" class="btn btn-danger" disabled={saving} onclick={archive}>
							Confirm delete
						</button>
						<button type="button" class="btn" disabled={saving} onclick={() => (confirmingDelete = false)}>
							Cancel
						</button>
					{:else}
						<button type="button" class="btn btn-ghost-danger" disabled={saving} onclick={() => (confirmingDelete = true)}>
							Delete
						</button>
					{/if}
				{:else if routine}
					<button type="button" class="btn" disabled={saving} onclick={unarchive}>Restore</button>
				{/if}
				<button type="submit" class="btn btn-accent save-btn" disabled={saving}>
					{saving ? 'Saving…' : routine ? 'Save' : 'Create'}
				</button>
			</div>
		</form>
	</div>
</div>

<style>
	h3 {
		margin: var(--space-lg) 0 var(--space-md);
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.12em;
		color: var(--color-text);
	}

	.field {
		margin-bottom: var(--space-md);
	}

	.field label {
		display: block;
		margin-bottom: var(--space-xs);
		font-size: 12px;
		color: var(--color-text-dim);
	}

	.field input,
	.field textarea {
		width: 100%;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		font: inherit;
		font-size: 13px;
		color: var(--color-text);
		resize: vertical;
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		margin-bottom: var(--space-sm);
		font-size: 14px;
	}

	.row select,
	.row input[type='time'] {
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-well);
		padding: var(--space-sm) var(--space-md);
		font-size: 13px;
		color: var(--color-text);
	}

	.day-input {
		width: 64px;
		text-align: center;
	}

	.hint {
		margin: 0 0 var(--space-md);
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.error {
		margin: 0 0 var(--space-md);
		font-size: 12.5px;
		color: var(--color-danger);
	}

	.actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-sm);
		margin-top: var(--space-lg);
	}

	.save-btn {
		margin-left: auto;
	}

	.btn-ghost-danger {
		background: transparent;
		color: var(--color-danger);
	}

	.btn-danger {
		background: var(--color-danger-bg);
		color: var(--color-danger);
	}

	/* Same switch construction as SettingsPanel.svelte/ComposerMenu.svelte —
	   duplicated, not shared, since Svelte scopes component styles
	   per-file (see SettingsPanel's own doc comment on this). */
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
</style>
