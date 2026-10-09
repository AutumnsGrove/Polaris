package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"polaris/uiblocks"
)

// HistoryEntry is one reconstructed turn from EffectiveHistory — a plain
// Role/Content pair rather than llm.ChatMessage, since store can't import
// package llm (llm has no dependency on store today, and adding one just
// for this return type isn't worth it) and search_chats' read action wants
// to render this as a flat transcript rather than a chat-message list
// anyway.
type HistoryEntry struct {
	Role    string
	Content string
	// TurnID is the source message's own turn_id ("" for the synthetic
	// compaction-summary entry) — lets gateway's loadHistory find that
	// turn's logged tool calls/results to rebuild a turn from before
	// transcripts existed (see ToolEventsForThread).
	TurnID string
	// Transcript is the source message's stored wire transcript ('' for
	// user messages and pre-transcript turns) — see Message.Transcript.
	Transcript string
}

// EffectiveHistory reconstructs a thread's prior turns exactly the way
// gateway/turn.go's loadHistory sends them to the LLM: if the thread has
// been auto-compacted, everything at or below CompactedThroughID collapses
// into one leading summary entry instead of being replayed message-by-
// message. excludeFromID, if nonzero, additionally skips every message
// with id >= excludeFromID — loadHistory's own retry/edit-path need (see
// its doc comment); pass 0 for a plain full-history read (search_chats'
// ReadThread below never needs it).
//
// Shared by loadHistory and ReadThread specifically so the two can't drift
// out of sync — see docs/plans/search-chats.md's "The read action" section,
// which calls out hand-duplicating this loop as the wrong move.
func EffectiveHistory(thread *Thread, msgs []Message, excludeFromID int64) []HistoryEntry {
	history := make([]HistoryEntry, 0, len(msgs)+1)
	if thread.CompactedSummary != "" {
		history = append(history, HistoryEntry{
			Role: "assistant",
			Content: "(Summary of earlier conversation, compacted to save context — the full history " +
				"is no longer available, only this summary)\n\n" + thread.CompactedSummary,
		})
	}
	for _, m := range msgs {
		if m.ID <= thread.CompactedThroughID {
			continue // covered by the summary above
		}
		if excludeFromID != 0 && m.ID >= excludeFromID {
			continue
		}
		content := appendPendingQuestionOptions(m.Content, m.PendingQuestion)
		if m.Role == "assistant" {
			content = appendCitedSources(content, m.Citations)
		}
		history = append(history, HistoryEntry{Role: m.Role, Content: content, TurnID: m.TurnID, Transcript: m.Transcript})
	}
	return history
}

// maxHistorySources caps how many of one answer's citations
// appendCitedSources lists — a turn's Citations include every web_search
// hit, not just pages actually read, so a research-heavy turn can carry
// dozens. The first N are enough for the model to recognize what it
// already looked at; the rest collapse into a count.
const maxHistorySources = 25

// polarisNoteMarker is the exact literal text appendCitedSources always
// opens its injected note with — shared with StripFakeSourcesNote below,
// which needs to recognize precisely this string to tell a real injected
// note apart from ordinary answer text that happens to mention sources.
const polarisNoteMarker = "\n\n[Polaris note, not part of the answer above — sources found or read while researching it:\n"

// appendCitedSources folds an assistant message's stored citations into
// the text its history entry carries. Before this, a follow-up turn saw
// only the prior answer's prose — the "N Sources" list the user sees under
// it (store.Message.Citations) never reached the model, so it read its own
// earlier, confidently-cited answer with no trace of where any of it came
// from. Seen live: the model's reasoning on a follow-up questioned whether
// its previous turn had fabricated its claims, then re-ran searches it had
// already done. Same "decode only the fields history needs" shape as
// appendPendingQuestionOptions (store can't import tools.Citation).
// Framed as an app-added note, not as part of the answer, so the model
// doesn't learn to write a trailing source list itself — prompt.md's
// "Earlier turns" section says the same. See StripFakeSourcesNote for the
// defense when a model imitates this format anyway.
func appendCitedSources(content, citationsJSON string) string {
	if citationsJSON == "" {
		return content
	}
	var cits []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal([]byte(citationsJSON), &cits); err != nil || len(cits) == 0 {
		return content
	}
	var sb strings.Builder
	sb.WriteString(content)
	sb.WriteString(polarisNoteMarker)
	for i, c := range cits {
		if i == maxHistorySources {
			fmt.Fprintf(&sb, "- ...and %d more\n", len(cits)-maxHistorySources)
			break
		}
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = c.URL
		}
		fmt.Fprintf(&sb, "- %s — %s\n", title, c.URL)
	}
	sb.WriteString("]")
	return sb.String()
}

// StripFakeSourcesNote removes a trailing block from answer that imitates
// appendCitedSources' own injected note, if the model wrote one into its
// own output. Seen live: once a thread's replayed history carries a few
// real injected notes (full_turn_history mode replays several turns'
// worth at once), the model sometimes copies the exact format into its
// own answer despite prompt.md telling it not to — and unlike the real
// note, a model-written one is fake positioning (its "sources" are
// whatever the model already cited inline, not a system-verified list),
// so it must never reach the stored message or a later turn's replayed
// history, or the mimicry compounds. Matches on the literal marker text,
// not fuzzy detection — a genuine answer has no legitimate reason to ever
// contain this exact string.
func StripFakeSourcesNote(answer string) string {
	if idx := strings.Index(answer, polarisNoteMarker); idx != -1 {
		return strings.TrimRight(answer[:idx], "\n")
	}
	return answer
}

// appendPendingQuestionOptions folds an ask_user_question call's suggested
// options into the plain-text content a history entry carries. The frontend
// renders Options as a separate tappable-row UI (see
// AskUserQuestionCard.svelte) built from store.Message.PendingQuestion, but
// that field never flowed into Content itself — so once the model resumed
// on the next turn, "a combo of 1, 2, and 3" referred to options the model
// had no record of ever offering. Can't import package tools here for its
// PendingQuestion type (tools already imports store), so this decodes just
// the two fields history actually needs.
func appendPendingQuestionOptions(content, pendingQuestionJSON string) string {
	if pendingQuestionJSON == "" {
		return content
	}
	var pq struct {
		Options []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(pendingQuestionJSON), &pq); err != nil || len(pq.Options) == 0 {
		return content
	}
	var sb strings.Builder
	sb.WriteString(content)
	sb.WriteString("\n\nOptions offered:\n")
	for i, opt := range pq.Options {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, opt)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ThreadReadResult is search_chats' read action's raw material — a past
// thread's full reconstructed transcript, compaction-substituted the same
// way a resumed live thread would be. See ReadThread.
type ThreadReadResult struct {
	ThreadID  string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Content   string
}

// ReadThread reconstructs a past thread's full transcript for search_chats'
// read action. Uses GetThreadRaw, not the public GetThread — same reasoning
// as loadHistory: a thread the model wants to read back should still be
// readable via a thread_id a prior search call actually returned, without
// re-litigating GetThread's own disabled/hidden-variant rejection here.
func (s *Store) ReadThread(threadID string) (*ThreadReadResult, error) {
	thread, err := s.GetThreadRaw(threadID)
	if err != nil {
		return nil, err
	}
	// A still-ghost thread must look entirely absent to this tool, same as
	// a genuinely missing id — see the ghost schema comment. Otherwise a
	// normal turn's chat_search/read_thread call could read back a live
	// incognito session's content if it somehow had the id in hand (e.g.
	// leaked via an earlier tool result before the session was promoted).
	if thread.Ghost {
		return nil, sql.ErrNoRows
	}
	msgs, err := s.GetMessages(threadID)
	if err != nil {
		return nil, err
	}
	entries := EffectiveHistory(thread, msgs, 0)

	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		label := "User"
		content := e.Content
		if e.Role == "assistant" {
			label = "Assistant"
			// A stored answer is the model's verbatim output, which may carry
			// Intelligent UI `ui` fences (JSON lines). This transcript is read
			// by a model or a person, not rendered, so give them the readable
			// text. Assistant only: a user may paste a ui fence as an example.
			content = uiblocks.Flatten(content)
		}
		sb.WriteString(label + ": " + content)
	}

	return &ThreadReadResult{
		ThreadID:  thread.ID,
		Title:     thread.Title,
		CreatedAt: thread.CreatedAt,
		UpdatedAt: thread.UpdatedAt,
		Content:   sb.String(),
	}, nil
}
