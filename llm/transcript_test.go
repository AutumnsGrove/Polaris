package llm

import (
	"encoding/json"
	"testing"
)

// The property gateway's history replay depends on: a stored-then-loaded
// transcript sends exactly the bytes the original messages did, images
// included (ChatMessage's own JSON tags would drop ImageURLs).
func TestTranscript_RoundTripsToIdenticalWireBytes(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "what's in this chart?"},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []ToolCall{
			{ID: "call_a", Type: "function", Function: FunctionCall{Name: "view_image", Arguments: `{"mode":"see"}`}},
			{ID: "call_b", Type: "function", Function: FunctionCall{Name: "web_read", Arguments: `{"url":"https://x"}`}},
		}},
		{Role: "tool", ToolCallID: "call_a", Content: "image attached below"},
		{Role: "tool", ToolCallID: "call_b", Content: "page text"},
		{Role: "user", Content: "", ImageURLs: []string{"data:image/png;base64,AAAA"}},
		{Role: "assistant", Content: "It shows a rising line."},
	}
	encoded, err := EncodeTranscript(msgs)
	if err != nil {
		t.Fatalf("EncodeTranscript: %v", err)
	}
	decoded, err := DecodeTranscript(encoded)
	if err != nil {
		t.Fatalf("DecodeTranscript: %v", err)
	}
	want, _ := json.Marshal(msgs)
	got, _ := json.Marshal(decoded)
	if string(got) != string(want) {
		t.Fatalf("wire bytes changed across a storage round trip:\nwant %s\ngot  %s", want, got)
	}
}
