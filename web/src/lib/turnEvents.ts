import type { ChatTurn, ServerEvent } from './types';
import { toolResultFields } from './toolResultFields';

// Reasoning always finishes before the visible answer (or a tool call)
// starts, per OpenRouter's ordering guarantee — so whenever something
// else is about to land on the timeline, mark any still-open reasoning
// item done first, so its UI stops showing a live/streaming state.
export function closeOpenReasoning(turn: ChatTurn) {
	const items = turn.timeline;
	if (!items || items.length === 0) return;
	const last = items[items.length - 1];
	if (last.kind === 'reasoning' && !last.done) {
		last.done = true;
		turn.timeline = [...items];
	}
}

// Applies the streaming events that only ever touch the in-flight turn itself
// (timeline items, streamed text, per-turn cost/Oracle fields) — no thread, socket
// or pending-turn bookkeeping, which is why AppState.handleEvent keeps the
// 'compacted'/'done'/'error' cases that do. Pure over its arguments so it can be
// tested without constructing an AppState.
export function applyStreamingEvent(turn: ChatTurn, e: ServerEvent): void {
	switch (e.type) {
		case 'thinking':
			closeOpenReasoning(turn);
			turn.timeline = [...(turn.timeline ?? []), { kind: 'thinking', content: e.content }];
			break;

		case 'reasoning': {
			const items = turn.timeline ?? [];
			const last = items[items.length - 1];
			if (last && last.kind === 'reasoning' && !last.done) {
				// Still the same reasoning pass — append to it in place
				// rather than spawning a new timeline item per chunk.
				last.content += e.content;
				turn.timeline = [...items];
			} else {
				turn.timeline = [...items, { kind: 'reasoning', content: e.content, done: false }];
			}
			break;
		}

		case 'tool_call':
			closeOpenReasoning(turn);
			turn.timeline = [
				...(turn.timeline ?? []),
				{ kind: 'tool', tool: e.tool, args: e.args, callId: e.call_id, done: false }
			];
			break;

		case 'tool_result': {
			const items = [...(turn.timeline ?? [])];
			// Prefer an exact call_id match — the model can fire two
			// concurrent calls to the same tool in one turn (e.g. two
			// memory writes), and goroutine completion order isn't
			// guaranteed to match launch order, so a name-only backward
			// scan can attach a result to the wrong card (see
			// agent/driver.go's dispatchToolCallsConcurrently doc
			// comment). Fall back to the old name-based scan only when
			// call_id is missing, for backward compatibility with any
			// path that doesn't send one.
			let matched = false;
			if (e.call_id) {
				for (let i = items.length - 1; i >= 0; i--) {
					const item = items[i];
					if (item.kind === 'tool' && item.callId === e.call_id && !item.done) {
						items[i] = { ...item, ...toolResultFields(e) };
						matched = true;
						break;
					}
				}
			}
			if (!matched) {
				for (let i = items.length - 1; i >= 0; i--) {
					const item = items[i];
					if (item.kind === 'tool' && item.tool === e.tool && !item.done) {
						items[i] = { ...item, ...toolResultFields(e) };
						break;
					}
				}
			}
			turn.timeline = items;
			break;
		}

		case 'cost_update':
			// Always the full running total, never a delta (see
			// gateway/protocol.go's doc comment) — overwrite, don't add.
			// appState.totalCost is untouched here on purpose: it only
			// moves on 'done'/'suggestions', which already add their
			// own cost_usd to it once, so adding this too would double-count.
			turn.costUsd = e.cost_usd;
			break;

		case 'oracle':
			// Oracle's verdict, arriving the moment it resolves — well
			// before 'done' (see gateway/protocol.go's "oracle" doc
			// comment). Writes the same fields the 'done' case below
			// writes again later (a harmless same-value overwrite), so
			// the composer's focus badge and reading ring can react now,
			// seconds before the answer starts streaming; oracleResolved
			// is the live-only signal those two key off. costOracle is
			// not added to any running total here — 'done' owns the
			// thread-wide cost bookkeeping.
			turn.oracleResult = e.oracle_result;
			turn.oracleFocusModeSource = e.oracle_focus_mode_source;
			turn.appliedFocusMode = e.applied_focus_mode;
			turn.costOracle = e.cost_oracle_usd;
			turn.oracleResolved = true;
			break;

		case 'commentary':
			closeOpenReasoning(turn);
			// Whatever just streamed in live via 'token' for this turn
			// was this commentary, not the final answer — the server
			// sends the same text again here as the authoritative
			// version once it knows that for certain (see
			// gateway/protocol.go's doc comment on this event). Drop
			// the flat accumulation and show it as its own timeline
			// item instead, positioned exactly where it happened
			// relative to the tool calls before/after it, rather than
			// letting it silently pile into the real final answer.
			turn.content = '';
			turn.timeline = [...(turn.timeline ?? []), { kind: 'commentary', content: e.content }];
			break;

		case 'token':
			closeOpenReasoning(turn);
			// e.content can be absent (not just empty) — ServerEvent's
			// omitempty JSON tag drops the field entirely for an empty
			// string, so a plain `turn.content += e.content` would
			// string-concatenate the literal text "undefined" here.
			turn.content += e.content ?? '';
			break;
	}
}
