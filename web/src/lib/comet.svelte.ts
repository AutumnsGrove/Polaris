import type { CometConfig, CometConfigResponse, CometEdition, CometPeriodsResponse } from './types';

// CometConfigInput is the settings request shape — full overwrite, matching
// PulsarDailyConfigInput's convention.
export interface CometConfigInput {
	enabled: boolean;
	time_of_day: string;
	model: string;
}

// CometState is Comet's own store — a dedicated class, same shape/reasoning
// as PulsarDailyState. Comet's data (config, the viewed edition, the list of
// periods for edition navigation) is a separate concern from chat threads,
// Pulsar, Daily, and Constellation.
export class CometState {
	config = $state<CometConfig | null>(null);
	// currentPeriod: what the scheduler would generate right now (the most
	// recent complete month), regardless of whether an edition exists for it.
	currentPeriod = $state('');

	// The edition currently on screen — not necessarily the latest, since
	// the ← / → edition nav can move this backward/forward. null while
	// loading; 'not-found' distinguishes "no edition exists for this period
	// yet" (a real, expected state before Comet's first run) from "still
	// fetching"; 'error' is a network failure rather than a real 404.
	edition = $state<CometEdition | null>(null);
	editionState = $state<'loading' | 'loaded' | 'not-found' | 'error'>('loading');

	// periods: every period that has an edition, newest first — drives the
	// edition-nav arrows and whether "next" is available.
	periods = $state<string[]>([]);

	// hasNewEdition backs the footer icon's pulsing dot. Set by comparing
	// the latest edition's period against the last one actually opened
	// (localStorage, same scheme as Pulsar Daily's own dot).
	hasNewEdition = $state(false);

	async loadConfig() {
		try {
			const res = await fetch('/api/comet/config');
			// A failed refresh leaves whatever config was already loaded in
			// place — a transient blip shouldn't blank the sidebar icon or flip
			// the settings toggle off. Until a first load succeeds, config stays
			// null and the UI treats Comet as "unknown", i.e. hidden.
			if (!res.ok) return;
			const body = (await res.json()) as CometConfigResponse;
			this.config = body.config;
			this.currentPeriod = body.current_period;
		} catch {
			// silent — see above
		}
	}

	// setEnabled is the settings panel's toggle — a full-overwrite save of
	// the current config with just `enabled` changed, same shape as
	// ConstellationState.setEnabled.
	async setEnabled(v: boolean): Promise<{ error: string }> {
		return this.updateConfig({
			enabled: v,
			time_of_day: this.config?.time_of_day ?? '06:00',
			model: this.config?.model ?? ''
		});
	}

	async updateConfig(input: CometConfigInput): Promise<{ error: string }> {
		try {
			const res = await fetch('/api/comet/config', {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(input)
			});
			if (!res.ok) return { error: await res.text() };
			const body = (await res.json()) as CometConfigResponse;
			this.config = body.config;
			this.currentPeriod = body.current_period;
			return { error: '' };
		} catch {
			return { error: 'Could not reach the server — try again.' };
		}
	}

	// checkForNewEdition is a background poll (the sidebar's call site),
	// not a user action — failures are silent, same as Daily's own.
	async checkForNewEdition(lastSeenPeriod: string | null) {
		try {
			const res = await fetch('/api/comet/editions/latest');
			if (!res.ok) return;
			const latest = (await res.json()) as CometEdition;
			this.hasNewEdition = lastSeenPeriod === null || latest.period > lastSeenPeriod;
		} catch {
			// silent
		}
	}

	// markSeen records that the given period has been opened, clearing the
	// dot until a newer edition appears.
	markSeen(period: string) {
		localStorage.setItem('polaris-comet-last-seen', period);
		this.hasNewEdition = false;
	}

	async loadPeriods() {
		try {
			const res = await fetch('/api/comet/periods');
			if (!res.ok) return;
			const body = (await res.json()) as CometPeriodsResponse;
			this.periods = body.periods ?? [];
			this.currentPeriod = body.current_period;
		} catch {
			// Leave whatever we had — the edition itself still renders.
		}
	}

	// Bumped at the top of every loadEdition call so a fast pair of nav
	// clicks can't let a stale response land last — same guard shape as
	// PulsarDailyState's editionSeq.
	private editionSeq = 0;

	async loadEdition(period: string) {
		// Already showing this exact period — a no-op. The page drives loads
		// off the URL, so canonicalizing ?period once resolved would otherwise
		// re-fetch the edition it just loaded.
		if (period !== 'latest' && this.editionState === 'loaded' && this.edition?.period === period) {
			return;
		}
		this.editionState = 'loading';
		const seq = ++this.editionSeq;
		try {
			const res = await fetch(`/api/comet/editions/${encodeURIComponent(period)}`);
			if (seq !== this.editionSeq) return;
			if (res.status === 404) {
				this.edition = null;
				this.editionState = 'not-found';
				return;
			}
			// Any other non-OK status is a failed load, not "no edition yet" —
			// showing the first-run empty state (with a Generate button) for a
			// 500 would send the person to regenerate a month that exists.
			if (!res.ok) {
				this.edition = null;
				this.editionState = 'error';
				return;
			}
			this.edition = (await res.json()) as CometEdition;
			if (seq !== this.editionSeq) return;
			this.editionState = 'loaded';
		} catch {
			if (seq !== this.editionSeq) return;
			this.edition = null;
			this.editionState = 'error';
		}
	}

	// generateNow kicks off a manual classify+generate run for a period (the
	// most recent complete month when omitted). Returns an error string on
	// failure, '' when the run was accepted.
	async generateNow(period?: string): Promise<{ error: string }> {
		try {
			const url = period ? `/api/comet/generate?period=${encodeURIComponent(period)}` : '/api/comet/generate';
			const res = await fetch(url, { method: 'POST' });
			if (!res.ok) return { error: await res.text() };
			return { error: '' };
		} catch {
			return { error: 'Could not reach the server — try again.' };
		}
	}
}

export const cometState = new CometState();
