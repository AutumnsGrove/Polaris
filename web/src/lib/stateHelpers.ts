import type { Citation, StoredEvent, TimelineItem, VerificationMark } from './types';

export function safeParseJSON<T>(json: string): T[] {
	try {
		return JSON.parse(json) ?? [];
	} catch {
		return [];
	}
}

export function safeParseObject(json: string): Record<string, any> {
	try {
		return JSON.parse(json) ?? {};
	} catch {
		return {};
	}
}

// Sets citations[].verified (the aggregate "found in source" mark the
// source-list chip uses) from marks — true for any citation whose URL has
// at least one supported claim. citations is left untouched (same array
// reference) when marks is empty, so callers that always run this don't
// force an unnecessary re-render. See ChatTurn.verification's doc comment
// for why claim_index-level precision lives separately, for the inline
// chip.
export function applyVerification(citations: Citation[] | undefined, marks: VerificationMark[] | undefined): Citation[] | undefined {
	if (!citations || !marks || marks.length === 0) return citations;
	const verifiedUrls = new Set(marks.filter((m) => m.choice === 'supported').map((m) => m.url));
	if (verifiedUrls.size === 0) return citations;
	return citations.map((c) => (verifiedUrls.has(c.url) ? { ...c, verified: true } : c));
}

// TEMPORARY instrumentation for chasing the "thread bump-back" bug (see
// memory: field_thread_bump_back_root_cause) — fires a fire-and-forget
// beacon to the server's event log at the handful of places
// currentThreadId changes or a version-mismatch reload fires, so the next
// occurrence can be read back from the events table afterward instead of
// needing the user to have DevTools open at the exact moment it happens.
// keepalive (not navigator.sendBeacon) is what survives the page unloading
// (the exact moment a reload/href navigation fires) here — sendBeacon
// would do the same in a real browser, but its rejection can't be caught
// the way a plain fetch promise's can, which surfaced as unhandled
// rejections under happy-dom's polyfill. Remove this and its call sites
// once the mechanism is confirmed and fixed.
export function debugBeacon(message: string, data: Record<string, unknown> = {}) {
	if (typeof fetch === 'undefined') return;
	fetch('/api/debug-log', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		keepalive: true,
		body: JSON.stringify({ message, data })
	}).catch(() => {
		// Best-effort — never let diagnostics themselves break the app.
	});
}

// Rebuilds one turn's timeline from its persisted events (thinking steps,
// tool call start/finish pairs, compaction) — the same shape handleEvent
// builds live while a turn streams, so a reopened thread renders
// identically to one that's still on screen. events must be this turn's
// slice only, oldest-first (see ListEvents' ORDER BY id ASC).
export function buildTimelineFromEvents(events: StoredEvent[]): TimelineItem[] {
	const timeline: TimelineItem[] = [];
	for (const evt of events) {
		const data = safeParseObject(evt.data);
		if (evt.source === 'turn' && evt.message === 'thinking') {
			timeline.push({ kind: 'thinking', content: data.content ?? '' });
		} else if (evt.source === 'turn' && evt.message === 'commentary') {
			timeline.push({ kind: 'commentary', content: data.content ?? '' });
		} else if (evt.source === 'turn' && evt.message === 'reasoning') {
			// Persisted as one row per burst (see gateway/turn.go's
			// flushReasoning), already complete — done: true, unlike the
			// live-streaming case where a burst starts as done: false and
			// gets closed out by closeOpenReasoning once something else
			// interrupts it.
			timeline.push({
				kind: 'reasoning',
				content: data.content ?? '',
				done: true,
				durationMs: typeof data.duration_ms === 'number' ? data.duration_ms : undefined
			});
		} else if (evt.source === 'compaction' && evt.message === 'compaction notice shown') {
			// 'compaction notice shown', not 'thread auto-compacted' — the
			// latter is the backend's untagged audit row (it feeds the
			// Auto-compactions stat in store/stats.go) and is written with an
			// empty turn_id, so it never lands in a turn's event slice and
			// never reaches here. The rendered note is the row the *next*
			// turn writes when it actually shows the notice, which is why
			// this reads the summary off a different message than the live
			// 'compacted' ServerEvent's name suggests. No cost is applied
			// here: the compaction's cost is already inside the thread's
			// stored cost_usd, which openThread assigns to totalCost
			// wholesale — adding this row's cost_usd too would double-count
			// it on every reload.
			timeline.push({ kind: 'compacted', summary: data.summary ?? '' });
		} else if (evt.source.startsWith('tool.')) {
			const tool = evt.source.slice('tool.'.length);
			if (evt.message === 'tool call started') {
				timeline.push({ kind: 'tool', tool, args: data.args, callId: data.call_id, done: false });
			} else if (evt.message === 'tool call finished') {
				// Same call_id-first matching as handleEvent's live
				// 'tool_result' case (see its doc comment) — persisted
				// events from two concurrent same-tool calls (e.g. two
				// memory writes) are just as ambiguous to a name-only
				// backward scan as the live stream is, so a reopened
				// thread needs the same fix or the cross-wired-card bug
				// just reappears on reload.
				let matched = false;
				if (data.call_id) {
					for (let i = timeline.length - 1; i >= 0; i--) {
						const item = timeline[i];
						if (item.kind === 'tool' && item.callId === data.call_id && !item.done) {
							timeline[i] = {
								...item,
								result: data.result,
								citations: data.citations,
								url: data.url,
								caption: data.caption,
								images: data.images,
								done: true
							};
							matched = true;
							break;
						}
					}
				}
				if (!matched) {
					for (let i = timeline.length - 1; i >= 0; i--) {
						const item = timeline[i];
						if (item.kind === 'tool' && item.tool === tool && !item.done) {
							timeline[i] = {
								...item,
								result: data.result,
								citations: data.citations,
								url: data.url,
								caption: data.caption,
								images: data.images,
								done: true
							};
							break;
						}
					}
				}
			}
		}
	}
	return timeline;
}
