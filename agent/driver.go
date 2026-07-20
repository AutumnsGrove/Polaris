// Package agent implements the tool-use loop: alternate between the
// model calling think/web_search/web_read and the model answering
// directly, until it produces a plain-text final answer (no reply
// signal tool — see tools.Defs for why).
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"polaris/llm"
	"polaris/tools"
)

// defaultMaxTurns is used when a caller doesn't set ctx.MaxTurns — the
// real value normally comes from config.Config.MaxAgentTurns, configurable
// so this can be raised without a rebuild if a model needs more room.
const defaultMaxTurns = 50

// promptPath is read fresh on every turn — no recompiling to change how
// Polaris behaves. Matches her-go's convention of hot-reloaded prompt
// files living as plain text in the working directory.
const promptPath = "prompt.md"

// fallbackSystemPrompt is used only if prompt.md is missing, so a fresh
// clone still works before the user copies prompt.md.example into place.
const fallbackSystemPrompt = `You are Polaris, a private, self-hosted research assistant. You have four tools:

- think: reason privately about strategy before acting.
- web_search: search the web via a private SearXNG instance.
- web_read: fetch a URL and extract its content (optionally filtered to just what's needed).
- nearby_search: find real-world places (restaurants, pharmacies, etc.) near a location.

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

// Some providers emit a tool call as literal text in the content field
// instead of populating the API's structured tool_calls array — the model
// was clearly trained on some ReAct/agent-framework XML tool-call
// convention and falls back to writing it out as prose when the
// provider's function-calling translation doesn't intercept it. Without
// detecting this, that raw XML gets treated as the final answer and shown
// to the user verbatim. Two distinct formats observed in practice so far,
// from two different providers — pseudoToolCallPrefixes below must list
// every format's opening tag so streamSniffer knows what to watch for.
var (
	// MiMo/Qwen-Agent style: <tool_call><function=NAME><parameter=KEY>VAL</parameter>...</function></tool_call>
	mimoToolCallRe = regexp.MustCompile(`(?s)<tool_call>\s*<function=([^>]+)>(.*?)</function>\s*</tool_call>`)
	mimoParamRe    = regexp.MustCompile(`(?s)<parameter=([^>]+)>(.*?)</parameter>`)

	// DeepSeek's "DSML" style: one <tool_calls> block can contain several
	// <invoke name="NAME">...</invoke> entries, each answered separately.
	dsmlInvokeRe = regexp.MustCompile(`(?s)<｜｜DSML｜｜invoke name="([^"]+)">(.*?)</｜｜DSML｜｜invoke>`)
	dsmlParamRe  = regexp.MustCompile(`(?s)<｜｜DSML｜｜parameter name="([^"]+)"[^>]*>(.*?)</｜｜DSML｜｜parameter>`)
)

// pseudoToolCallPrefixes is what streamSniffer buffers up to before
// deciding whether a response is about to be one of the formats above —
// every prefix here must be a literal opening tag from a regex above.
var pseudoToolCallPrefixes = []string{"<tool_call>", "<｜｜DSML｜｜"}

type pseudoCall struct {
	name     string
	argsJSON string
}

// buildArgsJSON turns key/value string pairs into the same JSON shape a
// real tool call's Function.Arguments would be. Values that parse as
// integers are kept numeric (not stringified) since tool arg structs like
// web_search's max_results expect a JSON number, not a numeric string.
func buildArgsJSON(pairs [][2]string) (string, bool) {
	args := make(map[string]interface{}, len(pairs))
	for _, kv := range pairs {
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if n, err := strconv.Atoi(val); err == nil {
			args[key] = n
		} else {
			args[key] = val
		}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// parsePseudoToolCalls tries each known fallback format in turn and
// returns every call found in the first one that matches. DSML blocks can
// contain multiple invokes (a model batching several tool calls at once
// in text form since it has no structured field to put them in); the
// MiMo format has only ever been observed with one call per block.
func parsePseudoToolCalls(content string) []pseudoCall {
	if m := mimoToolCallRe.FindStringSubmatch(content); m != nil {
		name := strings.TrimSpace(m[1])
		if name == "" {
			return nil
		}
		var pairs [][2]string
		for _, p := range mimoParamRe.FindAllStringSubmatch(m[2], -1) {
			pairs = append(pairs, [2]string{p[1], p[2]})
		}
		argsJSON, ok := buildArgsJSON(pairs)
		if !ok {
			return nil
		}
		return []pseudoCall{{name: name, argsJSON: argsJSON}}
	}

	var calls []pseudoCall
	for _, inv := range dsmlInvokeRe.FindAllStringSubmatch(content, -1) {
		name := strings.TrimSpace(inv[1])
		if name == "" {
			continue
		}
		var pairs [][2]string
		for _, p := range dsmlParamRe.FindAllStringSubmatch(inv[2], -1) {
			pairs = append(pairs, [2]string{p[1], p[2]})
		}
		argsJSON, ok := buildArgsJSON(pairs)
		if !ok {
			continue
		}
		calls = append(calls, pseudoCall{name: name, argsJSON: argsJSON})
	}
	return calls
}

// streamSniffer buffers the first chunks of a streamed response just long
// enough to tell whether it's about to be one of pseudoToolCallPrefixes —
// once resolved, either forwards chunks live as normal (the common case)
// or stays silent because the caller will parse and dispatch it as a tool
// call instead, so the raw pseudo-syntax never flashes on screen.
type streamSniffer struct {
	emit     func(string)
	prefixes []string
	buf      strings.Builder
	resolved bool
	isPseudo bool
}

func (s *streamSniffer) maxPrefixLen() int {
	max := 0
	for _, p := range s.prefixes {
		if len(p) > max {
			max = len(p)
		}
	}
	return max
}

func (s *streamSniffer) resolve() {
	s.resolved = true
	buf := s.buf.String()
	for _, p := range s.prefixes {
		if strings.HasPrefix(buf, p) {
			s.isPseudo = true
			return
		}
	}
	s.emit(buf)
}

func (s *streamSniffer) onChunk(chunk string) {
	if s.resolved {
		if !s.isPseudo {
			s.emit(chunk)
		}
		return
	}
	s.buf.WriteString(chunk)
	if s.buf.Len() < s.maxPrefixLen() {
		return
	}
	s.resolve()
}

// flush handles a response that ended before enough chunks arrived to
// resolve — a very short plain answer, most likely. Whatever's buffered
// clearly can't be a full pseudo-tool-call block at that point, so it's
// real content that still needs to reach the user.
func (s *streamSniffer) flush() {
	if s.resolved {
		return
	}
	s.resolve()
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
				for _, pc := range calls {
					result := tools.Dispatch(pc.name, pc.argsJSON, ctx)
					messages = append(messages, llm.ChatMessage{
						Role: "user",
						Content: fmt.Sprintf("[%s result]\n%s\n\nContinue answering the original question using this — "+
							"use the real tool-calling mechanism if you need to search again, not text-formatted "+
							"tool call syntax.", pc.name, result),
					})
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

		call := resp.ToolCalls[0]
		messages = append(messages, llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{call}})
		result := tools.Dispatch(call.Function.Name, call.Function.Arguments, ctx)
		messages = append(messages, llm.ChatMessage{Role: "tool", Content: result, ToolCallID: call.ID})
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
