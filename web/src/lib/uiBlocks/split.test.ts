import { describe, it, expect } from 'vitest';
import { splitContent } from './split';

describe('splitContent', () => {
	it('returns plain prose as one md segment', () => {
		expect(splitContent('hello\n\nworld', false)).toEqual([{ kind: 'md', text: 'hello\n\nworld' }]);
	});

	it('returns nothing for empty content', () => {
		expect(splitContent('', true)).toEqual([]);
	});

	it('splits prose / mermaid / prose and keeps the prose byte-for-byte', () => {
		const src = 'before\n\n```mermaid\ngraph TD\nA-->B\n```\n\nafter\n';
		expect(splitContent(src, false)).toEqual([
			{ kind: 'md', text: 'before\n\n' },
			{ kind: 'mermaid', src: 'graph TD\nA-->B\n', closed: true },
			{ kind: 'md', text: '\nafter\n' }
		]);
	});

	it('recognizes ui fences exactly, and mermaid case-insensitively', () => {
		const out = splitContent('```ui\n{"c":"stat"}\n```\n```Mermaid\ngraph TD\n```\n', false);
		expect(out.map((s) => s.kind)).toEqual(['ui', 'mermaid']);
	});

	it('leaves a ui fence with extra info as an ordinary code block', () => {
		const src = '```ui title\n{}\n```\n';
		expect(splitContent(src, false)).toEqual([{ kind: 'md', text: src }]);
	});

	it('treats an indented fence as ordinary markdown (list-nested code stays marked\'s job)', () => {
		const src = '- item\n\n  ```mermaid\n  graph TD\n  ```\n';
		expect(splitContent(src, false)).toEqual([{ kind: 'md', text: src }]);
	});

	it('does not look inside another fence', () => {
		const src = '````md\n```ui\n{"c":"stat"}\n```\n````\n';
		expect(splitContent(src, false)).toEqual([{ kind: 'md', text: src }]);
	});

	it('only closes on a fence of the same character and at least the same length', () => {
		const out = splitContent('````ui\n{"a":1}\n```\n{"b":2}\n````\n', false);
		expect(out).toEqual([{ kind: 'ui', src: '{"a":1}\n```\n{"b":2}\n', closed: true }]);
	});

	it('keeps an open fence open mid-stream, including its partial last line', () => {
		expect(splitContent('hi\n```ui\n{"c":"stat"}\n{"c":"co', true)).toEqual([
			{ kind: 'md', text: 'hi\n' },
			{ kind: 'ui', src: '{"c":"stat"}\n{"c":"co', closed: false }
		]);
	});

	it('treats an unclosed fence as final once the turn stops streaming', () => {
		expect(splitContent('```mermaid\ngraph TD\nA-->B\n', false)).toEqual([
			{ kind: 'mermaid', src: 'graph TD\nA-->B\n', closed: true }
		]);
	});

	it('does not commit to a fence kind from an unterminated opener line', () => {
		// "```mer" could still become "```merge" or "```mermaid".
		expect(splitContent('text\n```mermaid', true)).toEqual([{ kind: 'md', text: 'text\n```mermaid' }]);
		expect(splitContent('text\n```mermaid\n', true)).toEqual([
			{ kind: 'md', text: 'text\n' },
			{ kind: 'mermaid', src: '', closed: false }
		]);
	});

	it('honours the kinds filter, leaving filtered fences as markdown', () => {
		const src = '```ui\n{"c":"stat"}\n```\n';
		expect(splitContent(src, false, ['mermaid'])).toEqual([{ kind: 'md', text: src }]);
	});

	it('handles several fences in one answer', () => {
		const src = '```mermaid\na\n```\ntext\n```mermaid\nb\n```\n';
		expect(splitContent(src, false).map((s) => s.kind)).toEqual(['mermaid', 'md', 'mermaid']);
	});

	it('never throws on any prefix of a mixed answer, and the full text splits exactly', () => {
		const src = 'intro\n\n```ui\n{"c":"compare"}\n{"row":"a"}\n```\n\nmid\n\n```mermaid\ngraph TD\nA-->B\n```\n\nend\n';
		for (let n = 0; n <= src.length; n++) {
			expect(() => splitContent(src.slice(0, n), true)).not.toThrow();
		}
		expect(splitContent(src, true)).toEqual([
			{ kind: 'md', text: 'intro\n\n' },
			{ kind: 'ui', src: '{"c":"compare"}\n{"row":"a"}\n', closed: true },
			{ kind: 'md', text: '\nmid\n\n' },
			{ kind: 'mermaid', src: 'graph TD\nA-->B\n', closed: true },
			{ kind: 'md', text: '\nend\n' }
		]);
	});
});
