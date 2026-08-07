import type { Citation } from './types';

/**
 * Turns the model's inline `[Title](URL)` citations — already rendered to
 * plain `<a>` tags by marked+DOMPurify — into named source chips (Claude.ai's
 * "claim (The Hollywood Reporter)" pattern) for every link whose URL is one
 * of this turn's tracked citations. A link whose URL ISN'T a tracked
 * citation (some other reference the model added inline) is left as an
 * ordinary link — this only touches citations that are actually backed by
 * a source in the list, so it can never silently drop or misrepresent one.
 *
 * Each chip keeps its native href — tapping it goes straight to the
 * source, not down to the source list at the bottom of the turn. The chip
 * text itself already names the source, so there's nothing left worth
 * digging through the full list for.
 *
 * DOM-based (not regex) since correctly walking arbitrary nested HTML for
 * `<a>` tags is exactly what a DOM parser is for — html is already
 * DOMPurify-sanitized by the caller before this runs, so re-parsing it
 * here doesn't reintroduce any risk.
 *
 * Skips links inside table cells: the chip pattern assumes the link is a
 * citation marker riding along inline prose, where swapping its text for a
 * source name loses nothing since the claim it supports is right there in
 * the same sentence. In a table cell the link text is often the entire
 * content of that cell (an item name, a project title) — replacing it with
 * a generic source name like "Github" destroys the one piece of data the
 * row exists to show, with no surrounding sentence to recover it from.
 */
export function renderInlineCitations(html: string, citations: Citation[]): string {
	if (typeof document === 'undefined' || citations.length === 0 || !html) return html;

	const urlToCitation = new Map(citations.map((c) => [c.url, c]));
	const container = document.createElement('div');
	container.innerHTML = html;

	for (const anchor of container.querySelectorAll('a[href]')) {
		if (anchor.closest('td, th')) continue;

		const href = anchor.getAttribute('href') ?? '';
		const citation = urlToCitation.get(href);
		if (!citation) continue;

		const label = citationLabel(citation);
		anchor.setAttribute('class', 'citation-chip');
		anchor.setAttribute('target', '_blank');
		anchor.setAttribute('rel', 'noreferrer');
		anchor.setAttribute('title', citation.title || href);
		anchor.textContent = label;
	}

	return container.innerHTML;
}

/**
 * The name shown on an inline chip. Prefers the publisher's own
 * self-reported name (og:site_name, set by web_read when it fetched the
 * page — see tools/web_read.go) over the page's article title, which is
 * usually too long and specific to read well as a source label. Falls back
 * to a best-effort name derived from the hostname when neither is
 * available (web_search hits never get a page fetch, so never carry a
 * site_name).
 */
function citationLabel(citation: Citation): string {
	if (citation.site_name) return citation.site_name;
	return friendlySiteName(citation.url) ?? citation.title;
}

/** en.wikipedia.org -> "Wikipedia", www.nytimes.com -> "Nytimes". No claim
 * to perfect capitalization for unhyphenated multi-word domains — that's
 * exactly the gap og:site_name (see above) fills whenever a site sets it. */
function friendlySiteName(url: string): string | null {
	let host: string;
	try {
		host = new URL(url).hostname;
	} catch {
		return null;
	}

	const labels = host.replace(/^www\./, '').split('.');
	// Drop the TLD (and, for a two-label host like "co.uk"-style suffixes,
	// just the last label — good enough for the common case without a full
	// public-suffix-list dependency for a cosmetic fallback).
	const main = labels.length > 1 ? labels[labels.length - 2] : labels[0];
	if (!main) return null;

	return main
		.split('-')
		.filter(Boolean)
		.map((word) => word[0].toUpperCase() + word.slice(1))
		.join(' ');
}
