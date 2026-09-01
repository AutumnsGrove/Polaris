import { getManualLocation, setManualLocation as persistManualLocation } from './geolocation';
import type { FocusMode, ToggleableTool } from './types';

export type UpdateState = 'idle' | 'updating' | 'restarting' | 'error';

// Which operation updateState/updateLog currently describe — 'update' is
// the full git-pull-and-rebuild flow (pushUpdate), 'restart' is just the
// service restart with no pull or rebuild (pushRestart), for when there's
// nothing new to pull and running the full update flow anyway would stall
// on a no-op pull + a real (if usually fast) rebuild for zero benefit. Both
// share one status slot server-side (see gateway/update.go's updateStatus
// doc comment on why they're mutually exclusive) and this same client-side
// state machine — kind only changes which copy the settings panel shows.
export type UpdateKind = 'update' | 'restart';

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
	// How many web_search calls each provider key actually answered —
	// see store.Stats.SearchProviderCounts' doc comment for why this
	// isn't the same thing as a billing/usage-cap count.
	search_provider_counts: Record<string, number>;
	check_in_count: number;
	stale_streak_count: number;
	max_turns_wrapup_count: number;
	compaction_count: number;
}

// Mirrors store.Memory (store/memory.go) — the full row, content included,
// for the settings panel's Memory list. Distinct from the lean name/type/
// description index the model gets every turn (see tools.MemoryIndexEntry)
// since a settings-panel page load isn't cost-sensitive the way every
// single agent turn is.
export interface Memory {
	name: string;
	type: 'user' | 'feedback' | 'project' | 'reference';
	description: string;
	content: string;
	created_at: string;
	updated_at: string;
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

	// Tools section — see gateway/settings.go's toggleable_tools/
	// disabled_tools and ToolSettings.svelte. toggleableTools is static
	// catalog data (name + description), refreshed on every load() the
	// same as everything else here; disabledTools is the actual per-user
	// setting, a plain name list rather than a Set since it's small and
	// only ever iterated/rebuilt wholesale, never looked up by key.
	toggleableTools = $state<ToggleableTool[]>([]);
	disabledTools = $state<string[]>([]);

	// Master on/off for the whole Memory feature — a dedicated setting
	// (not part of disabledTools/toggleableTools above), since it's not
	// just a tool the model can call: turning it off also stops the
	// {memories} prompt section (see MemoryEnabledFromStore's doc comment
	// in gateway/settings.go). Defaults true so an install that's never
	// touched this setting behaves exactly as before it existed.
	memoryEnabled = $state(true);

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
	// Which of pushUpdate/pushRestart updateState/updateLog describe right
	// now — set at kickoff by whichever one ran, or by checkUpdateStatus
	// when resuming a run this client didn't itself start (a reload mid-
	// operation, or another tab/device having triggered it).
	updateKind = $state<UpdateKind>('update');

	// Usage/tuning snapshot for the settings panel's Usage section — null
	// until loadUsage() resolves (or forever, on a fetch failure; the
	// panel just doesn't render that section then). Trailing-30-day scope
	// matches the CLI's `polaris stats` default.
	usage = $state<UsageStats | null>(null);

	// Memory settings section — see MemorySettings.svelte. memoriesLoaded
	// mirrors `loaded` above: null/empty is a real, valid state ("nothing
	// saved yet"), so a separate flag is what distinguishes "hasn't fetched
	// yet" from "fetched, and there's nothing there".
	memories = $state<Memory[]>([]);
	memoriesLoaded = $state(false);
	// True only while a memory-chat instruction is in flight — a real LLM
	// round trip (see gateway/memories.go's handleMemoryChat), not
	// instant, so the input needs a visible busy state same as the
	// composer does for a normal turn.
	memoryChatBusy = $state(false);
	// Last instruction's plain-text confirmation, shown under the input
	// until the next one replaces it or the panel closes — not an error
	// channel; a failed request uses showToast instead (see
	// sendMemoryInstruction).
	memoryChatMessage = $state('');

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
		this.toggleableTools = data.toggleable_tools ?? [];
		this.disabledTools = data.disabled_tools ?? [];
		this.memoryEnabled = data.memory_enabled ?? true;
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

	// Flips one tool's enabled state and persists the whole updated list —
	// handlePutSettings replaces disabled_tools wholesale rather than
	// diffing a single add/remove, same "send the full value" shape as
	// every other setting here.
	async setToolEnabled(name: string, enabled: boolean) {
		const next = enabled ? this.disabledTools.filter((t) => t !== name) : [...this.disabledTools, name];
		this.disabledTools = next;
		await this.put({ disabled_tools: next });
	}

	async setMemoryEnabled(enabled: boolean) {
		this.memoryEnabled = enabled;
		await this.put({ memory_enabled: enabled });
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
		this.updateKind = 'update';
		this.updateState = 'updating';
		this.updateLog = '';
		try {
			const res = await fetch('/api/update', { method: 'POST' });
			const result = await res.json();
			this.updateLog = result.log ?? '';
			if (!result.success) {
				this.updateState = 'error';
				if (result.already_running) {
					this.updateLog = result.error ?? 'an update or restart is already in progress';
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

	// Cleanly restarts the service with no git pull, no go build — just
	// `mgr.Restart()` server-side (see gateway/update.go's handleRestart).
	// For when there's nothing new to pull: running pushUpdate anyway
	// still does a real pull (a no-op) and rebuild before it ever
	// restarts anything, which can stall on the potato's weak CPU for no
	// benefit — this is the "just clear things out" button. Otherwise
	// identical in shape to pushUpdate (same status slot server-side, same
	// isBusy-guarded reload), just without the build phase.
	async pushRestart(isBusy: () => boolean = () => false) {
		this.updateKind = 'restart';
		this.updateState = 'updating';
		this.updateLog = '';
		try {
			const res = await fetch('/api/restart', { method: 'POST' });
			const result = await res.json();
			if (!result.success) {
				this.updateState = 'error';
				this.updateLog = result.already_running
					? (result.error ?? 'an update or restart is already in progress')
					: (result.error ?? 'restart failed');
				return;
			}
			this.updateState = 'restarting';
			await this.waitForServerAndReload(isBusy);
		} catch (err) {
			this.updateLog = String(err);
			this.updateState = 'error';
		}
	}

	// Phrasing for a failed restart command differs by kind — "build
	// succeeded, but..." is misleading when there was no build (a plain
	// restart). Shared by waitForServerAndReload and checkUpdateStatus/
	// pollUntilFinished's identical restart_error branches.
	private restartFailedPrefix(): string {
		return this.updateKind === 'restart' ? 'Restart command failed: ' : 'Build succeeded, but restarting the service failed: ';
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

		// A plain restart (updateKind === 'restart') never changes the
		// reported version at all — same binary/image, just a fresh
		// process, by definition (see gateway/docker_update.go's
		// handleDockerRestart / handleRestart's "no build, just
		// restart" doc comments) — so requiring newVersion !== baseline
		// below would spin for the full 2 minutes and report failure
		// even on a genuinely successful restart. baseline is almost
		// always captured successfully just above (the old process is
		// still briefly answering when this function starts), so
		// !baseline essentially never saves this case either. Tracking
		// whether the server was ever actually observed unreachable
		// during this poll is real evidence a restart happened; for a
		// restart, "back up AND we saw it go down first" replaces "the
		// version changed" as the success condition. Covers both
		// failure shapes a down backend can produce: fetch() throwing
		// (connection refused) and a reverse proxy like Tailscale Serve
		// returning a non-2xx instead of refusing the connection.
		let sawDowntime = false;

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
						this.updateLog += `\n\n${this.restartFailedPrefix()}${status.restart_error}`;
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
					const versionChanged = !baseline || (newVersion && newVersion !== baseline);
					const restartConfirmed = this.updateKind === 'restart' && sawDowntime;
					if (versionChanged || restartConfirmed) {
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
					// Still answering with the same version, and (for a
					// restart) haven't yet seen it go down — the real
					// restart hasn't landed yet. Keep polling instead of
					// treating this as "the new binary is up".
				} else {
					sawDowntime = true;
				}
			} catch {
				// still down — keep polling
				sawDowntime = true;
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
			// Resuming a run this client didn't itself start (a reload mid-
			// operation, another tab/device) — take kind from the server's
			// own record of which one is/was actually running.
			if (data.kind === 'update' || data.kind === 'restart') this.updateKind = data.kind;
			if (data.running) {
				this.updateState = 'updating';
				await this.pollUntilFinished(isBusy);
			} else if (data.restart_error) {
				// The operation succeeded and a restart was attempted, but
				// the restart command itself (systemctl/launchctl) failed —
				// the process now serving this request is still the
				// pre-restart one. See gateway/update.go's restartErr doc
				// comment.
				this.updateState = 'error';
				this.updateLog = (data.log ?? '') + `\n\n${this.restartFailedPrefix()}${data.restart_error}`;
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

	// Re-fetched every time MemorySettings mounts, same "don't trust a
	// stale snapshot from the last time this was open" reasoning as
	// loadUsage.
	async loadMemories() {
		try {
			const res = await fetch('/api/memories');
			if (!res.ok) return;
			this.memories = await res.json();
		} catch {
			// Best-effort — the section just shows its loading/empty state.
		} finally {
			this.memoriesLoaded = true;
		}
	}

	async deleteMemory(name: string) {
		const res = await fetch(`/api/memories/${encodeURIComponent(name)}`, { method: 'DELETE' });
		if (res.ok) {
			this.memories = this.memories.filter((m) => m.name !== name);
		}
		return res.ok;
	}

	// Drives the "tell it what to change or remove" box — a single
	// stateless instruction (see gateway/memories.go's handleMemoryChat's
	// doc comment for why this isn't a persisted conversation), resolved
	// server-side into whatever memory tool calls it implies, then
	// returned as a short confirmation plus the fully refreshed list so
	// this doesn't need a separate loadMemories() round trip after.
	async sendMemoryInstruction(instruction: string) {
		this.memoryChatBusy = true;
		this.memoryChatMessage = '';
		try {
			const res = await fetch('/api/memories/chat', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ instruction })
			});
			if (!res.ok) {
				this.memoryChatMessage = "Couldn't process that — try rephrasing.";
				return;
			}
			const data = await res.json();
			this.memoryChatMessage = data.message ?? '';
			this.memories = data.memories ?? this.memories;
		} catch {
			this.memoryChatMessage = "Couldn't reach the server — try again.";
		} finally {
			this.memoryChatBusy = false;
		}
	}

	private async pollUntilFinished(isBusy: () => boolean) {
		while (this.updateState === 'updating') {
			await new Promise((r) => setTimeout(r, 2000));
			try {
				const res = await fetch('/api/update/status');
				if (!res.ok) continue;
				const data = await res.json();
				if (data.kind === 'update' || data.kind === 'restart') this.updateKind = data.kind;
				if (data.running) continue;
				if (data.restart_error) {
					this.updateState = 'error';
					this.updateLog = (data.log ?? '') + `\n\n${this.restartFailedPrefix()}${data.restart_error}`;
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
