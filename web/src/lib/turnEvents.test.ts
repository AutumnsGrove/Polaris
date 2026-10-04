import { describe, it, expect } from 'vitest';
import { applyStreamingEvent, closeOpenReasoning } from './turnEvents';
import type { ChatTurn, ServerEvent } from './types';

function assistantTurn(overrides: Partial<ChatTurn> = {}): ChatTurn {
	return { role: 'assistant', content: '', streaming: true, ...overrides };
}

// The union is wide and these tests only exercise a few members' fields, so
// build events loosely and assert on the resulting turn instead.
function ev(e: Record<string, unknown>): ServerEvent {
	return e as unknown as ServerEvent;
}

describe('applyStreamingEvent', () => {
	it('appends token content, treating an absent content field as empty', () => {
		const turn = assistantTurn();
		applyStreamingEvent(turn, ev({ type: 'token', content: 'Hel' }));
		applyStreamingEvent(turn, ev({ type: 'token' })); // omitempty drops empty strings
		applyStreamingEvent(turn, ev({ type: 'token', content: 'lo' }));
		expect(turn.content).toBe('Hello');
	});

	it('grows one open reasoning item in place, then starts a new one after it closes', () => {
		const turn = assistantTurn();
		applyStreamingEvent(turn, ev({ type: 'reasoning', content: 'think ' }));
		applyStreamingEvent(turn, ev({ type: 'reasoning', content: 'more' }));
		expect(turn.timeline).toEqual([
			{ kind: 'reasoning', content: 'think more', done: false, startedAt: expect.any(Number) }
		]);

		// A tool call interrupts reasoning, which marks it done and stamps
		// how long the burst ran.
		applyStreamingEvent(turn, ev({ type: 'tool_call', tool: 'web_search', call_id: 'a' }));
		expect(turn.timeline?.[0]).toMatchObject({ kind: 'reasoning', done: true, durationMs: expect.any(Number) });

		applyStreamingEvent(turn, ev({ type: 'reasoning', content: 'again' }));
		expect(turn.timeline?.at(-1)).toMatchObject({ kind: 'reasoning', content: 'again', done: false });
	});

	it('routes a tool_result to its own call_id when two calls to the same tool are open', () => {
		const turn = assistantTurn();
		applyStreamingEvent(turn, ev({ type: 'tool_call', tool: 'memory', call_id: 'first' }));
		applyStreamingEvent(turn, ev({ type: 'tool_call', tool: 'memory', call_id: 'second' }));
		// The second call finishes first — a name-only backward scan would
		// wrongly attach this to 'second' anyway, so finish 'first' second to
		// prove the id (not position) decides.
		applyStreamingEvent(turn, ev({ type: 'tool_result', tool: 'memory', call_id: 'second', result: 'B' }));
		applyStreamingEvent(turn, ev({ type: 'tool_result', tool: 'memory', call_id: 'first', result: 'A' }));
		expect(turn.timeline).toMatchObject([
			{ kind: 'tool', callId: 'first', result: 'A', done: true },
			{ kind: 'tool', callId: 'second', result: 'B', done: true }
		]);
	});

	it('falls back to the newest open call of that tool when no call_id is sent', () => {
		const turn = assistantTurn();
		applyStreamingEvent(turn, ev({ type: 'tool_call', tool: 'weather' }));
		applyStreamingEvent(turn, ev({ type: 'tool_result', tool: 'weather', result: 'sunny' }));
		expect(turn.timeline).toMatchObject([{ kind: 'tool', tool: 'weather', result: 'sunny', done: true }]);
	});

	it('turns commentary into its own timeline item and clears the flat content streamed for it', () => {
		const turn = assistantTurn({ content: 'Let me look that up.' });
		applyStreamingEvent(turn, ev({ type: 'commentary', content: 'Let me look that up.' }));
		expect(turn.content).toBe('');
		expect(turn.timeline).toEqual([{ kind: 'commentary', content: 'Let me look that up.' }]);
	});

	it('cost_update overwrites the running total rather than adding to it', () => {
		const turn = assistantTurn({ costUsd: 0.01 });
		applyStreamingEvent(turn, ev({ type: 'cost_update', cost_usd: 0.03 }));
		expect(turn.costUsd).toBe(0.03);
	});
});

describe('closeOpenReasoning', () => {
	it('is a no-op when there is no open reasoning item', () => {
		const turn = assistantTurn({ timeline: [{ kind: 'thinking', content: 'x' }] });
		const before = turn.timeline;
		closeOpenReasoning(turn);
		expect(turn.timeline).toBe(before);
	});
});
