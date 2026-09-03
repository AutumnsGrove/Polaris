<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import { appState } from '$lib/state.svelte';
	import { pulsarState } from '$lib/pulsar.svelte';
	import PulsarRoutineForm from '$lib/components/PulsarRoutineForm.svelte';
	import { PanelLeft, ChevronLeft, Pencil, Loader2 } from '@lucide/svelte';

	let routineId = $derived(Number(page.params.id));
	let routine = $derived(pulsarState.routineById(routineId));

	// Polls while any pulse in this routine's history is still running —
	// same "no live push for a scheduler-fired turn" gap
	// appState.threadTurnInProgress exists for on the thread-view side
	// (see its doc comment), just scoped to a whole list here instead of
	// one thread.
	//
	// onMount's callback here is deliberately synchronous (the loading
	// itself is kicked off as a detached async call inside it) — an
	// async onMount callback's return value is a Promise, which Svelte
	// does NOT treat as a teardown function, only a plain function
	// return is; returning the cleanup from an async callback would be
	// silently ignored and leak this interval forever.
	onMount(() => {
		// Both lists, not just one — the routine being viewed could be
		// either active or archived (its detail/pulse-history view is
		// identical either way, per the plan doc's "Routine lifecycle": an
		// archived routine is still one tap away). Also covers landing here
		// directly (a reload, a shared link) with nothing preloaded yet.
		void (async () => {
			await Promise.all([pulsarState.loadRoutines(), pulsarState.loadArchivedRoutines()]);
			await pulsarState.loadPulses(routineId);
		})();

		const pollTimer = setInterval(() => {
			if (pulsarState.currentPulses.some((p) => p.in_progress)) {
				void pulsarState.loadPulses(routineId);
			}
		}, 4000);

		return () => clearInterval(pollTimer);
	});

	let showForm = $state(false);

	function openPulse(threadId: string) {
		// &pulsar=<routineId> lets /t/[id] show a "back to routine" header
		// affordance instead of just the plain sidebar-toggle chrome — see
		// ChatView.svelte's pulsarBackHref.
		void goto(`/t/${threadId}?pulsar=${routineId}`);
	}
</script>

<svelte:head>
	<title>{routine ? `${routine.name} — Pulsar` : 'Pulsar'} — Polaris</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		{#if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<button class="icon-btn" onclick={() => goto('/pulsar')} title="Back to Pulsar">
			<ChevronLeft size={18} />
		</button>
		<h1 class="page-title">{routine?.name ?? 'Pulsar'}</h1>
	</div>
	{#if routine}
		<button class="icon-btn" onclick={() => (showForm = true)} title="Edit routine">
			<Pencil size={17} />
		</button>
	{/if}
</header>

<div class="content">
	{#if !routine && pulsarState.loaded}
		<p class="empty">This routine doesn't exist, or was removed.</p>
	{:else if pulsarState.currentPulsesLoading && pulsarState.currentPulses.length === 0}
		<p class="empty">Loading…</p>
	{:else if pulsarState.currentPulses.length === 0}
		<p class="empty">No pulses yet — this routine hasn't fired.</p>
	{:else}
		<div class="pulse-list">
			{#each pulsarState.currentPulses as pulse, i (pulse.thread_id)}
				<div
					class="pulse-row"
					class:unread={!pulse.seen}
					onclick={() => openPulse(pulse.thread_id)}
					onkeydown={(e) => e.key === 'Enter' && openPulse(pulse.thread_id)}
					role="button"
					tabindex="0"
					in:fly={{ y: 8, duration: 220, delay: Math.min(i, 10) * 22, easing: quintOut }}
				>
					<span class="pulse-dot" aria-hidden="true"></span>
					<div class="pulse-title">{pulse.title || 'Untitled'}</div>
					{#if pulse.in_progress}
						<span class="pulse-status">
							<Loader2 size={13} class="spin" />
							In progress
						</span>
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

{#if showForm && routine}
	<PulsarRoutineForm
		{routine}
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
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
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

	.pulse-list {
		display: flex;
		flex-direction: column;
		max-width: 640px;
	}

	.pulse-row {
		position: relative;
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		border-radius: var(--radius-md);
		padding: var(--space-md) var(--space-md);
		cursor: pointer;
		transition: background-color 0.15s var(--ease-out-expo);
		/* Read pulses are visually dimmed — unread ones stay full-weight,
		   marked by the amber dot below — per the plan doc's "routine
		   detail" UI. */
		opacity: 0.6;
	}

	.pulse-row.unread {
		opacity: 1;
	}

	.pulse-row:hover {
		background: var(--color-surface-2);
	}

	.pulse-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: transparent;
		flex-shrink: 0;
	}

	.pulse-row.unread .pulse-dot {
		background: var(--color-accent);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 22%, transparent);
	}

	.pulse-title {
		flex: 1;
		min-width: 0;
		font-size: 14px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.pulse-status {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		flex-shrink: 0;
		font-size: 11.5px;
		color: var(--color-accent);
	}

	:global(.pulse-status .spin) {
		animation: pulse-status-spin 0.8s linear infinite;
	}

	@keyframes pulse-status-spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
