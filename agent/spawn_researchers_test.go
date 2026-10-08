package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"polaris/llm"
	"polaris/tools"
)

// taskAwareClient is a minimal llm.ChatClient test double that decides
// success/failure by inspecting the actual outgoing user message, rather
// than a fixed queued-response order — SpawnResearchers runs sub-agents
// concurrently, so which goroutine's call reaches a shared mock client
// first is nondeterministic, and a llmtest.MockClient-style ordered
// queue can't reliably target "the third task fails" under that.
type taskAwareClient struct{}

func (c *taskAwareClient) ChatCompletionWithTools(_ context.Context, messages []llm.ChatMessage, _ []llm.ToolDef, onChunk, _ func(string)) (*llm.ChatResponse, error) {
	var userMsg string
	for _, m := range messages {
		if m.Role == "user" {
			userMsg = m.Content
		}
	}
	if strings.Contains(userMsg, "SHOULD_FAIL") {
		return nil, errors.New("simulated sub-agent failure")
	}
	content := `{"findings":[{"claim":"ok","sources":["https://example.com/ok"]}]}`
	if onChunk != nil {
		onChunk(content)
	}
	return &llm.ChatResponse{Content: content}, nil
}

func (c *taskAwareClient) ChatCompletionStreaming(reqCtx context.Context, messages []llm.ChatMessage, onChunk, onReasoning func(string)) (*llm.ChatResponse, error) {
	return c.ChatCompletionWithTools(reqCtx, messages, nil, onChunk, onReasoning)
}

func TestSpawnResearchers_ReturnsOneReportPerTask(t *testing.T) {
	tasks := []tools.SubAgentTask{
		{Objective: "task A"},
		{Objective: "task B"},
		{Objective: "task C"},
	}
	baseCtx := &tools.Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}

	reports := SpawnResearchers(context.Background(), baseCtx, &taskAwareClient{}, tasks)

	if len(reports) != len(tasks) {
		t.Fatalf("len(reports) = %d, want %d", len(reports), len(tasks))
	}
	for i, task := range tasks {
		if reports[i].Objective != task.Objective {
			t.Errorf("reports[%d].Objective = %q, want %q (must line up positionally with tasks)", i, reports[i].Objective, task.Objective)
		}
	}
}

func TestSpawnResearchers_FailedSubAgentReportedNotDropped(t *testing.T) {
	tasks := []tools.SubAgentTask{
		{Objective: "task A"},
		{Objective: "task B — SHOULD_FAIL"},
		{Objective: "task C"},
	}
	baseCtx := &tools.Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}

	reports := SpawnResearchers(context.Background(), baseCtx, &taskAwareClient{}, tasks)

	if len(reports) != 3 {
		t.Fatalf("len(reports) = %d, want 3 — a failed sub-agent must still produce a placeholder report, not vanish", len(reports))
	}
	failed := reports[1]
	if len(failed.Findings) == 0 {
		t.Fatal("failed sub-agent's report has no findings, want a finding describing the failure")
	}
	if !strings.Contains(failed.Findings[0].Claim, "simulated sub-agent failure") {
		t.Errorf("failed report claim = %q, want it to mention the underlying error", failed.Findings[0].Claim)
	}
	// The other two tasks must be unaffected by their sibling's failure.
	if reports[0].Findings[0].Claim != "ok" || reports[2].Findings[0].Claim != "ok" {
		t.Errorf("reports = %+v, want tasks A and C to have succeeded normally", reports)
	}
}

func TestSpawnResearchers_LazilyInitializesSharedBudgetAndDedup(t *testing.T) {
	baseCtx := &tools.Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	if baseCtx.ResearchBudget != nil || baseCtx.SearchDedup != nil {
		t.Fatal("test setup invalid: expected nil budget/dedup before the call")
	}

	SpawnResearchers(context.Background(), baseCtx, &taskAwareClient{}, []tools.SubAgentTask{{Objective: "x"}})

	if baseCtx.ResearchBudget == nil {
		t.Error("ResearchBudget is still nil after SpawnResearchers, want it lazily initialized")
	}
	if baseCtx.SearchDedup == nil {
		t.Error("SearchDedup is still nil after SpawnResearchers, want it lazily initialized")
	}
}

func TestSpawnResearchers_ReusesExistingBudgetRatherThanReplacing(t *testing.T) {
	budget := tools.NewResearchBudget()
	budget.RecordCall(false) // give it some pre-existing state to check for
	baseCtx := &tools.Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, ResearchBudget: budget}

	SpawnResearchers(context.Background(), baseCtx, &taskAwareClient{}, []tools.SubAgentTask{{Objective: "x"}})

	if baseCtx.ResearchBudget != budget {
		t.Error("ResearchBudget was replaced, want the pre-existing one reused so its state (and any budget shared across multiple spawn_researchers calls in one session) survives")
	}
}

// concurrencyTrackingClient tracks how many calls are in flight
// simultaneously, blocking each one on release until the test signals —
// used to prove SpawnResearchers' semaphore actually bounds concurrency
// rather than firing every task's goroutine at once.
type concurrencyTrackingClient struct {
	mu      sync.Mutex
	current int
	peak    int
	release chan struct{}
}

func (c *concurrencyTrackingClient) ChatCompletionWithTools(_ context.Context, _ []llm.ChatMessage, _ []llm.ToolDef, onChunk, _ func(string)) (*llm.ChatResponse, error) {
	c.mu.Lock()
	c.current++
	if c.current > c.peak {
		c.peak = c.current
	}
	c.mu.Unlock()

	<-c.release

	c.mu.Lock()
	c.current--
	c.mu.Unlock()

	content := `{"findings":[]}`
	if onChunk != nil {
		onChunk(content)
	}
	return &llm.ChatResponse{Content: content}, nil
}

func (c *concurrencyTrackingClient) ChatCompletionStreaming(reqCtx context.Context, messages []llm.ChatMessage, onChunk, onReasoning func(string)) (*llm.ChatResponse, error) {
	return c.ChatCompletionWithTools(reqCtx, messages, nil, onChunk, onReasoning)
}

func (c *concurrencyTrackingClient) currentCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func TestSpawnResearchers_BoundsConcurrency(t *testing.T) {
	numTasks := maxConcurrentSubAgents * 2 // deliberately more than the cap
	tasks := make([]tools.SubAgentTask, numTasks)
	for i := range tasks {
		tasks[i] = tools.SubAgentTask{Objective: fmt.Sprintf("task %d", i)}
	}
	client := &concurrencyTrackingClient{release: make(chan struct{})}
	baseCtx := &tools.Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}

	done := make(chan []tools.SubAgentReport, 1)
	go func() {
		done <- SpawnResearchers(context.Background(), baseCtx, client, tasks)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for client.currentCount() < maxConcurrentSubAgents {
		if time.Now().After(deadline) {
			t.Fatalf("concurrency never reached the cap (%d) within the timeout (stuck at %d)", maxConcurrentSubAgents, client.currentCount())
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Give any over-limit goroutine a moment to also start, so a broken
	// semaphore (or none at all) would show up as a higher peak.
	time.Sleep(30 * time.Millisecond)
	close(client.release)
	reports := <-done

	client.mu.Lock()
	peak := client.peak
	client.mu.Unlock()

	if peak != maxConcurrentSubAgents {
		t.Errorf("peak concurrent sub-agent calls = %d, want exactly %d", peak, maxConcurrentSubAgents)
	}
	if len(reports) != numTasks {
		t.Errorf("len(reports) = %d, want %d", len(reports), numTasks)
	}
}

// recordedEvent is one event captured from a test Emit sink.
type recordedEvent struct {
	typ     string
	payload map[string]interface{}
}

func captureEmit() (func(string, map[string]interface{}), func() []recordedEvent) {
	var mu sync.Mutex
	var events []recordedEvent
	emit := func(typ string, payload map[string]interface{}) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, recordedEvent{typ, payload})
	}
	snapshot := func() []recordedEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedEvent(nil), events...)
	}
	return emit, snapshot
}

// TestSpawnResearchers_TagsSubAgentEventsAndAnnouncesLifecycle guards the
// contract the Deep Research UI is built on: every researcher gets a
// subagent_start/subagent_end pair keyed by its AgentID, and nothing a
// researcher streams reaches the turn untagged — in particular its final
// answer ("token") must not be appended to the orchestrator's visible answer.
func TestSpawnResearchers_TagsSubAgentEventsAndAnnouncesLifecycle(t *testing.T) {
	emit, snapshot := captureEmit()
	baseCtx := &tools.Context{Ctx: context.Background(), Emit: emit}
	tasks := []tools.SubAgentTask{
		{Objective: "task A", ParentCallID: "call_x", Index: 0},
		{Objective: "task B", ParentCallID: "call_x", Index: 1},
	}

	SpawnResearchers(context.Background(), baseCtx, &taskAwareClient{}, tasks)

	starts, ends := map[string]bool{}, map[string]recordedEvent{}
	for _, ev := range snapshot() {
		switch ev.typ {
		case "subagent_start":
			starts[ev.payload["agent_id"].(string)] = true
		case "subagent_end":
			ends[ev.payload["agent_id"].(string)] = ev
		case "token", "cost_update":
			t.Errorf("%q leaked from a sub-agent into the turn's event stream: %v", ev.typ, ev.payload)
		default:
			if id, _ := ev.payload["agent_id"].(string); id == "" {
				t.Errorf("sub-agent event %q has no agent_id: %v", ev.typ, ev.payload)
			}
		}
	}
	for _, id := range []string{"call_x.0", "call_x.1"} {
		if !starts[id] {
			t.Errorf("no subagent_start for %s", id)
		}
		end, ok := ends[id]
		if !ok {
			t.Errorf("no subagent_end for %s", id)
			continue
		}
		if end.payload["status"] != "done" || !strings.Contains(end.payload["result"].(string), "ok") || end.payload["call_id"] != "call_x" {
			t.Errorf("subagent_end for %s = %v, want status done, the findings summary, and the parent call_id", id, end.payload)
		}
	}
}

func TestSpawnResearchers_FailedSubAgentEndsWithFailedStatus(t *testing.T) {
	emit, snapshot := captureEmit()
	baseCtx := &tools.Context{Ctx: context.Background(), Emit: emit}

	SpawnResearchers(context.Background(), baseCtx, &taskAwareClient{}, []tools.SubAgentTask{{Objective: "SHOULD_FAIL", ParentCallID: "c", Index: 0}})

	for _, ev := range snapshot() {
		if ev.typ == "subagent_end" {
			if ev.payload["status"] != "failed" {
				t.Errorf("status = %v, want failed", ev.payload["status"])
			}
			return
		}
	}
	t.Error("no subagent_end emitted for a failed sub-agent — its card would spin forever")
}

// costClient reports a fixed per-call cost, to prove sub-agent spend reaches
// the turn total rather than being discarded with the sub-agent's context.
type costClient struct{ taskAwareClient }

func (c *costClient) ChatCompletionWithTools(ctx context.Context, m []llm.ChatMessage, tl []llm.ToolDef, onChunk, onR func(string)) (*llm.ChatResponse, error) {
	resp, err := c.taskAwareClient.ChatCompletionWithTools(ctx, m, tl, onChunk, onR)
	if resp != nil {
		resp.CostUSD = 0.01
	}
	return resp, err
}

func TestSpawnResearchers_FoldsSubAgentCostIntoTurnTotal(t *testing.T) {
	baseCtx := &tools.Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	tasks := []tools.SubAgentTask{{Objective: "a", ParentCallID: "c", Index: 0}, {Objective: "b", ParentCallID: "c", Index: 1}, {Objective: "c", ParentCallID: "c", Index: 2}}

	reports := SpawnResearchers(context.Background(), baseCtx, &costClient{}, tasks)

	if got, want := baseCtx.ExtraCostUSD, 0.03; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("baseCtx.ExtraCostUSD = %v, want %v (3 sub-agents at $0.01)", got, want)
	}
	for i, r := range reports {
		if r.CostUSD < 0.01-1e-9 {
			t.Errorf("reports[%d].CostUSD = %v, want 0.01", i, r.CostUSD)
		}
	}
}
