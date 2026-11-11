import DOMPurify from 'dompurify';
import { marked } from '$lib/markdown';
import { renderInlineCitations } from '$lib/citations';
import type { Citation, VerificationMark } from '$lib/types';
import { splitContent, type FenceKind } from './split';

export type RenderedSegment =
	| { kind: 'md'; html: string }
	// `fence` is a ui fence's ordinal among the answer's ui fences (the first
	// segment of a block's verification locator, see uiblocks/sites.go).
	| { kind: FenceKind; src: string; closed: boolean; fence?: number };

// Fence kinds that get their own component; anything else stays Markdown.
const SEGMENT_KINDS: readonly FenceKind[] = ['ui', 'mermaid'];

// marked.parse + DOMPurify is the expensive, citation-independent part of
// rendering a Markdown segment. While a reply streams only the LAST segment's
// text changes, so caching by text means every finished segment is a lookup,
// not a re-parse — a side benefit of splitting the answer up. Bounded because
// a long session streams many distinct partial strings through here.
const sanitizedCache = new Map<string, string>();
const CACHE_LIMIT = 64;

function sanitizedHtml(text: string): string {
	const hit = sanitizedCache.get(text);
	if (hit !== undefined) return hit;
	// Content can originate from fetched web pages (via web_read) as well as
	// the model itself, so it is sanitized like any other untrusted input.
	const html = DOMPurify.sanitize(marked.parse(text) as string);
	if (sanitizedCache.size >= CACHE_LIMIT) {
		// Map iterates in insertion order: drop the oldest entry.
		sanitizedCache.delete(sanitizedCache.keys().next().value as string);
	}
	sanitizedCache.set(text, html);
	return html;
}

/**
 * Splits an answer around its mermaid/ui fences and renders each Markdown
 * piece through the existing marked -> DOMPurify -> renderInlineCitations
 * pipeline. One occurrence counter is threaded through the pieces in order so
 * "found in source" ticks still land on the right chip (see
 * renderInlineCitations).
 */
export function renderAnswer(
	content: string,
	streaming: boolean,
	citations: Citation[],
	verification?: VerificationMark[]
): RenderedSegment[] {
	const occurrences = new Map<string, number>();
	let uiFences = 0;
	return splitContent(content, streaming, SEGMENT_KINDS).map((seg): RenderedSegment => {
		// Every ui fence renders, however many an answer carries: a stress-test
		// answer showing off the whole block catalog is legitimate, and silently
		// dumping the overflow as JSON read as a rendering failure. The parser's
		// per-fence line cap and per-field character cap bound each fence, and the
		// model's own output length bounds the count.
		if (seg.kind === 'ui') return { ...seg, fence: uiFences++ };
		if (seg.kind !== 'md') return seg;
		return {
			kind: 'md',
			html: renderInlineCitations(sanitizedHtml(seg.text), citations, verification, occurrences)
		};
	});
}
