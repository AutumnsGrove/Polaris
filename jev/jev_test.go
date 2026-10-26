package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
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

func TestAskChoiceReportsTheResolvedModel(t *testing.T) {
	var cap capture
	srv := recordingServer(t, &cap, http.StatusOK,
		`{"model":"typesafe/jev-1.14-20261014","answers":{"q":{"choice":"yes","probabilities":{"yes":1},"confidence":1}},"usage":{"cost":0}}`)
	defer srv.Close()

	resp, err := NewClient(srv.URL, "k").AskChoice(context.Background(), "s", oneQuestion())
	if err != nil {
		t.Fatalf("AskChoice: %v", err)
	}
	if resp.Model != "typesafe/jev-1.14-20261014" {
		t.Errorf("Model = %q, want the dated build the server reported", resp.Model)
	}

	// A server that omits it (System One today) leaves it empty, not an error.
	srv2 := recordingServer(t, &cap, http.StatusOK, answerBody)
	defer srv2.Close()
	resp, err = NewClient(srv2.URL, "k").AskChoice(context.Background(), "s", oneQuestion())
	if err != nil || resp.Model != "" {
		t.Errorf("without a model field: Model = %q err = %v, want empty and nil", resp.Model, err)
	}
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

func questionSet(n int) map[string]ChoiceQuestion {
	qs := make(map[string]ChoiceQuestion, n)
	for i := 0; i < n; i++ {
		qs[fmt.Sprintf("q%02d", i)] = ChoiceQuestion{Instructions: "?", Criteria: map[string]string{"yes": "y", "no": "n"}}
	}
	return qs
}

func TestSplitQuestionsIsBalancedAndDeterministic(t *testing.T) {
	cases := []struct {
		n, max int
		want   []int
	}{
		{23, 16, []int{12, 11}},
		{17, 16, []int{9, 8}},
		{16, 16, []int{16}},
		{33, 16, []int{11, 11, 11}},
		{1, 16, []int{1}},
	}
	for _, tc := range cases {
		chunks := splitQuestions(questionSet(tc.n), tc.max)
		var got []int
		seen := map[string]bool{}
		for _, ch := range chunks {
			got = append(got, len(ch))
			for k := range ch {
				if seen[k] {
					t.Errorf("n=%d: question %s landed in two chunks", tc.n, k)
				}
				seen[k] = true
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) || len(seen) != tc.n {
			t.Errorf("n=%d max=%d: chunk sizes %v (%d questions), want %v (%d)", tc.n, tc.max, got, len(seen), tc.want, tc.n)
		}
	}
	// Same set, same split, every time: map iteration order must not leak in.
	first := splitQuestions(questionSet(23), 16)
	for i := 0; i < 20; i++ {
		again := splitQuestions(questionSet(23), 16)
		for j := range first {
			for k := range first[j] {
				if _, ok := again[j][k]; !ok {
					t.Fatalf("split changed between calls: %s left chunk %d", k, j)
				}
			}
		}
	}
}

// splitServer answers each request with one answer per question it was sent,
// recording how many questions every request carried.
func splitServer(t *testing.T, sizes *[]int, mu *sync.Mutex, failWhenContains string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body requestBody
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		*sizes = append(*sizes, len(body.Questions))
		mu.Unlock()
		// In the failure scenario the healthy request is slow, so the failing
		// one lands first and its sibling is still in flight to be cancelled.
		if _, failsHere := body.Questions[failWhenContains]; failWhenContains != "" && !failsHere {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		answers := map[string]ChoiceAnswer{}
		for k := range body.Questions {
			if failWhenContains != "" && k == failWhenContains {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":{"message":"boom","code":422}}`))
				return
			}
			answers[k] = ChoiceAnswer{Choice: "yes", Probabilities: map[string]float64{"yes": 1}, Confidence: 1}
		}
		out, _ := json.Marshal(map[string]interface{}{
			"model": "m-dated", "answers": answers,
			"usage": map[string]interface{}{"input_tokens": 10, "output_tokens": 1, "cost": 0.5},
		})
		_, _ = w.Write(out)
	}))
}

func TestWithMaxQuestionsSplitsAndMerges(t *testing.T) {
	var sizes []int
	var mu sync.Mutex
	srv := splitServer(t, &sizes, &mu, "")
	defer srv.Close()

	resp, err := NewClient(srv.URL, "k").WithMaxQuestions(16).AskChoice(context.Background(), "s", questionSet(23))
	if err != nil {
		t.Fatalf("AskChoice: %v", err)
	}
	if len(resp.Answers) != 23 {
		t.Errorf("merged answers = %d, want all 23", len(resp.Answers))
	}
	sort.Ints(sizes)
	if fmt.Sprint(sizes) != "[11 12]" {
		t.Errorf("request sizes = %v, want two requests of 11 and 12", sizes)
	}
	// Usage is the sum across requests: the real cost of the one logical call.
	if resp.Usage.CostUSD != 1.0 || resp.Usage.InputTokens != 20 || resp.Model != "m-dated" {
		t.Errorf("usage/model = %+v / %q, want cost 1.0, 20 input tokens, model m-dated", resp.Usage, resp.Model)
	}

	// At or under the cap, and with no cap at all, it stays a single request.
	for _, c := range []*Client{
		NewClient(srv.URL, "k").WithMaxQuestions(16),
		NewClient(srv.URL, "k"),
	} {
		sizes = nil
		if _, err := c.AskChoice(context.Background(), "s", questionSet(16)); err != nil || len(sizes) != 1 {
			t.Errorf("16 questions: %d requests err=%v, want exactly 1", len(sizes), err)
		}
	}
}

func TestWithMaxQuestionsOneFailedChunkFailsTheCall(t *testing.T) {
	var sizes []int
	var mu sync.Mutex
	srv := splitServer(t, &sizes, &mu, "q20")
	defer srv.Close()

	resp, err := NewClient(srv.URL, "k").WithMaxQuestions(16).AskChoice(context.Background(), "s", questionSet(23))
	if err == nil || resp != nil {
		t.Fatalf("resp=%v err=%v, want an error and no partial response", resp, err)
	}
	// The healthy chunk (index 0) is cancelled by the failure; its "context
	// canceled" must not be what the caller sees in place of the real cause.
	if !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "canceled") {
		t.Errorf("err = %v, want the failing chunk's own error, not the sibling's cancellation", err)
	}
}

func TestSettersAreNilSafe(t *testing.T) {
	var c *Client
	if c.WithMaxQuestions(16) != nil {
		t.Error("WithMaxQuestions on nil client should stay nil")
	}
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
