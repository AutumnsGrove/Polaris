<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import { appState } from '$lib/state.svelte';
	import { pulsarState } from '$lib/pulsar.svelte';
	import PulsarRoutineForm from '$lib/components/PulsarRoutineForm.svelte';
	import PulsarUnreadBadge from '$lib/components/PulsarUnreadBadge.svelte';
	import { PanelLeft, Plus, Archive } from '@lucide/svelte';
	import type { PulsarRoutine } from '$lib/types';

	onMount(() => {
		void pulsarState.loadRoutines();
		void pulsarState.loadArchivedRoutines();
		void pulsarState.loadUnreadCounts();
	});

	let showForm = $state(false);

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
		<h1 class="page-title">Pulsar</h1>
	</div>
	<button class="btn btn-accent" onclick={() => (showForm = true)}>
		<Plus size={16} />
		New Pulsar
	</button>
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
				<PulsarUnreadBadge count={pulsarState.unreadCounts[String(routine.id)] ?? 0} />
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
				</div>
			{/each}
		</div>
	{/if}
</div>

{#if showForm}
	<PulsarRoutineForm
		onClose={() => (showForm = false)}
		onSaved={() => {
			showForm = false;
		}}
	/>
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

	.page-title {
		margin: 0;
		font-family: var(--font-serif);
		font-size: 20px;
		font-weight: 700;
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

	.routine-row.archived {
		opacity: 0.55;
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
