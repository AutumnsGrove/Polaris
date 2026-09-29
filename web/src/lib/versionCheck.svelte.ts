import { debugBeacon } from './stateHelpers';

// Polls /api/version and reloads the page once a new build lands and no turn is
// in flight. context() supplies the two bits of AppState it needs to decide that
// (busy and currentThreadId) without this class depending on AppState itself.
export class VersionState {
	version = $state<string>('');
	// 'bare-metal' | 'docker' | '' (not yet loaded) — see gateway/version.go's
	// deploymentMode. Purely a display signal for the settings panel's
	// version-row icon, set alongside version in checkVersion() below.
	deployment = $state<string>('');
	interval: number | null = null;

	constructor(private context: () => { busy: boolean; currentThreadId: string | null }) {}

	async start() {
		// Check version immediately on connect
		await this.check();
	
		// Then poll every 30 seconds
		if (typeof window !== 'undefined') {
			this.interval = window.setInterval(() => {
				void this.check();
			}, 30000);
		}
	}
	
	async check() {
		try {
			const res = await fetch('/api/version');
			const data = await res.json();
			const newVersion = data.version ?? '';
			// Static for the process's whole lifetime (only a real restart
			// changes it) — fine to just assign unconditionally on every
			// poll, unlike version's mismatch-triggers-a-reload dance below.
			this.deployment = data.deployment ?? '';
	
			if (this.version && newVersion && this.version !== newVersion) {
				debugBeacon('checkVersion mismatch detected', {
					oldVersion: this.version,
					newVersion,
					busy: this.context().busy,
					currentThreadId: this.context().currentThreadId
				});
				// A new build landed — but reloading immediately would yank
				// an in-flight turn out from under the user: it wipes
				// busy/pendingTurn/pendingThreadId client-side while the
				// turn keeps running server-side regardless. Deferring
				// until nothing's in flight — and deliberately NOT updating
				// this.version below so this same branch re-fires — is what
				// makes the reload land at a safe moment. handleEvent's
				// 'done'/'error' cases call this again the instant busy
				// clears, so the retry happens within moments of the turn
				// finishing rather than waiting out the rest of this 30s
				// poll interval.
				//
				// Navigating to an explicit href (not a bare reload())
				// matters: a bare reload() trusts window.location.pathname
				// to already reflect whatever thread is actually current,
				// but syncURL's replaceState calls only fire from specific
				// call sites (openThread, newThread, a just-learned new
				// thread id) — 'done' itself never re-syncs the URL, so any
				// path where the address bar and currentThreadId can
				// legitimately drift apart for a moment (confirmed
				// happening in practice, not just theoretical) turns into
				// reload() silently landing on whatever the browser's
				// address bar happened to still say, which can be a
				// completely unrelated thread from earlier in the session
				// rather than "the homescreen" this comment used to assume.
				// Building the URL explicitly from currentThreadId — the
				// same source of truth syncURL itself uses — removes that
				// gap by construction instead of relying on timing.
				if (!this.context().busy && typeof window !== 'undefined') {
					const path = this.context().currentThreadId ? `/t/${this.context().currentThreadId}` : '/';
					debugBeacon('checkVersion reloading', {
						oldVersion: this.version,
						newVersion,
						currentThreadId: this.context().currentThreadId,
						currentPathname: window.location.pathname,
						targetPath: path
					});
					// Checked before navigating, not after: whether an href
					// assignment updates window.location synchronously or
					// only once the new document actually loads isn't
					// consistent across environments (confirmed different
					// between real browsers and jsdom), so branching on the
					// current path up front is the only deterministic way
					// to pick reload() vs. href — see this block's doc
					// comment above for why the target must be explicit.
					if (window.location.pathname === path) {
						window.location.reload();
					} else {
						window.location.href = path;
					}
				}
				return;
			}
			this.version = newVersion;
		} catch (err) {
			// Silently ignore - don't spam errors for version checks
		}
	}
}
