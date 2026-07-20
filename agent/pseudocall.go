package agent

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

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
