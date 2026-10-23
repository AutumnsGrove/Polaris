package llm

import "testing"

func FuzzDecodeTranscript(f *testing.F) {
	f.Add(`[{"role":"user","content":"hi"}]`)
	f.Add(`[{"role":"assistant","tool_calls":[{"id":"1","type":"function","function":{"name":"x","arguments":"{}"}}]}]`)
	f.Add(`null`)
	f.Add(`{`)
	f.Fuzz(func(t *testing.T, s string) {
		msgs, err := DecodeTranscript(s)
		if err != nil {
			return
		}
		if _, err := EncodeTranscript(msgs); err != nil {
			t.Errorf("decoded transcript fails to re-encode: %v", err)
		}
	})
}
