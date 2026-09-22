import type { Citation, VerificationMark } from './types';

// lucide's check-check glyph (two overlapping checkmarks), inlined as raw
// SVG rather than imported from @lucide/svelte — that package is
// Svelte-component-based and this module does plain DOM string
// manipulation, not component rendering. Sized/colored entirely by CSS
// (.citation-chip's :global rule in ChatTurnView.svelte), not inline
// attributes, so it inherits the chip's currentColor like every other
// lucide icon in this app.
const checkCheckIconSVG =
	'<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="citation-verified-icon"><path d="M18 6 7 17l-5-5"/><path d="m22 10-7.5 7.5L13 16"/></svg>';

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
 *
 * verification, when present, marks the specific chip a "found in source"
 * check passed for — see docs/plans/source-verification-badge.md and
 * VerificationMark's doc comment. Matched by (url, occurrence index): the
 * same URL can be cited more than once in one answer with different
 * verdicts, so this walks anchors in document order and only marks the
 * *nth* occurrence of a URL whose matching claim_index cleared the
 * threshold, not every chip citing that URL (the source-list chip's own
 * aggregate mark, Citation.verified, covers "any claim for this URL" —
 * see ChatTurnView.svelte's .source-chip).
 */
export function renderInlineCitations(html: string, citations: Citation[], verification?: VerificationMark[]): string {
	if (typeof document === 'undefined' || citations.length === 0 || !html) return html;

	const urlToCitation = new Map(citations.map((c) => [c.url, c]));
	const verifiedClaimIndexes = new Map<string, Set<number>>();
	for (const mark of verification ?? []) {
		if (mark.choice !== 'supported') continue;
		if (!verifiedClaimIndexes.has(mark.url)) verifiedClaimIndexes.set(mark.url, new Set());
		verifiedClaimIndexes.get(mark.url)!.add(mark.claim_index);
	}

	const container = document.createElement('div');
	container.innerHTML = html;

	const occurrenceByUrl = new Map<string, number>();
	for (const anchor of container.querySelectorAll('a[href]')) {
		if (anchor.closest('td, th')) continue;

		const href = anchor.getAttribute('href') ?? '';
		const citation = urlToCitation.get(href);
		if (!citation) continue;

		const occurrence = occurrenceByUrl.get(href) ?? 0;
		occurrenceByUrl.set(href, occurrence + 1);

		const label = citationLabel(citation);
		anchor.setAttribute('class', 'citation-chip');
		anchor.setAttribute('target', '_blank');
		anchor.setAttribute('rel', 'noreferrer');
		anchor.textContent = '';
		if (verifiedClaimIndexes.get(href)?.has(occurrence)) {
			anchor.insertAdjacentHTML('afterbegin', checkCheckIconSVG);
			anchor.setAttribute('title', `${citation.title || href} — found in source`);
		} else {
			anchor.setAttribute('title', citation.title || href);
		}
		anchor.appendChild(document.createTextNode(label));
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
