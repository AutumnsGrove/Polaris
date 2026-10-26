package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	novaagent "github.com/AutumnsGrove/nova/agent"
	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/proto"
	"github.com/AutumnsGrove/nova/provider"

	"polaris/llm"
	"polaris/prompts"
	"polaris/tools"
)

// This file is Polaris's half of the Nova adoption (docs/plans/nova-loop.md):
// agent.Run's contract and Result are unchanged, but the request/tool/retry
// loop underneath is Nova's, with Polaris's behaviour (research nudges,
// pseudo-tool-call recovery, early exits, the wrap-up text) expressed as
// Nova hooks instead of a second hand-rolled loop.

// novaHookTimeout bounds every Nova hook. Nova's default is 5s, which is far
// too short for the TurnEnd hook here: it dispatches pseudo tool calls
// (a web_read can take a minute) and a timed-out hook is abandoned silently,
// which would end the run with no answer.
const novaHookTimeout = 10 * time.Minute

// novaMaxParallel is the per-batch concurrency cap. The bespoke loop had none;
// Nova defaults to 8, which a single model turn essentially never exceeds, but
// a Pulsar Daily block can, so keep it comfortably above any realistic batch.
const novaMaxParallel = 32

// novaRunState is the per-run mutable state the hooks and the event handler
// share — the same lifetime as the bespoke loop's locals (plan §5.5). It is
// rebuilt on every runWithNova call, never cached.
type novaRunState struct {
	ctx      *tools.Context
	maxTurns int

	checkInInterval int
	staleThreshold  int

	researchCalls     int
	lastCitationCount int
	staleStreak       int
	queryTracker      searchQueryTracker

	// Accumulated from the per-call usage events, so they cover the loop's own
	// model calls only — a tool's side call has its own unrelated prefix and
	// reports through ctx.AddCost (issue #107, same rule as the bespoke loop).
	totalCost            float64
	promptTokens         int
	cacheReadTokens      int
	completionTokens     int
	lastPromptTokens     int
	lastCompletionTokens int
	llmCalls             int

	// lastMsg is the assistant message of the call in flight, captured from
	// message_end so the usage event that follows can decide about commentary.
	lastMsg     message.Message
	awaitUsage  bool
	wrapMsg     *message.Message // the wrap-up turn's raw assistant message
	liveText    strings.Builder  // text shown live this turn, for a graceful stop
	early       *novaEarlyExit
	sawWrapUpAt bool
}

// novaEarlyExit records which finalize/ask tool ended the turn, and the text
// that stands in for the answer.
type novaEarlyExit struct {
	answer string
}

func runWithNova(reqCtx context.Context, ctx *tools.Context, history []llm.ChatMessage, userMessage string) (*Result, error) {
	ctx.Ctx = reqCtx
	warmUpEmbedClient(ctx)
	if ctx.LLM == nil {
		return nil, errors.New("agent: no LLM client configured")
	}

	system := currentContextPreamble() + loadSystemPrompt(ctx, ctx.VoiceMode, ctx.FocusMode, ctx.DeepResearch, ctx.NoResearch)

	// The turn's own messages: the user message, then the mode reinforcement
	// near the end (see runBespoke and modeReinforcement). Nova's Agent.Run
	// would append its prompt last, which would put the reinforcement before
	// the user message, so the whole conversation goes in as history and Nova's
	// prompt-less entry point is used.
	turnPrefix := []llm.ChatMessage{{Role: "user", Content: userMessage}}
	if len(history) > 0 {
		if reinforcement := modeReinforcement(prompts.Get(), ctx.VoiceMode, ctx.FocusMode); reinforcement != "" {
			turnPrefix = append(turnPrefix, llm.ChatMessage{Role: "user", Content: reinforcement})
		}
		if ctx.OracleSection != "" {
			turnPrefix = append(turnPrefix, llm.ChatMessage{Role: "user", Content: ctx.OracleSection})
		}
	}
	conversation := make([]llm.ChatMessage, 0, len(history)+len(turnPrefix))
	conversation = append(conversation, history...)
	conversation = append(conversation, turnPrefix...)

	maxTurns := ctx.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	st := &novaRunState{ctx: ctx, checkInInterval: researchCheckInInterval, staleThreshold: staleStreakThreshold}
	if ctx.DeepResearch || ctx.FocusMode == FocusModeResearcher {
		maxTurns *= deepResearchTurnMultiplier
		st.checkInInterval *= deepResearchCheckInMultiplier
		st.staleThreshold *= deepResearchCheckInMultiplier
	}
	st.maxTurns = maxTurns

	defs := tools.Defs(ctx)
	novaDefs := toNovaDefs(defs)
	prov, model := novaProviderFor(ctx.LLM)
	temp := model.Temperature
	promptCache := model.PromptCache

	cfg := novaagent.Config{
		Provider:        prov,
		Model:           model.Name,
		System:          system,
		Tools:           novaToolRegistry(defs),
		ToolData:        ctx,
		MaxTurns:        maxTurns,
		MaxParallel:     novaMaxParallel,
		Temperature:     &temp,
		MaxTokens:       model.MaxTokens,
		ProviderOptions: model.Options,
		PromptCache:     &promptCache,
		Retry:           novaagent.RetryPolicy{Max: novaRetry.Max, Base: novaRetry.Base, Jitter: -1},
		HookTimeout:     novaHookTimeout,
		RequestTimeout:  novaRequestTimeout,
		WrapUp:          &novaagent.WrapUp{Instruction: wrapUpInstruction, Fallback: wrapUpFallback},
		TextFilter:      newPseudoCallFilter,
		Emit:            st.onEvent,
		Hooks: novaagent.Hooks{
			// The model must see Polaris's own tool schemas, not the permissive
			// ones the registry validates against (see polarisTool).
			TransformContext:      func(_ context.Context, msgs []message.Message) []message.Message { return dropUnsendable(msgs) },
			BeforeProviderRequest: func(_ context.Context, req *provider.Request) { shapeToolRequest(req, novaDefs) },
			TurnEnd:               st.turnEnd,
		},
	}
	if ctx.Multimodal {
		cfg.InputModalities = []string{"text", "image"}
	}

	res, err := novaagent.Run(reqCtx, cfg, toNovaMessages(conversation))
	return st.build(reqCtx, res, err, turnPrefix, prov)
}

// shapeToolRequest makes a tool-offering request match llm.Client's wire body.
//
// The model must see Polaris's own tool schemas, not the permissive ones the
// registry validates against (see polarisTool). tool_choice "auto" and
// parallel_tool_calls true are sent explicitly, not left to the provider's
// default: the bespoke client documents that as deliberate, and the parity
// goldens caught Nova omitting both. They are only valid alongside tools, so
// the wrap-up request — which Nova builds with none — must not carry them; at
// this hook that is exactly the case where req.Tools is still empty (Nova also
// omits parallel_tool_calls from a request with no tools).
func shapeToolRequest(req *provider.Request, defs []provider.ToolDef) {
	if len(req.Tools) == 0 {
		return
	}
	req.Tools = defs
	req.ToolChoice = "auto"
	yes := true
	req.ParallelToolCalls = &yes
}

// build maps Nova's result back into Polaris's, whatever way the run ended.
func (s *novaRunState) build(reqCtx context.Context, res *novaagent.Result, err error, turnPrefix []llm.ChatMessage, prov *polarisProvider) (*Result, error) {
	ctx := s.ctx
	out := &Result{
		Citations:        ctx.Citations,
		Cards:            ctx.Cards,
		Chart:            ctx.Chart,
		CostUSD:          s.totalCost + ctx.ExtraCostUSD,
		ResearchCalls:    s.researchCalls,
		PromptTokens:     s.promptTokens,
		CacheReadTokens:  s.cacheReadTokens,
		CompletionTokens: s.completionTokens,
		LastPromptTokens: s.lastPromptTokens,
		LLMCalls:         s.llmCalls,
	}
	var newMsgs []message.Message
	if res != nil {
		out.TurnCount = res.Turns
		newMsgs = res.New
	}

	aborted := res != nil && res.Reason == "aborted"
	failed := err != nil && !aborted
	switch {
	case failed:
		// Cost and partial state still come back with the error, like the
		// bespoke loop: real, billed calls happened.
		out.Answer = ""
	case aborted:
		// A stop is a graceful early finish, not an error (Run's doc comment):
		// whatever streamed live is the answer.
		out.Answer = s.liveText.String()
		out.ContextTokens = s.lastPromptTokens + s.lastCompletionTokens
	default:
		out.Answer = res.Final
		if w := s.wrapMsg; w != nil {
			switch {
			case len(w.ToolCalls) == 0 && strings.TrimSpace(w.Content) == "":
				// Deliberately no empty-answer fallback after the wrap-up (see
				// runBespoke): gateway's "model returned no answer" check owns
				// that case, and Nova's own fallback text would defeat it.
				out.Answer = ""
			case len(w.ToolCalls) == 0 && len(parsePseudoToolCalls(w.Content)) > 0:
				// Even told not to, the model wrote a tool call as text. The
				// attempt was already demoted to commentary (onEvent); an honest
				// "couldn't finish" beats showing raw pseudo-call syntax.
				out.Answer = wrapUpFallback
				ctx.Emit("token", map[string]interface{}{"content": wrapUpFallback})
			}
		}
		out.ContextTokens = s.lastPromptTokens + s.lastCompletionTokens
	}
	if s.early != nil && !failed && !aborted {
		out.Answer = s.early.answer
		out.WizardFinal = ctx.WizardFinal
		out.DailyItemsFinal = ctx.DailyItemsFinal
		out.TrailClassification = ctx.TrailClassification
		out.PendingQuestion = ctx.PendingQuestion
		// Only the structured field that actually ended the turn is set, as in
		// the bespoke loop's separate return statements.
		switch {
		case ctx.WizardFinal != nil:
			out.DailyItemsFinal, out.TrailClassification, out.PendingQuestion = nil, nil, nil
		case ctx.DailyItemsFinal != nil:
			out.WizardFinal, out.TrailClassification, out.PendingQuestion = nil, nil, nil
		case ctx.TrailClassification != nil:
			out.WizardFinal, out.DailyItemsFinal, out.PendingQuestion = nil, nil, nil
		default:
			out.WizardFinal, out.DailyItemsFinal, out.TrailClassification = nil, nil, nil
		}
	}

	// The transcript gateway persists and replays byte-for-byte next turn: the
	// turn's own prefix, everything Nova recorded, and the final answer as a
	// closing assistant message. A trailing plain assistant message from Nova
	// is that same answer (or the wrap-up's raw attempt), so it is replaced
	// rather than duplicated.
	if n := len(newMsgs); n > 0 && newMsgs[n-1].Role == message.RoleAssistant && len(newMsgs[n-1].ToolCalls) == 0 {
		newMsgs = newMsgs[:n-1]
	}
	newMsgs = dropUnsendable(newMsgs)
	transcript := make([]llm.ChatMessage, 0, len(turnPrefix)+len(newMsgs)+1)
	transcript = append(transcript, turnPrefix...)
	transcript = append(transcript, fromNovaMessages(newMsgs)...)
	out.Transcript = append(transcript, llm.ChatMessage{Role: "assistant", Content: out.Answer})

	if failed {
		return out, prov.translateRunError(err)
	}
	return out, nil
}

// dropUnsendable removes the assistant messages Nova records that the bespoke
// loop never kept: an empty one (the empty-answer retry; Nova no longer sends it
// either, but the persisted transcript must not keep it) and a pseudo-tool-call
// attempt, whose result the loop replays as a user message instead. Applied to
// the wire request and to the persisted transcript, so the next turn replays
// exactly what the bespoke loop would have stored.
func dropUnsendable(msgs []message.Message) []message.Message {
	out := make([]message.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == message.RoleAssistant && len(m.ToolCalls) == 0 && len(m.Parts) == 0 &&
			(strings.TrimSpace(m.Content) == "" || len(parsePseudoToolCalls(m.Content)) > 0) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// onEvent maps Nova's events onto Polaris's live event vocabulary (plan §5.4).
// Nova's own tool_call/tool_start/tool_end are dropped on purpose: each Polaris
// tool already emits its own tool_call/tool_result with the call_id, and
// forwarding both would draw every card twice.
func (s *novaRunState) onEvent(e proto.Event) {
	ctx := s.ctx
	switch e.Type {
	case proto.EventTurnStart:
		s.liveText.Reset()
		if e.Turn != nil && *e.Turn >= s.maxTurns && !s.sawWrapUpAt {
			// The turn index only reaches maxTurns for the forced wrap-up, the
			// strongest signal that the budget is tuned too low.
			s.sawWrapUpAt = true
			emitNudge(ctx, "max_turns_wrapup", s.researchCalls, len(ctx.Citations))
		}
	case proto.EventTextDelta:
		s.liveText.WriteString(e.Text)
		ctx.Emit("token", map[string]interface{}{"content": e.Text})
	case proto.EventThinkingDelta:
		ctx.Emit("reasoning", map[string]interface{}{"content": e.Text})
	case proto.EventMessageEnd:
		if e.Message != nil {
			s.lastMsg = *e.Message
			s.awaitUsage = true
		}
	case proto.EventUsage:
		if !s.awaitUsage || e.Usage == nil {
			return // usage a tool reported itself, not one of the loop's own calls
		}
		s.awaitUsage = false
		u := e.Usage
		s.totalCost += u.CostUSD
		s.promptTokens += u.PromptTokens
		s.cacheReadTokens += u.CachedTokens
		s.completionTokens += u.CompletionTokens
		s.lastPromptTokens = u.PromptTokens
		s.lastCompletionTokens = u.CompletionTokens
		s.llmCalls++
		msg := s.lastMsg
		wrapUp := e.Turn != nil && *e.Turn >= s.maxTurns
		if wrapUp {
			m := msg
			s.wrapMsg = &m
		}
		// Live-only running total — see runBespoke's matching emit. Not after the
		// forced wrap-up call: the bespoke loop doesn't either (the closing
		// "done" event carries the final figure), and event parity is the bar.
		if !wrapUp {
			ctx.Emit("cost_update", map[string]interface{}{"cost_usd": s.totalCost + ctx.ExtraCostUSD})
		}
		if len(msg.ToolCalls) > 0 || len(parsePseudoToolCalls(msg.Content)) > 0 {
			emitCommentary(ctx, msg.Content)
		}
	case proto.EventRetry:
		log.Warn("model call failed, retrying", "attempt", e.Attempt, "max", e.Max, "delay_ms", e.DelayMS, "err", errString(e.Error))
	case proto.EventExtensionError:
		log.Warn("nova hook failed", "hook", e.Hook, "err", errString(e.Error))
	}
}

func errString(e *proto.Error) string {
	if e == nil {
		return ""
	}
	return e.Message
}

// turnEnd is the single hook that carries every between-turn behaviour of the
// bespoke loop: pseudo-tool-call recovery, the empty-answer retry, the early
// exits, image flushing, and the research nudges (plan §5.5).
func (s *novaRunState) turnEnd(_ context.Context, t novaagent.Turn) novaagent.TurnDecision {
	if t.Index >= s.maxTurns {
		// The wrap-up turn: Nova runs the hook for observers only and ignores
		// its decision, and nothing here may dispatch past the budget.
		return novaagent.TurnDecision{}
	}
	if len(t.Message.ToolCalls) == 0 {
		return s.afterPlainMessage(t.Message)
	}
	return s.afterToolBatch(t.Message)
}

// afterPlainMessage handles an assistant message with no native tool calls.
func (s *novaRunState) afterPlainMessage(msg message.Message) novaagent.TurnDecision {
	ctx := s.ctx
	if calls := parsePseudoToolCalls(msg.Content); len(calls) > 0 {
		var inject []message.Message
		for i, pc := range calls {
			result := dispatchGuarded(pc.name, pc.argsJSON, ctx, fmt.Sprintf("pseudo-%d", i))
			inject = append(inject, message.User(fmt.Sprintf("[%s result]\n%s\n\nContinue answering the original question using this — "+
				"use the real tool-calling mechanism if you need to search again, not text-formatted "+
				"tool call syntax.", pc.name, result)))
			inject = append(inject, s.nudgesFor(pc.name, pc.argsJSON)...)
		}
		return novaagent.TurnDecision{Continue: true, Inject: inject}
	}
	if strings.TrimSpace(msg.Content) == "" {
		// No tool call AND no answer text — the model spent the turn on private
		// reasoning and stopped. Nudge and retry; bounded by the turn budget.
		emitNudge(ctx, "empty_answer", s.researchCalls, len(ctx.Citations))
		return novaagent.TurnDecision{Continue: true, Inject: []message.Message{message.User(prompts.Get().Agent.EmptyAnswerRetry)}}
	}
	return novaagent.TurnDecision{}
}

// afterToolBatch runs once every result of a native tool-call batch has been
// recorded.
func (s *novaRunState) afterToolBatch(msg message.Message) novaagent.TurnDecision {
	ctx := s.ctx
	// A handler may have just called ctx.AddCost — surface that in the same
	// running total the per-call emit uses (see runBespoke).
	ctx.Emit("cost_update", map[string]interface{}{"cost_usd": s.totalCost + ctx.ExtraCostUSD})
	// view_image "see" calls queued real image messages; flushed after every
	// tool result and before any nudge, never interleaved (wire-protocol rule).
	inject := toNovaMessages(ctx.FlushPendingImageMessages())

	// Precedence matches the bespoke loop: a finished wizard beats everything.
	switch {
	case ctx.WizardFinal != nil:
		return s.endEarly(ctx.WizardFinal.Prompt)
	case ctx.DailyItemsFinal != nil:
		return s.endEarly(flattenDailyItems(ctx.DailyItemsFinal.Items))
	case ctx.TrailClassification != nil:
		return s.endEarly("(classified this conversation)")
	case ctx.PendingQuestion != nil:
		return s.endEarly(ctx.PendingQuestion.Question)
	}

	for _, call := range msg.ToolCalls {
		inject = append(inject, s.nudgesFor(call.Function.Name, call.Function.Arguments)...)
	}
	return novaagent.TurnDecision{Inject: inject}
}

// endEarly ends the turn without another model call. The answer text never
// streamed as "token" chunks (it came from tool arguments), so it is emitted
// here — otherwise a live session's turn.content stays empty until reload.
func (s *novaRunState) endEarly(answer string) novaagent.TurnDecision {
	s.early = &novaEarlyExit{answer: answer}
	s.ctx.Emit("token", map[string]interface{}{"content": answer})
	return novaagent.TurnDecision{End: true}
}

// nudgesFor returns the steering messages one dispatched call warrants.
func (s *novaRunState) nudgesFor(name, argsJSON string) []message.Message {
	var out []message.Message
	if isResearchTool(name) {
		for _, n := range trackResearchCall(s.ctx, &s.researchCalls, &s.lastCitationCount, &s.staleStreak, s.checkInInterval, s.staleThreshold) {
			out = append(out, message.User(n))
		}
	}
	if name == "web_search" {
		if q := extractSearchQuery(argsJSON); q != "" {
			if n, fired := trackSearchQuery(s.ctx, &s.queryTracker, q); fired {
				out = append(out, message.User(n))
			}
		}
	}
	return out
}

// pseudoCallFilter is streamSniffer as a Nova StreamFilter: it holds the first
// chunks until it knows the answer is not a pseudo tool call, then releases or
// drops them. Live-only — the stored message keeps the original text, which is
// what parsePseudoToolCalls reads.
type pseudoCallFilter struct {
	sniff *streamSniffer
	out   strings.Builder
}

func newPseudoCallFilter() novaagent.StreamFilter {
	f := &pseudoCallFilter{}
	f.sniff = &streamSniffer{emit: func(s string) { f.out.WriteString(s) }, prefixes: pseudoToolCallPrefixes}
	return f
}

func (f *pseudoCallFilter) Delta(_ context.Context, text string) string {
	f.out.Reset()
	f.sniff.onChunk(text)
	return f.out.String()
}

func (f *pseudoCallFilter) Flush(_ context.Context) string {
	f.out.Reset()
	f.sniff.flush()
	return f.out.String()
}
