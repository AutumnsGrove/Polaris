import { describe, it, expect } from 'vitest';
import { renderInlineCitations } from './citations';
import type { Citation } from './types';

const citations: Citation[] = [
	{ title: 'Voyager - NASA Solar System Exploration', url: 'https://nasa.gov/voyager', site_name: 'NASA' },
	{ title: 'Wikipedia: Voyager 1', url: 'https://en.wikipedia.org/wiki/Voyager_1' }
];

describe('renderInlineCitations', () => {
	it('replaces a tracked citation link with a named chip that keeps its href', () => {
		const html = '<p>Voyager 1 is the farthest spacecraft <a href="https://nasa.gov/voyager">NASA overview</a>.</p>';
		const out = renderInlineCitations(html, citations);
		expect(out).toContain('class="citation-chip"');
		expect(out).toContain('href="https://nasa.gov/voyager"');
		expect(out).toContain('target="_blank"');
		expect(out).toContain('>NASA<');
		// Full article title becomes the hover tooltip, not the model's own
		// arbitrary inline link text.
		expect(out).toContain('title="Voyager - NASA Solar System Exploration"');
	});

	it('prefers site_name over a hostname-derived fallback', () => {
		const html = '<p>See <a href="https://nasa.gov/voyager">nasa</a>.</p>';
		const out = renderInlineCitations(html, citations);
		expect(out).toContain('>NASA<');
	});

	it('falls back to a hostname-derived name when site_name is missing', () => {
		const html = '<p>See <a href="https://en.wikipedia.org/wiki/Voyager_1">wiki</a>.</p>';
		const out = renderInlineCitations(html, citations);
		expect(out).toContain('>Wikipedia<');
	});

	it('leaves a link untouched if its URL is not a tracked citation', () => {
		const html = '<p>Unrelated <a href="https://example.com/other">link</a>.</p>';
		const out = renderInlineCitations(html, citations);
		expect(out).toContain('href="https://example.com/other"');
		expect(out).not.toContain('citation-chip');
		expect(out).toContain('>link<');
	});

	it('leaves links inside table cells untouched, even when tracked', () => {
		const html =
			'<table><tr><td><a href="https://nasa.gov/voyager">Voyager Program</a></td><td>Active</td></tr></table>';
		const out = renderInlineCitations(html, citations);
		expect(out).not.toContain('citation-chip');
		expect(out).toContain('>Voyager Program<');
		expect(out).toContain('href="https://nasa.gov/voyager"');
	});

	it('still converts a citation chip outside a table even when other tracked links sit inside one', () => {
		const html =
			'<p>Voyager 1 is the farthest spacecraft <a href="https://nasa.gov/voyager">NASA overview</a>.</p>' +
			'<table><tr><td><a href="https://en.wikipedia.org/wiki/Voyager_1">Voyager 1</a></td></tr></table>';
		const out = renderInlineCitations(html, citations);
		expect(out).toContain('class="citation-chip"');
		expect(out).toContain('>NASA<');
		expect(out).toContain('>Voyager 1<');
	});

	it('returns html unchanged when there are no citations', () => {
		const html = '<p>No sources here.</p>';
		expect(renderInlineCitations(html, [])).toBe(html);
	});

	it('handles empty html', () => {
		expect(renderInlineCitations('', citations)).toBe('');
	});
});
