import type {
	Card,
	ChartSpec,
	ChatTurn,
	Citation,
	MessageAttachment,
	OracleResult,
	PendingQuestion,
	StoredEvent,
	VerificationMark
} from './types';
import { applyVerification, buildTimelineFromEvents, safeParseJSON, safeParseObject } from './stateHelpers';

// Shared by openThread and swapVariant — both end up with the exact
// same GetThread response shape (see gateway/threads.go's
// handleGetThread/handleSwapVariant) and need to turn it into the same
// ChatTurn[]/suggestions/variants state, just triggered differently
// (navigating to a thread vs. browsing to a different reply within
// the one already open).
export async function fetchEventsByTurn(id: string): Promise<Map<string, StoredEvent[]>> {
	const eventsByTurn = new Map<string, StoredEvent[]>();
	const eventsRes = await fetch(`/api/threads/${id}/events`);
	if (eventsRes.ok) {
		const events: StoredEvent[] = (await eventsRes.json()) ?? [];
		for (const evt of events) {
			if (!evt.turn_id) continue;
			const group = eventsByTurn.get(evt.turn_id);
			if (group) group.push(evt);
			else eventsByTurn.set(evt.turn_id, [evt]);
		}
	}
	return eventsByTurn;
}

export function buildTurnsFromMessages(
	messages: any[],
	eventsByTurn: Map<string, StoredEvent[]>,
	threadId: string | null
): ChatTurn[] {
	return messages.map((m: any) => {
		const verification = m.verification ? safeParseJSON<VerificationMark>(m.verification) : undefined;
		return {
			role: m.role,
			content: m.content,
			citations: applyVerification(safeParseJSON<Citation>(m.citations), verification),
			verification,
			cards: safeParseJSON<Card>(m.cards),
			chart: m.chart ? (safeParseObject(m.chart) as unknown as ChartSpec) : undefined,
			pendingQuestion: m.pending_question ? (safeParseObject(m.pending_question) as PendingQuestion) : undefined,
			costUsd: m.cost_usd,
			durationMs: m.duration_ms || undefined,
			// Oracle mode — oracle_result is JSON-encoded gateway.OracleResult
			// (see store.Message.OracleResult), same double-encoded shape as
			// chart/pending_question above. "" for Oracle off/unconfigured.
			oracleResult: m.oracle_result
				? (safeParseObject(m.oracle_result) as unknown as OracleResult)
				: undefined,
			oracleFocusModeSource: m.focus_mode_source || undefined,
			costAnswer: m.cost_answer_usd,
			costVerification: m.cost_verification_usd,
			costOracle: m.cost_oracle_usd,
			promptTokens: m.prompt_tokens || undefined,
			cacheReadTokens: m.cache_read_tokens || undefined,
			completionTokens: m.completion_tokens || undefined,
			toolCallCount: m.tool_call_count || undefined,
			ttftMs: m.ttft_ms || undefined,
			tokensPerSecond: m.tokens_per_second || undefined,
			appliedFocusMode: m.applied_focus_mode,
			appliedModel: m.applied_model,
			// Both roles now carry their real DB id — see ChatTurn.id's doc
			// comment (assistant turns need it too, for read-aloud's
			// persisted-audio attachment; this used to be user-only before
			// that existed).
			id: m.id,
			ttsAudioFile: m.tts_audio_file_id
				? `/api/workspace/${threadId}/${m.tts_audio_file_id}`
				: undefined,
			attachments: safeParseJSON<MessageAttachment>(m.attachments),
			timeline:
				m.role === 'assistant' && m.turn_id && eventsByTurn.has(m.turn_id)
					? buildTimelineFromEvents(eventsByTurn.get(m.turn_id)!)
					: undefined
		};
	});
}
