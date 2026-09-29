import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AppState } from './state.svelte';
import { FieldsState, fieldsState, fieldColorVar, FIELD_COLORS } from './fields.svelte';
import type { Field } from './types';

function field(over: Partial<Field> = {}): Field {
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

describe('fieldColorVar', () => {
	it('maps a known tag to its --color-cat-* token and everything else to null', () => {
		expect(fieldColorVar('technology')).toBe('var(--color-cat-technology)');
		expect(fieldColorVar('')).toBeNull();
		// A palette that changed since a field was saved must not yield an
		// undefined CSS variable (which would render as an invisible color).
		expect(fieldColorVar('no-such-color')).toBeNull();
		for (const c of FIELD_COLORS) expect(fieldColorVar(c)).not.toBeNull();
	});
});

describe('FieldsState', () => {
	let state: FieldsState;
	beforeEach(() => {
		state = new FieldsState();
	});

	it('load() fills the list and flags a failure without wiping what was there', async () => {
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse([field()]))));
		await state.load();
		expect(state.fields).toHaveLength(1);
		expect(state.loaded).toBe(true);

		vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('offline'))));
		await state.load();
		expect(state.error).toBe(true);
		expect(state.fields).toHaveLength(1); // a network blip must not empty the list
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

	it('an update floats the field to the top and refreshes every reader', async () => {
		state.fields = [field({ id: 'a', name: 'A' }), field({ id: 'b', name: 'B' })];
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(field({ id: 'b', name: 'B', favorite: true })))));

		const res = await state.update('b', { favorite: true });

		expect(res.ok).toBe(true);
		expect(state.fields.map((p) => p.id)).toEqual(['b', 'a']);
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

	it('remove() drops the field from the list only on success', async () => {
		state.fields = [field({ id: 'a' })];
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse('boom', false, 500))));
		await state.remove('a');
		expect(state.fields).toHaveLength(1);

		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 204 })));
		await state.remove('a');
		expect(state.fields).toHaveLength(0);
	});

	it('percent-encodes a filename when deleting a shared file', async () => {
		const spy = vi.fn(() => Promise.resolve({ ok: true, status: 204 }));
		vi.stubGlobal('fetch', spy);
		await state.deleteFile('p1', 'my notes #1.md');
		expect((spy.mock.calls[0] as unknown[])[0]).toBe('/api/fields/p1/files/my%20notes%20%231.md');
	});
});

describe('AppState field threads', () => {
	let state: AppState;
	beforeEach(() => {
		state = new AppState();
		fieldsState.fields = [field({ id: 'p1', default_model: 'cheap-model' })];
		state.models = [
			{ id: 'cheap-model', name: 'Cheap', default: false },
			{ id: 'big-model', name: 'Big', default: true }
		];
		state.selectedModel = 'big-model';
		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse([]))));
	});

	it("startThreadInField seeds the field's default model when this install has it", () => {
		state.startThreadInField('p1');
		expect(state.pendingFieldId).toBe('p1');
		expect(state.selectedModel).toBe('cheap-model');
	});

	it('leaves the picker alone when the field names a model that no longer exists', () => {
		fieldsState.fields = [field({ id: 'p1', default_model: 'removed-model' })];
		state.startThreadInField('p1');
		expect(state.selectedModel).toBe('big-model');
	});

	it("sends field_id on a brand-new thread's first turn and never after", () => {
		const sendSpy = vi.spyOn((state as any).socket, 'send');
		state.startThreadInField('p1');
		state.send('first');
		expect(sendSpy).toHaveBeenLastCalledWith(expect.objectContaining({ field_id: 'p1', thread_id: undefined }));

		// The thread now has an id (as after its first 'done') — a later turn
		// must not re-send it; re-homing is a separate, explicit action.
		state.busy = false;
		state.currentThreadId = 'thread-1';
		state.send('second');
		expect(sendSpy).toHaveBeenLastCalledWith(expect.objectContaining({ field_id: undefined, thread_id: 'thread-1' }));
	});

	it('an ordinary new thread carries no field_id', () => {
		const sendSpy = vi.spyOn((state as any).socket, 'send');
		state.send('plain');
		expect(sendSpy).toHaveBeenLastCalledWith(expect.objectContaining({ field_id: undefined }));
	});

	it('newThread() clears a pending field so it cannot leak into the next chat', () => {
		state.startThreadInField('p1');
		state.newThread();
		expect(state.pendingFieldId).toBeNull();
		expect(state.activeFieldId).toBeNull();
	});

	it("activeFieldId prefers an opened thread's own field over the pending one", () => {
		state.pendingFieldId = 'p1';
		state.currentThread = { id: 't', field_id: 'p2' } as any;
		expect(state.activeFieldId).toBe('p2');
		state.currentThread = { id: 't' } as any; // opened, ungrouped
		expect(state.activeFieldId).toBeNull();
	});

	it('moving the open thread updates every reader, and a server refusal changes nothing', async () => {
		state.currentThreadId = 't1';
		state.currentThread = { id: 't1' } as any;

		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: true, status: 204, json: async () => [] })));
		expect(await state.moveCurrentThreadToField('p1')).toBeNull();
		expect(state.currentThread?.field_id).toBe('p1');
		expect(state.activeFieldId).toBe('p1');

		vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse('field not found', false, 404))));
		expect(await state.moveCurrentThreadToField('gone')).toBe('field not found');
		expect(state.activeFieldId).toBe('p1'); // unchanged
	});
});
