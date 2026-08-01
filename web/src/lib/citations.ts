import type { Citation } from './types';

/**
 * Turns the model's inline `[Title](URL)` citations — already rendered to
 * plain `<a>` tags by marked+DOMPurify — into small numbered badges that
 * match the source list below the answer (Perplexity's "claim¹" pattern),
 * for every link whose URL is one of this turn's tracked citations. A
 * link whose URL ISN'T a tracked citation (some other reference the model
 * added inline) is left as an ordinary link — this only touches citations
 * that are actually backed by a source in the list, so it can never
 * silently drop or misrepresent one.
 *
 * DOM-based (not regex) since correctly walking arbitrary nested HTML for
 * `<a>` tags is exactly what a DOM parser is for — html is already
 * DOMPurify-sanitized by the caller before this runs, so re-parsing it
 * here doesn't reintroduce any risk.
 */
export function numberInlineCitations(html: string, citations: Citation[], turnKey: string | number): string {
	if (typeof document === 'undefined' || citations.length === 0 || !html) return html;

	const urlToIndex = new Map(citations.map((c, i) => [c.url, i]));
	const container = document.createElement('div');
	container.innerHTML = html;

	for (const anchor of container.querySelectorAll('a[href]')) {
		const href = anchor.getAttribute('href') ?? '';
		const i = urlToIndex.get(href);
		if (i === undefined) continue;

		const title = anchor.textContent?.trim() || href;
		anchor.setAttribute('class', 'citation-badge');
		anchor.setAttribute('data-source-target', `source-${turnKey}-${i}`);
		anchor.setAttribute('title', title);
		anchor.textContent = String(i + 1);
	}

	return container.innerHTML;
}
