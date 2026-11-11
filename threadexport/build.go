package threadexport

import (
	"encoding/json"
	"time"

	"polaris/uiblocks"
)

// Build assembles the neutral document for one thread. Options are applied
// here, once; the renderers are then total functions of the result.
func Build(in Input, opts Options) Doc {
	return build(in, opts, time.Now())
}

// build is Build with an injectable clock so tests can freeze ExportedAt
// and keep their golden files stable.
func build(in Input, opts Options, now time.Time) Doc {
	doc := Doc{
		Version:    Version,
		ExportedAt: now.UTC(),
		AppVersion: in.AppVersion,
		Options:    opts,
		Thread:     in.Thread,
		// Non-nil so the JSON renders `"turns": []` / `"omitted": []` for an
		// empty case rather than null — an empty list is a statement, null
		// reads as "unknown".
		Turns:   []Turn{},
		Omitted: []Omission{},
	}
	doc.Availability.HandoffAvailable = in.CompactedSummary != ""

	// Handoff reuses the stored compaction summary verbatim and appends only
	// the messages after it. It never generates a summary: a thread with none
	// exports full scope instead, with the reason recorded rather than a
	// silently empty document.
	messages := in.Messages
	if opts.Scope == ScopeHandoff {
		if in.CompactedSummary != "" {
			doc.Summary = &Summary{Text: in.CompactedSummary, ThroughMessageID: in.CompactedThroughID}
			messages = messagesAfterID(in.Messages, in.CompactedThroughID)
		} else {
			doc.Omitted = append(doc.Omitted, Omission{
				What: "summary", Reason: "no stored summary; exported full scope",
			})
		}
	}

	// Pairs messages sequentially rather than grouping by turn_id: a
	// thread from before turn_id existed has "" on every row, and grouping
	// on that would collapse the whole thread into one turn. Order is by
	// id and a turn always writes its user message before its assistant
	// one, so a pending-user buffer reproduces the pairing exactly while
	// still carrying each turn's real id through.
	turns := []Turn{}
	var pending *Turn // a user message still awaiting its answer
	flushPending := func() {
		if pending != nil {
			turns = append(turns, *pending)
			pending = nil
		}
	}

	for _, m := range messages {
		if m.Role == "assistant" {
			if pending != nil {
				pending.Assistant = entryFor(m, opts)
				pending.costUSD = m.CostUSD
				pending.durationMs = m.DurationMs
				pending.model = m.AppliedModel
				pending.transcript = m.Transcript
				turns = append(turns, *pending)
				pending = nil
			} else {
				// An assistant message with no preceding user message — not
				// expected in normal turns, but recorded rather than dropped.
				turns = append(turns, Turn{
					TurnID:     m.TurnID,
					Assistant:  entryFor(m, opts),
					costUSD:    m.CostUSD,
					durationMs: m.DurationMs,
					model:      m.AppliedModel,
					transcript: m.Transcript,
				})
			}
			continue
		}

		// A user message while one is already pending means the previous
		// turn never produced an answer (streaming/failed/cancelled). Flush
		// it as unanswered before starting this one.
		flushPending()
		pending = &Turn{TurnID: m.TurnID, User: entryFor(m, opts)}
	}
	flushPending()

	// Group the audit trail by turn once, so each turn sees only its own
	// events (thinking, and the pre-transcript tool fallback).
	eventsByTurn := map[string][]Event{}
	for _, e := range in.Events {
		if e.TurnID != "" {
			eventsByTurn[e.TurnID] = append(eventsByTurn[e.TurnID], e)
		}
	}

	priv := privacy{memory: in.MemoryTools, personal: in.PersonalTools}
	var droppedMemory, droppedPersonal, missingThinking []string
	doc.Availability.Turns = len(turns)

	for i := range turns {
		t := &turns[i]
		if t.Assistant == nil && t.User != nil {
			// The JSON side already reads as `"assistant": null`; this
			// manifest entry is what makes that unambiguous to an auditor.
			doc.Omitted = append(doc.Omitted, Omission{
				What:   "answer",
				Turns:  []string{t.TurnID},
				Reason: "no answer recorded",
			})
		}
		if opts.Meta && t.Assistant != nil {
			model := t.model
			if model == "" {
				model = in.Thread.Model
			}
			t.Meta = &TurnMeta{CostUSD: t.costUSD, DurationMs: t.durationMs, Model: model}
		}

		// Trail and thinking describe how an answer was reached, so an
		// unanswered turn (no answer) has neither — counting it as
		// "missing thinking" would just be noise in the manifest.
		if t.Assistant != nil {
			evs := eventsByTurn[t.TurnID]
			if opts.Trail {
				kept, mem, pers := priv.filter(buildTrail(t.transcript, evs), opts)
				for _, name := range mem {
					droppedMemory = appendUnique(droppedMemory, name)
				}
				for _, name := range pers {
					droppedPersonal = appendUnique(droppedPersonal, name)
				}
				if len(kept) > 0 {
					t.Trace = kept
					doc.Availability.TurnsWithTrail++
				}
			}
			if opts.Thinking {
				bursts := thinkingFromEvents(evs)
				if len(bursts) > 0 {
					t.Thinking = bursts
					doc.Availability.TurnsWithThinking++
				} else {
					missingThinking = append(missingThinking, t.TurnID)
				}
			}
		}
	}

	// Privacy omissions are aggregated across turns (which tools were left
	// out, never which turn each appeared in) so the manifest stays a short,
	// tool-scoped statement rather than a per-turn delta.
	if len(droppedMemory) > 0 {
		doc.Omitted = append(doc.Omitted, Omission{
			What: "tool_calls", Tools: droppedMemory, Reason: "privacy: memories left out",
		})
	}
	if len(droppedPersonal) > 0 {
		doc.Omitted = append(doc.Omitted, Omission{
			What: "tool_calls", Tools: droppedPersonal, Reason: "privacy: personalization left out",
		})
	}
	if len(missingThinking) > 0 {
		doc.Omitted = append(doc.Omitted, Omission{
			What: "thinking", Turns: missingThinking, Reason: "no thinking recorded",
		})
	}

	doc.Turns = turns
	return doc
}

// entryFor builds one side of a turn, applying the display options that
// affect a single message.
func entryFor(m Message, opts Options) *Entry {
	e := &Entry{
		ID:        m.ID,
		CreatedAt: m.CreatedAt,
		// Every non-UI consumer of an answer flattens its ```ui fences to
		// readable text (see the uiblocks package doc), and an export is
		// exactly such a consumer: the JSON lines are a rendering detail,
		// not something a pasted-into-Claude transcript should carry.
		Content: uiblocks.Flatten(m.Content),
	}
	if opts.Sources {
		e.Citations = parseCitations(m.Citations)
	}
	if opts.Attachments {
		e.Attachments = parseAttachments(m.Attachments)
	}
	return e
}

// messagesAfterID returns the messages whose id is greater than throughID,
// preserving order — the post-compaction tail of a handoff export.
func messagesAfterID(msgs []Message, throughID int64) []Message {
	var out []Message
	for _, m := range msgs {
		if m.ID > throughID {
			out = append(out, m)
		}
	}
	return out
}

// parseCitations decodes the stored citations JSON. A malformed or empty
// column yields nil rather than an error: an export must not fail because
// one old row's blob is unreadable, and a missing source list is visible in
// the rendered output on its own.
func parseCitations(raw string) []Citation {
	if raw == "" || raw == "[]" {
		return nil
	}
	var out []Citation
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// parseAttachments decodes the stored attachments JSON into the name/type
// pair v1 exports. Same best-effort contract as parseCitations.
func parseAttachments(raw string) []Attachment {
	if raw == "" || raw == "[]" {
		return nil
	}
	var stored []struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil
	}
	out := make([]Attachment, 0, len(stored))
	for _, a := range stored {
		out = append(out, Attachment{Filename: a.Filename, ContentType: a.ContentType})
	}
	return out
}
