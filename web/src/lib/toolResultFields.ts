import type { Card, Citation, MapPayload } from './types';

/** The payload fields a finished tool call can attach to its timeline card. */
export interface ToolResultSource {
	result?: string;
	provider?: string;
	citations?: Citation[];
	url?: string;
	caption?: string;
	images?: Card[];
	map?: MapPayload;
}

/**
 * What to spread onto a timeline tool item when its result arrives. One place
 * for this list on purpose: it used to be copied by hand into four spots (live
 * and persisted-replay, each with a call_id match and a name-fallback match), so
 * a new payload field added to only some of them rendered fine live and silently
 * vanished on reload — the exact gap `show` hit. Add a field here, not there.
 */
export function toolResultFields(src: ToolResultSource) {
	return {
		result: src.result,
		provider: src.provider,
		citations: src.citations,
		url: src.url,
		caption: src.caption,
		images: src.images,
		map: src.map,
		done: true as const
	};
}
