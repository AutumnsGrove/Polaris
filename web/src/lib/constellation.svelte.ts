import type {
	ConstellationConfig,
	ConstellationDigest,
	ConstellationMap,
	ConstellationStarDetail,
	ConstellationStats,
	ConstellationWeekItem,
	Star
} from './types';
import { appState } from './state.svelte';

// ConstellationConfigInput is the settings modal's request shape — full
// overwrite, matching PulsarDailyConfigInput's convention in
// pulsarDaily.svelte.ts.
export interface ConstellationConfigInput {
	enabled: boolean;
	poll_interval_minutes: number;
	model: string;
}

// ConstellationState is Constellation's own store, same shape/reasoning as
// PulsarDailyState — a dedicated class since this feature's data (config,
// stats, four star sections, digest, week feed, map) is its own concern,
// not something to fold into the main chat AppState.
export class ConstellationState {
	config = $state<ConstellationConfig | null>(null);
	stats = $state<ConstellationStats | null>(null);
	digest = $state<ConstellationDigest | null>(null);

	libraryStars = $state<Star[]>([]);
	aboutYouStars = $state<Star[]>([]);
	rejectedStars = $state<Star[]>([]);
	inboxStars = $state<Star[]>([]);

	weekItems = $state<ConstellationWeekItem[]>([]);
	mapData = $state<ConstellationMap | null>(null);

	// libraryLoaded/inboxLoaded/mapLoaded/statsLoaded/configLoaded
	// distinguish "still fetching" from "fetched, genuinely empty" — same
	// reasoning as PulsarDailyState.editionState's distinct states, so an
	// empty section isn't shown as a loading spinner forever, and a
	// still-loading one doesn't flash an empty state first. The *Error
	// flags additionally distinguish "fetched, genuinely empty/nothing
	// there" from "the fetch itself failed" — without them, a network
	// failure silently rendered identically to a real empty state (no
	// error, no retry affordance), which is what previously made the Map
	// tab and the Usage modal get stuck on "Loading…" forever whenever
	// their fetch failed instead of showing a distinct, actionable error.
	libraryLoaded = $state(false);
	libraryError = $state(false);
	inboxLoaded = $state(false);
	inboxError = $state(false);
	mapLoaded = $state(false);
	mapError = $state(false);
	statsLoaded = $state(false);
	statsError = $state(false);
	configLoaded = $state(false);
	configError = $state(false);

	// loadLibrary fetches everything the Library screen (screen 1) needs
	// in one go: the three non-inbox sections, the digest banner, and
	// stats (for the inbox-count banner) — all independent reads, so
	// Promise.all rather than sequential awaits.
	async loadLibrary() {
		this.libraryLoaded = false;
		this.libraryError = false;
		try {
			const [library, aboutYou, rejected, digest, stats] = await Promise.all([
				fetchSection('library'),
				fetchSection('about_you'),
				fetchSection('rejected'),
				fetchDigest(),
				fetchStats()
			]);
			this.libraryStars = library;
			this.aboutYouStars = aboutYou;
			this.rejectedStars = rejected;
			this.digest = digest;
			this.stats = stats;
		} catch {
			// Network failure (offline/DNS/TLS) — leave whatever was
			// previously loaded in place rather than clearing it out from
			// under the user; libraryError lets the screen show a distinct
			// "couldn't refresh" state instead of rendering this the same
			// as a genuinely empty library.
			this.libraryError = true;
		} finally {
			this.libraryLoaded = true;
		}
	}

	async loadInbox() {
		this.inboxLoaded = false;
		this.inboxError = false;
		try {
			this.inboxStars = await fetchSection('inbox');
		} catch {
			this.inboxStars = [];
			this.inboxError = true;
		} finally {
			this.inboxLoaded = true;
		}
	}

	async loadStarDetail(id: number): Promise<ConstellationStarDetail | null> {
		try {
			const res = await fetch(`/api/constellation/stars/${id}`);
			if (!res.ok) return null;
			return (await res.json()) as ConstellationStarDetail;
		} catch {
			return null;
		}
	}

	// searchStars backs the Library's search box — a plain fetch, not
	// state stored on this class, since results are ephemeral to whatever
	// the search box currently shows rather than something other views
	// need to react to (unlike libraryStars/aboutYouStars etc).
	async searchStars(query: string): Promise<Star[]> {
		try {
			const res = await fetch(`/api/constellation/stars/search?q=${encodeURIComponent(query)}`);
			return res.ok ? ((await res.json()) as Star[]) : [];
		} catch {
			return [];
		}
	}

	async loadWeek() {
		try {
			const res = await fetch('/api/constellation/week');
			this.weekItems = res.ok ? ((await res.json()) as ConstellationWeekItem[]) : [];
		} catch {
			this.weekItems = [];
		}
	}

	async loadMap() {
		this.mapLoaded = false;
		this.mapError = false;
		try {
			const res = await fetch('/api/constellation/map');
			if (!res.ok) throw new Error('map fetch failed');
			this.mapData = (await res.json()) as ConstellationMap;
		} catch {
			this.mapError = true;
		} finally {
			this.mapLoaded = true;
		}
	}

	async loadConfig() {
		this.configLoaded = false;
		this.configError = false;
		try {
			const res = await fetch('/api/constellation/config');
			if (!res.ok) throw new Error('config fetch failed');
			this.config = (await res.json()) as ConstellationConfig;
		} catch {
			this.configError = true;
		} finally {
			this.configLoaded = true;
		}
	}

	async loadStats(periodDays?: number) {
		this.statsLoaded = false;
		this.statsError = false;
		try {
			this.stats = await fetchStats(periodDays);
		} catch {
			this.statsError = true;
		} finally {
			this.statsLoaded = true;
		}
	}

	async updateConfig(input: ConstellationConfigInput): Promise<{ error: string }> {
		try {
			const res = await fetch('/api/constellation/config', {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(input)
			});
			if (!res.ok) return { error: await res.text() };
			this.config = (await res.json()) as ConstellationConfig;
			return { error: '' };
		} catch {
			return { error: 'Could not reach the server — try again.' };
		}
	}

	async patchStar(
		id: number,
		patch: { title?: string; disabled?: boolean }
	): Promise<{ error: string; star?: Star }> {
		try {
			const res = await fetch(`/api/constellation/stars/${id}`, {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(patch)
			});
			if (!res.ok) return { error: (await res.text()) || 'Something went wrong — try again.' };
			return { error: '', star: (await res.json()) as Star };
		} catch {
			return { error: 'Could not reach the server — try again.' };
		}
	}

	async restoreStar(id: number): Promise<{ error: string; star?: Star }> {
		try {
			const res = await fetch(`/api/constellation/stars/${id}/restore`, { method: 'POST' });
			if (!res.ok) return { error: (await res.text()) || 'Something went wrong — try again.' };
			const star = (await res.json()) as Star;
			// Drop it out of the local rejected list immediately rather than
			// waiting on a full loadLibrary() round-trip — the Library
			// screen's Rejected section is the only caller of this today.
			this.rejectedStars = this.rejectedStars.filter((s) => s.id !== id);
			return { error: '', star };
		} catch {
			return { error: 'Could not reach the server — try again.' };
		}
	}

	async reviewStar(
		id: number,
		action: 'approve' | 'discard' | 'refine',
		correction?: string
	): Promise<{ error: string; star?: Star }> {
		try {
			const res = await fetch(`/api/constellation/stars/${id}/review`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ action, correction: correction ?? '' })
			});
			if (!res.ok) return { error: (await res.text()) || 'Something went wrong — try again.' };
			const star = (await res.json()) as Star;
			this.inboxStars = this.inboxStars.filter((s) => s.id !== id);
			return { error: '', star };
		} catch {
			return { error: 'Could not reach the server — try again.' };
		}
	}

	async editStar(id: number, correction: string): Promise<{ error: string; star?: Star }> {
		try {
			const res = await fetch(`/api/constellation/stars/${id}/edit`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ correction })
			});
			if (!res.ok) return { error: (await res.text()) || 'Something went wrong — try again.' };
			return { error: '', star: (await res.json()) as Star };
		} catch {
			return { error: 'Could not reach the server — try again.' };
		}
	}

	// resolveSourceTitle looks up a star_sources thread_id's display title.
	// StarSource only ever carries thread_id (see store.StarSource's own
	// doc comment) — no title column exists to fetch. Rather than an
	// extra per-source request, this reads appState.threads, already
	// loaded into memory on app mount for the sidebar's own thread list.
	// Falls back to a generic label for a thread not in that list (e.g.
	// disabled/hidden, or the list hasn't loaded yet).
	resolveSourceTitle(threadId: string): string {
		return appState.threads.find((t) => t.id === threadId)?.title ?? 'Thread';
	}
}

async function fetchSection(section: 'library' | 'about_you' | 'inbox' | 'rejected'): Promise<Star[]> {
	const res = await fetch(`/api/constellation/stars?section=${section}`);
	return res.ok ? ((await res.json()) as Star[]) : [];
}

async function fetchDigest(): Promise<ConstellationDigest> {
	const res = await fetch('/api/constellation/digest');
	if (!res.ok) return { new_count: 0, links_count: 0, highlight: '', show: false };
	return (await res.json()) as ConstellationDigest;
}

async function fetchStats(periodDays?: number): Promise<ConstellationStats> {
	const qs = periodDays ? `?period_days=${periodDays}` : '';
	const res = await fetch(`/api/constellation/stats${qs}`);
	if (!res.ok) throw new Error('stats fetch failed');
	return (await res.json()) as ConstellationStats;
}

export const constellationState = new ConstellationState();
