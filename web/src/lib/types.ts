// Mirrors gateway/protocol.go 1:1 — keep these in sync by hand, there's
// no shared codegen between the Go backend and this frontend.

export interface Citation {
	title: string;
	url: string;
	site_name?: string;
	// General-purpose thumbnail (album art, a repo avatar, an article's
	// lead image, etc.) — see tools/registry.go's Citation.ImageURL doc
	// comment. Rendered by the source list in place of the numbered index
	// badge when present.
	image_url?: string;
}

// A structured rich-result item — see tools/registry.go's Card doc
// comment. General-purpose; music's recommendation carousel is the first
// user.
export interface Card {
	title: string;
	subtitle?: string;
	image_url?: string;
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
			cards?: Card[];
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
			cards?: Card[];
			user_message_id?: number;
			context_tokens?: number;
			// How long agent.Run took to produce this answer, in
			// milliseconds — see StoredMessage.duration_ms.
			duration_ms?: number;
	  }
	// Sent once, shortly after 'done' — up to 3 follow-up questions for the
	// answer that just finished, persisted alongside it (see
	// StoredMessage.suggestions) so they're still there when this thread is
	// reopened later. Deliberately its own event, not part of 'done': that
	// call runs after 'done' ships so the turn footer doesn't wait on it —
	// see gateway/protocol.go's doc comment. cost_usd here is just this
	// call's own cost, additive with 'done''s, not a replacement for it.
	| { type: 'suggestions'; thread_id: string; cost_usd: number; suggestions: string[] }
	// The thread just crossed the context-window threshold and was
	// auto-summarized — content is the summary, shown as a collapsible
	// timeline note like a tool call, not a normal answer.
	| { type: 'compacted'; thread_id?: string; content: string }
	// nearby_search or weather wants a live GPS fix for this turn and none
	// of the cheaper sources (query text, the cookie from last time) had
	// one — see gateway/protocol.go's doc comment. Reply with a
	// 'location_response' ClientMessage; there's no need to correlate a
	// request ID since the server only ever has one of these outstanding
	// per connection at a time.
	| { type: 'location_request'; thread_id?: string }
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
			// Set from the composer's "+" sheet — see agent/driver.go's
			// focusModeInstructions and Run's DeepResearch handling.
			focus_mode?: FocusMode;
			deep_research?: boolean;
			// Set when the composer's "+" sheet attached a file, already
			// uploaded via POST /api/upload before this message is sent —
			// see gateway/attachments.go's resolveAttachment.
			attachment_id?: string;
			attachment_filename?: string;
			attachment_content_type?: string;
	  }
	// Cancels whatever turn is currently in flight on this connection — the
	// server only ever runs one turn at a time per socket, so this needs
	// no thread_id to target it.
	| { type: 'stop' }
	// Reply to a 'location_request' ServerEvent. user_location is a fresh
	// "lat, lon" fix, or omitted/empty if the browser couldn't get one
	// (denied, unavailable, or the request timed out client-side) — either
	// way the server treats "no answer" as normal and falls back to its
	// own default, so this should be sent promptly rather than held back
	// waiting for a "good" answer.
	| { type: 'location_response'; user_location?: string };

// 'off' isn't a selectable mode in the sheet — it's just what focusMode
// resets to when the active mode is tapped again to turn it off.
export type FocusMode = 'off' | 'brief' | 'academic' | 'news' | 'first_principles' | 'socratic';

// The response from POST /api/upload (see gateway/attachments.go's
// UploadResponse) — id is what rides along in the next ClientMessage.
export interface UploadedAttachment {
	id: string;
	filename: string;
	content_type: string;
	size_bytes: number;
}

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
	favorite: boolean;
	created_at: string;
	updated_at: string;
}

// VariantGroup describes the alternatives available at one message
// position — every reply an edit/regenerate at that spot has ever
// produced (oldest first) plus which one is currently shown. Keyed by
// message array index in GetThread's response (see gateway/threads.go's
// buildVariantsMap); a position with no entry here has never been
// edited/regenerated, so ChatTurnView shows no switcher for it.
export interface VariantGroup {
	ids: string[];
	active: string;
}

export interface StoredMessage {
	id: number;
	thread_id: string;
	role: string;
	content: string;
	citations: string; // JSON-encoded Citation[]
	suggestions: string; // JSON-encoded string[], assistant messages only
	cards: string; // JSON-encoded Card[], assistant messages only — see store.Store.SetMessageCards
	cost_usd: number;
	// Shared by the user/assistant message pair from one turn, and by
	// every StoredEvent logged while that turn ran — the join key
	// openThread uses to regroup a past turn's timeline.
	turn_id: string;
	// How long agent.Run took to produce this answer, in milliseconds —
	// 0 for user messages, and briefly for a not-yet-finished assistant
	// message (see store.Store.SetMessageDuration).
	duration_ms: number;
	// Set only on a user message that carried an upload — see
	// store.Store.SetMessageAttachment. '' on every other message.
	attachment_filename?: string;
	attachment_content_type?: string;
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
	cards?: Card[];
	costUsd?: number;
	streaming?: boolean;
	// DB message id. Only ever set on 'user' turns — needed to retry/edit
	// from this point. Undefined until the server confirms it's persisted.
	id?: number;
	// How long agent.Run took to produce this answer, in milliseconds.
	// Assistant turns only, set once "done" arrives (or on reopening a
	// past thread, from the persisted message).
	durationMs?: number;
	// Set only on a user turn that carried an upload (see
	// StoredMessage.attachment_filename) — shown as a small chip above
	// the message text.
	attachmentFilename?: string;
	attachmentContentType?: string;
}
