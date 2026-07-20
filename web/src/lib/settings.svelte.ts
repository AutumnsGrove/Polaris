// User-adjustable UI preferences, split out of state.svelte.ts since
// they're a self-contained concern: load once, persist to /api/settings,
// apply the theme attribute — none of it touches thread/turn state.
export class SettingsState {
	open = $state(false);
	theme = $state<'dark' | 'light'>('dark');
	showPrices = $state(true);
	defaultModel = $state('');

	// Context-usage display, next to thread cost. contextWindowTokens is
	// the auto-compaction threshold from config.yaml (loaded once via
	// load()) — the denominator for the % shown in +page.svelte.
	contextWindowTokens = $state(100_000);

	async load() {
		const res = await fetch('/api/settings');
		if (!res.ok) return;
		const data = await res.json();
		this.theme = data.theme === 'light' ? 'light' : 'dark';
		this.showPrices = data.show_prices ?? true;
		this.defaultModel = data.default_model ?? '';
		this.contextWindowTokens = data.context_window_tokens ?? 100_000;
		this.applyTheme();
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

	/** Runs the same git-pull-and-rebuild the CLI's `polaris update` does. */
	async pushUpdate(): Promise<{ success: boolean; log: string; restarting?: boolean; error?: string }> {
		const res = await fetch('/api/update', { method: 'POST' });
		return res.json();
	}
}
