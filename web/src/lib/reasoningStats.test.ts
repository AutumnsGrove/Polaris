import { describe, expect, it } from 'vitest';
import { estimateReasoningTokens, formatDuration, formatTokens, reasoningStatsLabel } from './reasoningStats';

describe('reasoning stats', () => {
	it('formats durations', () => {
		expect(formatDuration(0)).toBe('0s');
		expect(formatDuration(12_400)).toBe('12s');
		expect(formatDuration(60_000)).toBe('1m');
		expect(formatDuration(65_000)).toBe('1m 5s');
	});

	it('formats token counts compactly', () => {
		expect(formatTokens(340)).toBe('340');
		expect(formatTokens(2800)).toBe('2.8k');
		expect(formatTokens(14_200)).toBe('14k');
	});

	it('estimates ~4 chars per token', () => {
		expect(estimateReasoningTokens('a'.repeat(400))).toBe(100);
	});

	it('labels a running burst from startedAt and a finished one from durationMs', () => {
		const content = 'x'.repeat(11_200);
		expect(reasoningStatsLabel({ content, done: false, startedAt: 1000 }, 9000)).toBe('Thinking for 8s, ~2.8k tokens');
		expect(reasoningStatsLabel({ content, done: true, durationMs: 12_000 }, 99999)).toBe('Thought for 12s, ~2.8k tokens');
	});

	it('degrades for legacy rows with no duration, and for empty content', () => {
		expect(reasoningStatsLabel({ content: 'x'.repeat(40), done: true }, 0)).toBe('~10 tokens');
		expect(reasoningStatsLabel({ content: '', done: true }, 0)).toBe('');
	});
});
