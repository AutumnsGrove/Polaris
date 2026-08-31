// Package agent implements the tool-use loop: alternate between the
// model calling think/web_search/web_read and the model answering
// directly, until it produces a plain-text final answer (no reply
// signal tool — see tools.Defs for why).
package agent

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"polaris/llm"
	"polaris/logger"
	"polaris/prompts"
	"polaris/tools"
)

var log = logger.WithPrefix("agent")

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
	return name == "web_search" || name == "web_read" || name == "nearby_search" || name == "youtube_transcript" ||
		name == "weather" || name == "reference_lookup" || name == "github_repo" || name == "dictionary" ||
		name == "music"
}

// researchCheckInMessage nudges the model to consider answering instead
// of continuing to search, grounded in what it's actually gathered
// (citation count) rather than a vague "are you sure?" — mirrors the
// literature's finding that structured, externally-grounded check-ins
// beat asking a model to self-assess confidence in free text. Wording
// lives in prompts.yaml (agent.research_check_in) — see that file to
// tune it without a rebuild.
func researchCheckInMessage(citationCount, callCount int) string {
	return fmt.Sprintf(prompts.Get().Agent.ResearchCheckIn, callCount, citationCount)
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
// rationalize past with "just one more search." Wording lives in
// prompts.yaml (agent.stale_streak_warning).
func staleStreakMessage(streak, citationCount int) string {
	return fmt.Sprintf(prompts.Get().Agent.StaleStreakWarning, streak, citationCount)
}

// trackResearchCall updates the running research-call/citation-novelty
// state after a single research tool dispatch and returns whichever
// steering message(s) are warranted. The two signals are deliberately
// independent and can both fire on the same call — one measures elapsed
// effort (checkInInterval), the other measures actual productivity
// (citation growth via staleThreshold) — so neither resets or suppresses
// the other; a call that's both the 5th research call AND the 2nd stale
// one in a row genuinely warrants both nudges.
//
// checkInInterval/staleThreshold are parameters, not the package
// constants directly, so Run can widen them for Deep Research mode (see
// its doc comment) without a second copy of this function.
//
// Takes ctx (not just citations) so each fired nudge can also be emitted
// as a durable "agent_nudge" event (see emitNudge) — otherwise the only
// trace of researchCheckInInterval/staleStreakThreshold actually firing
// is the synthetic message baked into that one LLM call's request body,
// gone the moment the turn finishes. Without that record there's no way
// to tell, from real usage, whether those constants are tuned well.
func trackResearchCall(ctx *tools.Context, researchCalls, lastCitationCount, staleStreak *int, checkInInterval, staleThreshold int) []string {
	*researchCalls++
	current := len(ctx.Citations)

	var nudges []string
	if current > *lastCitationCount {
		*staleStreak = 0
	} else {
		*staleStreak++
		if *staleStreak >= staleThreshold {
			nudges = append(nudges, staleStreakMessage(*staleStreak, current))
			emitNudge(ctx, "stale_streak", *researchCalls, current)
			*staleStreak = 0 // fire once per streak, not every call past the threshold
		}
	}
	*lastCitationCount = current

	if *researchCalls%checkInInterval == 0 {
		nudges = append(nudges, researchCheckInMessage(current, *researchCalls))
		emitNudge(ctx, "check_in", *researchCalls, current)
	}
	return nudges
}

// emitNudge persists a durable record of a research-steering signal
// firing — kind is "check_in", "stale_streak", or "max_turns_wrapup" (see
// trackResearchCall and Run's turn-budget exhaustion branch). Consumed by
// store.Store.GetStats to answer "how often does each signal actually
// fire, and against how many tool calls" from real usage.
func emitNudge(ctx *tools.Context, kind string, callCount, citationCount int) {
	ctx.Emit("agent_nudge", map[string]interface{}{
		"args": map[string]interface{}{
			"kind":           kind,
			"call_count":     callCount,
			"citation_count": citationCount,
		},
	})
}

// promptPath is read fresh on every turn — no recompiling to change how
// Polaris behaves. Matches her-go's convention of hot-reloaded prompt
// files living as plain text in the working directory. Kept as its own
// plain-text file rather than folded into prompts.yaml alongside every
// other prompt fragment: it's long-form personality/identity prose meant
// to be written and pasted freely, and YAML string-escaping would make
// that materially more annoying to edit for no benefit.
const promptPath = "prompt.md"

// FocusMode values — mirrored from web/src/lib/types.ts's FocusMode union
// (minus "off", which just means "no focus mode instruction added"); keep
// both in sync by hand. Also mirrored by prompts.yaml's agent.focus_modes
// keys — see loadSystemPrompt.
const (
	FocusModeBrief           = "brief"
	FocusModeAcademic        = "academic"
	FocusModeNews            = "news"
	FocusModeFirstPrinciples = "first_principles"
	FocusModeSocratic        = "socratic"
)

// loadSystemPrompt reads prompt.md fresh every call — edit the file,
// see the change on your very next message, no rebuild or restart. The
// voice/focus-mode/deep-research additions come from prompts.yaml (via
// prompts.Get(), itself cached and hot-reloaded the same way) rather than
// being hardcoded here — pure prompt engineering, no change to the
// tool-use loop itself. "brief" deliberately only changes the FINAL
// answer's style, not the research loop — the composer describes it as
// "same research, shorter replies", and that's exactly what this does:
// Run's turn-budget/research-check-in logic never reads FocusMode, only
// this function's output differs.
func loadSystemPrompt(ctx *tools.Context, voiceMode bool, focusMode string, deepResearch bool) string {
	p := prompts.Get()

	data, err := os.ReadFile(promptPath)
	prompt := p.Agent.FallbackSystemPrompt
	if err == nil {
		prompt = string(data)
	} else if !os.IsNotExist(err) {
		// A missing prompt.md is the expected first-run state and not
		// worth logging every turn — but any other error (permissions, a
		// directory where the file should be, disk trouble) means every
		// turn from here on silently runs on the generic fallback prompt
		// instead of the operator's actual configured behavior, with
		// nothing to explain why.
		log.Warn("failed to read prompt.md, using fallback system prompt", "err", err)
	}
	prompt = applyToolsPlaceholder(prompt, ctx)
	if voiceMode {
		prompt += "\n\n" + p.Agent.VoiceModeInstruction
	}
	if instr, ok := p.Agent.FocusModes[focusMode]; ok {
		prompt += "\n\n" + instr
	}
	if deepResearch {
		prompt += "\n\n" + p.Agent.DeepResearchInstruction
	}
	return prompt
}

// applyToolsPlaceholder replaces every "{tools}" occurrence in prompt with
// tools.ToolsPrompt(ctx) — the one substitution point all three system-prompt
// sources (prompt.md, prompts.yaml's fallback_system_prompt, buildDefaults()'s
// Go literal) funnel through, so whichever one loadSystemPrompt picked, the
// rendered tool list is identical.
func applyToolsPlaceholder(prompt string, ctx *tools.Context) string {
	return strings.ReplaceAll(prompt, "{tools}", tools.ToolsPrompt(ctx))
}

// deepResearchTurnMultiplier/deepResearchCheckInMultiplier scale up the
// turn budget and how rarely the check-in/stale-streak nudges fire when
// Deep Research is on — the nudges exist to stop a model from searching
// past the point of diminishing returns (see researchCheckInInterval's
// doc comment), which is exactly the behavior Deep Research is asking
// for more of, not less.
const deepResearchTurnMultiplier = 2
const deepResearchCheckInMultiplier = 2

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
	Cards     []tools.Card
	CostUSD   float64
	// ContextTokens is the prompt+completion token count of the LAST LLM
	// call this turn made — the best available estimate of how much
	// context this thread now occupies, since it reflects every message,
	// tool result, and reasoning pass accumulated through the whole loop.
	ContextTokens int
	// PendingQuestion is non-nil when the turn ended early because
	// ask_user_question was called — Answer holds the literal question
	// text (so it reads naturally as this turn's assistant reply), and
	// the caller (gateway.handleTurn) should persist this alongside the
	// message and skip anything that assumes the turn produced a normal
	// finished answer (follow-up suggestions, most notably).
	PendingQuestion *tools.PendingQuestion
	// TurnCount is how many iterations of the main loop below actually
	// ran before this Result was produced (1 for a plain first-turn
	// answer, more for each tool-call round-trip) — including the forced
	// wrap-up call, if the loop reached it. Primarily for
	// cmd/benchmark.go's per-question tracking (see benchmark/tracking.go);
	// not otherwise consumed today.
	TurnCount int
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
	warmUpEmbedClient(ctx)

	messages := make([]llm.ChatMessage, 0, len(history)+2)
	messages = append(messages, llm.ChatMessage{Role: "system", Content: currentContextPreamble() + loadSystemPrompt(ctx, ctx.VoiceMode, ctx.FocusMode, ctx.DeepResearch)})
	messages = append(messages, history...)
	messages = append(messages, llm.ChatMessage{Role: "user", Content: userMessage})

	toolDefs := tools.Defs(ctx)
	var totalCost float64
	var answer strings.Builder

	maxTurns := ctx.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	checkInInterval := researchCheckInInterval
	staleThreshold := staleStreakThreshold
	if ctx.DeepResearch {
		maxTurns *= deepResearchTurnMultiplier
		checkInInterval *= deepResearchCheckInMultiplier
		staleThreshold *= deepResearchCheckInMultiplier
	}
	researchCalls := 0
	lastCitationCount := 0
	staleStreak := 0
	queryTracker := searchQueryTracker{}

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
						for _, nudge := range trackResearchCall(ctx, &researchCalls, &lastCitationCount, &staleStreak, checkInInterval, staleThreshold) {
							messages = append(messages, llm.ChatMessage{Role: "user", Content: nudge})
						}
					}
					if pc.name == "web_search" {
						if q := extractSearchQuery(pc.argsJSON); q != "" {
							if nudge, fired := trackSearchQuery(ctx, &queryTracker, q); fired {
								messages = append(messages, llm.ChatMessage{Role: "user", Content: nudge})
							}
						}
					}
				}
				continue
			}
			if strings.TrimSpace(resp.Content) == "" {
				// No tool call AND no answer text — the model spent this
				// whole turn on private reasoning (visible only via
				// onReasoning, never onChunk) without ever committing to
				// output, and stopped before producing anything. Returning
				// this as a "final answer" would silently hand back an
				// empty Result with a nil error — indistinguishable from a
				// real, considered response. Nudge and let it try again
				// instead; bounded by maxTurns like every other branch of
				// this loop, so a model that keeps doing this still
				// terminates via the wrap-up path below rather than
				// looping forever.
				emitNudge(ctx, "empty_answer", researchCalls, len(ctx.Citations))
				messages = append(messages, llm.ChatMessage{Role: "user", Content: prompts.Get().Agent.EmptyAnswerRetry})
				continue
			}
			// Plain content = the final answer. It was already streamed
			// token-by-token via the onChunk callback above.
			return &Result{
				Answer:        resp.Content,
				Citations:     ctx.Citations,
				Cards:         ctx.Cards,
				CostUSD:       totalCost,
				ContextTokens: resp.PromptTokens + resp.CompletionTokens,
				TurnCount:     turn + 1,
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

		// ask_user_question was called — end the turn now instead of
		// looping back to the model. See tools.PendingQuestion's doc
		// comment for why this is a clean early return rather than a
		// blocking wait: the answer is just whatever message the user
		// sends next, handled by a brand-new Run call on the next turn,
		// not by anything still alive in this goroutine.
		if ctx.PendingQuestion != nil {
			// The question text never streamed as "token" chunks the way a
			// normal final answer does — it came from the tool call's
			// arguments (a different part of the SSE stream), not a content
			// completion onChunk ever saw. Without this, a live session's
			// turn.content stays empty until the next reload re-reads it
			// from the persisted message instead — the question would be
			// invisible in the very session that just asked it.
			ctx.Emit("token", map[string]interface{}{"content": ctx.PendingQuestion.Question})
			return &Result{
				Answer:          ctx.PendingQuestion.Question,
				Citations:       ctx.Citations,
				Cards:           ctx.Cards,
				CostUSD:         totalCost,
				ContextTokens:   resp.PromptTokens + resp.CompletionTokens,
				TurnCount:       turn + 1,
				PendingQuestion: ctx.PendingQuestion,
			}, nil
		}

		for _, r := range results {
			if isResearchTool(r.call.Function.Name) {
				for _, nudge := range trackResearchCall(ctx, &researchCalls, &lastCitationCount, &staleStreak, checkInInterval, staleThreshold) {
					messages = append(messages, llm.ChatMessage{Role: "user", Content: nudge})
				}
			}
			if r.call.Function.Name == "web_search" {
				if q := extractSearchQuery(r.call.Function.Arguments); q != "" {
					if nudge, fired := trackSearchQuery(ctx, &queryTracker, q); fired {
						messages = append(messages, llm.ChatMessage{Role: "user", Content: nudge})
					}
				}
			}
		}
	}

	// Ran out of turns — force a wrap-up instead of failing outright. No
	// more tool dispatching allowed past this point (that's the whole
	// point of the bound), so if the model still tries to emit a pseudo
	// tool call here, that's treated as "couldn't produce a real answer
	// in time" rather than given yet another turn.
	//
	// Recorded as a nudge event too (see emitNudge) — this firing often is
	// the strongest possible signal that maxTurns is tuned too low for
	// what's actually being asked.
	emitNudge(ctx, "max_turns_wrapup", researchCalls, len(ctx.Citations))
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
	// Deliberately no empty-answerText fallback here (unlike the main
	// loop's mid-turn retry above) — this is the last LLM call Run makes,
	// so there's no budget left to nudge-and-retry, and callers already
	// have their own contract for a genuinely empty Result.Answer:
	// gateway/turn.go's handleTurn checks for it and surfaces a proper
	// "error" event plus a warn-level log instead of persisting a blank
	// assistant turn (see TestWebSocket_EmptyAnswerSurfacesAsErrorEvent).
	// Rewriting it into placeholder text here would silently defeat that
	// downstream check for every caller, not just the ones lacking it.

	return &Result{
		Answer:        answerText,
		Citations:     ctx.Citations,
		Cards:         ctx.Cards,
		CostUSD:       totalCost,
		ContextTokens: resp.PromptTokens + resp.CompletionTokens,
		TurnCount:     maxTurns + 1, // every loop iteration ran, plus this forced wrap-up call
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
					log.Error("panic in tool dispatch", "tool", call.Function.Name, "panic", r, "stack", string(debug.Stack()))
					results[i] = toolCallResult{call: call, result: fmt.Sprintf("error: internal error running %s", call.Function.Name)}
				}
			}()
			results[i] = toolCallResult{call: call, result: tools.Dispatch(call.Function.Name, call.Function.Arguments, ctx)}
		}(i, call)
	}
	wg.Wait()
	return results
}
