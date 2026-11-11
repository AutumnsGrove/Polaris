// Package threadexport builds one neutral document from a single thread's
// stored messages and renders it as Markdown or JSON, so a finished thread
// can leave Polaris as a file (docs/plans/thread-export.md).
//
// It is deliberately pure: no database access, and no import of the store
// or tools packages. That's not just tidiness — store is meant to host the
// read helper that fills Input (so it would import this package), and tools
// already imports store, so importing either here would close a cycle.
// Build therefore parses the raw JSON columns (citations, attachments)
// into its own small structs. A future public-share feature (#120) can
// reuse the same Doc without dragging in the schema.
package threadexport

import "time"

// Version is the schema version stamped into a JSON export as
// "polaris_export". Bump it only for a breaking field change; additive
// fields keep the same version so an old reader still accepts a newer file.
const Version = 1

// Format is the chosen output encoding. It is recorded in the export's
// options for auditability but does not affect Build — the caller picks
// RenderMarkdown or RenderJSON.
type Format string

const (
	FormatMarkdown Format = "md"
	FormatJSON     Format = "json"
)

// Scope selects how much of the thread to build. Only ScopeFull is
// implemented today; ScopeHandoff (stored compaction summary plus the
// messages after it) is docs/plans/thread-export.md phase 3.
type Scope string

const (
	ScopeFull    Scope = "full"
	ScopeHandoff Scope = "handoff"
)

// Options are the modal's choices, applied once when Build runs. The two
// privacy toggles and the trail/thinking toggles are part of the API now so
// the endpoint's parameters stay stable, but only the fields noted below
// are honored by the current builder.
type Options struct {
	Format Format `json:"format"`
	Scope  Scope  `json:"scope"`

	Sources     bool `json:"sources"`     // include citations and the Sources list
	Attachments bool `json:"attachments"` // include attachment names and types
	Meta        bool `json:"meta"`        // include per-turn cost/duration/model

	// Trail and Thinking are independent. Neither is populated yet (phase 2).
	Trail    bool `json:"trail"`
	Thinking bool `json:"thinking"`

	// Private drops memory tool calls/results from the trail and never reads
	// the system prompt (phase 2). PrivatePersonal additionally drops chat
	// search, star reads and save-to-field calls (phase 2).
	Private         bool `json:"private"`
	PrivatePersonal bool `json:"private_personal"`
}

// Input is everything Build needs, gathered by the caller in one pass.
type Input struct {
	// AppVersion is the Polaris build stamp (main.Version) recorded in the
	// export so an auditor can correlate it with a release.
	AppVersion string

	Thread   Thread
	Messages []Message // effective variant path only, oldest first

	// Events is the thread's full audit trail (never pruned — see the
	// package's retention note in store/events.go), oldest first. Thinking
	// lives only here, so an export that needs reasoning needs this.
	Events []Event

	// MemoryTools and PersonalTools are the tool-name sets the two privacy
	// toggles suppress. The caller fills them from the tool catalog
	// (tools.ExportPrivacyClass) rather than this package hardcoding a list
	// that a new tool could silently fall outside of.
	MemoryTools   map[string]bool
	PersonalTools map[string]bool

	// CompactedSummary/CompactedThroughID mirror the thread's stored
	// compaction fields, for handoff scope (phase 3).
	CompactedSummary   string
	CompactedThroughID int64
}

// Event is one stored audit-trail row in the neutral shape Build consumes
// (see store.Event). Data is the raw JSON detail blob, parsed lazily by
// whichever phase needs a given field.
type Event struct {
	TurnID    string
	Source    string // e.g. "turn", "tool.web_search"
	Message   string // e.g. "reasoning", "tool call started"
	Data      string
	CreatedAt time.Time
}

// Thread is a thread's identifying metadata, carried verbatim into the
// export. It mirrors the store.Thread fields the document needs; the
// compaction fields and hidden-variant internals are not part of it.
type Thread struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Model     string    `json:"model,omitempty"`
	Source    string    `json:"source"`
}

// Message is one stored message in the neutral shape Build consumes. The
// string fields carry raw JSON exactly as the columns store it — Build
// parses citations and attachments; cards/chart/verification/oracle_result
// are reserved for the rich-blocks pass and are intentionally not read yet.
type Message struct {
	ID          int64
	Role        string // "user" | "assistant"
	Content     string
	Citations   string // JSON []Citation
	Attachments string // JSON []Attachment
	CostUSD     float64
	DurationMs  int64
	TurnID      string
	CreatedAt   time.Time
	// AppliedModel is the model this turn actually ran on, when recorded.
	// Empty for user messages, and for assistant messages from before the
	// column existed; Build falls back to the thread's model.
	AppliedModel string
	// Transcript is the turn's exact wire messages as stored JSON
	// (llm.EncodeTranscript) — the preferred source for the research trail.
	// Empty for turns that predate transcripts, which fall back to Events.
	Transcript string
}

// Doc is the neutral intermediate document: Build produces it once, then
// RenderMarkdown or RenderJSON is a dumb, total function of it.
type Doc struct {
	Version    int       `json:"polaris_export"`
	ExportedAt time.Time `json:"exported_at"`
	AppVersion string    `json:"app_version"`
	Options    Options   `json:"options"`
	Thread     Thread    `json:"thread"`
	// Summary is present only for handoff scope (phase 3).
	Summary *Summary `json:"summary,omitempty"`
	Turns   []Turn   `json:"turns"`
	// Availability reports how fully an included option could be honored,
	// for the modal's "thinking: 14 of 40 turns" note.
	Availability Availability `json:"availability"`
	// Omitted records what was left out and why, so a reader can tell "this
	// never happened" apart from "this was removed" — the point of the JSON
	// export being auditable rather than just lossy.
	Omitted []Omission `json:"omitted"`
}

// Turn is one user/assistant exchange, keyed by the turn_id the two
// messages share. Assistant is nil when the turn never finished (a
// streaming, failed or cancelled turn leaves a user message with no
// answer), which is recorded in the omitted manifest.
type Turn struct {
	TurnID    string    `json:"turn_id"`
	User      *Entry    `json:"user"`
	Assistant *Entry    `json:"assistant"`
	Meta      *TurnMeta `json:"meta,omitempty"`
	// Trace is the research trail (tool calls and results), present only
	// when Options.Trail is on. Thinking is separate and independently
	// toggled; both render before the answer they belong to.
	Trace    []TraceStep `json:"trace,omitempty"`
	Thinking []Thinking  `json:"thinking,omitempty"`

	// Raw metadata lifted into Meta only when Options.Meta is on. Unexported
	// so it never leaks into the JSON export outside the meta object.
	costUSD    float64
	durationMs int64
	model      string
	// transcript is the assistant message's stored wire transcript, kept
	// for the trail pass so Entry itself need not carry a field that would
	// bloat every turn's JSON output.
	transcript string
}

// Entry is one side of a turn.
type Entry struct {
	ID          int64        `json:"id"`
	CreatedAt   time.Time    `json:"created_at"`
	Content     string       `json:"content"`
	Citations   []Citation   `json:"citations,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Citation is the subset of a stored citation the export renders. Kept
// local rather than importing tools.Citation, which would be a cycle.
type Citation struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	SiteName string `json:"site_name,omitempty"`
}

// Attachment is one file that accompanied a user message. v1 carries the
// name and type only — the bytes are not bundled
// (docs/plans/thread-export.md "Attachments v1").
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// TraceStep is one normalized tool call in a turn's research trail — what
// was called, with what arguments, and what came back. Built from the
// turn's transcript when it has one, else reconstructed from events.
type TraceStep struct {
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`   // JSON object as the model sent it
	Result string `json:"result,omitempty"` // tool output text
}

// Thinking is one reasoning burst. Agent is "" for the orchestrator and a
// sub-agent id for Tier 2 Deep Research (the per-agent reasoning buffers in
// gateway/turn_emit.go), so a reader can tell the main thread of thought
// from a researcher's.
type Thinking struct {
	Agent      string `json:"agent,omitempty"`
	Content    string `json:"content"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// Availability reports how fully an included option could be honored.
// Counts are only meaningful for options that were on: an option left off
// is not "unavailable", it just wasn't asked for.
type Availability struct {
	Turns             int `json:"turns"`
	TurnsWithTrail    int `json:"turns_with_trail,omitempty"`
	TurnsWithThinking int `json:"turns_with_thinking,omitempty"`
	// HandoffAvailable reports whether the thread has a stored compaction
	// summary. The modal uses it to disable the handoff option with a reason
	// rather than offering a scope that would export nothing (the summary is
	// never generated on demand — see docs/plans/thread-export.md).
	HandoffAvailable bool `json:"handoff_available,omitempty"`
}

// TurnMeta is a turn's cost/duration/model, present only when Options.Meta
// is on.
type TurnMeta struct {
	CostUSD    float64 `json:"cost_usd"`
	DurationMs int64   `json:"duration_ms"`
	Model      string  `json:"model,omitempty"`
}

// Summary is a thread's stored compaction summary, used by handoff scope
// (phase 3). It is reused verbatim from threads.compacted_summary — the
// export never generates one.
type Summary struct {
	Text             string `json:"text"`
	ThroughMessageID int64  `json:"through_message_id"`
}

// Omission is one entry in the omitted manifest: a category and the reason
// it is absent from the document.
type Omission struct {
	// What names the missing content: "answer", "thinking", "tool_calls", ...
	What string `json:"what"`
	// Turns lists the affected turn ids, when the omission is turn-scoped.
	Turns []string `json:"turns,omitempty"`
	// Tools lists the affected tool names, when the omission is tool-scoped.
	Tools []string `json:"tools,omitempty"`
	// Reason is a short machine-readable explanation, e.g. "no answer
	// recorded", "events pruned before 2026-10-12", "privacy: memories left
	// out".
	Reason string `json:"reason"`
}
