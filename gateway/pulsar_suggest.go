// pulsar_suggest.go is the one-shot "derive a routine from this
// conversation" pass behind Oracle mode's "Set up as Pulsar" offer chip
// (see docs/plans/oracle-mode.md's 7a and web/src/lib/components/
// ChatTurnView.svelte's activateOffer).
//
// Why this exists at all: the chip used to seed the new-routine form with
// the preceding user message verbatim. That's the right text only when the
// conversation *is* the recurring question — on a follow-up ("what about
// the second one?", "did the price change?") it produces a routine that
// can't return anything useful when it fires, because the thing it refers
// back to isn't in the prompt. A scheduled run has no thread history, so
// the seed has to be rewritten into a standalone prompt first.
//
// Deliberately NOT the wizard (gateway/pulsar_wizard.go): that's an
// interactive interview, and the chip navigates straight to the routine
// form — there's no surface to answer questions on. One completion, one
// parseable answer, or a clean failure the client falls back from.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"polaris/llm"
	"polaris/prompts"
	"polaris/store"
)

// pulsarSuggestTimeout bounds the single completion. The client is
// waiting on this with a spinner on the chip, so it needs to fail fast
// enough to fall back to the plain seed rather than hang the navigation.
const pulsarSuggestTimeout = 45 * time.Second

// Bounds on the transcript handed to the model. A chat can be long, but
// the recurring interest is almost always visible in the recent tail —
// and this call runs on every "Set up as Pulsar" tap, so keeping it cheap
// matters more than completeness. Per-message first, so one giant answer
// can't crowd out the whole exchange; then a total, oldest dropped first.
const (
	pulsarSuggestMaxPerMessage = 1200
	pulsarSuggestMaxTranscript = 9000
	pulsarSuggestMaxTokens     = 700
)

type pulsarSuggestRequest struct {
	ThreadID string `json:"thread_id"`
}

type pulsarSuggestResponse struct {
	Name    string  `json:"name"`
	Prompt  string  `json:"prompt"`
	CostUSD float64 `json:"cost_usd"`
}

// pulsar_suggest.go's own endpoint. This call happens outside any turn, so
// there is no message row to bill — its cost goes to store.Store's
// aux_usage ledger instead, which GetStats folds into the Polaris bucket
// (see aux_usage's schema comment), so it reaches the settings panel's
// grand total like any other assistant-side spend. Best-effort: a ledger
// write failing must not fail a request the client is waiting on.
//
// (The Pulsar *wizard*'s turns are still unattributed — a separate,
// pre-existing gap of the same shape.)
func (s *Server) handleSuggestPulsarPrompt(w http.ResponseWriter, r *http.Request) {
	var req pulsarSuggestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		http.Error(w, "thread_id is required", http.StatusBadRequest)
		return
	}

	messages, err := s.db.GetMessages(threadID)
	if err != nil {
		log.Warn("pulsar suggest: loading messages failed", "thread", threadID, "err", err)
		http.Error(w, "couldn't read that conversation", http.StatusInternalServerError)
		return
	}
	transcript := buildPulsarSuggestTranscript(messages)
	if transcript == "" {
		http.Error(w, "that conversation has nothing to build a routine from", http.StatusBadRequest)
		return
	}

	title := ""
	if thread, err := s.db.GetThread(threadID); err == nil {
		title = thread.Title
	}
	if title == "" {
		// A brand-new thread whose title generation hasn't landed — a
		// filler keeps the task's "Conversation title:" line meaningful
		// instead of sending an empty label.
		title = "(untitled)"
	}

	cfg := s.liveConfig()
	modelCfg := cfg.ModelByID(s.effectiveDefaultModel(cfg))
	// Same narrow construction as generateSuggestions (gateway/turn.go):
	// its own client, no tools offered, a tight cap, reasoning explicitly
	// off — this is a single formatting task, and a reasoning model would
	// otherwise spend part of the budget thinking before writing anything
	// (see ReasoningParams.Enabled's doc comment). Fallbacks allowed for
	// the same reason every other client here allows them: an escape
	// valve when the pinned provider is down.
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, pulsarSuggestMaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(true)}).
		WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(false)})

	p := prompts.Get()
	chat := []llm.ChatMessage{
		{Role: "system", Content: p.PulsarSuggest.System},
		{Role: "user", Content: fmt.Sprintf(p.PulsarSuggest.Task, title, transcript)},
	}

	ctx, cancel := context.WithTimeout(r.Context(), pulsarSuggestTimeout)
	defer cancel()
	resp, err := client.ChatCompletionStreaming(ctx, chat, func(string) {}, nil)
	if err != nil {
		log.Warn("pulsar suggest: completion failed", "thread", threadID, "err", err)
		http.Error(w, "couldn't draft a routine prompt — try again", http.StatusBadGateway)
		return
	}

	name, prompt := parsePulsarSuggestion(resp.Content)
	if prompt == "" {
		// A parse failure or an empty draft must not silently hand the
		// form a blank prompt — the client falls back to the raw seed.
		log.Warn("pulsar suggest: model returned no usable prompt", "thread", threadID)
		http.Error(w, "the model didn't return a usable prompt — try again", http.StatusBadGateway)
		return
	}

	if resp.CostUSD > 0 {
		if err := s.db.RecordAuxCost("pulsar_suggest", resp.CostUSD); err != nil {
			log.Warn("pulsar suggest: recording cost failed", "thread", threadID, "err", err)
		}
	}

	writeJSON(w, pulsarSuggestResponse{Name: name, Prompt: prompt, CostUSD: resp.CostUSD})
}

// buildPulsarSuggestTranscript renders the conversation tail the model
// sees — user and assistant turns only (tool calls and reasoning are
// deliberately not part of this: a message row's own content already
// carries the answer, and the mechanics in between aren't what a routine
// should be derived from). Empty string when there's nothing usable.
func buildPulsarSuggestTranscript(messages []store.Message) string {
	var lines []string
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		switch m.Role {
		case "user":
			content = "User: " + truncateRunes(content, pulsarSuggestMaxPerMessage)
		case "assistant":
			content = "Assistant: " + truncateRunes(content, pulsarSuggestMaxPerMessage)
		default:
			continue
		}
		lines = append(lines, content)
		// Stop once the tail is big enough — measured as we go so the
		// oldest dropped messages are never even built.
		if totalLen(lines) >= pulsarSuggestMaxTranscript {
			break
		}
	}
	// Built newest-first above; flip back to reading order.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n\n")
}

func totalLen(lines []string) int {
	total := 0
	for _, l := range lines {
		total += len(l)
	}
	return total
}

// truncateRunes keeps a whole number of characters up to max, appending
// an ellipsis marker when it actually cut something (so the model doesn't
// mistake a truncated sentence for the person's complete thought).
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max])) + " […]"
}

// parsePulsarSuggestion reads the "Name: …\n---\n<prompt>" shape
// prompts.PulsarSuggest.Task asks for. Tolerant by design: the model
// occasionally fences or labels things slightly differently, and a
// slightly-wrong parsing is much better than discarding a usable draft —
// a missing separator just means no suggested name, a missing prefix just
// means the name line is the whole first line.
func parsePulsarSuggestion(raw string) (name, prompt string) {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	text = strings.TrimSpace(text)

	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	sep := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			sep = i
			break
		}
	}
	if sep == -1 {
		// No separator — treat the whole reply as the prompt (the part
		// that actually matters) rather than throwing it away.
		return "", strings.TrimSpace(normalized)
	}

	name = strings.TrimSpace(strings.Join(lines[:sep], " "))
	name = strings.TrimSpace(strings.TrimPrefix(name, "Name:"))
	name = strings.Trim(name, `"'`)
	prompt = strings.TrimSpace(strings.Join(lines[sep+1:], "\n"))
	return name, prompt
}
