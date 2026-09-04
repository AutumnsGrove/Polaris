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
	// Selects which frontend treatment renders this card — omitted means
	// "media" (RecommendationsCarousel, today's behavior). image_search is
	// the only "image" producer — see ImageGallery.svelte.
	kind?: 'image';
	// A higher-resolution image than image_url's deliberately small
	// thumbnail, for ImageGallery's lightbox to use instead of upscaling
	// the thumbnail — set only by image_search. Falls back to image_url
	// when absent.
	full_image_url?: string;
}

// A structured chart — see tools/registry.go's ChartSpec doc comment.
// At most one per turn, unlike Card above.
export interface ChartSpec {
	// "range" is Tier-1-only — never offered to the model via visualize's
	// kind enum (see tools/visualize.go), set exclusively by weather.go's
	// setWeatherChart for a daily high/low forecast. See ChartCard.svelte
	// for why a plain two-line chart was replaced with this for weather
	// specifically: no hover/tooltip in a static SVG made the compressed
	// axis hard to read at a glance.
	kind: 'line' | 'bar' | 'timeline' | 'meter' | 'range';
	title: string;
	x_label?: string;
	y_label?: string;
	series?: ChartSeries[]; // line, bar, range
	events?: ChartEvent[]; // timeline
	value?: ChartValue; // meter
	// "range"'s own field — one icon key per row, same order/count as
	// series[0]'s points. A fixed, small vocabulary set by weather.go's
	// weatherCodeIcon (see registry.go's Icons doc comment) — ChartCard.
	// svelte's iconFor maps each key to a Lucide icon component.
	icons?: string[]; // range
}

export interface ChartSeries {
	label: string;
	points: ChartPoint[];
}

export interface ChartPoint {
	x: string | number;
	y: number;
}

export interface ChartEvent {
	date: string;
	label: string;
}

export interface ChartValue {
	current: number;
	min: number;
	max: number;
	label: string;
}

// A clarifying question the model asked instead of answering, ending its
// turn — see tools/registry.go's PendingQuestion doc comment. Answering
// it is just sending the next ordinary chat message, not a dedicated
// response frame.
export interface PendingQuestion {
	question: string;
	options?: string[];
	wants_location?: boolean;
	// Set when chat mode (Research off) is active and the model wants to
	// ask whether to turn research back on for this — shows an "enable web
	// search" action alongside the text input, same shape as
	// wants_location's "share my location". See tools/registry.go's
	// PendingQuestion.WantsWebSearch.
	wants_web_search?: boolean;
	// Set only for a Tier 2 Deep Research plan-confirmation question — see
	// tools/registry.go's PendingQuestion.Plan. The plan's content is also
	// in `question` itself, so this is a rendering enhancement, not the
	// only place the plan appears.
	plan?: ResearchPlan;
}

// See tools/registry.go's ResearchPlan.
export interface ResearchPlan {
	sub_agent_objectives: string[];
	estimated_search_calls?: number;
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
			// web_search's normalized fallback-source key ("searxng" /
			// "brave" / "parallel" / "tavily") — see gateway/protocol.go's
			// ServerEvent.Provider doc comment. Absent for every other tool.
			provider?: string;
			citations?: Citation[];
			cards?: Card[];
			chart?: ChartSpec;
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
			chart?: ChartSpec;
			user_message_id?: number;
			context_tokens?: number;
			// How long agent.Run took to produce this answer, in
			// milliseconds — see StoredMessage.duration_ms.
			duration_ms?: number;
			// Set when this turn ended with ask_user_question instead of a
			// normal finished answer — see PendingQuestion above.
			pending_question?: PendingQuestion;
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
			// True when the composer's "Research" toggle is off (chat
			// mode) for this turn — see tools.Context.NoResearch. Also set
			// explicitly false by AskUserQuestionCard's "enable web
			// search" action, overriding the composer's current toggle
			// state for that one follow-up reply.
			no_research?: boolean;
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
export type FocusMode =
	| 'off'
	| 'brief'
	| 'academic'
	| 'news'
	| 'first_principles'
	| 'socratic'
	| 'researcher'
	| 'safari';

// The response from POST /api/upload (see gateway/attachments.go's
// UploadResponse) — id is what rides along in the next ClientMessage.
export interface UploadedAttachment {
	id: string;
	filename: string;
	content_type: string;
	size_bytes: number;
}

// Mirrors tools.ToolInfo (tools/catalog.go) — one individually toggleable
// tool's identity for the settings panel's Tools section.
export interface ToggleableTool {
	name: string;
	description: string;
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
	// focus_mode/deep_research are this thread's sticky turn config,
	// alongside model above — read back into the composer on open (see
	// ChatView.svelte's thread-open effect), written through on every
	// change (see AppState.persistThreadConfig).
	focus_mode: FocusMode;
	deep_research: boolean;
	// pulsar_routine_id is set only on a pulse (source "pulsar") —
	// undefined for every other thread. Drives ChatView.svelte's "back to
	// routine" header affordance on a pulse's thread view.
	pulsar_routine_id?: number;
	created_at: string;
	updated_at: string;
}

// PulsarRoutine mirrors store.PulsarRoutine's JSON shape — see
// gateway/pulsar_routes.go. last_run_at/archived_at are null (not just
// absent) rather than undefined, since the Go side always includes the
// key with an explicit null for an unset *time.Time.
export interface PulsarRoutine {
	id: number;
	name: string;
	prompt: string;
	model: string;
	focus_mode: FocusMode;
	deep_research: boolean;
	schedule_type: 'daily' | 'weekly' | 'monthly';
	schedule_params: string;
	time_of_day: string;
	created_at: string;
	last_run_at: string | null;
	archived_at: string | null;
}

// PulsarPulse mirrors gateway/pulsar_routes.go's handleListPulsarPulses
// response — store.PulsarPulseSummary's fields plus in_progress, computed
// server-side from Server.IsTurnInFlight (in-memory state, not a DB
// column) since a pulse has no live WebSocket connection for the
// frontend to otherwise tell "still running" apart from "crashed".
export interface PulsarPulse {
	thread_id: string;
	title: string;
	seen: boolean;
	created_at: string;
	in_progress: boolean;
}

// WizardFinal mirrors tools.WizardFinal — the drafted prompt
// finalize_pulsar_prompt handed back, ending a wizard turn the same way
// PendingQuestion ends an ordinary one. See gateway/pulsar_wizard.go.
export interface WizardFinal {
	prompt: string;
	name?: string;
}

// WizardResponse mirrors gateway/pulsar_wizard.go's wizardResponse —
// exactly one of question/final/answer is set per turn. answer is the
// fallback for a plain-prose reply with no tool call (see its Go doc
// comment for why that's handled rather than dropped).
export interface WizardResponse {
	session_id: string;
	question?: PendingQuestion;
	final?: WizardFinal;
	answer?: string;
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
	// JSON-encoded ChartSpec, assistant messages only — see
	// store.Store.SetMessageChart. '' on every other message.
	chart?: string;
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
	// JSON-encoded PendingQuestion, set only on an assistant message that
	// ended its turn via ask_user_question — see
	// store.Store.SetMessagePendingQuestion. '' on every other message.
	pending_question?: string;
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
			// web_search's fallback-source key, see ServerEvent's tool_result case.
			provider?: string;
			citations?: Citation[];
			done: boolean;
	  };

export interface ChatTurn {
	role: 'user' | 'assistant';
	content: string;
	timeline?: TimelineItem[];
	citations?: Citation[];
	cards?: Card[];
	chart?: ChartSpec;
	// A clarifying question this turn ended with instead of a normal
	// finished answer — see PendingQuestion above. Rendered as an
	// interactive card only when this is the thread's current last turn
	// (see AskUserQuestionCard.svelte); any later message already implies
	// it's resolved.
	pendingQuestion?: PendingQuestion;
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

// Mirrors search/domain_rankings.go's RankState constants.
export type RankState = 'block' | 'lower' | 'default' | 'raise' | 'pin' | '';

// Mirrors search/searxng.go's SearchResult/SearchResponse — Atlas's own
// wire format, distinct from the chat protocol above.
export interface SearchResult {
	title: string;
	url: string;
	content: string;
	score: number;
	thumbnail?: string;
	engine?: string;
	engines?: string[];
	rank_state?: RankState;
	pinned?: boolean;
}

export interface SearchResponse {
	query: string;
	answer?: string;
	results: SearchResult[];
}

// Mirrors store.SearchHistoryEntry — Atlas's sidebar list.
export interface SearchHistoryEntry {
	id: number;
	query: string;
	favorite: boolean;
	created_at: string;
	updated_at: string;
}

// Mirrors store.MessageSearchResult — one matching message from
// GET /api/threads/search, backing the sidebar's chat-thread search box.
// snippet wraps each matched term in \x02...\x03 (ASCII STX/ETX) rather
// than HTML — see Sidebar.svelte's renderSnippet, which splits on these
// markers into real text nodes instead of using {@html}.
export interface MessageSearchResult {
	thread_id: string;
	thread_title: string;
	role: string;
	snippet: string;
	created_at: string;
}
