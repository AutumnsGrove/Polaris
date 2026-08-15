import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AppState } from './state.svelte';
import type { ServerEvent } from './types';

// handleEvent is private — it's the socket's real message-driven event
// path (there's no other public entry point for "a token/tool_call/done
// event arrived"), so tests reach it via bracket access rather than
// pretending it doesn't exist.
function fireEvent(state: AppState, e: ServerEvent) {
	(state as any).handleEvent(e);
}

// openThread fetches both the thread itself and its persisted events in
// parallel — a bare mockResolvedValue would hand the events call the same
// payload as the thread call, breaking `for (const evt of events)`. Any
// URL ending in "/events" gets an empty array instead, since none of
// these tests care about timeline reconstruction specifically.
function fakeFetch(data: unknown, ok = true) {
	return vi.fn((url: string) => {
		if (typeof url === 'string' && url.endsWith('/events')) {
			return Promise.resolve({ ok: true, json: async () => [] });
		}
		return Promise.resolve({ ok, json: async () => data });
	});
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

	// Mirrors the exact wire sequence gateway/turn.go now emits for an
	// image attachment (see gateway/attachments.go's resolveAttachment):
	// user_message, then a synthetic describe_image tool_call/tool_result
	// pair BEFORE the main answer's first token — the fix for the "blank
	// screen for several seconds while the vision model looks at the
	// photo" gap. Confirms the client builds the same live-then-completed
	// timeline entry for it as a real tool call, using the generic
	// tool/args/result handling already in place — no special-casing
	// needed client-side beyond ToolEvent.svelte's label/icon.
	it('a synthetic describe_image tool_call/tool_result pair (image attachment processing) completes the matching timeline entry before the answer streams', () => {
		state.send("what's in this photo?");
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });

		fireEvent(state, {
			type: 'tool_call',
			thread_id: 't1',
			tool: 'describe_image',
			args: { filename: 'bike.jpg' }
		});
		expect(state.turns[1].timeline).toHaveLength(1);
		expect(state.turns[1].timeline![0]).toMatchObject({
			kind: 'tool',
			tool: 'describe_image',
			args: { filename: 'bike.jpg' },
			done: false
		});

		// While it's "running" (done: false), nothing else has streamed yet —
		// this is the moment that used to be a silent blank wait.
		expect(state.turns[1].content).toBe('');

		fireEvent(state, {
			type: 'tool_result',
			thread_id: 't1',
			tool: 'describe_image',
			result: 'A red bicycle leaning against a brick wall.'
		});
		expect(state.turns[1].timeline![0]).toMatchObject({
			kind: 'tool',
			tool: 'describe_image',
			done: true,
			result: 'A red bicycle leaning against a brick wall.'
		});

		// The main answer streams in afterward, as its own separate timeline
		// content — the synthetic tool entry doesn't interfere with it.
		fireEvent(state, { type: 'token', thread_id: 't1', content: 'It looks like a red bicycle.' });
		expect(state.turns[1].content).toBe('It looks like a red bicycle.');
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
			context_tokens: 500
		});

		expect(state.turns[1].streaming).toBe(false);
		expect(state.busy).toBe(false);
		expect(state.currentThreadId).toBe('t1');
		expect(state.totalCost).toBe(0.002);
		expect(state.contextTokens).toBe(500);
		expect(state.suggestions).toEqual([]);
	});

	// The backend now generates follow-up suggestions in a detached
	// goroutine kicked off after 'done' ships (see gateway/turn.go), so
	// the turn footer doesn't stall behind that extra LLM call. They
	// arrive later via their own 'suggestions' event instead of riding
	// along on 'done' — see state.svelte.ts's handling of both event types.
	it("a later 'suggestions' event fills in follow-ups and adds their cost", () => {
		state.send('hello');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });
		fireEvent(state, { type: 'done', thread_id: 't1', cost_usd: 0.002 });
		fireEvent(state, {
			type: 'suggestions',
			thread_id: 't1',
			cost_usd: 0.0005,
			suggestions: ['a follow-up?']
		});

		expect(state.totalCost).toBeCloseTo(0.0025);
		expect(state.suggestions).toEqual(['a follow-up?']);
	});

	it("a 'suggestions' event for a thread the user has since navigated away from is ignored", () => {
		state.send('hello');
		fireEvent(state, { type: 'user_message', thread_id: 't1', user_message_id: 1 });
		fireEvent(state, { type: 'done', thread_id: 't1', cost_usd: 0.002 });
		state.currentThreadId = 't2';
		fireEvent(state, {
			type: 'suggestions',
			thread_id: 't1',
			cost_usd: 0.0005,
			suggestions: ['a follow-up?']
		});

		expect(state.totalCost).toBe(0.002);
		expect(state.suggestions).toEqual([]);
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

	it('reconstructs a reopened turn timeline from persisted events', async () => {
		// Mirrors exactly what gateway/turn.go's logTurnEvent persists for
		// one turn_id: a thinking step, then a tool call's start/finish
		// pair — the durable trail that used to be fetched and discarded
		// on every page reload before openThread learned to read it back.
		vi.stubGlobal(
			'fetch',
			vi.fn((url: string) => {
				if (url.endsWith('/events')) {
					return Promise.resolve({
						ok: true,
						json: async () => [
							{ id: 1, level: 'info', source: 'turn', message: 'thinking', data: '{"content":"considering the question"}', turn_id: 'turn-1', created_at: '' },
							{ id: 2, level: 'info', source: 'tool.web_search', message: 'tool call started', data: '{"args":{"query":"capital of france"}}', turn_id: 'turn-1', created_at: '' },
							{ id: 3, level: 'info', source: 'tool.web_search', message: 'tool call finished', data: '{"result":"Paris","citations":[{"title":"France","url":"https://example.com"}]}', turn_id: 'turn-1', created_at: '' }
						]
					});
				}
				return Promise.resolve({
					ok: true,
					json: async () => ({
						cost_usd: 0.01,
						context_tokens: 10,
						messages: [
							{ id: 1, role: 'user', content: 'what is the capital of france', citations: '[]', suggestions: '[]', cost_usd: 0, turn_id: 'turn-1' },
							{ id: 2, role: 'assistant', content: 'Paris', citations: '[]', suggestions: '[]', cost_usd: 0.01, turn_id: 'turn-1' }
						]
					})
				});
			})
		);

		await state.openThread('t1');

		const assistantTurn = state.turns[1];
		expect(assistantTurn.timeline).toHaveLength(2);
		expect(assistantTurn.timeline?.[0]).toMatchObject({ kind: 'thinking', content: 'considering the question' });
		expect(assistantTurn.timeline?.[1]).toMatchObject({
			kind: 'tool',
			tool: 'web_search',
			result: 'Paris',
			citations: [{ title: 'France', url: 'https://example.com' }],
			done: true
		});
	});

	it('reconstructs persisted reasoning bursts as done reasoning items', async () => {
		// Mirrors gateway/turn.go's flushReasoning — one row per reasoning
		// burst, source "turn" message "reasoning", distinct from the
		// explicit "thinking" tool. Reconstructed items are done: true
		// (unlike a live-streaming burst, which starts done: false and
		// gets closed out once something else interrupts it).
		vi.stubGlobal(
			'fetch',
			vi.fn((url: string) => {
				if (url.endsWith('/events')) {
					return Promise.resolve({
						ok: true,
						json: async () => [
							{ id: 1, level: 'info', source: 'turn', message: 'reasoning', data: '{"content":"Let me think about this first."}', turn_id: 'turn-1', created_at: '' }
						]
					});
				}
				return Promise.resolve({
					ok: true,
					json: async () => ({
						cost_usd: 0.01,
						context_tokens: 10,
						messages: [
							{ id: 1, role: 'user', content: 'a question', citations: '[]', suggestions: '[]', cost_usd: 0, turn_id: 'turn-1' },
							{ id: 2, role: 'assistant', content: 'an answer', citations: '[]', suggestions: '[]', cost_usd: 0.01, turn_id: 'turn-1' }
						]
					})
				});
			})
		);

		await state.openThread('t1');

		const assistantTurn = state.turns[1];
		expect(assistantTurn.timeline).toHaveLength(1);
		expect(assistantTurn.timeline?.[0]).toMatchObject({
			kind: 'reasoning',
			content: 'Let me think about this first.',
			done: true
		});
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

describe('AppState.regenerateTitle', () => {
	it('posts to the regenerate-title endpoint and reloads threads on success', async () => {
		const fetchMock = vi.fn((url: string, opts?: RequestInit) => {
			if (typeof url === 'string' && url.endsWith('/regenerate-title')) {
				expect(opts?.method).toBe('POST');
				return Promise.resolve({ ok: true, json: async () => ({ title: 'New Title' }) });
			}
			return Promise.resolve({ ok: true, json: async () => [{ id: 't1', title: 'New Title' }] });
		});
		vi.stubGlobal('fetch', fetchMock);

		const state = new AppState();
		const ok = await state.regenerateTitle('t1');

		expect(ok).toBe(true);
		expect(fetchMock).toHaveBeenCalledWith('/api/threads/t1/regenerate-title', { method: 'POST' });
		expect(state.threads).toEqual([{ id: 't1', title: 'New Title' }]);
	});

	it('returns false and skips the reload when the request fails', async () => {
		const fetchMock = vi.fn(() => Promise.resolve({ ok: false, json: async () => ({}) }));
		vi.stubGlobal('fetch', fetchMock);

		const state = new AppState();
		const ok = await state.regenerateTitle('t1');

		expect(ok).toBe(false);
		expect(fetchMock).toHaveBeenCalledTimes(1); // no loadThreads follow-up call
	});
});

describe('AppState.showToast', () => {
	// The copy buttons' checkmark icon-swap alone turned out not to be a
	// clear enough "did that work" signal (clipboard writes can also fail
	// silently over plain HTTP, where the Clipboard API isn't available) —
	// showToast is the app-level confirmation banner that backs those.
	beforeEach(() => {
		vi.useFakeTimers();
	});

	afterEach(() => {
		vi.useRealTimers();
	});

	it('adds a toast and auto-dismisses it after the given duration', () => {
		const state = new AppState();
		state.showToast('Copied answer', 2000);

		expect(state.toasts).toHaveLength(1);
		expect(state.toasts[0].message).toBe('Copied answer');

		vi.advanceTimersByTime(2000);
		expect(state.toasts).toHaveLength(0);
	});

	it('stacks multiple toasts independently instead of one replacing another', () => {
		const state = new AppState();
		state.showToast('Copied answer', 2000);
		state.showToast('Copied answer with sources', 2000);

		expect(state.toasts.map((t) => t.message)).toEqual(['Copied answer', 'Copied answer with sources']);

		vi.advanceTimersByTime(2000);
		expect(state.toasts).toHaveLength(0);
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

	it('resets the URL back to /', () => {
		window.history.replaceState({}, '', '/t/some-thread');
		const state = new AppState();

		state.newThread();

		expect(window.location.pathname).toBe('/');
	});
});

// The URL is what makes a thread survive a refresh (see routes/t/[id]) —
// without this sync, currentThreadId lived only in memory and a reload
// always landed back on the homescreen with no way to tell which thread
// had been open.
describe('AppState URL sync', () => {
	beforeEach(() => {
		window.history.replaceState({}, '', '/');
	});

	it('openThread updates the URL to /t/<id>', async () => {
		const state = new AppState();
		vi.stubGlobal('fetch', fakeFetch({ cost_usd: 0, context_tokens: 0, messages: [] }));

		await state.openThread('abc-123');

		expect(window.location.pathname).toBe('/t/abc-123');
	});

	it('a brand-new thread updates the URL as soon as its id is known, not just once the answer finishes', () => {
		const state = new AppState();
		vi.stubGlobal('fetch', fakeFetch([]));

		state.send('a fresh question');
		expect(window.location.pathname).toBe('/'); // no id yet

		fireEvent(state, { type: 'user_message', thread_id: 'new-thread-id', user_message_id: 1 });

		expect(window.location.pathname).toBe('/t/new-thread-id');
	});
});

// Regression test for a live, confirmed bug: checkVersion's post-turn
// reload used to be a bare window.location.reload(), which trusts the
// address bar to already reflect currentThreadId. 'done' never re-syncs
// the URL (only openThread/newThread/the new-thread-id branch above do),
// so any drift between the two turned a version-bump reload into landing
// on whatever unrelated thread the address bar happened to still say —
// reproduced against the real app, not just a theoretical gap.
describe('AppState.checkVersion reload target', () => {
	beforeEach(() => {
		window.history.replaceState({}, '', '/');
	});

	function fakeVersionFetch(version: string) {
		return vi.fn(() => Promise.resolve({ ok: true, json: async () => ({ version }) }));
	}

	it('navigates explicitly to currentThreadId instead of trusting the address bar', async () => {
		const state = new AppState();
		vi.stubGlobal('fetch', fakeVersionFetch('r1'));
		await (state as any).checkVersion(); // captures the baseline version

		// Simulate the drift: currentThreadId has moved on to a real thread,
		// but nothing has re-synced the address bar since (the exact gap
		// 'done' leaves — see this describe block's doc comment).
		state.currentThreadId = 'the-real-current-thread';
		window.history.replaceState({}, '', '/'); // address bar lagging behind

		vi.stubGlobal('fetch', fakeVersionFetch('r2'));
		const reloadSpy = vi.spyOn(window.location, 'reload').mockImplementation(() => {});

		await (state as any).checkVersion();

		expect(window.location.pathname).toBe('/t/the-real-current-thread');
		// The href navigation above is what actually reloads the page here
		// (a real browser navigates to a new URL); reload() is only the
		// same-URL fallback path and must not have fired for a genuine
		// cross-thread correction.
		expect(reloadSpy).not.toHaveBeenCalled();
	});

	it('still force-reloads when the address bar already matches currentThreadId', async () => {
		const state = new AppState();
		vi.stubGlobal('fetch', fakeVersionFetch('r1'));
		await (state as any).checkVersion();

		state.currentThreadId = 'already-synced-thread';
		window.history.replaceState({}, '', '/t/already-synced-thread');

		vi.stubGlobal('fetch', fakeVersionFetch('r2'));
		const reloadSpy = vi.spyOn(window.location, 'reload').mockImplementation(() => {});

		await (state as any).checkVersion();

		expect(reloadSpy).toHaveBeenCalledOnce();
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

	it('does not send a stop once the pending turn has been abandoned by navigating elsewhere', async () => {
		const state = new AppState();
		vi.stubGlobal('fetch', fakeFetch({ cost_usd: 0, context_tokens: 0, messages: [] }));
		const sendSpy = vi.spyOn((state as any).socket, 'send');

		state.send('question');
		fireEvent(state, { type: 'user_message', thread_id: 'A', user_message_id: 1 });
		await state.openThread('B'); // navigate away while A is still generating

		state.stopGeneration();

		// Only the original 'message' send should be recorded — no 'stop'.
		expect(sendSpy).not.toHaveBeenCalledWith({ type: 'stop' });
	});
});

// These cover the three related symptoms of one root cause: busy/
// pendingThreadId/pendingTurn track a single in-flight turn globally, with
// no notion of "but has the user since navigated away from it?" — found in
// a latent-bug audit, see pendingAbandoned's doc comment in state.svelte.ts
// for the full mechanism.
describe('AppState in-flight thread tracking across navigation', () => {
	let state: AppState;

	beforeEach(() => {
		state = new AppState();
		vi.stubGlobal('fetch', fakeFetch({ cost_usd: 0, context_tokens: 0, messages: [] }));
	});

	it('busyOnCurrentThread is true while watching a brand-new thread create itself', () => {
		expect(state.busyOnCurrentThread).toBe(false);
		state.send('question');
		expect(state.busyOnCurrentThread).toBe(true); // currentThreadId still null, nothing navigated away

		fireEvent(state, { type: 'user_message', thread_id: 'A', user_message_id: 1 });
		expect(state.busyOnCurrentThread).toBe(true); // pendingThreadId known now, still watching it
	});

	it('newThread() while busy abandons the pending turn, so its later done event does not resurrect it as current', () => {
		state.send('question');
		fireEvent(state, { type: 'user_message', thread_id: 'A', user_message_id: 1 });

		state.newThread();
		expect(state.currentThreadId).toBeNull();
		expect(state.busyOnCurrentThread).toBe(false);

		fireEvent(state, { type: 'done', thread_id: 'A', cost_usd: 0.02, citations: [] });

		expect(state.currentThreadId).toBeNull(); // not silently reset to 'A'
		expect(state.totalCost).toBe(0); // A's cost never applied to this (unrelated) view
	});

	it('opening a different existing thread while busy abandons the pending turn the same way', async () => {
		state.send('question');
		fireEvent(state, { type: 'user_message', thread_id: 'A', user_message_id: 1 });

		await state.openThread('B');
		expect(state.busyOnCurrentThread).toBe(false);

		fireEvent(state, { type: 'done', thread_id: 'A', cost_usd: 0.02, citations: [] });

		expect(state.currentThreadId).toBe('B'); // not reverted to A
	});

	it('navigating back to the pending thread before it finishes un-abandons it', async () => {
		state.send('question');
		fireEvent(state, { type: 'user_message', thread_id: 'A', user_message_id: 1 });

		await state.openThread('B'); // abandon
		await state.openThread('A'); // come back before it's done
		expect(state.busyOnCurrentThread).toBe(true);

		fireEvent(state, { type: 'done', thread_id: 'A', cost_usd: 0.02, citations: [] });

		expect(state.currentThreadId).toBe('A');
		expect(state.totalCost).toBe(0.02);
	});

	it('a stale openThread() response cannot overwrite a newer one that resolved first', async () => {
		let resolveFirst!: (v: unknown) => void;
		let callCount = 0;
		vi.stubGlobal(
			'fetch',
			vi.fn((url: string) => {
				if (url.endsWith('/events')) return Promise.resolve({ ok: true, json: async () => [] });
				callCount++;
				if (callCount === 1) {
					// First call (thread "slow") hangs until resolveFirst() is called below.
					return new Promise((resolve) => {
						resolveFirst = () =>
							resolve({
								ok: true,
								json: async () => ({ cost_usd: 1, context_tokens: 0, messages: [] })
							});
					});
				}
				// Second call (thread "fast") resolves immediately.
				return Promise.resolve({
					ok: true,
					json: async () => ({ cost_usd: 2, context_tokens: 0, messages: [] })
				});
			})
		);

		const slowCall = state.openThread('slow');
		const fastCall = state.openThread('fast');
		await fastCall;
		expect(state.currentThreadId).toBe('fast');

		resolveFirst(undefined);
		await slowCall;

		// The slow call's stale result must not clobber "fast", which the
		// user actually clicked last and is now looking at.
		expect(state.currentThreadId).toBe('fast');
		expect(state.totalCost).toBe(2);
	});
});
