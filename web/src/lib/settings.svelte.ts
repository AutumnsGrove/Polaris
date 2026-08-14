import { getManualLocation, setManualLocation as persistManualLocation } from './geolocation';
import type { FocusMode } from './types';

export type UpdateState = 'idle' | 'updating' | 'restarting' | 'error';

// Mirrors store.Stats (store/stats.go) — kept as plain counts/percentages
// rather than a time series, since the settings panel only ever shows a
// handful of numbers, not a chart.
export interface UsageStats {
	period_days: number;
	total_cost_usd: number;
	period_cost_usd: number;
	thread_count: number;
	turn_count: number;
	avg_turn_duration_ms: number;
	tool_call_counts: Record<string, number>;
	tool_error_counts: Record<string, number>;
	check_in_count: number;
	stale_streak_count: number;
	max_turns_wrapup_count: number;
	compaction_count: number;
}

// User-adjustable UI preferences, split out of state.svelte.ts since
// they're a self-contained concern: load once, persist to /api/settings,
// apply the theme attribute — none of it touches thread/turn state.
export class SettingsState {
	open = $state(false);
	theme = $state<'dark' | 'light'>('dark');
	defaultModel = $state('');

	// The composer's standing focus mode — applied to every new message
	// until changed from the "+" menu, same "sticky until changed"
	// semantics as defaultModel (see +page.svelte's initial focusMode).
	// '' means "off" — no default.
	defaultFocusMode = $state<FocusMode>('off');

	// How VoiceButton.svelte's mic button behaves — 'toggle' (tap to
	// start, tap again to stop) or 'hold' (press and hold, release to
	// send — the original behavior). See gateway/settings.go's
	// settingVoiceInputMode for why 'toggle' is the default.
	voiceInputMode = $state<'hold' | 'toggle'>('toggle');

	// Fallback for nearby_search when the browser's real Geolocation API
	// isn't available (plain HTTP, permission denied) — a plain-text
	// address/city, client-side only (a cookie, not /api/settings), since
	// it describes this browser's location, not shared server config. See
	// geolocation.ts's getUserLocation for the precedence over a real fix.
	manualLocation = $state('');

	// Context-usage display, next to thread cost. contextWindowTokens is
	// the auto-compaction threshold from config.yaml (loaded once via
	// load()) — the denominator for the % shown in +page.svelte.
	contextWindowTokens = $state(100_000);

	// Self-update progress. Deliberately owned here, not as local state in
	// SettingsPanel.svelte — that component unmounts entirely whenever the
	// panel closes ({#if appState.settings.open} in +layout.svelte), which
	// used to throw this away and show a freshly-idle button on reopening
	// even while a git pull + go build was still churning away server-side.
	// Living on this always-alive singleton means the in-flight pushUpdate()
	// promise keeps updating these fields regardless of whether the panel
	// is currently mounted to see them.
	updateState = $state<UpdateState>('idle');
	updateLog = $state('');

	// Usage/tuning snapshot for the settings panel's Usage section — null
	// until loadUsage() resolves (or forever, on a fetch failure; the
	// panel just doesn't render that section then). Trailing-30-day scope
	// matches the CLI's `polaris stats` default.
	usage = $state<UsageStats | null>(null);

	// True once load() has resolved — +page.svelte's composer uses this
	// (not just checking defaultFocusMode's value) to apply the loaded
	// default exactly once at startup, since 'off' is itself a valid
	// loaded value and can't be distinguished from "hasn't loaded yet".
	loaded = $state(false);

	async load() {
		const res = await fetch('/api/settings');
		if (!res.ok) return;
		const data = await res.json();
		this.theme = data.theme === 'light' ? 'light' : 'dark';
		this.defaultModel = data.default_model ?? '';
		this.defaultFocusMode = (data.default_focus_mode || 'off') as FocusMode;
		this.voiceInputMode = data.voice_input_mode === 'hold' ? 'hold' : 'toggle';
		this.contextWindowTokens = data.context_window_tokens ?? 100_000;
		this.manualLocation = getManualLocation();
		this.applyTheme();
		this.loaded = true;
	}

	private applyTheme() {
		if (typeof document !== 'undefined') {
			document.documentElement.setAttribute('data-theme', this.theme);
		}
	}

	async setTheme(theme: 'dark' | 'light') {
		this.theme = theme;
		this.applyTheme();
		await this.put({ theme });
	}

	// onModelChanged lets the caller (AppState) refresh its model list's
	// `default` flag after this settings-panel override takes effect —
	// kept as a callback rather than importing AppState here to avoid a
	// circular module dependency.
	async setDefaultModel(modelId: string, onModelChanged?: () => void) {
		this.defaultModel = modelId;
		await this.put({ default_model: modelId });
		await onModelChanged?.();
	}

	async setDefaultFocusMode(mode: FocusMode) {
		this.defaultFocusMode = mode;
		await this.put({ default_focus_mode: mode });
	}

	async setVoiceInputMode(mode: 'hold' | 'toggle') {
		this.voiceInputMode = mode;
		await this.put({ voice_input_mode: mode });
	}

	// Client-side only — no server round trip, unlike the settings above.
	setManualLocation(value: string) {
		this.manualLocation = value.trim();
		persistManualLocation(value);
	}

	private async put(body: Record<string, unknown>) {
		await fetch('/api/settings', {
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(body)
		});
	}

	toggle() {
		this.open = !this.open;
	}

	// Runs the same git-pull-and-rebuild the CLI's `polaris update` does.
	// Guarded by updateState itself (SettingsPanel disables the trigger
	// button unless idle/error) — the server independently rejects a
	// second overlapping call regardless, since this button surviving a
	// closed-and-reopened panel is exactly what invites a double-click.
	//
	// isBusy lets the caller (SettingsPanel.svelte, via appState.busy) tell
	// waitForServerAndReload not to fire its reload while a turn is still
	// streaming — see that function's doc comment for why this matters.
	// Optional, defaulting to "never busy", so a caller with nothing to
	// check (there is none today, but nothing here should require one)
	// still gets a working reload.
	async pushUpdate(isBusy: () => boolean = () => false) {
		this.updateState = 'updating';
		this.updateLog = '';
		try {
			const res = await fetch('/api/update', { method: 'POST' });
			const result = await res.json();
			this.updateLog = result.log ?? '';
			if (!result.success) {
				this.updateState = 'error';
				if (result.already_running) {
					this.updateLog = result.error ?? 'an update is already in progress';
				}
				return;
			}
			if (!result.restarting) {
				// Not running under systemd/launchd — rebuilt, but nothing
				// will restart it automatically.
				this.updateState = 'idle';
				return;
			}
			this.updateState = 'restarting';
			await this.waitForServerAndReload(isBusy);
		} catch (err) {
			this.updateLog = String(err);
			this.updateState = 'error';
		}
	}

	// The build (go build on the potato's ARM CPU can take a while) and
	// restart happen out from under the request that triggered them, so
	// poll until the *new* binary answers, then hard-reload — a normal
	// client-side nav would keep running the *old* JS bundle even after
	// the backend and its embedded frontend assets have updated.
	//
	// Polls /api/version specifically, and waits for it to actually
	// *change* from the baseline captured at the top — not just "did some
	// server respond" (this used to hit /api/models and reload the
	// instant that came back 200). handleUpdate's restart goroutine
	// sleeps only 300ms before calling `sudo systemctl restart`
	// (procmgr/systemd.go), which itself is a polkit/D-Bus round trip
	// that can easily take longer than that on the potato's weak CPU — so
	// for the first poll or two, the OLD process is very often still
	// alive and answering completely normally. Reloading on "a server
	// responded" landed back on that same still-old binary and embedded
	// frontend, silently failing to update while reporting success —
	// which is exactly the "had to click the update button twice" pattern
	// this was hardened against: the first click's reload fired too
	// early, and the real restart only happened later, unannounced,
	// possibly mid-conversation.
	//
	// isBusy is the OTHER half of that same "possibly mid-conversation"
	// problem: the new binary can easily come up while a turn is still
	// streaming on this very client (e.g. someone hits Update, then, out
	// of habit or to test whether it worked, sends another message before
	// the poll below has resolved). checkVersion()'s own periodic version
	// poll (state.svelte.ts) learned this lesson already — it defers its
	// reload until !appState.busy specifically to avoid yanking
	// busy/pendingTurn/pendingThreadId out from under an in-flight turn.
	// This loop is a separate reload path that grew independently and
	// never got the same guard, so it kept firing unconditionally the
	// instant the version changed — wiping client-side turn state while
	// the server kept streaming, and (worse, on a brand-new thread)
	// landing the reload on whatever URL happened to be in the address
	// bar at that instant rather than the thread the turn belongs to.
	// Once the version has changed, this simply keeps polling instead of
	// reloading for as long as isBusy() says so — same bound as the outer
	// 2-minute deadline below, not a separate one.
	private async waitForServerAndReload(isBusy: () => boolean) {
		let baseline = '';
		try {
			const res = await fetch('/api/version', { cache: 'no-store' });
			if (res.ok) baseline = (await res.json()).version ?? '';
		} catch {
			// Already down by the time we got here — fine, the loop below
			// falls back to "any successful response" when there's no
			// baseline to compare against.
		}

		const deadline = Date.now() + 120_000;
		while (Date.now() < deadline) {
			await new Promise((r) => setTimeout(r, 1500));

			// Check for a definitive restart failure first so a dead
			// `sudo systemctl restart` (bad unit file, polkit denial, ...)
			// surfaces immediately instead of silently spinning here for
			// the full 2 minutes while the old process keeps answering
			// with its old, unchanged version.
			try {
				const statusRes = await fetch('/api/update/status', { cache: 'no-store' });
				if (statusRes.ok) {
					const status = await statusRes.json();
					if (status.restart_error) {
						this.updateState = 'error';
						this.updateLog += `\n\nBuild succeeded, but restarting the service failed: ${status.restart_error}`;
						return;
					}
				}
			} catch {
				// Best-effort — fall through to the version poll below.
			}

			try {
				const res = await fetch('/api/version', { cache: 'no-store' });
				if (res.ok) {
					const newVersion = (await res.json()).version ?? '';
					if (!baseline || (newVersion && newVersion !== baseline)) {
						if (isBusy()) {
							// The new binary is up, but this client still has a
							// turn in flight — reloading now would cut it off
							// mid-stream. Keep polling; the next iteration
							// re-checks isBusy() and reloads the moment it
							// clears, same deadline as everything else here.
							continue;
						}
						window.location.reload();
						return;
					}
					// Still answering, but with the same version as before —
					// the real restart hasn't landed yet. Keep polling
					// instead of treating this as "the new binary is up".
				}
			} catch {
				// still down — keep polling
			}
		}
		this.updateState = 'error';
		this.updateLog += '\n\nServer did not come back within 2 minutes — check it manually.';
	}

	// Checks whether an update triggered earlier (possibly by a now-gone
	// page load — a reload, or the tab getting backgrounded/killed and
	// reopened, common on mobile) is still running, so this session can
	// resume showing progress instead of assuming idle. Called once at
	// app startup; cheap enough to also call whenever the settings panel
	// opens, in case an update finished (or started, from another tab)
	// since the last check.
	//
	// isBusy — see waitForServerAndReload's doc comment — is threaded
	// through to whichever of pollUntilFinished/waitForServerAndReload
	// this ends up calling, same as pushUpdate does.
	async checkUpdateStatus(isBusy: () => boolean = () => false) {
		if (this.updateState === 'updating' || this.updateState === 'restarting') return;
		try {
			const res = await fetch('/api/update/status');
			if (!res.ok) return;
			const data = await res.json();
			if (data.running) {
				this.updateState = 'updating';
				await this.pollUntilFinished(isBusy);
			} else if (data.restart_error) {
				// The build succeeded and a restart was attempted, but the
				// restart command itself (systemctl/launchctl) failed — the
				// process now serving this request is still the pre-update
				// one. See gateway/update.go's restartErr doc comment.
				this.updateState = 'error';
				this.updateLog = (data.log ?? '') + `\n\nBuild succeeded, but restarting the service failed: ${data.restart_error}`;
			} else if (data.done && data.restarting) {
				// Caught in the narrow window after the build finished but
				// before the process actually restarts — /api/version below
				// will keep answering with the pre-update version for a
				// little while yet, then change once the new binary is
				// actually up (see waitForServerAndReload's doc comment).
				this.updateState = 'restarting';
				await this.waitForServerAndReload(isBusy);
			} else if (data.done && !data.success) {
				this.updateState = 'error';
				this.updateLog = (data.log ?? '') + (data.error ? `\n\n${data.error}` : '');
			}
		} catch {
			// Best-effort — a normal idle server with nothing to report
			// shouldn't surface a network hiccup here as an error state.
		}
	}

	// Re-fetched on every settings panel open (see SettingsPanel.svelte),
	// same as checkUpdateStatus — cheap enough, and stats from the last
	// time the panel happened to be open would be a stale, misleading
	// snapshot for a "should I tune maxAgentTurns" decision.
	async loadUsage() {
		try {
			const res = await fetch('/api/stats?days=30');
			if (!res.ok) return;
			this.usage = await res.json();
		} catch {
			// Best-effort — same rationale as checkUpdateStatus: a network
			// hiccup here shouldn't surface as an error, the section just
			// stays hidden.
		}
	}

	private async pollUntilFinished(isBusy: () => boolean) {
		while (this.updateState === 'updating') {
			await new Promise((r) => setTimeout(r, 2000));
			try {
				const res = await fetch('/api/update/status');
				if (!res.ok) continue;
				const data = await res.json();
				if (data.running) continue;
				if (data.restart_error) {
					this.updateState = 'error';
					this.updateLog = (data.log ?? '') + `\n\nBuild succeeded, but restarting the service failed: ${data.restart_error}`;
				} else if (data.restarting) {
					this.updateState = 'restarting';
					await this.waitForServerAndReload(isBusy);
				} else if (data.success) {
					this.updateState = 'idle';
					this.updateLog = data.log ?? '';
				} else {
					this.updateState = 'error';
					this.updateLog = (data.log ?? '') + (data.error ? `\n\n${data.error}` : '');
				}
				return;
			} catch {
				// Transient — keep polling until the deadline-free loop
				// above naturally resolves once the server responds again.
			}
		}
	}
}
