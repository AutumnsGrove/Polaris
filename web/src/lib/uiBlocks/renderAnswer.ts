import DOMPurify from 'dompurify';
import { marked } from '$lib/markdown';
import { renderInlineCitations } from '$lib/citations';
import type { Citation, VerificationMark } from '$lib/types';
import { splitContent, type FenceKind } from './split';

export type RenderedSegment =
	| { kind: 'md'; html: string }
	// `fence` is a ui fence's ordinal among the answer's ui fences (the first
	// segment of a block's verification locator, see gateway/uiblocks/sites.go).
	| { kind: FenceKind; src: string; closed: boolean; fence?: number };

// Fence kinds that get their own component; anything else stays Markdown.
const SEGMENT_KINDS: readonly FenceKind[] = ['ui', 'mermaid'];

// docs/plans/intelligent-ui.md "Caps": fences past this render as an ordinary
// code block instead, so a runaway answer can't mount unbounded components.
export const MAX_UI_FENCES = 8;

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
		if (seg.kind === 'ui' && ++uiFences > MAX_UI_FENCES) {
			return { kind: 'md', html: sanitizedHtml('```ui\n' + seg.src + (seg.closed ? '```\n' : '')) };
		}
		if (seg.kind === 'ui') return { ...seg, fence: uiFences - 1 };
		if (seg.kind !== 'md') return seg;
		return {
			kind: 'md',
			html: renderInlineCitations(sanitizedHtml(seg.text), citations, verification, occurrences)
		};
	});
}
