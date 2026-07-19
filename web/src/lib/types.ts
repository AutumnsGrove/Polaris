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
	created_at: string;
}

export type TimelineItem =
	| { kind: 'thinking'; content: string }
	| { kind: 'reasoning'; content: string; done: boolean }
	| { kind: 'compacted'; summary: string }
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
}
