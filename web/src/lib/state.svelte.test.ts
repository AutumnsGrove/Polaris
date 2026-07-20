import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AppState } from './state.svelte';
import type { ServerEvent } from './types';

// handleEvent is private — it's the socket's real message-driven event
// path (there's no other public entry point for "a token/tool_call/done
// event arrived"), so tests reach it via bracket access rather than
// pretending it doesn't exist.
function fireEvent(state: AppState, e: ServerEvent) {
	(state as any).handleEvent(e);
}

function fakeFetch(data: unknown, ok = true) {
	return vi.fn().mockResolvedValue({ ok, json: async () => data });
}

describe('AppState.send / dispatch', () => {
	let state: AppState;

	beforeEach(() => {
		state = new AppState();
		vi.stubGlobal('fetch', fakeFetch([]));
	});

	it('pushes a user turn and a streaming assistant placeholder', () => {
		state.send('hello there');
		expect(state.turns).toHaveLength(2);
		expect(state.turns[0]).toMatchObject({ role: 'user', content: 'hello there' });
		expect(state.turns[1]).toMatchObject({ role: 'assistant', content: '', streaming: true });
		expect(state.busy).toBe(true);
	});

	it('ignores blank content', () => {
		state.send('   ');
		expect(state.turns).toHaveLength(0);
		expect(state.busy).toBe(false);
	});

	it('ignores a second send while busy', () => {
		state.send('first');
		state.send('second');
		expect(state.turns).toHaveLength(2); // not 4
	});

	it('sends the selected model and thread id over the socket', () => {
		state.selectedModel = 'test-model';
		state.currentThreadId = 'thread-1';
		const sendSpy = vi.spyOn((state as any).socket, 'send');

		state.send('a question');

		expect(sendSpy).toHaveBeenCalledWith(
			expect.objectContaining({ type: 'message', content: 'a question', model: 'test-model', thread_id: 'thread-1' })
		);
	});
});

describe('AppState.retry / editMessage', () => {
	let state: AppState;

	beforeEach(() => {
		state = new AppState();
		vi.stubGlobal('fetch', fakeFetch([]));
	});

	it('retry re-dispatches the same content from the preceding user turn', () => {
		state.turns = [
			{ role: 'user', content: 'original question', id: 5 },
			{ role: 'assistant', content: 'an answer' }
		];
		state.retry(1);
		expect(state.turns).toHaveLength(2); // truncated back to 0, then re-pushed
		expect(state.turns[0]).toMatchObject({ role: 'user', content: 'original question' });
		expect(state.busy).toBe(true);
	});

	it('retry does nothing if the preceding turn is not a user message', () => {
		state.turns = [
			{ role: 'assistant', content: 'a' },
			{ role: 'assistant', content: 'b' }
		];
		state.retry(1);
		expect(state.turns).toHaveLength(2);
		expect(state.busy).toBe(false);
	});

	it('retry does nothing if the user turn has no persisted id yet', () => {
		state.turns = [
			{ role: 'user', content: 'q' }, // id undefined — not yet confirmed by the server
			{ role: 'assistant', content: 'a' }
		];
		state.retry(1);
		expect(state.busy).toBe(false);
	});

	it('editMessage replaces the content and truncates from that point', () => {
		state.turns = [
			{ role: 'user', content: 'old', id: 1 },
			{ role: 'assistant', content: 'old answer' },
			{ role: 'user', content: 'follow up', id: 2 },
			{ role: 'assistant', content: 'follow up answer' }
		];
		state.editMessage(0, 'revised question');
		expect(state.turns).toHaveLength(2);
		expect(state.turns[0].content).toBe('revised question');
	});

	it('editMessage ignores blank replacement text', () => {
		state.turns = [{ role: 'user', content: 'old', id: 1 }];
		state.editMessage(0, '   ');
		expect(state.turns).toHaveLength(1);
		expect(state.turns[0].content).toBe('old');
	});
});

describe('AppState.handleEvent', () => {
	let state: AppState;

	beforeEach(() => {
		state = new AppState();
		vi.stubGlobal('fetch', fakeFetch([]));
	});

	it('user_message assigns the persisted id and refreshes the thread list', () => {
		state.send('hello');
		const fetchSpy = vi.fn().mockResolvedValue({ ok: true, json: async () => [] });
		vi.stubGlobal('fetch', fetchSpy);

		fireEvent(state, { type: 'user_message', thread_id: 'new-thread', user_message_id: 42 });

		expect(state.turns[0].id).toBe(42);
		expect(fetchSpy).toHaveBeenCalledWith('/api/threads');
	});

	it('token events append to the pending assistant turn', () => {
		state.send('hello');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });
		fireEvent(state, { type: 'token', thread_id: 't1', content: 'Hel' });
		fireEvent(state, { type: 'token', thread_id: 't1', content: 'lo' });
		expect(state.turns[1].content).toBe('Hello');
	});

	it('tool_call then tool_result completes the matching timeline entry', () => {
		state.send('search something');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });
		fireEvent(state, { type: 'tool_call', thread_id: 't1', tool: 'web_search', args: { query: 'x' } });
		expect(state.turns[1].timeline).toHaveLength(1);
		expect(state.turns[1].timeline![0]).toMatchObject({ kind: 'tool', tool: 'web_search', done: false });

		fireEvent(state, { type: 'tool_result', thread_id: 't1', tool: 'web_search', result: 'found stuff' });
		expect(state.turns[1].timeline![0]).toMatchObject({ kind: 'tool', done: true, result: 'found stuff' });
	});

	it('events for a different thread than the one in flight are ignored', () => {
		state.send('hello');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });
		fireEvent(state, { type: 'token', thread_id: 'some-other-thread', content: 'should not appear' });
		expect(state.turns[1].content).toBe('');
	});

	it('done finalizes the turn and updates cost/context when still watching', () => {
		state.send('hello');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });
		fireEvent(state, {
			type: 'done',
			thread_id: 't1',
			cost_usd: 0.002,
			citations: [{ title: 'Src', url: 'https://x.com' }],
			context_tokens: 500,
			suggestions: ['a follow-up?']
		});

		expect(state.turns[1].streaming).toBe(false);
		expect(state.busy).toBe(false);
		expect(state.currentThreadId).toBe('t1');
		expect(state.totalCost).toBe(0.002);
		expect(state.contextTokens).toBe(500);
		expect(state.suggestions).toEqual(['a follow-up?']);
	});

	it('done does not overwrite cost/thread if the user navigated to a different thread first', () => {
		state.send('hello');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });
		// Simulate having navigated to an unrelated, already-open thread.
		state.currentThreadId = 'some-other-open-thread';
		state.totalCost = 9.99;

		fireEvent(state, { type: 'done', thread_id: 't1', cost_usd: 0.5, citations: [] });

		expect(state.currentThreadId).toBe('some-other-open-thread');
		expect(state.totalCost).toBe(9.99);
	});

	it('error finalizes the turn with a message when no content streamed', () => {
		state.send('hello');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });
		fireEvent(state, { type: 'error', thread_id: 't1', message: 'boom' });

		expect(state.turns[1].streaming).toBe(false);
		expect(state.turns[1].content).toBe('Error: boom');
		expect(state.busy).toBe(false);
	});
});

describe('AppState.openThread', () => {
	let state: AppState;

	beforeEach(() => {
		state = new AppState();
	});

	it('loads persisted messages into turns', async () => {
		vi.stubGlobal(
			'fetch',
			fakeFetch({
				cost_usd: 0.01,
				context_tokens: 10,
				messages: [
					{ id: 1, role: 'user', content: 'q', citations: '[]', suggestions: '[]', cost_usd: 0 },
					{ id: 2, role: 'assistant', content: 'a', citations: '[]', suggestions: '[]', cost_usd: 0.01 }
				]
			})
		);

		await state.openThread('t1');
		expect(state.currentThreadId).toBe('t1');
		expect(state.turns).toHaveLength(2);
		expect(state.turns[1].content).toBe('a');
		expect(state.totalCost).toBe(0.01);
	});

	it('splices the live in-flight turn back in when reopening a still-generating thread', async () => {
		// A turn is mid-flight for "t1" (send() + the server confirming the
		// thread id via user_message), but nothing assistant-side has
		// persisted yet — the fetch below reflects exactly that.
		vi.stubGlobal('fetch', fakeFetch([]));
		state.send('what is the capital of france');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 7 });
		fireEvent(state, { type: 'token', thread_id: 't1', content: 'Pa' }); // streamed so far

		vi.stubGlobal(
			'fetch',
			fakeFetch({
				cost_usd: 0,
				context_tokens: 0,
				messages: [{ id: 7, role: 'user', content: 'what is the capital of france', citations: '[]', suggestions: '[]', cost_usd: 0 }]
			})
		);

		await state.openThread('t1');

		expect(state.turns).toHaveLength(2);
		expect(state.turns[0]).toMatchObject({ role: 'user', content: 'what is the capital of france' });
		expect(state.turns[1]).toMatchObject({ role: 'assistant', content: 'Pa', streaming: true });

		// And it keeps updating live from here — the whole point of the splice.
		fireEvent(state, { type: 'token', thread_id: 't1', content: 'ris' });
		expect(state.turns[1].content).toBe('Paris');
	});

	it('does not splice anything for a thread with no turn in flight', async () => {
		vi.stubGlobal(
			'fetch',
			fakeFetch({
				cost_usd: 0,
				context_tokens: 0,
				messages: [{ id: 1, role: 'user', content: 'q', citations: '[]', suggestions: '[]', cost_usd: 0 }]
			})
		);
		await state.openThread('unrelated-thread');
		expect(state.turns).toHaveLength(1);
	});
});

describe('AppState.newThread', () => {
	it('resets thread-scoped fields', () => {
		const state = new AppState();
		state.turns = [{ role: 'user', content: 'x' }];
		state.currentThreadId = 't1';
		state.totalCost = 1.23;
		state.contextTokens = 500;
		state.suggestions = ['a'];

		state.newThread();

		expect(state.turns).toEqual([]);
		expect(state.currentThreadId).toBeNull();
		expect(state.totalCost).toBe(0);
		expect(state.contextTokens).toBe(0);
		expect(state.suggestions).toEqual([]);
	});
});

describe('AppState.stopGeneration', () => {
	it('sends a stop message only when busy', () => {
		const state = new AppState();
		const sendSpy = vi.spyOn((state as any).socket, 'send');

		state.stopGeneration();
		expect(sendSpy).not.toHaveBeenCalled();

		state.busy = true;
		state.stopGeneration();
		expect(sendSpy).toHaveBeenCalledWith({ type: 'stop' });
	});
});
