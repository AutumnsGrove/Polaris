import { describe, it, expect, vi, beforeEach } from 'vitest';
import { SettingsState } from './settings.svelte';

function fakeFetch(data: unknown, ok = true) {
	return vi.fn().mockResolvedValue({ ok, json: async () => data });
}

describe('SettingsState.load', () => {
	it('applies server values and the data-theme attribute', async () => {
		vi.stubGlobal(
			'fetch',
			fakeFetch({ theme: 'light', default_model: 'm1', context_window_tokens: 50000 })
		);

		const settings = new SettingsState();
		await settings.load();

		expect(settings.theme).toBe('light');
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

	it('applies voice_input_mode from the server, defaulting to toggle for anything else', async () => {
		vi.stubGlobal('fetch', fakeFetch({ voice_input_mode: 'hold' }));
		const settings = new SettingsState();
		await settings.load();
		expect(settings.voiceInputMode).toBe('hold');

		vi.stubGlobal('fetch', fakeFetch({ voice_input_mode: 'not_a_real_mode' }));
		const settings2 = new SettingsState();
		await settings2.load();
		expect(settings2.voiceInputMode).toBe('toggle');
	});
});

describe('SettingsState.setTheme', () => {
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

	it('setVoiceInputMode updates local state and persists', async () => {
		const settings = new SettingsState();
		await settings.setVoiceInputMode('hold');
		expect(settings.voiceInputMode).toBe('hold');
		expect(putCalls).toEqual([{ url: '/api/settings', body: { voice_input_mode: 'hold' } }]);
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
	// updateState/updateLog live on SettingsState itself (not a component)
	// specifically so they survive the settings panel closing and
	// reopening mid-update — the panel unmounts entirely when closed,
	// which used to throw local component state away.
	it('lands on idle when the rebuild succeeds but nothing restarts it', async () => {
		const fetchSpy = fakeFetch({ success: true, log: 'build successful', restarting: false });
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		await settings.pushUpdate();

		expect(fetchSpy).toHaveBeenCalledWith('/api/update', { method: 'POST' });
		expect(settings.updateState).toBe('idle');
		expect(settings.updateLog).toBe('build successful');
	});

	it('surfaces a failed build as an error with its log', async () => {
		vi.stubGlobal('fetch', fakeFetch({ success: false, error: 'go build failed', log: 'pull ok\nbuild failed' }));

		const settings = new SettingsState();
		await settings.pushUpdate();

		expect(settings.updateState).toBe('error');
		expect(settings.updateLog).toBe('pull ok\nbuild failed');
	});

	it('surfaces the server rejecting a second overlapping update', async () => {
		vi.stubGlobal(
			'fetch',
			fakeFetch({ success: false, already_running: true, error: 'an update is already in progress' })
		);

		const settings = new SettingsState();
		await settings.pushUpdate();

		expect(settings.updateState).toBe('error');
		expect(settings.updateLog).toBe('an update is already in progress');
	});
});

describe('SettingsState.checkUpdateStatus', () => {
	it('resumes showing "updating" if the server reports one still running', async () => {
		// First poll (inside checkUpdateStatus): still running. Second poll
		// (inside the pollUntilFinished loop it kicks off): finished, idle.
		const fetchSpy = vi
			.fn()
			.mockResolvedValueOnce({ ok: true, json: async () => ({ running: true, done: false }) })
			.mockResolvedValueOnce({ ok: true, json: async () => ({ running: false, done: true, success: true, log: 'done' }) });
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		await settings.checkUpdateStatus();

		expect(settings.updateState).toBe('idle');
		expect(settings.updateLog).toBe('done');
	});

	it('leaves state untouched when nothing is running and nothing finished', async () => {
		vi.stubGlobal('fetch', fakeFetch({ running: false, done: false }));

		const settings = new SettingsState();
		await settings.checkUpdateStatus();

		expect(settings.updateState).toBe('idle');
	});

	it('does not re-check while already tracking an in-progress update', async () => {
		const fetchSpy = vi.fn();
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		settings.updateState = 'updating';
		await settings.checkUpdateStatus();

		expect(fetchSpy).not.toHaveBeenCalled();
	});
});
