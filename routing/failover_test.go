package routing

import (
	"context"
	"testing"

	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/testkit"

	"polaris/config"
)

func loadTestConfigBoth(t *testing.T, openRouterBaseURL, goBaseURL, extra string) *config.Config {
	t.Helper()
	contents := "providers:\n  openrouter:\n    api_key: \"k\"\n    base_url: \"" + openRouterBaseURL + "\"\n" +
		"  opencode_go:\n    api_key: \"go-key\"\n    base_url: \"" + goBaseURL + "\"\n" + extra
	return loadTestConfig(t, contents)
}

// A spent Go window spills to OpenRouter visibly: the OpenRouter leg
// answers, OnFallback fires once with the classified error, and the
// response is attributed to the backend that served it — never silent.
func TestRouter_FailoverGoToOpenRouter(t *testing.T) {
	goSrv := testkit.NewGoServer(t,
		testkit.GoConfig{RequireSession: true},
		testkit.ServerTurn{Fault: testkit.Fault429},
	)
	orSrv := testkit.NewServer(t,
		testkit.ServerTurn{Turn: testkit.Turn{
			Text:  "answered by openrouter",
			Usage: &provider.Usage{PromptTokens: 8, CompletionTokens: 4, TotalTokens: 12, CostUSD: 0.005, CostSource: "provider"},
		}},
	)
	cfg := loadTestConfigBoth(t, orSrv.URL, goSrv.URL, "routing:\n  default_backend: auto\n")

	var fallbackFrom, fallbackTo string
	fallbacks := 0
	r := Build(cfg, Options{
		SessionID: func() string { return "thread-1" },
		UserAgent: "polaris-test/1",
		OnFallback: func(from, to string, err error) {
			fallbacks++
			fallbackFrom, fallbackTo = from, to
		},
	})

	resp, err := r.Stream(context.Background(), provider.Request{
		Model:    "vendor/go-model-1",
		ModelID:  "go-model",
		Messages: []message.Message{message.User("hello")},
	}, nil)
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if resp.Message.Content != "answered by openrouter" {
		t.Errorf("content = %q, want the OpenRouter leg's answer", resp.Message.Content)
	}
	if resp.Provider != OpenRouter {
		t.Errorf("Provider = %q, want openrouter (the attempt that answered)", resp.Provider)
	}
	if fallbacks != 1 || fallbackFrom != OpenCodeGo || fallbackTo != OpenRouter {
		t.Errorf("OnFallback fired %d times (%q -> %q), want once opencode-go -> openrouter", fallbacks, fallbackFrom, fallbackTo)
	}
	if got := len(goSrv.Requests()); got != 1 {
		t.Errorf("go server saw %d requests, want 1", got)
	}
	if got := len(orSrv.Requests()); got != 1 {
		t.Errorf("openrouter server saw %d requests, want 1 (exactly one retry, never a loop)", got)
	}
	// The combined usage carries the serving leg's retail; the list-cost
	// helper keeps it denominated correctly even across the spill.
	if got := ListCost(resp.Usage); got != 0.005 {
		t.Errorf("ListCost(combined) = %v, want 0.005", got)
	}
}

// A missing x-opencode-session is our own misconfiguration (400), not a
// limit — it must surface as an error and never spill, or failover would
// hide the bug while billing OpenRouter for every call.
func TestRouter_MissingSessionDoesNotSpill(t *testing.T) {
	goSrv := testkit.NewGoServer(t, testkit.GoConfig{RequireSession: true})
	orSrv := testkit.NewServer(t)
	cfg := loadTestConfigBoth(t, orSrv.URL, goSrv.URL, "routing:\n  default_backend: auto\n")

	fallbacks := 0
	r := Build(cfg, Options{
		// No SessionID: the header goes unsent, like a misconfigured client.
		OnFallback: func(from, to string, err error) { fallbacks++ },
	})

	_, err := r.Stream(context.Background(), provider.Request{
		Model:    "vendor/go-model-1",
		ModelID:  "go-model",
		Messages: []message.Message{message.User("hello")},
	}, nil)
	if err == nil {
		t.Fatal("Stream succeeded without a session header, want the 400 to surface")
	}
	if fallbacks != 0 {
		t.Errorf("OnFallback fired %d times, want 0 (a 400 never spills)", fallbacks)
	}
	if got := len(orSrv.Requests()); got != 0 {
		t.Errorf("openrouter server saw %d requests, want 0 (no silent fallback)", got)
	}
}

// The session header carries the thread id and is read fresh on every
// call, not captured once at build — a cached process-wide router could
// not do this, which is why Build happens per turn (see routing.Build).
func TestRouter_SessionHeaderIsFreshPerCall(t *testing.T) {
	var sessions []string
	goSrv := testkit.NewGoServer(t, testkit.GoConfig{RequireSession: true},
		testkit.ServerTurn{Turn: testkit.Turn{Text: "one"}},
		testkit.ServerTurn{Turn: testkit.Turn{Text: "two"}},
	)
	orSrv := testkit.NewServer(t)
	cfg := loadTestConfigBoth(t, orSrv.URL, goSrv.URL, "routing:\n  default_backend: auto\n")

	thread := "thread-1"
	r := Build(cfg, Options{SessionID: func() string { return thread }})
	for i := 0; i < 2; i++ {
		if _, err := r.Stream(context.Background(), provider.Request{
			Model:    "go-model-1",
			ModelID:  "go-model",
			Messages: []message.Message{message.User("hi")},
		}, nil); err != nil {
			t.Fatalf("Stream %d returned error: %v", i, err)
		}
		thread = "thread-2"
	}
	for _, req := range goSrv.Requests() {
		sessions = append(sessions, req.Header.Get(testkit.SessionHeader))
	}
	if len(sessions) != 2 || sessions[0] != "thread-1" || sessions[1] != "thread-2" {
		t.Errorf("session headers = %q, want [thread-1 thread-2] read fresh per call", sessions)
	}
}
