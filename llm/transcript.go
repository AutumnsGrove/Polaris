package llm

import "encoding/json"

// transcriptMessage is ChatMessage's storage shape. ChatMessage itself can't
// round-trip through JSON: ImageURLs is tagged json:"-" (its MarshalJSON
// folds it into an array-form "content" for the wire instead), so a
// view_image "see" message would come back with its image silently dropped
// — and then no longer match the bytes the provider cached. Every field is
// carried explicitly here instead.
type transcriptMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ImageURLs  []string   `json:"image_urls,omitempty"`
}

// EncodeTranscript serializes a turn's exact message list for storage —
// see gateway's loadHistory and docs/plans/verbatim-turn-transcripts.md.
// DecodeTranscript(EncodeTranscript(m)) marshals to the same wire bytes as
// m itself, which is the whole point: a replayed turn has to match what
// the provider saw (and cached) the first time, byte for byte.
func EncodeTranscript(msgs []ChatMessage) (string, error) {
	out := make([]transcriptMessage, len(msgs))
	for i, m := range msgs {
		out[i] = transcriptMessage{Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ImageURLs: m.ImageURLs}
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// DecodeTranscript is EncodeTranscript's inverse.
func DecodeTranscript(s string) ([]ChatMessage, error) {
	var in []transcriptMessage
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, err
	}
	out := make([]ChatMessage, len(in))
	for i, m := range in {
		out[i] = ChatMessage{Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, ImageURLs: m.ImageURLs}
	}
	return out, nil
}
