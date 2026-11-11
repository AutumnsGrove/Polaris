package agent

import (
	"encoding/json"
	"strings"

	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"

	"polaris/llm"
)

// The two loops speak different transcript types — llm.ChatMessage (what the
// store replays and gateway persists) and Nova's message.Message — so the
// adapter converts at the boundary and nowhere else. gateway/ never learns
// Nova's types exist (docs/plans/nova-loop.md §5.2).

const dataURLPrefix = "data:"

// toNovaMessages maps Polaris messages onto Nova's, same order. An ImageURLs
// entry that is a data: URL becomes an inline-bytes part (what view_image
// always produces); anything else is kept as a remote URL part.
func toNovaMessages(in []llm.ChatMessage) []message.Message {
	out := make([]message.Message, 0, len(in))
	for _, m := range in {
		nm := message.Message{Role: message.Role(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			nm.ToolCalls = append(nm.ToolCalls, message.ToolCall{
				ID:       tc.ID,
				Type:     tc.Type,
				Function: message.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
		for _, u := range m.ImageURLs {
			if mime, data, ok := splitDataURL(u); ok {
				nm.Parts = append(nm.Parts, message.ImagePart(mime, data))
			} else {
				nm.Parts = append(nm.Parts, message.ContentPart{Type: message.PartImage, URL: u})
			}
		}
		out = append(out, nm)
	}
	return out
}

// fromNovaMessages is the inverse, used to hand gateway the transcript it
// persists. Reasoning is deliberately dropped: llm.ChatMessage never carried
// it, and replaying it would change the next turn's prompt bytes.
func fromNovaMessages(in []message.Message) []llm.ChatMessage {
	out := make([]llm.ChatMessage, 0, len(in))
	for _, m := range in {
		cm := llm.ChatMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, llm.ToolCall{
				ID:       tc.ID,
				Type:     tc.Type,
				Function: llm.FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
		for _, p := range m.Parts {
			if p.Type != message.PartImage {
				continue
			}
			if p.Data != "" {
				cm.ImageURLs = append(cm.ImageURLs, dataURLPrefix+p.MimeType+";base64,"+p.Data)
			} else if p.URL != "" {
				cm.ImageURLs = append(cm.ImageURLs, p.URL)
			}
		}
		out = append(out, cm)
	}
	return out
}

// splitDataURL parses "data:<mime>;base64,<data>".
func splitDataURL(u string) (mime, data string, ok bool) {
	rest, found := strings.CutPrefix(u, dataURLPrefix)
	if !found {
		return "", "", false
	}
	meta, data, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}
	mime, found = strings.CutSuffix(meta, ";base64")
	if !found {
		return "", "", false
	}
	return mime, data, true
}

// toNovaDefs converts Polaris tool definitions to Nova's. The parameter
// schemas are plain maps on both sides, so a JSON round trip is lossless and
// keeps the encoded bytes identical (encoding/json sorts map keys) — which is
// what keeps the provider's prompt-cache prefix stable across the flip.
func toNovaDefs(defs []llm.ToolDef) []provider.ToolDef {
	out := make([]provider.ToolDef, 0, len(defs))
	for _, d := range defs {
		var params map[string]any
		if raw, err := json.Marshal(d.Function.Parameters); err == nil {
			_ = json.Unmarshal(raw, &params)
		}
		out = append(out, provider.ToolDef{
			Type: d.Type,
			Function: provider.ToolFunction{
				Name:        d.Function.Name,
				Description: d.Function.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

// fromNovaDefs is toNovaDefs' inverse, for the ChatClient bridge.
func fromNovaDefs(defs []provider.ToolDef) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(defs))
	for _, d := range defs {
		out = append(out, llm.ToolDef{
			Type: d.Type,
			Function: llm.ToolFunctionDef{
				Name:        d.Function.Name,
				Description: d.Function.Description,
				Parameters:  d.Function.Parameters,
			},
		})
	}
	return out
}
