import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AppState } from './state.svelte';
import { ProjectsState, projectsState, projectColorVar, PROJECT_COLORS } from './projects.svelte';
import type { Project } from './types';

function project(over: Partial<Project> = {}): Project {
	return {
		id: 'p1',
		name: 'Alpha',
		description: '',
		custom_instructions: '',
		favorite: false,
		default_focus_mode: '',
		default_model: '',
		memory_mode: 'default',
		constellation_visible: true,
		exclude_from_chat_search: false,
		color: '',
		thread_count: 0,
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-01T00:00:00Z',
		...over
	};
}

function jsonResponse(data: unknown, ok = true, status = 200) {
	return { ok, status, json: async () => data, text: async () => (typeof data === 'string' ? data : JSON.stringify(data)) };
}

describe('projectColorVar', () => {
	it('maps a known tag to its --color-cat-* token and everything else to null', () => {
		expect(projectColorVar('technology')).toBe('var(--color-cat-technology)');
		expect(projectColorVar('')).toBeNull();
		// A palette that changed since a project was saved must not yield an
		// undefined CSS variable (which would render as an invisible color).
		expect(projectColorVar('no-such-color')).toBeNull();
		for (const c of PROJECT_COLORS) expect(projectColorVar(c)).not.toBeNull();
	});
});

describe('ProjectsState', () => {
	let state: ProjectsState;
	beforeEach(() => {
		state = new ProjectsState();
	});

	it('load() fills the list and flags a failure without wiping what was there', async () => {
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse([project()]))));
		await state.load();
		expect(state.projects).toHaveLength(1);
		expect(state.loaded).toBe(true);

		vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))));
		await state.load();
		expect(state.error).toBe(true);
		expect(state.projects).toHaveLength(1); // a network blip must not empty the list
	});

	it('a 404 from a backend that predates the routes is an error, never a silent "still loading"', async () => {
		// The ThreadMenu picker distinguishes error/loaded/neither; this pins
		// the state it reads. loaded must stay false and error must be set.
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse('404 page not found', false, 404))));
		await state.load();
		expect(state.error).toBe(true);
		expect(state.loaded).toBe(false);

		// And a later successful retry clears it.
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse([]))));
		await state.load();
		expect(state.error).toBe(false);
		expect(state.loaded).toBe(true);
	});

	it('an update floats the project to the top and refreshes every reader', async () => {
		state.projects = [project({ id: 'a', name: 'A' }), project({ id: 'b', name: 'B' })];
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(project({ id: 'b', name: 'B', favorite: true })))));

		const res = await state.update('b', { favorite: true });

		expect(res.ok).toBe(true);
		expect(state.projects.map((p) => p.id)).toEqual(['b', 'a']);
		expect(state.favorites.map((p) => p.id)).toEqual(['b']);
	});

	it("surfaces the server's own message on a rejected save", async () => {
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse('unknown default_model', false, 400))));
		const res = await state.update('a', { default_model: 'nope' });
		expect(res).toEqual({ ok: false, error: 'unknown default_model' });
	});

	it('reports a network failure as a result instead of throwing', async () => {
		vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))));
		const res = await state.remove('a');
		expect(res.ok).toBe(false);
	});

	it('remove() drops the project from the list only on success', async () => {
		state.projects = [project({ id: 'a' })];
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse('boom', false, 500))));
		await state.remove('a');
		expect(state.projects).toHaveLength(1);

		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 204 })));
		await state.remove('a');
		expect(state.projects).toHaveLength(0);
	});

	it('percent-encodes a filename when deleting a shared file', async () => {
		const spy = vi.fn(() => Promise.resolve({ ok: true, status: 204 }));
		vi.stubGlobal('fetch', spy);
		await state.deleteFile('p1', 'my notes #1.md');
		expect((spy.mock.calls[0] as unknown[])[0]).toBe('/api/projects/p1/files/my%20notes%20%231.md');
	});
});

describe('AppState project threads', () => {
	let state: AppState;
	beforeEach(() => {
		state = new AppState();
		projectsState.projects = [project({ id: 'p1', default_model: 'cheap-model' })];
		state.models = [
			{ id: 'cheap-model', name: 'Cheap', default: false },
			{ id: 'big-model', name: 'Big', default: true }
		];
		state.selectedModel = 'big-model';
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse([]))));
	});

	it("startThreadInProject seeds the project's default model when this install has it", () => {
		state.startThreadInProject('p1');
		expect(state.pendingProjectId).toBe('p1');
		expect(state.selectedModel).toBe('cheap-model');
	});

	it('leaves the picker alone when the project names a model that no longer exists', () => {
		projectsState.projects = [project({ id: 'p1', default_model: 'removed-model' })];
		state.startThreadInProject('p1');
		expect(state.selectedModel).toBe('big-model');
	});

	it("sends project_id on a brand-new thread's first turn and never after", () => {
		const sendSpy = vi.spyOn((state as any).socket, 'send');
		state.startThreadInProject('p1');
		state.send('first');
		expect(sendSpy).toHaveBeenLastCalledWith(expect.objectContaining({ project_id: 'p1', thread_id: undefined }));

		// The thread now has an id (as after its first 'done') — a later turn
		// must not re-send it; re-homing is a separate, explicit action.
		state.busy = false;
		state.currentThreadId = 'thread-1';
		state.send('second');
		expect(sendSpy).toHaveBeenLastCalledWith(expect.objectContaining({ project_id: undefined, thread_id: 'thread-1' }));
	});

	it('an ordinary new thread carries no project_id', () => {
		const sendSpy = vi.spyOn((state as any).socket, 'send');
		state.send('plain');
		expect(sendSpy).toHaveBeenLastCalledWith(expect.objectContaining({ project_id: undefined }));
	});

	it('newThread() clears a pending project so it cannot leak into the next chat', () => {
		state.startThreadInProject('p1');
		state.newThread();
		expect(state.pendingProjectId).toBeNull();
		expect(state.activeProjectId).toBeNull();
	});

	it("activeProjectId prefers an opened thread's own project over the pending one", () => {
		state.pendingProjectId = 'p1';
		state.currentThread = { id: 't', project_id: 'p2' } as any;
		expect(state.activeProjectId).toBe('p2');
		state.currentThread = { id: 't' } as any; // opened, ungrouped
		expect(state.activeProjectId).toBeNull();
	});

	it('moving the open thread updates every reader, and a server refusal changes nothing', async () => {
		state.currentThreadId = 't1';
		state.currentThread = { id: 't1' } as any;

		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 204, json: async () => [] })));
		expect(await state.moveCurrentThreadToProject('p1')).toBeNull();
		expect(state.currentThread?.project_id).toBe('p1');
		expect(state.activeProjectId).toBe('p1');

		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse('project not found', false, 404))));
		expect(await state.moveCurrentThreadToProject('gone')).toBe('project not found');
		expect(state.activeProjectId).toBe('p1'); // unchanged
	});
});
