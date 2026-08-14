import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
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

describe('SettingsState.pushUpdate restart handling', () => {
	// waitForServerAndReload is private, exercised only through pushUpdate's
	// restarting:true path — these pin down the exact bug being hardened
	// against: reloading the instant *any* server answers, even the
	// still-alive old process, rather than waiting for /api/version to
	// actually change. See its doc comment in settings.svelte.ts.
	beforeEach(() => {
		vi.useFakeTimers();
	});

	afterEach(() => {
		vi.useRealTimers();
		vi.restoreAllMocks();
	});

	it('does not reload while /api/version keeps reporting the pre-update version', async () => {
		const reloadSpy = vi.spyOn(window.location, 'reload').mockImplementation(() => {});

		const fetchSpy = vi.fn().mockImplementation(async (url: string) => {
			if (url === '/api/update') {
				return { ok: true, json: async () => ({ success: true, log: 'build successful', restarting: true }) };
			}
			if (url === '/api/update/status') {
				return { ok: true, json: async () => ({}) };
			}
			if (url === '/api/version') {
				// The old process is still alive and answering — restart
				// hasn't actually landed yet (systemctl restart is a slow
				// polkit round trip on the potato). Same version every time.
				return { ok: true, json: async () => ({ version: 'r100.aaaaaaa' }) };
			}
			throw new Error(`unexpected fetch: ${url}`);
		});
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		const done = settings.pushUpdate();

		// Let the baseline fetch and a handful of poll iterations run —
		// each iteration waits 1500ms, then hits /api/update/status and
		// /api/version, both of which report "nothing's changed yet".
		for (let i = 0; i < 5; i++) {
			await vi.advanceTimersByTimeAsync(1500);
		}

		expect(reloadSpy).not.toHaveBeenCalled();
		expect(settings.updateState).toBe('restarting');

		// Now the real restart has landed — /api/version starts reporting
		// the new build.
		fetchSpy.mockImplementation(async (url: string) => {
			if (url === '/api/update/status') return { ok: true, json: async () => ({}) };
			if (url === '/api/version') return { ok: true, json: async () => ({ version: 'r101.bbbbbbb' }) };
			throw new Error(`unexpected fetch: ${url}`);
		});
		await vi.advanceTimersByTimeAsync(1500);
		await done;

		expect(reloadSpy).toHaveBeenCalledOnce();
	});

	it('does not reload while isBusy() reports a turn still in flight, then reloads once it clears', async () => {
		// Regression test for the gap this exact hardening pattern (see the
		// test above) was supposed to close everywhere: checkVersion()
		// (state.svelte.ts) already deferred its own reload until
		// !appState.busy, but this restart-triggered path kept calling
		// window.location.reload() unconditionally the instant the new
		// binary answered — even mid-turn, wiping busy/pendingTurn/
		// pendingThreadId client-side while the server kept streaming
		// regardless, and landing wherever the address bar happened to be
		// pointed at that instant rather than the thread the turn belongs
		// to.
		const reloadSpy = vi.spyOn(window.location, 'reload').mockImplementation(() => {});

		// First /api/version call is waitForServerAndReload's own baseline
		// read (still the pre-update process); every call after that is the
		// polling loop, already seeing the new build.
		let versionCalls = 0;
		const fetchSpy = vi.fn().mockImplementation(async (url: string) => {
			if (url === '/api/update') {
				return { ok: true, json: async () => ({ success: true, log: 'build successful', restarting: true }) };
			}
			if (url === '/api/update/status') {
				return { ok: true, json: async () => ({}) };
			}
			if (url === '/api/version') {
				versionCalls++;
				const version = versionCalls === 1 ? 'r100.aaaaaaa' : 'r101.bbbbbbb';
				return { ok: true, json: async () => ({ version }) };
			}
			throw new Error(`unexpected fetch: ${url}`);
		});
		vi.stubGlobal('fetch', fetchSpy);

		let busy = true;
		const settings = new SettingsState();
		const done = settings.pushUpdate(() => busy);

		// The new version is visible on every poll, but a turn is still
		// in flight on this client — must keep polling, not reload.
		for (let i = 0; i < 5; i++) {
			await vi.advanceTimersByTimeAsync(1500);
		}
		expect(reloadSpy).not.toHaveBeenCalled();
		expect(settings.updateState).toBe('restarting');

		// The turn finishes — the very next poll should reload immediately.
		busy = false;
		await vi.advanceTimersByTimeAsync(1500);
		await done;

		expect(reloadSpy).toHaveBeenCalledOnce();
	});

	it('surfaces a failed restart command instead of waiting out the full 2 minutes', async () => {
		const reloadSpy = vi.spyOn(window.location, 'reload').mockImplementation(() => {});

		const fetchSpy = vi.fn().mockImplementation(async (url: string) => {
			if (url === '/api/update') {
				return { ok: true, json: async () => ({ success: true, log: 'build successful', restarting: true }) };
			}
			if (url === '/api/update/status') {
				return {
					ok: true,
					json: async () => ({ restart_error: 'sudo systemctl restart polaris: exit status 1' })
				};
			}
			if (url === '/api/version') {
				return { ok: true, json: async () => ({ version: 'r100.aaaaaaa' }) };
			}
			throw new Error(`unexpected fetch: ${url}`);
		});
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		const done = settings.pushUpdate();
		await vi.advanceTimersByTimeAsync(1500);
		await done;

		expect(reloadSpy).not.toHaveBeenCalled();
		expect(settings.updateState).toBe('error');
		expect(settings.updateLog).toContain('sudo systemctl restart polaris: exit status 1');
	});
});

describe('SettingsState.pushRestart', () => {
	beforeEach(() => {
		vi.useFakeTimers();
	});

	afterEach(() => {
		vi.useRealTimers();
		vi.restoreAllMocks();
	});

	it('hits /api/restart (not /api/update) and reloads once the version changes, with no build step', async () => {
		const reloadSpy = vi.spyOn(window.location, 'reload').mockImplementation(() => {});

		let versionCalls = 0;
		const fetchSpy = vi.fn().mockImplementation(async (url: string) => {
			if (url === '/api/restart') {
				return { ok: true, json: async () => ({ success: true, restarting: true }) };
			}
			if (url === '/api/update') {
				throw new Error('pushRestart must not hit /api/update — that pulls and rebuilds');
			}
			if (url === '/api/update/status') {
				return { ok: true, json: async () => ({}) };
			}
			if (url === '/api/version') {
				versionCalls++;
				const version = versionCalls === 1 ? 'r100.aaaaaaa' : 'r101.bbbbbbb';
				return { ok: true, json: async () => ({ version }) };
			}
			throw new Error(`unexpected fetch: ${url}`);
		});
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		const done = settings.pushRestart();
		expect(settings.updateKind).toBe('restart');

		await vi.advanceTimersByTimeAsync(1500);
		await done;

		expect(reloadSpy).toHaveBeenCalledOnce();
		expect(settings.updateKind).toBe('restart');
	});

	it('reports "already in progress" without starting a second restart', async () => {
		vi.stubGlobal(
			'fetch',
			fakeFetch({ success: false, already_running: true, error: 'an update or restart is already in progress' })
		);

		const settings = new SettingsState();
		await settings.pushRestart();

		expect(settings.updateState).toBe('error');
		expect(settings.updateLog).toContain('already in progress');
	});

	it('surfaces "service is not managed" instead of hanging on a reload that will never come', async () => {
		vi.stubGlobal(
			'fetch',
			fakeFetch({ success: false, error: 'service is not managed by systemd/launchd — restart manually' })
		);

		const settings = new SettingsState();
		await settings.pushRestart();

		expect(settings.updateState).toBe('error');
		expect(settings.updateLog).toContain('not managed');
	});

	it('uses restart-specific wording (not "build succeeded") when the restart command itself fails', async () => {
		const reloadSpy = vi.spyOn(window.location, 'reload').mockImplementation(() => {});

		const fetchSpy = vi.fn().mockImplementation(async (url: string) => {
			if (url === '/api/restart') {
				return { ok: true, json: async () => ({ success: true, restarting: true }) };
			}
			if (url === '/api/update/status') {
				return {
					ok: true,
					json: async () => ({ restart_error: 'sudo systemctl restart polaris: exit status 1' })
				};
			}
			if (url === '/api/version') {
				return { ok: true, json: async () => ({ version: 'r100.aaaaaaa' }) };
			}
			throw new Error(`unexpected fetch: ${url}`);
		});
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		const done = settings.pushRestart();
		await vi.advanceTimersByTimeAsync(1500);
		await done;

		expect(reloadSpy).not.toHaveBeenCalled();
		expect(settings.updateState).toBe('error');
		expect(settings.updateLog).toContain('Restart command failed:');
		expect(settings.updateLog).not.toContain('Build succeeded');
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

	it('resumes tracking a restart (not an update) another tab/reload started, by kind', async () => {
		const fetchSpy = vi
			.fn()
			.mockResolvedValueOnce({ ok: true, json: async () => ({ kind: 'restart', running: true, done: false }) })
			.mockResolvedValueOnce({
				ok: true,
				json: async () => ({ kind: 'restart', running: false, done: true, success: true, log: 'restart requested' })
			});
		vi.stubGlobal('fetch', fetchSpy);

		const settings = new SettingsState();
		await settings.checkUpdateStatus();

		expect(settings.updateKind).toBe('restart');
	});
});
