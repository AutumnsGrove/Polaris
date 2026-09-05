import type { PulsarDailyConfig, PulsarDailyEdition } from './types';

// PulsarDailyConfigInput is the setup modal's request shape — full
// overwrite, matching PulsarRoutineInput's convention in pulsar.svelte.ts.
export interface PulsarDailyConfigInput {
	enabled_blocks: string[];
	sports_teams: string;
	architect_model: string;
	writer_model: string;
	time_of_day: string;
}

// PulsarDailyState is Pulsar Daily's own store, same shape/reasoning as
// PulsarState in pulsar.svelte.ts — a dedicated class since Daily's data
// (config, today's edition, browsed history) is a separate concern from
// both chat threads and routines.
export class PulsarDailyState {
	config = $state<PulsarDailyConfig | null>(null);

	// The edition currently on screen — not necessarily today's, since
	// the "← Yesterday"/"Tomorrow →" nav can move this backward/forward.
	// null while loading; 'not-found' distinguishes "no edition exists
	// for this date yet" (a real, expected state before the first
	// generation ever runs) from "still fetching".
	edition = $state<PulsarDailyEdition | null>(null);
	editionState = $state<'loading' | 'loaded' | 'not-found'>('loading');

	// hasNewEdition backs the sidebar's plain dot indicator (singleton,
	// so just a boolean — see the plan doc's "Indicator is a plain dot,
	// not a numeric badge"). Set by checking whether the latest edition's
	// date is newer than the last one actually opened.
	hasNewEdition = $state(false);

	async loadConfig() {
		const res = await fetch('/api/pulsar/daily/config');
		this.config = res.ok ? ((await res.json()) as PulsarDailyConfig) : null;
	}

	async updateConfig(input: PulsarDailyConfigInput): Promise<{ error: string }> {
		const res = await fetch('/api/pulsar/daily/config', {
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(input)
		});
		if (!res.ok) return { error: await res.text() };
		this.config = (await res.json()) as PulsarDailyConfig;
		return { error: '' };
	}

	// loadEdition fetches "latest", a specific "YYYY-MM-DD", or the
	// edition strictly before one (for the "← Yesterday" nav — via
	// LatestDailyEdition's semantics, which correctly skips a missed day
	// rather than 404ing on it).
	async loadEdition(date: string, mode: 'exact' | 'before' = 'exact') {
		this.editionState = 'loading';
		const path =
			mode === 'before'
				? `/api/pulsar/daily/editions/${date}/previous`
				: `/api/pulsar/daily/editions/${date}`;
		const res = await fetch(path);
		if (res.status === 404) {
			this.edition = null;
			this.editionState = 'not-found';
			return;
		}
		if (!res.ok) {
			this.edition = null;
			this.editionState = 'not-found';
			return;
		}
		this.edition = (await res.json()) as PulsarDailyEdition;
		this.editionState = 'loaded';
	}

	async checkForNewEdition(lastSeenDate: string | null) {
		const res = await fetch('/api/pulsar/daily/editions/latest');
		if (!res.ok) return;
		const latest = (await res.json()) as PulsarDailyEdition;
		this.hasNewEdition = lastSeenDate === null || latest.date > lastSeenDate;
	}

	// expandBlock seeds a real thread from one card's content — see
	// gateway/pulsar_daily_routes.go's handleExpandDailyBlock. Returns the
	// new thread id to navigate to, or null on failure.
	async expandBlock(date: string, blockKey: string): Promise<string | null> {
		const res = await fetch('/api/pulsar/daily/expand', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ date, block_key: blockKey })
		});
		if (!res.ok) return null;
		const { thread_id } = (await res.json()) as { thread_id: string };
		return thread_id;
	}
}

export const pulsarDailyState = new PulsarDailyState();
