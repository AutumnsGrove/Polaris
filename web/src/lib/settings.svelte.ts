import { getManualLocation, setManualLocation as persistManualLocation } from './geolocation';
import type { FocusMode } from './types';

export type UpdateState = 'idle' | 'updating' | 'restarting' | 'error';

// User-adjustable UI preferences, split out of state.svelte.ts since
// they're a self-contained concern: load once, persist to /api/settings,
// apply the theme attribute — none of it touches thread/turn state.
export class SettingsState {
	open = $state(false);
	theme = $state<'dark' | 'light'>('dark');
	showPrices = $state(true);
	defaultModel = $state('');

	// The composer's standing focus mode — applied to every new message
	// until changed from the "+" menu, same "sticky until changed"
	// semantics as defaultModel (see +page.svelte's initial focusMode).
	// '' means "off" — no default.
	defaultFocusMode = $state<FocusMode>('off');

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
		this.showPrices = data.show_prices ?? true;
		this.defaultModel = data.default_model ?? '';
		this.defaultFocusMode = (data.default_focus_mode || 'off') as FocusMode;
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

	async setShowPrices(show: boolean) {
		this.showPrices = show;
		await this.put({ show_prices: show });
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
	async pushUpdate() {
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
			await this.waitForServerAndReload();
		} catch (err) {
			this.updateLog = String(err);
			this.updateState = 'error';
		}
	}

	// The build (go build on the potato's ARM CPU can take a while) and
	// restart happen out from under the request that triggered them, so
	// poll until the server answers again, then hard-reload — a normal
	// client-side nav would keep running the *old* JS bundle even after
	// the backend and its embedded frontend assets have updated.
	private async waitForServerAndReload() {
		const deadline = Date.now() + 120_000;
		while (Date.now() < deadline) {
			await new Promise((r) => setTimeout(r, 1500));
			try {
				const res = await fetch('/api/models', { cache: 'no-store' });
				if (res.ok) {
					window.location.reload();
					return;
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
	async checkUpdateStatus() {
		if (this.updateState === 'updating' || this.updateState === 'restarting') return;
		try {
			const res = await fetch('/api/update/status');
			if (!res.ok) return;
			const data = await res.json();
			if (data.running) {
				this.updateState = 'updating';
				await this.pollUntilFinished();
			} else if (data.done && data.restarting) {
				// Caught in the narrow window after the build finished but
				// before the process actually restarts — /api/models below
				// will start failing any moment now, then come back once
				// the new binary is up.
				this.updateState = 'restarting';
				await this.waitForServerAndReload();
			} else if (data.done && !data.success) {
				this.updateState = 'error';
				this.updateLog = (data.log ?? '') + (data.error ? `\n\n${data.error}` : '');
			}
		} catch {
			// Best-effort — a normal idle server with nothing to report
			// shouldn't surface a network hiccup here as an error state.
		}
	}

	private async pollUntilFinished() {
		while (this.updateState === 'updating') {
			await new Promise((r) => setTimeout(r, 2000));
			try {
				const res = await fetch('/api/update/status');
				if (!res.ok) continue;
				const data = await res.json();
				if (data.running) continue;
				if (data.restarting) {
					this.updateState = 'restarting';
					await this.waitForServerAndReload();
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
