// Mirrors gateway/protocol.go 1:1 — keep these in sync by hand, there's
// no shared codegen between the Go backend and this frontend.

export interface Citation {
	title: string;
	url: string;
}

export type ServerEvent =
	| { type: 'thinking'; thread_id?: string; content: string }
	| { type: 'reasoning'; thread_id?: string; content: string }
	| { type: 'tool_call'; thread_id?: string; tool: string; args?: Record<string, unknown> }
	| {
			type: 'tool_result';
			thread_id?: string;
			tool: string;
			result: string;
			citations?: Citation[];
	  }
	| { type: 'token'; thread_id?: string; content: string }
	// What the model said before deciding to call a tool (or before an
	// aborted attempt got discarded) — see gateway/protocol.go's doc
	// comment on this event type for the full rationale.
	| { type: 'commentary'; thread_id?: string; content: string }
	| { type: 'user_message'; thread_id: string; user_message_id: number }
	| {
			type: 'done';
			thread_id: string;
			cost_usd: number;
			citations?: Citation[];
			user_message_id?: number;
			context_tokens?: number;
			// Up to 3 follow-up questions for the answer that just finished,
			// persisted alongside it (see StoredMessage.suggestions) — still
			// there when this thread is reopened later.
			suggestions?: string[];
			// How long agent.Run took to produce this answer, in
			// milliseconds — see StoredMessage.duration_ms.
			duration_ms?: number;
	  }
	// The thread just crossed the context-window threshold and was
	// auto-summarized — content is the summary, shown as a collapsible
	// timeline note like a tool call, not a normal answer.
	| { type: 'compacted'; thread_id?: string; content: string }
	| { type: 'error'; thread_id?: string; message: string; user_message_id?: number };

// edit_from_id turns this into a retry/edit: the server deletes every
// message in the thread with id >= edit_from_id before treating content
// as the new user message at that point. stt_cost_usd carries a voice
// memo's transcription cost so it's folded into the thread total. Not
// set from the frontend yet: voice_mode nudges the model toward a brief,
// speakable answer — reserved for a future always-on voice session.
export type ClientMessage =
	| {
			type: 'message';
			thread_id?: string;
			content: string;
			model: string;
			edit_from_id?: number;
			voice_mode?: boolean;
			stt_cost_usd?: number;
			user_location?: string;
	  }
	// Cancels whatever turn is currently in flight on this connection — the
	// server only ever runs one turn at a time per socket, so this needs
	// no thread_id to target it.
	| { type: 'stop' };

export interface ModelOption {
	id: string;
	name: string;
	default: boolean;
}

export interface Thread {
	id: string;
	title: string;
	model: string;
	cost_usd: number;
	context_tokens: number;
	created_at: string;
	updated_at: string;
}

export interface StoredMessage {
	id: number;
	thread_id: string;
	role: string;
	content: string;
	citations: string; // JSON-encoded Citation[]
	suggestions: string; // JSON-encoded string[], assistant messages only
	cost_usd: number;
	// Shared by the user/assistant message pair from one turn, and by
	// every StoredEvent logged while that turn ran — the join key
	// openThread uses to regroup a past turn's timeline.
	turn_id: string;
	// How long agent.Run took to produce this answer, in milliseconds —
	// 0 for user messages, and briefly for a not-yet-finished assistant
	// message (see store.Store.SetMessageDuration).
	duration_ms: number;
	created_at: string;
}

// Mirrors store.Event 1:1 — one row from GET /api/threads/{id}/events.
export interface StoredEvent {
	id: number;
	thread_id?: string;
	level: string;
	source: string; // "turn" | "tool.<name>" | "compaction" | ...
	message: string;
	data: string; // JSON-encoded structured detail
	turn_id?: string;
	created_at: string;
}

export type TimelineItem =
	| { kind: 'thinking'; content: string }
	| { kind: 'reasoning'; content: string; done: boolean }
	| { kind: 'compacted'; summary: string }
	// What the model said before calling a tool — rendered as real
	// markdown prose (it's genuine assistant reply text, not private
	// reasoning), positioned in the timeline between the tool calls that
	// came before and after it. See ServerEvent's 'commentary' case.
	| { kind: 'commentary'; content: string }
	| {
			kind: 'tool';
			tool: string;
			args?: Record<string, unknown>;
			result?: string;
			citations?: Citation[];
			done: boolean;
	  };

export interface ChatTurn {
	role: 'user' | 'assistant';
	content: string;
	timeline?: TimelineItem[];
	citations?: Citation[];
	costUsd?: number;
	streaming?: boolean;
	// DB message id. Only ever set on 'user' turns — needed to retry/edit
	// from this point. Undefined until the server confirms it's persisted.
	id?: number;
	// How long agent.Run took to produce this answer, in milliseconds.
	// Assistant turns only, set once "done" arrives (or on reopening a
	// past thread, from the persisted message).
	durationMs?: number;
}
