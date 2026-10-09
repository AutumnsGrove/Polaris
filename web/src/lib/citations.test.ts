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

describe('renderInlineCitations: block links ticked by locator', () => {
	const nasa = 'https://nasa.gov/voyager';
	const wiki = 'https://en.wikipedia.org/wiki/Voyager_1';
	const two = `<p><a href="${nasa}">a</a> <a href="${wiki}">b</a> <a href="${nasa}">c</a></p>`;
	const ticks = (out: string) => (out.match(/citation-verified-icon/g) ?? []).length;
	const mark = (url: string, locator?: string, claim_index = 0) => ({
		url,
		claim_index,
		choice: 'supported',
		confidence: 1,
		...(locator ? { locator } : {})
	});

	it("ticks only the field's nth tracked link whose locator matches", () => {
		// the third link (the 2nd nasa one) is field link #2
		const marks = [mark(nasa, '0.1.0.src#2')];
		const out = renderInlineCitations(two, citations, undefined, new Map(), { prefix: '0.1.0.src', marks });
		expect(ticks(out)).toBe(1);
		expect(out.indexOf('citation-verified-icon')).toBeGreaterThan(out.lastIndexOf(wiki));
	});

	it('does not tick when the url at that address differs, or the address does', () => {
		const marks = [mark(wiki, '0.1.0.src#0'), mark(nasa, '0.1.9.src#0')];
		const out = renderInlineCitations(two, citations, undefined, new Map(), { prefix: '0.1.0.src', marks });
		expect(ticks(out)).toBe(0);
	});

	it('a locator mark never ticks a prose chip, and a prose mark never ticks a block link', () => {
		const locatorOnly = [mark(nasa, '0.0.0.text#0')];
		expect(ticks(renderInlineCitations(two, citations, locatorOnly))).toBe(0);
		const proseOnly = [mark(nasa, undefined, 0)];
		const out = renderInlineCitations(two, citations, undefined, new Map(), { prefix: '0.0.0.text', marks: proseOnly });
		expect(ticks(out)).toBe(0);
	});

	it('still ticks prose by occurrence index when a locator mark for the same URL is also present', () => {
		const marks = [mark(nasa, '0.0.0.text#0'), mark(nasa, undefined, 1)];
		const out = renderInlineCitations(two, citations, marks);
		expect(ticks(out)).toBe(1);
		expect(out.indexOf('citation-verified-icon')).toBeGreaterThan(out.lastIndexOf(wiki));
	});
});
