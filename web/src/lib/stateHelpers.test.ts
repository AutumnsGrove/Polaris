import { describe, it, expect } from 'vitest';
import { buildTimelineFromEvents } from './stateHelpers';
import type { StoredEvent } from './types';

let nextId = 1;
function row(source: string, message: string, data: Record<string, unknown>): StoredEvent {
	return { id: nextId++, level: 'info', source, message, data: JSON.stringify(data), created_at: '' };
}

describe('buildTimelineFromEvents: Deep Research sub-agents', () => {
	it('rebuilds the same nesting the live reducer produces', () => {
		const timeline = buildTimelineFromEvents([
			row('tool.spawn_researchers', 'tool call started', { call_id: 'c', args: { task_count: 2 } }),
			row('subagent', 'subagent started', { agent_id: 'c.0', call_id: 'c', objective: 'X' }),
			row('subagent', 'subagent started', { agent_id: 'c.1', call_id: 'c', objective: 'Y' }),
			row('turn', 'reasoning', { agent_id: 'c.0', content: 'hmm', duration_ms: 1200 }),
			row('tool.web_search', 'tool call started', { agent_id: 'c.0', call_id: 'call_1', args: { query: 'q' } }),
			row('tool.web_read', 'tool call started', { agent_id: 'c.1', call_id: 'call_1', args: { url: 'u' } }),
			row('tool.web_read', 'tool call finished', { agent_id: 'c.1', call_id: 'call_1', result: 'page' }),
			row('tool.web_search', 'tool call finished', { agent_id: 'c.0', call_id: 'call_1', result: 'hits' }),
			row('subagent', 'subagent finished', { agent_id: 'c.0', status: 'done', result: '- f0', cost_usd: 0.01 }),
			row('subagent', 'subagent finished', { agent_id: 'c.1', status: 'failed', result: 'boom' }),
			row('tool.spawn_researchers', 'tool call finished', { call_id: 'c', result: 'all reports' })
		]);

		expect(timeline.map((i) => i.kind)).toEqual(['tool', 'subagent', 'subagent']);
		expect(timeline[0]).toMatchObject({ kind: 'tool', tool: 'spawn_researchers', done: true });
		expect(timeline[1]).toMatchObject({
			kind: 'subagent',
			agentId: 'c.0',
			objective: 'X',
			status: 'done',
			result: '- f0',
			costUsd: 0.01,
			items: [
				{ kind: 'reasoning', content: 'hmm', done: true, durationMs: 1200 },
				// Same call_id as c.1's call, matched to its own result.
				{ kind: 'tool', tool: 'web_search', done: true, result: 'hits' }
			]
		});
		expect(timeline[2]).toMatchObject({
			status: 'failed',
			items: [{ kind: 'tool', tool: 'web_read', done: true, result: 'page' }]
		});
	});

	it('shows a sub-agent that never finished as failed, not spinning forever', () => {
		const timeline = buildTimelineFromEvents([
			row('subagent', 'subagent started', { agent_id: 'c.0', call_id: 'c', objective: 'X' })
		]);
		expect(timeline[0]).toMatchObject({ kind: 'subagent', status: 'failed' });
	});

	it('leaves a turn without sub-agents exactly as before', () => {
		const timeline = buildTimelineFromEvents([
			row('turn', 'commentary', { content: 'hi' }),
			row('tool.web_search', 'tool call started', { call_id: 'a', args: {} }),
			row('tool.web_search', 'tool call finished', { call_id: 'a', result: 'r' })
		]);
		expect(timeline).toMatchObject([{ kind: 'commentary' }, { kind: 'tool', done: true, result: 'r' }]);
	});
});
