import { describe, it, expect } from 'vitest';
import { numberInlineCitations } from './citations';
import type { Citation } from './types';

const citations: Citation[] = [
	{ title: 'NASA Voyager overview', url: 'https://nasa.gov/voyager' },
	{ title: 'Wikipedia: Voyager 1', url: 'https://en.wikipedia.org/wiki/Voyager_1' }
];

describe('numberInlineCitations', () => {
	it('replaces a tracked citation link with a numbered badge', () => {
		const html = '<p>Voyager 1 is the farthest spacecraft <a href="https://nasa.gov/voyager">NASA overview</a>.</p>';
		const out = numberInlineCitations(html, citations, 0);
		expect(out).toContain('class="citation-badge"');
		expect(out).toContain('>1<');
		expect(out).toContain('data-source-target="source-0-0"');
		// Original link text is preserved as the hover title, not left inline.
		expect(out).toContain('title="NASA overview"');
	});

	it('numbers badges by the citation\'s position in the list, not appearance order', () => {
		const html =
			'<p>See <a href="https://en.wikipedia.org/wiki/Voyager_1">wiki</a> and ' +
			'<a href="https://nasa.gov/voyager">nasa</a>.</p>';
		const out = numberInlineCitations(html, citations, 0);
		expect(out).toContain('>2<'); // wikipedia is citations[1]
		expect(out).toContain('>1<'); // nasa is citations[0]
	});

	it('leaves a link untouched if its URL is not a tracked citation', () => {
		const html = '<p>Unrelated <a href="https://example.com/other">link</a>.</p>';
		const out = numberInlineCitations(html, citations, 0);
		expect(out).toContain('href="https://example.com/other"');
		expect(out).not.toContain('citation-badge');
		expect(out).toContain('>link<');
	});

	it('returns html unchanged when there are no citations', () => {
		const html = '<p>No sources here.</p>';
		expect(numberInlineCitations(html, [], 0)).toBe(html);
	});

	it('handles empty html', () => {
		expect(numberInlineCitations('', citations, 0)).toBe('');
	});
});
