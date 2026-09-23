// protocol.go defines the WebSocket message shapes exchanged between
// the SvelteKit frontend and this backend. Kept as plain structs (not
// hidden behind a client SDK) so the frontend's TypeScript types can
// mirror this file 1:1.
package gateway

import "polaris/tools"

// AttachmentRef identifies one uploaded file riding along with a turn —
// shared wire shape between ClientMessage.Attachments and
// AskRequest.Attachments (see ask.go), since both describe the same
// upload-then-reference handoff from POST /api/upload.
type AttachmentRef struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// ClientMessage is sent by the browser over /ws to start (or continue) a turn.
// ThreadID empty means "start a new thread".
//
// EditFromID turns this into a retry/edit instead of a fresh message: the
// server deletes every message in the thread with id >= EditFromID (the
// original user message plus its answer and anything after) before
// treating Content as the new user message at that point. Retry re-sends
// the original content unchanged; editing sends the revised text.
type ClientMessage struct {
	// Type is "message" for a normal turn, "stop" to cancel one in flight,
	// or "location_response" replying to a server-initiated
	// "location_request" (see ServerEvent's doc comment below) — only
	// UserLocation is read for that last one, empty meaning denied/
	// unavailable/timed out rather than "field omitted".
	Type       string `json:"type"`
	ThreadID   string `json:"thread_id,omitempty"`
	Content    string `json:"content"`
	Model      string `json:"model"` // config.ModelConfig.ID
	EditFromID int64  `json:"edit_from_id,omitempty"`
	// VoiceMode, when true, tells the driver this answer is likely to be
	// read aloud — nudges the model toward a brief, speakable answer
	// instead of a long markdown-formatted one. Set by Transponder (the
	// full-screen call UI, web/src/lib/components/Transponder.svelte) for
	// every turn made during a call. Ordinary read-aloud on a normal chat
	// turn is a separate, per-message opt-in that doesn't set this.
	VoiceMode bool `json:"voice_mode,omitempty"`
	// SttCostUSD carries the transcription cost from a push-to-talk memo
	// (already billed via /api/transcribe) so it gets folded into the
	// thread's running total instead of being tracked nowhere.
	SttCostUSD float64 `json:"stt_cost_usd,omitempty"`
	// Source tags a brand-new thread's origin (see store.Thread.Source) —
	// empty means "web", the normal chat UI. Only read on thread creation;
	// ignored on every later turn in the same thread. Populated by
	// handleAsk for API-originated threads; the WebSocket client otherwise
	// never sets this, with one deliberate exception — Pulsar Daily's
	// expand-to-chat sends "pulsar-daily" here so those threads are
	// distinguishable from ordinary typed messages (still shown in the
	// normal Assistant sidebar, unlike source = "pulsar" pulses — see
	// store.go's ListThreads filter).
	Source string `json:"source,omitempty"`
	// TitleSeed, when set on a brand-new thread, is what generateTitle
	// summarizes instead of msg.Content — still a real LLM-generated
	// title, just fed cleaner input. Pulsar Daily's expand-to-chat sets
	// this to the tapped block's own title/content, because its seeded
	// Content is a synthetic instruction wrapper ("The user tapped an
	// expand affordance on a block titled X with this content: Y. Tell
	// me more about what's shown in this image...") — generateTitle,
	// given only that, was observed live hallucinating a title that reads
	// like an answer to the wrapper's embedded instruction rather than an
	// actual title (e.g. "I need to see the actual image to describe it"
	// for a Picture of the Day expansion) — text matching nothing in the
	// real conversation. TitleSeed sidesteps this by giving the title
	// model the underlying subject directly, without the "tell me more"/
	// "the user tapped" framing that caused the confusion. Also used as
	// the initial truncated placeholder (before generation completes),
	// for the same reason.
	TitleSeed string `json:"title_seed,omitempty"`
	// UserLocation is "lat, lon" from the browser's Geolocation API. On a
	// "message" frame it's whatever fix the browser had cached client-side
	// last (see web/src/lib/geolocation.ts) — a fallback of last resort,
	// used as nearby_search/weather's location only if neither the user's
	// message, the model's tool call, nor a live "location_request" round
	// trip (see ServerEvent's doc comment, and tools.Context.RequestLocation)
	// produced one. On a "location_response" frame it's that round trip's
	// actual answer instead: a fresh fix if the browser got one, empty if
	// denied/unavailable/the user's tab was backgrounded. Empty on either
	// frame just means "nothing usable" — never a hard error.
	UserLocation string `json:"user_location,omitempty"`
	// FocusMode is set from the composer's "+" menu (see
	// web/src/lib/components/ComposerMenu.svelte) — one of
	// agent.FocusMode's values, or empty for normal behavior. Shapes the
	// system prompt for this turn only; see agent.focusModeInstruction.
	FocusMode string `json:"focus_mode,omitempty"`
	// DeepResearch, when true, raises this turn's research budget and
	// check-in leniency — see agent.Run's maxTurns/researchCheckInInterval
	// handling.
	DeepResearch bool `json:"deep_research,omitempty"`
	// NoResearch is the composer's "Research" toggle switched off — chat
	// mode for this turn. See tools.Context.NoResearch's doc comment for
	// what it actually does (strip research-tagged tools, append the
	// no_research_instruction prompt fragment). Independent of DeepResearch
	// above; both default to false (research on, normal depth), the safe
	// zero value for a caller (POST /api/ask, cmd/search.go) that never
	// sets either.
	NoResearch bool `json:"no_research,omitempty"`
	// QuickMode mirrors tools.Context.QuickMode — set by Atlas's Quick
	// Answer via POST /api/ask, never by the WebSocket chat client.
	QuickMode bool `json:"quick_mode,omitempty"`
	// Attachments describes zero or more files uploaded via POST
	// /api/upload ahead of this message (see gateway/attachments.go) —
	// same two-step shape as push-to-talk voice memos. Each ID is the
	// opaque name handleUpload saved that file under
	// (config.Attachments.Dir/<id>); Filename/ContentType are only for
	// display and content-type dispatch, both already known to the
	// frontend from the upload response, so the server doesn't need a
	// side table to look them back up. An empty (or nil) slice means no
	// attachment on this message.
	Attachments []AttachmentRef `json:"attachments,omitempty"`
	// PulsarRoutineID/PulsarRoutineName are set only by the scheduler
	// firing a pulse (see pulsar_scheduler.go's firePulse) — never by any
	// JSON-decoded request. A non-zero PulsarRoutineID makes handleTurn
	// link the new thread to its routine (threads.pulsar_routine_id) and
	// use a date-aware title instead of the normal LLM-generated one, per
	// docs/plans/pulsar-routines.md's "Pulse execution model".
	PulsarRoutineID   int64  `json:"-"`
	PulsarRoutineName string `json:"-"`
	// PulsarPreviousReport/PulsarPreviousReportAt carry the routine's last
	// completed pulse's answer (see store.Store.LatestPulseReport) so this
	// pulse's turn can be told what it already reported and skip repeating
	// it — the fix for a recurring routine otherwise restating the same
	// still-true information every single run with no memory of its own
	// prior output. Empty when this is the routine's first-ever pulse (or
	// every prior one failed before producing an answer). Also
	// scheduler-only, same as the PulsarRoutineID/Name pair above.
	PulsarPreviousReport   string `json:"-"`
	PulsarPreviousReportAt string `json:"-"`
	// Anonymous is the composer's ghost-mode toggle (issue #67) — set only
	// on a brand-new thread's first message (ghost mode is new-thread-only,
	// never flipped mid-conversation). When true, handleTurn skips every
	// store.Store write for this turn (no thread/message/cost/event rows —
	// see turn.go's Anonymous branches), which also means the turn never
	// becomes visible to Constellation's Weaver or search_chats, since both
	// only ever read persisted content. It additionally suppresses the
	// memory tool and the {custom_instructions} prompt placeholder — see
	// turn.go's tools.Context construction.
	Anonymous bool `json:"anonymous,omitempty"`
	// History is a ghost thread's own running transcript, held client-side
	// and replayed on every turn — the server has no persisted row to
	// reconstruct it from the way loadHistory normally does, since nothing
	// about a ghost thread is ever written to store.Store. Only meaningful
	// when Anonymous is true. Same {role, content} shape loadHistory itself
	// produces (tool calls were never part of persisted/replayed history
	// either, so this is exact parity, not a reduced approximation).
	History []GhostTurn `json:"history,omitempty"`
	// WaitVerification, when true, runs the per-claim "found in source"
	// pass (see gateway/verification.go) synchronously before handleTurn
	// returns instead of in its normal detached post-"done" goroutine, and
	// AskResponse.Verification (ask.go) carries the full result including
	// confidence — a debug/stress-testing knob for exercising verification
	// without needing the WebSocket client's async event round trip. Only
	// ever set by handleAsk from AskRequest.WaitVerification; the
	// WebSocket client never sets this — a real chat turn always wants the
	// non-blocking async path so the answer never stalls behind it.
	WaitVerification bool `json:"-"`
	// FullTurnHistoryOverride, when non-nil, replaces
	// FullTurnHistoryFromStore(s.db) for this turn only — same "debug knob,
	// API-only, WebSocket client never sets this" shape as WaitVerification
	// above. Without it, exercising the full_turn_history setting via
	// /api/ask meant actually flipping the operator's real settings row
	// first (affecting the live chat client too) and remembering to flip
	// it back after. Only ever set by handleAsk/handleAskStream from
	// AskRequest.FullTurnHistory.
	FullTurnHistoryOverride *bool `json:"-"`
}

// GhostTurn is one prior turn of a ghost (Anonymous) thread's client-held
// transcript — see ClientMessage.History.
type GhostTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ServerEvent is one streamed update. Type drives how the frontend
// renders it:
//
//	"thinking"     — content: a think-tool thought, shown as a collapsible reasoning step
//	"reasoning"     — content: one chunk of a reasoning-capable model's native "thinking" stream
//	                  (DeepSeek/MiMo-style), appended live — distinct from the think tool, which
//	                  the model calls explicitly; this is the model's own hidden reasoning pass
//	"tool_call"     — tool + args: a search/read call just started
//	"tool_result"   — tool + result + citations: that call finished
//	"token"         — content: one chunk of the final answer, appended live
//	"cost_update"   — cost_usd: the turn's running spend so far, fired after each LLM completion
//	                  and each tool-dispatch batch inside agent.Run (see its own doc comment on
//	                  the two ctx.Emit call sites) — a live-only signal, never persisted, so the
//	                  turn footer can show real spend as the turn progresses instead of sitting
//	                  at nothing until "done". Always the full running total, never a delta — the
//	                  frontend should overwrite whatever it's showing for this turn, not add to it
//	                  (unlike "done"/"suggestions", which add their cost_usd to the thread's total).
//	"commentary"    — content: what the model said before deciding to call a tool (or before an
//	                  aborted attempt got discarded) — sent once, with the full text, right before
//	                  that turn's tool_call events; the frontend clears whatever it had streamed
//	                  live as "token" for that turn and shows this as its own timeline item instead,
//	                  so it's positioned between the tool calls that came before and after it rather
//	                  than getting silently appended to the real final answer (see agent.emitCommentary)
//	"user_message"  — user_message_id: the persisted ID of the user message that started this
//	                  turn, sent as soon as it's saved (even if the turn later errors) so the
//	                  frontend can retry/edit from it
//	"done"          — thread_id + cost_usd + context_tokens + duration_ms: turn complete,
//	                  persisted, safe to re-enable input; duration_ms is how long agent.Run took
//	                  (see store.Message.DurationMs), shown next to cost in the turn footer.
//	                  Deliberately does NOT wait on follow-up suggestions — those are a separate
//	                  LLM call that runs after this event ships, so the turn footer appears the
//	                  moment the answer itself is ready instead of stalling behind it (see
//	                  "suggestions" below and handleTurn's comment on why generateSuggestions
//	                  moved after this send).
//	"suggestions"   — thread_id + cost_usd + suggestions: sent once, shortly after "done", once up
//	                  to 3 follow-up questions for the just-finished answer are ready; persisted
//	                  alongside the assistant message (see store.Message.Suggestions) so reopening
//	                  the thread later still shows them. cost_usd is this call's own cost, added to
//	                  the running total the same as "done"'s — not a replacement for it. May never
//	                  arrive if generation fails or the answer was stopped early; the frontend
//	                  should treat "no suggestions" as a normal, silent outcome, not an error.
//	"compacted"     — thread_id + content: the thread just crossed the context-window threshold
//	                  and was auto-summarized; content is the summary, shown as a collapsible
//	                  timeline note like a tool call, not a normal answer
//	"done" (continued)  — PendingQuestion, when non-nil, means this turn ended with
//	                  ask_user_question instead of a normal finished answer (see
//	                  tools.PendingQuestion, store.Message.PendingQuestion) — the frontend should
//	                  render its interactive controls under this turn's answer bubble instead of
//	                  (or in addition to) treating it as a fully-finished reply. Answering it is
//	                  just sending the next ordinary "message" — there's no dedicated response
//	                  frame for this.
//	"location_request" — thread_id only: nearby_search or weather wants a live GPS fix for this
//	                  turn and none of the cheaper sources (query text, cached cookie) had one —
//	                  see tools.Context.RequestLocation. The frontend should call
//	                  getCurrentPosition() right then (permission is already granted at this
//	                  point, or the tool wouldn't be running at all) and reply with a
//	                  "location_response" ClientMessage. Sent at most once per turn even if
//	                  multiple tool calls want a location — see handleTurn's requestLocation
//	                  wrapping. Never blocks the rest of the turn indefinitely: a client that
//	                  never replies (denied, tab backgrounded, closed) just times out and the
//	                  tool falls back to config.yaml's default_location like before.
//	"verification"  — thread_id + assistant_message_id + verification: sent once, well after
//	                  "done", once a per-claim "found in source" pass over this message's own
//	                  inline citations finishes (see docs/plans/source-verification-badge.md and
//	                  gateway/verification.go). Persisted alongside the assistant message (see
//	                  store.Message.Verification) so reopening the thread later still shows the
//	                  same marks. Only carries entries that actually cleared the confidence
//	                  threshold — a claim/source pair that wasn't checked, wasn't supported, or
//	                  fell below threshold is simply absent, not sent as an explicit "no" (mirrors
//	                  "suggestions": may never arrive at all if Jev isn't configured, every source's
//	                  budget cap was already hit, or nothing was found supported — the frontend
//	                  should treat that as a normal, silent outcome, not an error).
//	"error"         — message: something failed
type ServerEvent struct {
	Type     string         `json:"type"`
	ThreadID string         `json:"thread_id,omitempty"`
	Content  string         `json:"content,omitempty"`
	Tool     string         `json:"tool,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	Result   string         `json:"result,omitempty"`
	// CallID correlates a tool_result back to its own tool_call when the
	// model fires 2+ concurrent calls to the same tool in one turn (see
	// agent/driver.go's dispatchToolCallsConcurrently) — positional/
	// name-based matching alone is ambiguous once results can complete out
	// of call order, which caused two concurrent memory writes' results to
	// get cross-wired onto the wrong timeline card in the frontend.
	CallID string `json:"call_id,omitempty"`
	// Provider is web_search's normalized fallback-source key ("searxng",
	// "brave", "parallel", "tavily") — set only on web_search's tool_result
	// events, so store.Store.GetStats can tally how often each fallback
	// actually fires without regex-parsing the free-text "[via X]" prefix
	// in Result, which is a display label, not a stable machine key.
	Provider  string           `json:"provider,omitempty"`
	Citations []tools.Citation `json:"citations,omitempty"`
	// Cards is a tool_result/done event's structured rich-result items
	// (see tools.Card) — e.g. music's recommendation carousel. Same
	// "attached to the final answer" shape as Citations, just rendered as
	// its own visual block instead of a text source list.
	Cards []tools.Card `json:"cards,omitempty"`
	// Chart is a tool_result/done event's structured chart, if this turn
	// produced one (see tools.ChartSpec) — attached deterministically by
	// weather.go's Tier-1 "range" kind (the model-facing visualize tool
	// that used to also populate this was removed, see issue #44). At
	// most one per turn, unlike Cards.
	Chart *tools.ChartSpec `json:"chart,omitempty"`
	// URL/Caption are show's own tool_result payload — a workspace-file
	// route the frontend renders as a large inline embed (see
	// tools/show.go and gateway/workspace.go), plus its optional
	// model-supplied caption. Empty for every other tool.
	URL     string `json:"url,omitempty"`
	Caption string `json:"caption,omitempty"`
	// CostUSD and ContextTokens deliberately lack omitempty: 0 is a
	// legitimate value for both (a stopped turn that never reached an LLM
	// call costs exactly $0), and omitempty would drop the field from the
	// JSON entirely in that case rather than sending 0. The frontend's
	// `this.totalCost += e.cost_usd` would then add `undefined`, silently
	// and permanently turning totalCost into NaN for the rest of the
	// session — this bit us once already with the analogous "token"
	// event's content field (see streamSniffer.resolve in agent/pseudocall.go).
	CostUSD       float64 `json:"cost_usd"`
	ContextTokens int     `json:"context_tokens"`
	Message       string  `json:"message,omitempty"`
	UserMessageID int64   `json:"user_message_id,omitempty"`
	// AssistantMessageID is the persisted id of the assistant reply this
	// turn just wrote, sent on "done" — without it, a freshly-generated
	// turn's ChatTurn.id stays undefined for the rest of the session (only
	// a page reload's GetMessages populates it), which read-aloud needs to
	// know which message row to attach a persisted audio file to.
	AssistantMessageID int64    `json:"assistant_message_id,omitempty"`
	Suggestions        []string `json:"suggestions,omitempty"`
	// DurationMs is how long agent.Run took to produce the answer — unlike
	// CostUSD/ContextTokens above, omitempty is fine here: a real LLM call
	// always takes measurably more than 0ms, so there's no legitimate zero
	// value being silently dropped.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// PendingQuestion mirrors store.Message.PendingQuestion for the live
	// "done" event — see the doc comment above.
	PendingQuestion *tools.PendingQuestion `json:"pending_question,omitempty"`
	// Verification is the "verification" event's own payload — see its
	// doc comment above.
	Verification []VerificationMark `json:"verification,omitempty"`
	// VerificationDebug carries every claim's full result — including
	// ones that didn't clear the confidence threshold, or couldn't be
	// checked at all — only ever set when the turn that produced this
	// event had ClientMessage.WaitVerification set. Nil on every real
	// chat/WebSocket turn; see ask.go's AskRequest.WaitVerification.
	VerificationDebug []ClaimVerification `json:"verification_debug,omitempty"`
}

// VerificationMark is one claim/source pair that cleared the "found in
// source" confidence threshold — see the "verification" ServerEvent doc
// comment and docs/plans/source-verification-badge.md. ClaimIndex is the
// zero-based occurrence of URL among this answer's own inline citation
// links, in document order (first time the URL is cited = 0, second = 1,
// ...) — the frontend uses it to mark the *specific* chip the claim came
// from, not every chip citing that URL, since one source can back several
// claims with different verdicts. Choice is always "supported" today (see
// gateway/verification.go's doc comment on why only supported-at-threshold
// entries are ever sent) but kept as a string, not a bool, so a future
// "contradicted" warning mark doesn't need a wire-format change.
type VerificationMark struct {
	URL        string  `json:"url"`
	ClaimIndex int     `json:"claim_index"`
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
}
