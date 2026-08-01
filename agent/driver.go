// Package agent implements the tool-use loop: alternate between the
// model calling think/web_search/web_read and the model answering
// directly, until it produces a plain-text final answer (no reply
// signal tool — see tools.Defs for why).
package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"polaris/llm"
	"polaris/tools"
)

// defaultMaxTurns is used when a caller doesn't set ctx.MaxTurns — the
// real value normally comes from config.Config.MaxAgentTurns, configurable
// so this can be raised without a rebuild if a model needs more room.
const defaultMaxTurns = 50

// researchCheckInInterval is how often (in research tool calls) the loop
// injects a sufficiency check-in — real-time steering rather than a hard
// cap. Smaller/flash-tier models especially don't reliably self-monitor
// "have I converged yet?" from a general prompt instruction alone (seen
// in practice: 39-70+ search calls on one question, endlessly rephrasing
// after the model's own reasoning trace had already settled on an
// answer). Forcing an explicit, isolated check-in every few calls gives
// it a concrete chance to course-correct without ever blocking a
// genuinely hard multi-part question from digging as deep as it needs —
// nothing here stops the model from searching again if it decides to.
const researchCheckInInterval = 5

// isResearchTool is which tools count toward researchCheckInInterval —
// the ones that gather sources (and so plausibly reach a point of
// diminishing returns), not "think" (private reasoning, not research).
func isResearchTool(name string) bool {
	return name == "web_search" || name == "web_read" || name == "nearby_search" || name == "youtube_transcript"
}

// researchCheckInMessage nudges the model to consider answering instead
// of continuing to search, grounded in what it's actually gathered
// (citation count) rather than a vague "are you sure?" — mirrors the
// literature's finding that structured, externally-grounded check-ins
// beat asking a model to self-assess confidence in free text.
func researchCheckInMessage(citationCount, callCount int) string {
	return fmt.Sprintf(
		"Checkpoint: you've made %d research tool calls and gathered %d source(s) so far. "+
			"If you already have enough to answer confidently, stop searching and state your "+
			"conclusion now, citing what you've found — don't keep searching just to double-check "+
			"an answer you've already reasoned out. Only continue if there's a specific, concrete "+
			"gap in what you know that a further search could plausibly fill.",
		callCount, citationCount,
	)
}

// staleStreakThreshold is how many consecutive research calls with zero
// new citations trigger the stronger stale-streak warning below —
// independent of researchCheckInInterval, and evaluated every call (not
// just on the interval), since "you're re-finding the same sources" is a
// much less arguable signal than "you've made N calls" and deserves to
// interrupt sooner. tools.Context.Citations already dedupes by URL (see
// TestContext_AddCitation_DeduplicatesByURL), so a citation count that
// doesn't grow after a search/read call means it turned up nothing this
// loop hadn't already seen — the exact "echo chamber retrieval" failure
// mode observed live: 15+ calls, three interval check-ins acknowledged
// in the model's own reasoning, and it kept searching anyway.
const staleStreakThreshold = 2

// staleStreakMessage is deliberately blunter than researchCheckInMessage —
// evidence of repetition, not a time-based nudge the model can always
// rationalize past with "just one more search."
func staleStreakMessage(streak, citationCount int) string {
	return fmt.Sprintf(
		"Your last %d searches found zero new sources — you're re-finding the same %d source(s) "+
			"you already have. Searching again with a similar query will not help. Either answer now "+
			"with what you've gathered, or try a meaningfully different angle (a different tool, a "+
			"very different search term, or a specific named source) — not a reworded version of a "+
			"query you've already tried.",
		streak, citationCount,
	)
}

// trackResearchCall updates the running research-call/citation-novelty
// state after a single research tool dispatch and returns whichever
// steering message(s) are warranted. The two signals are deliberately
// independent and can both fire on the same call — one measures elapsed
// effort (researchCheckInInterval), the other measures actual
// productivity (citation growth via staleStreakThreshold) — so neither
// resets or suppresses the other; a call that's both the 5th research
// call AND the 2nd stale one in a row genuinely warrants both nudges.
func trackResearchCall(citations []tools.Citation, researchCalls, lastCitationCount, staleStreak *int) []string {
	*researchCalls++
	current := len(citations)

	var nudges []string
	if current > *lastCitationCount {
		*staleStreak = 0
	} else {
		*staleStreak++
		if *staleStreak >= staleStreakThreshold {
			nudges = append(nudges, staleStreakMessage(*staleStreak, current))
			*staleStreak = 0 // fire once per streak, not every call past the threshold
		}
	}
	*lastCitationCount = current

	if *researchCalls%researchCheckInInterval == 0 {
		nudges = append(nudges, researchCheckInMessage(current, *researchCalls))
	}
	return nudges
}

// promptPath is read fresh on every turn — no recompiling to change how
// Polaris behaves. Matches her-go's convention of hot-reloaded prompt
// files living as plain text in the working directory.
const promptPath = "prompt.md"

// fallbackSystemPrompt is used only if prompt.md is missing, so a fresh
// clone still works before the user copies prompt.md.example into place.
const fallbackSystemPrompt = `You are Polaris, a private, self-hosted research assistant. You have five tools:

- think: reason privately about strategy before acting.
- web_search: search the web via a private SearXNG instance.
- web_read: fetch a URL and extract its content (optionally filtered to just what's needed).
- nearby_search: find real-world places (restaurants, pharmacies, etc.) near a location.
- youtube_transcript: fetch a YouTube video's transcript, given its URL or video ID.

You can call multiple tools in the same turn when they're genuinely independent of each other's
results (they run concurrently) — don't batch when a later call depends on an earlier one's result.

There is no separate "reply" tool. Once you have enough information (or the question needs none),
just answer directly in plain text — that ends the research phase and streams straight to the user.

Be concise. Cite sources inline as [Title](URL) when you used web_search or web_read to support a claim.
Don't call tools for questions you can already answer confidently (general knowledge, math, writing help).`

// voiceModeInstruction is appended when the turn will be read aloud —
// long markdown-formatted answers with citation lists read terribly via
// TTS, so voice mode gets a stronger brevity/plain-text nudge than the
// base prompt asks for.
const voiceModeInstruction = "\n\nVoice mode is active: this answer will be read aloud, not just displayed. " +
	"Keep it brief and conversational (1-3 sentences when possible), and avoid markdown formatting, " +
	"bullet lists, or reciting citations inline — sources will still be shown in the UI regardless."

// loadSystemPrompt reads prompt.md fresh every call — edit the file,
// see the change on your very next message, no rebuild or restart.
func loadSystemPrompt(voiceMode bool) string {
	data, err := os.ReadFile(promptPath)
	prompt := fallbackSystemPrompt
	if err == nil {
		prompt = string(data)
	}
	if voiceMode {
		prompt += voiceModeInstruction
	}
	return prompt
}

// currentContextPreamble grounds the model in real wall-clock time, computed
// fresh on every turn — without this, a model has no way to know "today"
// beyond its training cutoff, and will confidently answer with a stale
// date or search for news anchored to the wrong week. Prepended ahead of
// the rest of the system prompt so it's the first thing the model reads.
func currentContextPreamble() string {
	now := time.Now()
	return fmt.Sprintf(
		"Current date and time: %s (timezone: %s). Treat this as ground truth for anything "+
			"relative — \"today\", \"this week\", \"latest\", \"currently\", how old something is "+
			"— rather than any date you might otherwise assume from training. If it conflicts with "+
			"a date implied by the user or a search result, trust this line.\n\n",
		now.Format("Monday, January 2, 2006, 15:04"), now.Location(),
	)
}

// Result is what one turn produces, once the model settles on a
// plain-text final answer.
type Result struct {
	Answer    string
	Citations []tools.Citation
	CostUSD   float64
	// ContextTokens is the prompt+completion token count of the LAST LLM
	// call this turn made — the best available estimate of how much
	// context this thread now occupies, since it reflects every message,
	// tool result, and reasoning pass accumulated through the whole loop.
	ContextTokens int
}

// Run executes one turn of the agent loop: given prior conversation
// history plus a new user message, it streams progress (thinking/
// tool_call/tool_result/token events) via ctx.Emit and returns once the
// model has produced its final answer. ctx must have LLM and Emit set;
// SearXNG/Foursquare/DefaultLocation are optional per-tool dependencies.
//
// reqCtx cancels the whole turn (the "stop" button) — a cancellation
// isn't treated as an error, since the LLM client already turns it into a
// graceful early finish with whatever content streamed so far.
func Run(reqCtx context.Context, ctx *tools.Context, history []llm.ChatMessage, userMessage string) (*Result, error) {
	client := ctx.LLM
	ctx.Ctx = reqCtx

	messages := make([]llm.ChatMessage, 0, len(history)+2)
	messages = append(messages, llm.ChatMessage{Role: "system", Content: currentContextPreamble() + loadSystemPrompt(ctx.VoiceMode)})
	messages = append(messages, history...)
	messages = append(messages, llm.ChatMessage{Role: "user", Content: userMessage})

	toolDefs := tools.Defs()
	var totalCost float64
	var answer strings.Builder

	maxTurns := ctx.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	researchCalls := 0
	lastCitationCount := 0
	staleStreak := 0

	for turn := 0; turn < maxTurns; turn++ {
		answer.Reset()
		sniff := &streamSniffer{
			emit:     func(s string) { ctx.Emit("token", map[string]interface{}{"content": s}) },
			prefixes: pseudoToolCallPrefixes,
		}
		resp, err := client.ChatCompletionWithTools(reqCtx, messages, toolDefs, func(chunk string) {
			answer.WriteString(chunk)
			sniff.onChunk(chunk)
		}, func(chunk string) {
			ctx.Emit("reasoning", map[string]interface{}{"content": chunk})
		})
		if err != nil {
			return nil, err
		}
		sniff.flush()
		totalCost += resp.CostUSD

		if len(resp.ToolCalls) == 0 {
			if calls := parsePseudoToolCalls(resp.Content); len(calls) > 0 {
				emitCommentary(ctx, resp.Content)
				for _, pc := range calls {
					result := tools.Dispatch(pc.name, pc.argsJSON, ctx)
					messages = append(messages, llm.ChatMessage{
						Role: "user",
						Content: fmt.Sprintf("[%s result]\n%s\n\nContinue answering the original question using this — "+
							"use the real tool-calling mechanism if you need to search again, not text-formatted "+
							"tool call syntax.", pc.name, result),
					})
					if isResearchTool(pc.name) {
						for _, nudge := range trackResearchCall(ctx.Citations, &researchCalls, &lastCitationCount, &staleStreak) {
							messages = append(messages, llm.ChatMessage{Role: "user", Content: nudge})
						}
					}
				}
				continue
			}
			// Plain content = the final answer. It was already streamed
			// token-by-token via the onChunk callback above.
			return &Result{
				Answer:        resp.Content,
				Citations:     ctx.Citations,
				CostUSD:       totalCost,
				ContextTokens: resp.PromptTokens + resp.CompletionTokens,
			}, nil
		}

		emitCommentary(ctx, resp.Content)

		// The OpenAI-compatible wire protocol requires one assistant
		// message carrying every tool call from this turn (not one
		// message per call), immediately followed by ALL of that batch's
		// "tool" role result messages with nothing else interleaved
		// between them — confirmed the hard way: DeepSeek 400s with
		// "insufficient tool messages following tool_calls message" if a
		// nudge (a "user" role message) lands between tool-result #2 and
		// #3 of a 3-call batch. So every result message gets appended
		// first, in one pass, and only THEN any nudges from the whole
		// batch — never interleaved per-call the way a single-call turn
		// could safely do it.
		calls := resp.ToolCalls
		messages = append(messages, llm.ChatMessage{Role: "assistant", ToolCalls: calls})

		results := dispatchToolCallsConcurrently(calls, ctx)
		for _, r := range results {
			messages = append(messages, llm.ChatMessage{Role: "tool", Content: r.result, ToolCallID: r.call.ID})
		}
		for _, r := range results {
			if isResearchTool(r.call.Function.Name) {
				for _, nudge := range trackResearchCall(ctx.Citations, &researchCalls, &lastCitationCount, &staleStreak) {
					messages = append(messages, llm.ChatMessage{Role: "user", Content: nudge})
				}
			}
		}
	}

	// Ran out of turns — force a wrap-up instead of failing outright. No
	// more tool dispatching allowed past this point (that's the whole
	// point of the bound), so if the model still tries to emit a pseudo
	// tool call here, that's treated as "couldn't produce a real answer
	// in time" rather than given yet another turn.
	messages = append(messages, llm.ChatMessage{
		Role:    "user",
		Content: "Wrap up now — give your best answer with what you've gathered so far. Do not call any more tools.",
	})
	wrapSniff := &streamSniffer{
		emit:     func(s string) { ctx.Emit("token", map[string]interface{}{"content": s}) },
		prefixes: pseudoToolCallPrefixes,
	}
	resp, err := client.ChatCompletionStreaming(reqCtx, messages, func(chunk string) {
		wrapSniff.onChunk(chunk)
	}, func(chunk string) {
		ctx.Emit("reasoning", map[string]interface{}{"content": chunk})
	})
	if err != nil {
		return nil, err
	}
	wrapSniff.flush()
	totalCost += resp.CostUSD

	answerText := resp.Content
	if calls := parsePseudoToolCalls(resp.Content); len(calls) > 0 {
		// Even told explicitly not to, the model tried to call a tool one
		// more time — it genuinely doesn't have enough to answer yet, and
		// there's no turn budget left to give it. An honest "couldn't
		// finish in time" beats showing raw pseudo-tool-call syntax.
		//
		// resp.Content (the aborted attempt) was already streamed live as
		// "token" via wrapSniff above — emitCommentary demotes it into the
		// timeline and clears the frontend's answer buffer first, so the
		// replacement message below starts clean instead of concatenating
		// onto the abandoned attempt (see emitCommentary's doc comment).
		emitCommentary(ctx, resp.Content)
		answerText = "I wasn't able to finish researching this in time to give a complete answer — " +
			"try asking again, or narrow the question a bit."
		ctx.Emit("token", map[string]interface{}{"content": answerText})
	}

	return &Result{
		Answer:        answerText,
		Citations:     ctx.Citations,
		CostUSD:       totalCost,
		ContextTokens: resp.PromptTokens + resp.CompletionTokens,
	}, nil
}

// emitCommentary sends whatever a turn said before deciding to call a tool
// (or before an aborted pseudo-tool-call attempt gets discarded) as its
// own "commentary" event, carrying the full text — not just a bare
// marker — so the frontend can persist and reconstruct it on reload the
// same way it already does for "thinking"/"reasoning" events.
//
// This content was ALREADY streamed live, chunk by chunk, as "token"
// events during generation (see the onChunk callback above/below) — that
// live-typing effect is intentional and shared with the true final
// answer, since at stream time there's no way to know in advance whether
// a given turn will end in tool_calls or not. The frontend's "commentary"
// handler (state.svelte.ts) clears whatever it had accumulated from that
// live stream and replaces it with this event's authoritative content as
// a distinct timeline item, positioned in true chronological order
// relative to the tool_call/tool_result/thinking items around it —
// instead of what used to happen: every turn's content silently piling
// into one flat answer string, so a multi-tool-call question read as
// "let me search... let me try another angle... here's the answer" all
// concatenated at the very end, in generation order but with no visual
// separation from the real answer. No-ops on empty content (a turn that
// went straight to a tool call with nothing said first).
func emitCommentary(ctx *tools.Context, content string) {
	if content == "" {
		return
	}
	ctx.Emit("commentary", map[string]interface{}{"content": content})
}

// toolCallResult pairs a tool call with the string tools.Dispatch returned
// for it, so results can be matched back up to their ToolCallID after
// running concurrently and in unpredictable completion order.
type toolCallResult struct {
	call   llm.ToolCall
	result string
}

// dispatchToolCallsConcurrently runs every tool call from one model turn
// in parallel — three independent web_search calls the model queued up in
// the same turn all fire their outbound requests at once instead of
// waiting for each to finish before the next starts. Results are returned
// in the same order as calls regardless of which finishes first, so the
// message history Run builds from them stays deterministic across runs
// even though the underlying dispatch order isn't.
//
// Each call runs in its own goroutine with its own recover: ws.go's panic
// recovery wraps the whole turn's goroutine, which protects that
// goroutine's own call stack, but NOT goroutines spawned underneath it —
// an unrecovered panic in any goroutine crashes the whole process
// regardless of a recover() elsewhere. Without this, one tool handler
// panicking (a bug three calls deep) would take down every other in-flight
// thread on the server instead of just failing that one call.
func dispatchToolCallsConcurrently(calls []llm.ToolCall, ctx *tools.Context) []toolCallResult {
	results := make([]toolCallResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call llm.ToolCall) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[i] = toolCallResult{call: call, result: fmt.Sprintf("error: internal error running %s", call.Function.Name)}
				}
			}()
			results[i] = toolCallResult{call: call, result: tools.Dispatch(call.Function.Name, call.Function.Arguments, ctx)}
		}(i, call)
	}
	wg.Wait()
	return results
}
