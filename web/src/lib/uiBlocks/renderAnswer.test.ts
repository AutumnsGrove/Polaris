import { describe, it, expect } from 'vitest';
import DOMPurify from 'dompurify';
import { marked } from '$lib/markdown';
import { renderInlineCitations } from '$lib/citations';
import type { Citation, VerificationMark } from '$lib/types';
import { renderAnswer } from './renderAnswer';

const citations: Citation[] = [
	{ title: 'NASA', url: 'https://nasa.gov/voyager', site_name: 'NASA' },
	{ title: 'Wiki', url: 'https://en.wikipedia.org/wiki/Voyager_1', site_name: 'Wikipedia' }
];

// The same URL cited before and after a diagram, with only the SECOND
// occurrence verified (claim_index 1, counted across the whole answer).
const content =
	'Voyager launched in 1977 [a](https://nasa.gov/voyager).\n\n' +
	'```mermaid\ngraph TD\nA-->B\n```\n\n' +
	'It is still operating [b](https://nasa.gov/voyager) and [c](https://en.wikipedia.org/wiki/Voyager_1).\n';

const verification: VerificationMark[] = [
	{ url: 'https://nasa.gov/voyager', claim_index: 1, choice: 'supported', confidence: 0.9 }
];

describe('renderAnswer', () => {
	it('emits md / mermaid / md segments in order', () => {
		const segs = renderAnswer(content, false, citations, verification);
		expect(segs.map((s) => s.kind)).toEqual(['md', 'mermaid', 'md']);
	});

	it('keeps the nth-occurrence counter running across segments', () => {
		const [first, , last] = renderAnswer(content, false, citations, verification);
		// First NASA chip (before the diagram) is occurrence 0: not verified.
		expect((first as { html: string }).html).not.toContain('citation-verified-icon');
		// Second NASA chip (after the diagram) is occurrence 1: verified.
		const html = (last as { html: string }).html;
		expect(html.match(/citation-verified-icon/g)).toHaveLength(1);
		expect(html.indexOf('citation-verified-icon')).toBeLessThan(html.indexOf('Wikipedia'));
	});

	it('marks the same chips as rendering the whole answer unsplit', () => {
		const unsplit = renderInlineCitations(
			DOMPurify.sanitize(marked.parse(content) as string),
			citations,
			verification
		);
		const verifiedTitles = (html: string) =>
			[...html.matchAll(/<a [^>]*title="([^"]*found in source)"/g)].map((m) => m[1]);
		const split = renderAnswer(content, false, citations, verification)
			.filter((s) => s.kind === 'md')
			.map((s) => (s as { html: string }).html)
			.join('');
		expect(verifiedTitles(split)).toEqual(verifiedTitles(unsplit));
		expect(verifiedTitles(split)).toHaveLength(1);
	});

	it('leaves a ui fence as an ordinary code block until its renderer exists', () => {
		const segs = renderAnswer('```ui\n{"c":"stat"}\n```\n', false, citations);
		expect(segs).toHaveLength(1);
		expect(segs[0].kind).toBe('md');
		expect((segs[0] as { html: string }).html).toContain('class="hljs"');
	});
});
