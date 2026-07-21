package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A stopped turn that never reached an LLM call costs exactly $0 — a
// real, common case (see TestWebSocket_LLMErrorSurfacesAsErrorEvent-
// adjacent scenarios). CostUSD/ContextTokens must never carry omitempty:
// dropping the field for a legitimate zero, rather than sending 0,
// leaves the frontend's `e.cost_usd` as undefined. `this.totalCost +=
// undefined` produces NaN, which is sticky — once poisoned, every later
// addition (even from a turn with a real cost) stays NaN for the rest of
// the session. This bug shipped once already; this test locks in the fix.
func TestServerEvent_ZeroCostAndContextTokensAreNotOmitted(t *testing.T) {
	evt := ServerEvent{Type: "done", ThreadID: "t1", CostUSD: 0, ContextTokens: 0}

	b, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)

	if !strings.Contains(s, `"cost_usd":0`) {
		t.Errorf("marshaled JSON = %s, want cost_usd:0 present (not omitted)", s)
	}
	if !strings.Contains(s, `"context_tokens":0`) {
		t.Errorf("marshaled JSON = %s, want context_tokens:0 present (not omitted)", s)
	}
}
