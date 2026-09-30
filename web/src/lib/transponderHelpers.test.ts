import { describe, it, expect } from 'vitest';
import {
	chipLabel,
	citationHost,
	computePeaks,
	formatElapsed,
	stripInlineMarkdownLinks,
	type ThinkingChip
} from './transponderHelpers';

describe('formatElapsed', () => {
	it('formats seconds as m:ss with zero-padded seconds', () => {
		expect(formatElapsed(0)).toBe('0:00');
		expect(formatElapsed(7)).toBe('0:07');
		expect(formatElapsed(65)).toBe('1:05');
		expect(formatElapsed(600)).toBe('10:00');
	});
});

describe('citationHost', () => {
	it('returns the hostname without a leading www.', () => {
		expect(citationHost('https://www.example.com/a/b?c=1')).toBe('example.com');
		expect(citationHost('https://en.wikipedia.org/wiki/X')).toBe('en.wikipedia.org');
	});

	it('falls back to the raw string when it is not a URL', () => {
		expect(citationHost('not a url')).toBe('not a url');
	});
});

describe('stripInlineMarkdownLinks', () => {
	it('keeps the link text and drops the URL', () => {
		expect(stripInlineMarkdownLinks('See [Investor Relations](https://ir.example.com/q3) for more.')).toBe(
			'See Investor Relations for more.'
		);
	});

	it('handles several links and leaves plain text alone', () => {
		expect(stripInlineMarkdownLinks('[a](http://x.co) and [b](http://y.co)')).toBe('a and b');
		expect(stripInlineMarkdownLinks('no links here (really)')).toBe('no links here (really)');
	});
});

describe('chipLabel', () => {
	const tool = (name: string, args?: Record<string, unknown>, done = false): ThinkingChip =>
		({ kind: 'tool', tool: name, args, done }) as ThinkingChip;

	it('labels reasoning by whether it has finished', () => {
		expect(chipLabel({ kind: 'reasoning', content: '', done: false })).toBe('Reasoning…');
		expect(chipLabel({ kind: 'reasoning', content: '', done: true })).toBe('Reasoned');
	});

	it('describes the well-known tools and falls back to the tool name', () => {
		expect(chipLabel(tool('web_search', { query: 'rust async' }))).toBe('Searching: rust async');
		expect(chipLabel(tool('web_read', { url: 'https://a.io' }))).toBe('Reading: https://a.io');
		expect(chipLabel(tool('weather'))).toBe('Checking the weather');
		expect(chipLabel(tool('code_exec'))).toBe('Running code');
		expect(chipLabel(tool('memory'))).toBe('memory');
	});
});

describe('computePeaks', () => {
	// A stand-in for AudioBuffer: computePeaks only reads channel 0 and the sample rate.
	function fakeBuffer(samples: number[], sampleRate: number): AudioBuffer {
		return { getChannelData: () => Float32Array.from(samples), sampleRate } as unknown as AudioBuffer;
	}

	it('buckets by resolution and normalizes against the loudest bucket', () => {
		// 1kHz sample rate, 10ms buckets => 10 samples per bucket.
		const quiet = new Array(10).fill(0.1);
		const loud = new Array(10).fill(0.8);
		const peaks = computePeaks(fakeBuffer([...quiet, ...loud], 1000), 10);
		expect(peaks).toHaveLength(2);
		expect(peaks[1]).toBeCloseTo(1, 5); // loudest bucket normalizes to 1
		expect(peaks[0]).toBeLessThan(0.1); // and exaggerates the quiet one downward
	});

	it('never returns a value below the visual floor, even for silence', () => {
		const peaks = computePeaks(fakeBuffer(new Array(30).fill(0), 1000), 10);
		expect(peaks.every((p) => p === 0.08)).toBe(true);
	});
});
