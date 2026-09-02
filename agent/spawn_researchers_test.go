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
