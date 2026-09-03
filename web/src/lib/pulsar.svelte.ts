import type { FocusMode, PulsarPulse, PulsarRoutine } from './types';

// PulsarRoutineInput is what the create/edit form (PulsarRoutineForm.svelte)
// submits — same shape for both POST (create) and PATCH (edit), matching
// the plan doc's "one form doing double duty" design.
export interface PulsarRoutineInput {
	name: string;
	prompt: string;
	model: string;
	focus_mode: FocusMode;
	deep_research: boolean;
	schedule_type: 'daily' | 'weekly' | 'monthly';
	schedule_params: string;
	time_of_day: string;
}

// PulsarState is Pulsar's own store, same shape as SearchState/
// SettingsState — a dedicated class rather than folding this into
// AppState, since Pulsar's data (routines, pulse history, unread counts)
// is a separate concern from chat threads, same reasoning
// docs/plans/local-search-frontend.md gives for SearchState.
export class PulsarState {
	routines = $state<PulsarRoutine[]>([]);
	archivedRoutines = $state<PulsarRoutine[]>([]);

	// Keyed by routine id as a string (JSON object keys can't be numeric —
	// see gateway/pulsar_routes.go's handlePulsarUnreadCounts) — the
	// amber indicator's per-routine scope reads this directly; the
	// sidebar's global Orbit-icon count is just the sum of its values
	// (see totalUnread below), not a second fetch.
	unreadCounts = $state<Record<string, number>>({});

	// Set once both loadRoutines() and loadArchivedRoutines() have
	// resolved at least once — /pulsar/[id] uses this to distinguish
	// "still loading" from "no such routine" when looking a routine up
	// by id (see routineById below).
	loaded = $state(false);

	// The currently-viewed routine's pulse history — /pulsar/[id] loads
	// this via loadPulses(). Not merged into routines/archivedRoutines
	// above since a pulse list can be long-ish and has nothing to do with
	// rendering the routines list itself.
	currentPulses = $state<PulsarPulse[]>([]);
	currentPulsesLoading = $state(false);

	// totalUnread backs the sidebar's global Orbit-icon badge — count
	// across every routine combined, per the plan doc's "Amber indicator
	// semantics".
	totalUnread = $derived(Object.values(this.unreadCounts).reduce((sum, n) => sum + n, 0));

	async loadRoutines() {
		const res = await fetch('/api/pulsar/routines');
		this.routines = res.ok ? ((await res.json()) ?? []) : [];
		this.loaded = true;
	}

	async loadArchivedRoutines() {
		const res = await fetch('/api/pulsar/routines?archived=true');
		this.archivedRoutines = res.ok ? ((await res.json()) ?? []) : [];
	}

	async loadUnreadCounts() {
		const res = await fetch('/api/pulsar/unread');
		this.unreadCounts = res.ok ? ((await res.json()) ?? {}) : {};
	}

	// routineById looks a routine up out of whichever of
	// routines/archivedRoutines already has it — /pulsar/[id] can be
	// reached directly (a reload, a shared link), not just by clicking
	// from the /pulsar list, so it loads both lists itself rather than
	// assuming they're already populated. There's no dedicated single-
	// routine GET endpoint — with routine counts this small for a
	// single-operator app, refetching both small lists is simpler than
	// adding one.
	routineById(id: number): PulsarRoutine | undefined {
		return this.routines.find((r) => r.id === id) ?? this.archivedRoutines.find((r) => r.id === id);
	}

	async loadPulses(routineId: number) {
		this.currentPulsesLoading = true;
		try {
			const res = await fetch(`/api/pulsar/routines/${routineId}/pulses`);
			this.currentPulses = res.ok ? ((await res.json()) ?? []) : [];
		} finally {
			this.currentPulsesLoading = false;
		}
	}

	// Returns { error } instead of throwing/returning null on failure —
	// PulsarRoutineForm.svelte shows validateSchedule's message (see
	// gateway/pulsar_routes.go) inline rather than just failing silently.
	async createRoutine(input: PulsarRoutineInput): Promise<{ routine: PulsarRoutine | null; error: string }> {
		const res = await fetch('/api/pulsar/routines', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(input)
		});
		if (!res.ok) return { routine: null, error: await res.text() };
		const routine = (await res.json()) as PulsarRoutine;
		await this.loadRoutines();
		return { routine, error: '' };
	}

	async updateRoutine(
		id: number,
		input: PulsarRoutineInput
	): Promise<{ routine: PulsarRoutine | null; error: string }> {
		const res = await fetch(`/api/pulsar/routines/${id}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(input)
		});
		if (!res.ok) return { routine: null, error: await res.text() };
		const routine = (await res.json()) as PulsarRoutine;
		await Promise.all([this.loadRoutines(), this.loadArchivedRoutines()]);
		return { routine, error: '' };
	}

	async archiveRoutine(id: number): Promise<boolean> {
		const res = await fetch(`/api/pulsar/routines/${id}/archive`, { method: 'POST' });
		if (res.ok) await Promise.all([this.loadRoutines(), this.loadArchivedRoutines()]);
		return res.ok;
	}

	async unarchiveRoutine(id: number): Promise<boolean> {
		const res = await fetch(`/api/pulsar/routines/${id}/unarchive`, { method: 'POST' });
		if (res.ok) await Promise.all([this.loadRoutines(), this.loadArchivedRoutines()]);
		return res.ok;
	}
}

export const pulsarState = new PulsarState();
