package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// capture is what a test server observed for one request — enough to assert the
// surface path, the model actually sent, and the auth header.
type capture struct {
	path  string
	query string
	auth  string
	body  requestBody
}

func recordingServer(t *testing.T, cap *capture, status int, resp string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.query = r.URL.RawQuery
		cap.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &cap.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}))
}

const answerBody = `{"answers":{"q":{"choice":"yes","probabilities":{"yes":1,"no":0},"confidence":1}},` +
	`"usage":{"input_tokens":7,"output_tokens":2,"cost":0.00001}}`

func oneQuestion() map[string]ChoiceQuestion {
	return map[string]ChoiceQuestion{"q": {Instructions: "?", Criteria: map[string]string{"yes": "y", "no": "n"}}}
}

func TestNewClientNilWithoutKey(t *testing.T) {
	if c := NewClient("https://openrouter.ai/api/v1", ""); c != nil {
		t.Fatalf("NewClient with empty key = %v, want nil", c)
	}
}

func TestAskChoiceDefaultsToSystemOneAndDefaultModel(t *testing.T) {
	var cap capture
	srv := recordingServer(t, &cap, http.StatusOK, answerBody)
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	resp, err := c.AskChoice(context.Background(), "some state", oneQuestion())
	if err != nil {
		t.Fatalf("AskChoice: %v", err)
	}
	if cap.path != "/systemone" {
		t.Errorf("path = %q, want /systemone", cap.path)
	}
	if cap.body.Model != Model {
		t.Errorf("model = %q, want %q", cap.body.Model, Model)
	}
	if cap.auth != "Bearer test-key" {
		t.Errorf("auth = %q, want Bearer test-key", cap.auth)
	}
	if got := resp.Answers["q"].Choice; got != "yes" {
		t.Errorf("choice = %q, want yes", got)
	}
	if resp.Usage.CostUSD != 0.00001 {
		t.Errorf("cost = %v, want 0.00001", resp.Usage.CostUSD)
	}
}

func TestWithModelAndDecisionsAPISwitchSurface(t *testing.T) {
	var cap capture
	srv := recordingServer(t, &cap, http.StatusOK, answerBody)
	defer srv.Close()

	c := NewClient(srv.URL, "k").WithModel("typesafe/jev-1.14").WithDecisionsAPI()
	if _, err := c.AskChoice(context.Background(), "state", oneQuestion()); err != nil {
		t.Fatalf("AskChoice: %v", err)
	}
	if cap.path != "/api/alpha/decisions" {
		t.Errorf("path = %q, want /api/alpha/decisions", cap.path)
	}
	if cap.body.Model != "typesafe/jev-1.14" {
		t.Errorf("model = %q, want typesafe/jev-1.14", cap.body.Model)
	}
}

func TestDecisionsURLFromRealBase(t *testing.T) {
	for _, base := range []string{
		"https://openrouter.ai/api/v1",
		"https://openrouter.ai/api/v1/",
	} {
		if got := decisionsURL(base); got != "https://openrouter.ai/api/alpha/decisions" {
			t.Errorf("decisionsURL(%q) = %q", base, got)
		}
	}
	// A base that doesn't parse as a URL still yields a usable path, not "".
	if got := decisionsURL("127.0.0.1:9999"); got != "127.0.0.1:9999/api/alpha/decisions" {
		t.Errorf("fallback decisionsURL = %q", got)
	}
}

func TestWithTimeout(t *testing.T) {
	c := NewClient("http://example.invalid", "k").WithTimeout(3 * time.Second)
	if c.http.Timeout != 3*time.Second {
		t.Errorf("timeout = %v, want 3s", c.http.Timeout)
	}
	// Zero/negative keeps the default rather than making the client hang forever.
	c2 := NewClient("http://example.invalid", "k").WithTimeout(0)
	if c2.http.Timeout != 20*time.Second {
		t.Errorf("timeout after 0 = %v, want 20s default", c2.http.Timeout)
	}
}

func TestSettersAreNilSafe(t *testing.T) {
	var c *Client
	if c.WithModel("x") != nil {
		t.Error("WithModel on nil client should stay nil")
	}
	if c.WithDecisionsAPI() != nil {
		t.Error("WithDecisionsAPI on nil client should stay nil")
	}
	if c.WithTimeout(time.Second) != nil {
		t.Error("WithTimeout on nil client should stay nil")
	}
}

func TestListDecisionModels(t *testing.T) {
	var cap capture
	srv := recordingServer(t, &cap, http.StatusOK,
		`{"data":[{"id":"typesafe/jev-1.14","name":"TypeSafe: Jev 1.14","context_length":262144,`+
			`"pricing":{"prompt":"0.000000042","completion":"0"}}]}`)
	defer srv.Close()

	models, err := ListDecisionModels(context.Background(), srv.URL, "k")
	if err != nil {
		t.Fatalf("ListDecisionModels: %v", err)
	}
	if cap.path != "/models" || cap.query != "output_modalities=decisions" {
		t.Errorf("catalog request = %s?%s", cap.path, cap.query)
	}
	if cap.auth != "Bearer k" {
		t.Errorf("catalog auth = %q", cap.auth)
	}
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1", len(models))
	}
	if models[0].ID != "typesafe/jev-1.14" || models[0].ContextLength != 262144 {
		t.Errorf("model = %+v", models[0])
	}
	if models[0].Pricing.Prompt != "0.000000042" {
		t.Errorf("prompt pricing = %q", models[0].Pricing.Prompt)
	}
}

func TestListDecisionModelsErrorStatusIsAnError(t *testing.T) {
	var cap capture
	srv := recordingServer(t, &cap, http.StatusBadGateway, `{"error":"upstream"}`)
	defer srv.Close()

	if _, err := ListDecisionModels(context.Background(), srv.URL, ""); err == nil {
		t.Fatal("ListDecisionModels on 502 returned nil error")
	}
}
