package gateway

import (
	"context"
	"fmt"
	"strings"

	"polaris/llm"
	"polaris/prompts"
)

// compactThread summarizes every message up to and including throughID,
// via one extra (non-streamed, not shown as a normal answer) LLM call,
// and records that summary so history loading substitutes it for the raw
// messages it covers on every subsequent turn.
func (s *Server) compactThread(client llm.ChatClient, threadID string, throughID int64) (summary string, cost float64, err error) {
	history, err := s.loadAnswerHistory(threadID)
	if err != nil {
		return "", 0, err
	}
	prompt := []llm.ChatMessage{
		{Role: "system", Content: prompts.Get().Turn.CompactionSystem},
	}
	prompt = append(prompt, history...)
	// The trailing task turn is load-bearing, not decoration. `history` is
	// the whole conversation as the LLM saw it, so it always ends on an
	// ASSISTANT message — a turn's own answer is the last thing persisted
	// before compaction is triggered by that turn. Handed an array that ends
	// there, the model reads it as its own turn to continue rather than as
	// something to summarize, and returns essentially nothing: measured live
	// against deepseek-v4.1-flash, the same prompt produced 1 character of
	// content ending on an assistant turn, and a real summary once this user
	// turn was appended. That empty response then trips the guard below and
	// aborts the compaction, which is why auto-compaction had never once
	// succeeded in production — it failed silently and looked like it had
	// simply not fired. generateTitle hit the identical wall and fixes it the
	// same way (see its doc comment); title_regenerate_task is the precedent.
	prompt = append(prompt, llm.ChatMessage{Role: "user", Content: prompts.Get().Turn.CompactionTask})

	resp, err := client.ChatCompletionStreaming(context.Background(), prompt, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}
	if strings.TrimSpace(resp.Content) == "" {
		// No error, but nothing usable came back — same reasoning-exhaustion
		// failure mode as generateTitle/generateSuggestions (see
		// generateTitle's doc comment), except unchecked here it would be
		// far worse: CompactThread below replaces the thread's ENTIRE prior
		// history with this string and marks it as covering everything
		// through throughID. An empty summary would silently and
		// permanently erase that history instead of just showing a blank
		// title/suggestion list. Treating it as an error routes through the
		// caller's existing "auto-compaction failed" log + skip path, which
		// already correctly leaves the thread's real messages untouched.
		return "", resp.CostUSD, fmt.Errorf("compaction returned no usable summary")
	}

	if err := s.db.CompactThread(threadID, resp.Content, throughID, resp.CostUSD, estimateTokens(resp.Content)); err != nil {
		return "", 0, err
	}
	return resp.Content, resp.CostUSD, nil
}

// estimateTokens is a rough tokens-per-character heuristic (English text
// averages ~4 chars/token) used only to seed context_tokens right after a
// compaction, before the next real LLM call reports an actual count.
func estimateTokens(s string) int {
	return len(s) / 4
}
