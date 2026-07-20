import { describe, it, expect, vi, beforeEach } from 'vitest';
import { SettingsState } from './settings.svelte';

function fakeFetch(data: unknown, ok = true) {
	return vi.fn().mockResolvedValue({ ok, json: async () => data });
}

describe('SettingsState.load', () => {
	it('applies server values and the data-theme attribute', async () => {
		vi.stubGlobal(
			'fetch',
			fakeFetch({ theme: 'light', show_prices: false, default_model: 'm1', context_window_tokens: 50000 })
		);

		const settings = new SettingsState();
		await settings.load();

		expect(settings.theme).toBe('light');
		expect(settings.showPrices).toBe(false);
		expect(settings.defaultModel).toBe('m1');
		expect(settings.contextWindowTokens).toBe(50000);
		expect(document.documentElement.getAttribute('data-theme')).toBe('light');
	});

	it('falls back to dark theme and default context window on a bad response', async () => {
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false }));
		const settings = new SettingsState();
		await settings.load();
		// load() returns early on !res.ok — defaults untouched.
		expect(settings.theme).toBe('dark');
		expect(settings.contextWindowTokens).toBe(100_000);
	});
});

describe('SettingsState.setTheme / setShowPrices', () => {
	let putCalls: Array<{ url: string; body: unknown }>;

	beforeEach(() => {
		putCalls = [];
		vi.stubGlobal(
			'fetch',
			vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
				if (init?.method === 'PUT') {
					putCalls.push({ url, body: JSON.parse(init.body as string) });
				}
				return { ok: true, json: async () => ({}) };
			})
		);
	});

	it('setTheme updates local state, the DOM, and persists via PUT', async () => {
		const settings = new SettingsState();
		await settings.setTheme('light');
		expect(settings.theme).toBe('light');
		expect(document.documentElement.getAttribute('data-theme')).toBe('light');
		expect(putCalls).toEqual([{ url: '/api/settings', body: { theme: 'light' } }]);
	});

	it('setShowPrices updates local state and persists', async () => {
		const settings = new SettingsState();
		await settings.setShowPrices(false);
		expect(settings.showPrices).toBe(false);
		expect(putCalls).toEqual([{ url: '/api/settings', body: { show_prices: false } }]);
	});
});

describe('SettingsState.setDefaultModel', () => {
	it('persists the model id and invokes the onModelChanged callback', async () => {
		vi.stubGlobal('fetch', fakeFetch({}));
		const settings = new SettingsState();
		const onModelChanged = vi.fn();

		await settings.setDefaultModel('other-model', onModelChanged);

		expect(settings.defaultModel).toBe('other-model');
		expect(onModelChanged).toHaveBeenCalledOnce();
	});

	it('works without a callback', async () => {
		vi.stubGlobal('fetch', fakeFetch({}));
		const settings = new SettingsState();
		await expect(settings.setDefaultModel('m2')).resolves.toBeUndefined();
		expect(settings.defaultModel).toBe('m2');
	});
});

describe('SettingsState.toggle', () => {
	it('flips open', () => {
		const settings = new SettingsState();
		expect(settings.open).toBe(false);
		settings.toggle();
		expect(settings.open).toBe(true);
		settings.toggle();
		expect(settings.open).toBe(false);
	});
});

describe('SettingsState.pushUpdate', () => {
	it('posts to /api/update and returns the parsed result', async () => {
		const fetchSpy = fakeFetch({ success: true, log: 'ok', restarting: true });
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		const result = await settings.pushUpdate();

		expect(fetchSpy).toHaveBeenCalledWith('/api/update', { method: 'POST' });
		expect(result).toEqual({ success: true, log: 'ok', restarting: true });
	});
});
