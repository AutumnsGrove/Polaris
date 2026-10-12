<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import { appState } from '$lib/state.svelte';
	import { pulsarState } from '$lib/pulsar.svelte';
	import PulsarRoutineForm from '$lib/components/PulsarRoutineForm.svelte';
	import PulsarUnreadBadge from '$lib/components/PulsarUnreadBadge.svelte';
	import PulsarUsageModal from '$lib/components/PulsarUsageModal.svelte';
	import { PanelLeft, Plus, Archive, Info, RotateCcw, Trash2 } from '@lucide/svelte';
	import type { PulsarRoutine } from '$lib/types';

	// Oracle mode's "Set up as Pulsar" offer chip (see pulsarState.pendingSeed's
	// doc comment) — captured once, before the field is cleared, so
	// PulsarRoutineForm below still has it after this mount effect runs.
	// newRoutineName is the chip's own suggested title (from
	// /api/pulsar/suggest), when the call managed to draft one.
	let newRoutineSeed = $state<string | undefined>(undefined);
	let newRoutineName = $state<string | undefined>(undefined);

	onMount(() => {
		void pulsarState.loadRoutines();
		void pulsarState.loadArchivedRoutines();
		void pulsarState.loadUnreadCounts();

		if (pulsarState.pendingSeed?.kind === 'pulsar') {
			newRoutineSeed = pulsarState.pendingSeed.text;
			newRoutineName = pulsarState.pendingSeed.name;
			pulsarState.pendingSeed = null;
			showForm = true;
		}
	});

	let showForm = $state(false);
	let showUsage = $state(false);

	function scheduleSummary(r: PulsarRoutine): string {
		const time = formatTime(r.time_of_day);
		if (r.schedule_type === 'daily') return `Daily at ${time}`;
		if (r.schedule_type === 'weekly') {
			const day = r.schedule_params.charAt(0).toUpperCase() + r.schedule_params.slice(1);
			return `Weekly on ${day} at ${time}`;
		}
		return `Monthly on day ${r.schedule_params} at ${time}`;
	}

	// "07:00" -> "7:00 AM" — time_of_day is stored/edited as a plain
	// 24-hour string (see the form's <input type="time">), formatted here
	// only for display.
	function formatTime(hhmm: string): string {
		const [h, m] = hhmm.split(':').map(Number);
		if (Number.isNaN(h) || Number.isNaN(m)) return hhmm;
		const period = h < 12 ? 'AM' : 'PM';
		const hour12 = h % 12 === 0 ? 12 : h % 12;
		return `${hour12}:${String(m).padStart(2, '0')} ${period}`;
	}

	// Which archived row is mid-"are you sure?" for a permanent delete, and
	// any failure message (e.g. a pulse still running) shown under the list.
	let confirmingDeleteId = $state<number | null>(null);
	let busyId = $state<number | null>(null);
	let archiveError = $state('');

	async function restore(id: number) {
		busyId = id;
		archiveError = '';
		const ok = await pulsarState.unarchiveRoutine(id);
		busyId = null;
		if (!ok) archiveError = "Couldn't restore that routine — try again.";
	}

	async function deleteForever(id: number) {
		busyId = id;
		archiveError = '';
		const result = await pulsarState.deleteRoutinePermanently(id);
		busyId = null;
		confirmingDeleteId = null;
		if (!result.ok) archiveError = result.error || "Couldn't delete that routine — try again.";
	}

	function openRoutine(id: number) {
		void goto(`/pulsar/${id}`);
	}
</script>

<svelte:head>
	<title>Pulsar — Polaris</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		{#if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<h1 class="page-title"><span class="wordmark">Pulsar</span></h1>
	</div>
	<div class="header-right">
		<button class="icon-btn" onclick={() => (showUsage = true)} title="Pulsar usage" aria-label="Pulsar usage">
			<Info size={18} />
		</button>
		<button class="btn btn-accent" onclick={() => (showForm = true)}>
			<Plus size={16} />
			New <span class="wordmark">Pulsar</span>
		</button>
	</div>
</header>

<div class="content">
	{#if pulsarState.loaded && pulsarState.routines.length === 0}
		<p class="empty">
			No routines yet. A routine is a saved prompt that fires on a schedule — try "Daily news"
			or "Weekly Guild Wars 3 roundup".
		</p>
	{/if}

	<div class="routine-list">
		{#each pulsarState.routines as routine, i (routine.id)}
			<div
				class="routine-row"
				onclick={() => openRoutine(routine.id)}
				onkeydown={(e) => e.key === 'Enter' && openRoutine(routine.id)}
				role="button"
				tabindex="0"
				in:fly={{ y: 8, duration: 220, delay: Math.min(i, 10) * 22, easing: quintOut }}
			>
				<div class="routine-meta">
					<div class="routine-name">{routine.name}</div>
					<div class="routine-schedule">{scheduleSummary(routine)}</div>
				</div>
				<PulsarUnreadBadge
					count={pulsarState.unreadCounts[String(routine.id)] ?? 0}
					alert={(pulsarState.alertCounts[String(routine.id)] ?? 0) > 0}
				/>
			</div>
		{/each}
	</div>

	{#if pulsarState.archivedRoutines.length > 0}
		<div class="section-label">
			<Archive size={11} />
			Archived
		</div>
		<div class="routine-list">
			{#each pulsarState.archivedRoutines as routine (routine.id)}
				<div
					class="routine-row archived"
					onclick={() => openRoutine(routine.id)}
					onkeydown={(e) => e.key === 'Enter' && openRoutine(routine.id)}
					role="button"
					tabindex="0"
				>
					<div class="routine-meta">
						<div class="routine-name">{routine.name}</div>
						<div class="routine-schedule">{scheduleSummary(routine)}</div>
					</div>
					<!-- Stop propagation so a tap on an action doesn't also open the row. -->
					<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
					<div class="row-actions" onclick={(e) => e.stopPropagation()}>
						{#if confirmingDeleteId === routine.id}
							<button class="btn btn-danger" disabled={busyId === routine.id} onclick={() => deleteForever(routine.id)}>
								Delete forever
							</button>
							<button class="btn" disabled={busyId === routine.id} onclick={() => (confirmingDeleteId = null)}>
								Cancel
							</button>
						{:else}
							<button class="btn" disabled={busyId === routine.id} onclick={() => restore(routine.id)} title="Restore this routine">
								<RotateCcw size={13} />
								Restore
							</button>
							<button
								class="icon-btn danger"
								disabled={busyId === routine.id}
								onclick={() => (confirmingDeleteId = routine.id)}
								title="Delete forever"
								aria-label="Delete {routine.name} forever"
							>
								<Trash2 size={15} />
							</button>
						{/if}
					</div>
				</div>
			{/each}
		</div>
		{#if archiveError}
			<p class="archive-error">{archiveError}</p>
		{/if}
		<p class="archive-hint">Deleting forever also removes every pulse this routine produced.</p>
	{/if}
</div>

{#if showForm}
	<PulsarRoutineForm
		initialPrompt={newRoutineSeed}
		initialName={newRoutineName}
		onClose={() => (showForm = false)}
		onSaved={() => {
			showForm = false;
		}}
	/>
{/if}

{#if showUsage}
	<PulsarUsageModal onClose={() => (showUsage = false)} />
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: max(var(--space-lg), env(safe-area-inset-top)) var(--space-lg) var(--space-lg);
		box-shadow: var(--shadow-well);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		min-width: 0;
	}

	.header-right {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.page-title {
		margin: 0;
		font-size: 20px;
	}

	/* Reserved brand-face treatment (see app.css's --font-wordmark) — this
	   page's title and "New Pulsar" button both render the literal word
	   "Pulsar" as a name, same as "Polaris" gets elsewhere in the app. */
	.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		letter-spacing: 0.02em;
	}

	.content {
		flex: 1;
		overflow-y: auto;
		padding: var(--space-lg);
	}

	.empty {
		max-width: 46ch;
		margin: var(--space-2xl) auto;
		text-align: center;
		font-size: 13.5px;
		line-height: 1.6;
		color: var(--color-text-dim);
	}

	.section-label {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin: var(--space-xl) var(--space-sm) var(--space-sm);
		font-size: 10.5px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--color-text-dim);
	}

	.routine-list {
		display: flex;
		flex-direction: column;
		max-width: 640px;
	}

	.routine-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		border-radius: var(--radius-md);
		padding: var(--space-md) var(--space-md);
		cursor: pointer;
		transition: background-color 0.15s var(--ease-out-expo);
	}

	.routine-row:hover {
		background: var(--color-surface-2);
	}

	/* Dim the text only — dimming the whole row would also wash out the
	   Restore / Delete forever buttons. */
	.routine-row.archived .routine-meta {
		opacity: 0.55;
	}

	.row-actions {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		flex-shrink: 0;
	}

	.row-actions :global(.btn) {
		padding: var(--space-xs) var(--space-md);
		font-size: 12.5px;
	}

	.row-actions :global(.btn-danger) {
		background: var(--color-danger-bg);
		color: var(--color-danger);
	}

	.icon-btn.danger:hover {
		color: var(--color-danger);
	}

	.archive-error {
		margin: var(--space-sm) var(--space-md) 0;
		font-size: 12.5px;
		color: var(--color-danger);
	}

	.archive-hint {
		margin: var(--space-sm) var(--space-md) 0;
		font-size: 11.5px;
		color: var(--color-text-dim);
	}

	.routine-meta {
		flex: 1;
		min-width: 0;
	}

	.routine-name {
		font-size: 14px;
		font-weight: 500;
	}

	.routine-schedule {
		margin-top: 2px;
		font-size: 12px;
		color: var(--color-text-dim);
	}
</style>
