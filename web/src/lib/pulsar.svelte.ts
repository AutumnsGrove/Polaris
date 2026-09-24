import type { FocusMode, PulsarPulse, PulsarRoutine, PulsarStats } from './types';

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

	// *Error flags — same "leave previously-loaded data in place, flag it
	// instead of clearing it" convention as constellation.svelte.ts's own
	// libraryError/etc. A transient network blip (this class's own
	// existing comments already call out "phone over Tailscale" as a real
	// case) shouldn't wipe the routines list, the unread badges, or a
	// routine's pulse history out from under whoever's looking at it —
	// especially now that /pulsar/[id]'s $effect (not just onMount)
	// re-fires these loaders on every routine-to-routine navigation, not
	// just once per page load.
	routinesError = $state(false);
	archivedRoutinesError = $state(false);
	unreadCountsError = $state(false);
	currentPulsesError = $state(false);

	// The currently-viewed routine's pulse history — /pulsar/[id] loads
	// this via loadPulses(). Not merged into routines/archivedRoutines
	// above since a pulse list can be long-ish and has nothing to do with
	// rendering the routines list itself.
	currentPulses = $state<PulsarPulse[]>([]);
	currentPulsesLoading = $state(false);

	// stats backs PulsarUsageModal — a period-scoped snapshot (cost,
	// pulse/failure counts, tool calls, nudges), same shape/reasoning as
	// constellation.svelte.ts's own stats field. statsSeq guards against
	// a slow request landing after a faster, more recent one (e.g. the
	// modal reopening with a different period before the first load
	// resolved) — same stale-response pattern used throughout this
	// codebase's other loaders.
	stats = $state<PulsarStats | null>(null);
	statsLoaded = $state(false);
	statsError = $state(false);
	private statsSeq = 0;

	// totalUnread backs the sidebar's global Orbit-icon badge — count
	// across every routine combined, per the plan doc's "Amber indicator
	// semantics".
	totalUnread = $derived(Object.values(this.unreadCounts).reduce((sum, n) => sum + n, 0));

	async loadRoutines() {
		this.routinesError = false;
		try {
			const res = await fetch('/api/pulsar/routines');
			if (!res.ok) throw new Error('routines fetch failed');
			this.routines = (await res.json()) ?? [];
		} catch {
			// Network failure (offline, DNS, TLS — a real case for "phone
			// over Tailscale") or a non-ok response — leave whatever was
			// previously loaded in place rather than clearing it out from
			// under whoever's looking at it; routinesError lets the UI show
			// a distinct "couldn't refresh" state instead of rendering this
			// the same as a genuinely empty routine list. Without this
			// catch, a rejected fetch() promise here is unhandled: nothing
			// downstream awaits loadRoutines() with its own try/catch.
			this.routinesError = true;
		} finally {
			this.loaded = true;
		}
	}

	async loadArchivedRoutines() {
		this.archivedRoutinesError = false;
		try {
			const res = await fetch('/api/pulsar/routines?archived=true');
			if (!res.ok) throw new Error('archived routines fetch failed');
			this.archivedRoutines = (await res.json()) ?? [];
		} catch {
			this.archivedRoutinesError = true;
		}
	}

	async loadUnreadCounts() {
		this.unreadCountsError = false;
		try {
			const res = await fetch('/api/pulsar/unread');
			if (!res.ok) throw new Error('unread counts fetch failed');
			this.unreadCounts = (await res.json()) ?? {};
		} catch {
			this.unreadCountsError = true;
		}
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
		this.currentPulsesError = false;
		try {
			const res = await fetch(`/api/pulsar/routines/${routineId}/pulses`);
			if (!res.ok) throw new Error('pulses fetch failed');
			this.currentPulses = (await res.json()) ?? [];
		} catch {
			this.currentPulsesError = true;
		} finally {
			this.currentPulsesLoading = false;
		}
	}

	async loadStats(periodDays?: number) {
		this.statsLoaded = false;
		this.statsError = false;
		const statsSeq = ++this.statsSeq;
		try {
			const qs = periodDays ? `?period_days=${periodDays}` : '';
			const res = await fetch(`/api/pulsar/stats${qs}`);
			if (!res.ok) throw new Error('stats fetch failed');
			const stats = (await res.json()) as PulsarStats;
			if (statsSeq === this.statsSeq) this.stats = stats;
		} catch {
			this.statsError = true;
		} finally {
			this.statsLoaded = true;
		}
	}

	// Returns { error } instead of throwing/returning null on failure —
	// PulsarRoutineForm.svelte shows validateSchedule's message (see
	// gateway/pulsar_routes.go) inline rather than just failing silently.
	// A network failure (fetch() itself rejecting) gets the same treatment
	// as a non-ok response, not an unhandled rejection.
	async createRoutine(input: PulsarRoutineInput): Promise<{ routine: PulsarRoutine | null; error: string }> {
		try {
			const res = await fetch('/api/pulsar/routines', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(input)
			});
			if (!res.ok) return { routine: null, error: await res.text() };
			const routine = (await res.json()) as PulsarRoutine;
			await this.loadRoutines();
			return { routine, error: '' };
		} catch {
			return { routine: null, error: 'Could not reach the server — try again.' };
		}
	}

	async updateRoutine(
		id: number,
		input: PulsarRoutineInput
	): Promise<{ routine: PulsarRoutine | null; error: string }> {
		try {
			const res = await fetch(`/api/pulsar/routines/${id}`, {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(input)
			});
			if (!res.ok) return { routine: null, error: await res.text() };
			const routine = (await res.json()) as PulsarRoutine;
			await Promise.all([this.loadRoutines(), this.loadArchivedRoutines()]);
			return { routine, error: '' };
		} catch {
			return { routine: null, error: 'Could not reach the server — try again.' };
		}
	}

	async archiveRoutine(id: number): Promise<boolean> {
		try {
			const res = await fetch(`/api/pulsar/routines/${id}/archive`, { method: 'POST' });
			if (res.ok) await Promise.all([this.loadRoutines(), this.loadArchivedRoutines()]);
			return res.ok;
		} catch {
			return false;
		}
	}

	async unarchiveRoutine(id: number): Promise<boolean> {
		try {
			const res = await fetch(`/api/pulsar/routines/${id}/unarchive`, { method: 'POST' });
			if (res.ok) await Promise.all([this.loadRoutines(), this.loadArchivedRoutines()]);
			return res.ok;
		} catch {
			return false;
		}
	}
}

export const pulsarState = new PulsarState();
