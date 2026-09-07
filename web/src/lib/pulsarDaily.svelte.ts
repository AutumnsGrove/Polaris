import type { PulsarDailyConfig, PulsarDailyCustomBlock, PulsarDailyEdition } from './types';

// PulsarDailyConfigInput is the setup modal's request shape — full
// overwrite, matching PulsarRoutineInput's convention in pulsar.svelte.ts.
export interface PulsarDailyConfigInput {
	enabled_blocks: string[];
	sports_teams: string;
	custom_instructions: Record<string, string>;
	custom_blocks: PulsarDailyCustomBlock[];
	weather_location: string;
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
	// edition strictly before/after one (for the "← Previous"/"Next →"
	// nav — via LatestDailyEdition/NextDailyEdition's semantics, which
	// correctly skip a missed day rather than 404ing on it).
	async loadEdition(date: string, mode: 'exact' | 'before' | 'after' = 'exact') {
		this.editionState = 'loading';
		const path =
			mode === 'before'
				? `/api/pulsar/daily/editions/${date}/previous`
				: mode === 'after'
					? `/api/pulsar/daily/editions/${date}/next`
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

	// generateNow triggers a real Daily generation immediately, bypassing
	// time_of_day — previously the only way to force a run was editing
	// time_of_day to land between last_generated_at and now and waiting
	// for the scheduler's own once-a-minute tick, a workaround with no
	// place in the settings panel. Fire-and-forget: the backend returns
	// 202 the instant the pipeline starts in the background, not once
	// it's done (Stage A-D can take several minutes of real research
	// calls). 409 means one's already running — surfaced as a distinct
	// result so the UI can say so instead of a generic failure.
	async generateNow(): Promise<{ error: string; alreadyRunning: boolean }> {
		const res = await fetch('/api/pulsar/daily/generate', { method: 'POST' });
		if (res.status === 409) return { error: 'A generation is already running.', alreadyRunning: true };
		if (!res.ok) return { error: (await res.text()) || 'Something went wrong — try again.', alreadyRunning: false };
		return { error: '', alreadyRunning: false };
	}

	async checkForNewEdition(lastSeenDate: string | null) {
		const res = await fetch('/api/pulsar/daily/editions/latest');
		if (!res.ok) return;
		const latest = (await res.json()) as PulsarDailyEdition;
		this.hasNewEdition = lastSeenDate === null || latest.date > lastSeenDate;
	}

	// resolveExpand looks up what a card's expand-to-chat message should
	// say — see gateway/pulsar_daily_routes.go's handleExpandDailyBlock.
	// Doesn't run the turn itself (that used to block the frontend from
	// navigating until the whole answer finished); the caller sends the
	// result over the live WebSocket instead, the same path any typed
	// message already takes, so navigation and streaming happen exactly
	// like a message the user sent themselves. itemIndex, when given,
	// scopes the seed to one story within a list-shaped block's items
	// instead of the whole block — the per-story "Continue in chat".
	async resolveExpand(
		date: string,
		blockKey: string,
		itemIndex?: number
	): Promise<DailyExpandResolution | null> {
		const res = await fetch('/api/pulsar/daily/expand', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ date, block_key: blockKey, item_index: itemIndex })
		});
		if (!res.ok) return null;
		return (await res.json()) as DailyExpandResolution;
	}
}

// DailyExpandResolution mirrors gateway/pulsar_daily_routes.go's
// pulsarDailyExpandResponse.
export interface DailyExpandResolution {
	content: string;
	attachment_id?: string;
	attachment_filename?: string;
	attachment_content_type?: string;
}

export const pulsarDailyState = new PulsarDailyState();
