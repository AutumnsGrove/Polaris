package gateway

import (
	"context"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
)

func TestBackfillConstellation_ProcessesEligibleThreadsUpToLimit(t *testing.T) {
	db := openTestStoreForConstellation(t)
	seedWeaverThread(t, db, "thread one")
	seedWeaverThread(t, db, "thread two")
	seedWeaverThread(t, db, "thread three")

	// Each thread's first pass makes exactly one call to the mock (a
	// plain-text answer, no tool call) — three threads means three queued
	// responses if -n is unlimited, or fewer if capped.
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "Noted."}},
		{Resp: &llm.ChatResponse{Content: "Noted."}},
	}}

	processed, err := BackfillConstellation(context.Background(), db, mock, 2)
	if err != nil {
		t.Fatalf("BackfillConstellation: %v", err)
	}
	if processed != 2 {
		t.Errorf("processed = %d, want 2 (capped by limit)", processed)
	}
}

func TestBackfillConstellation_ZeroLimitProcessesEverything(t *testing.T) {
	db := openTestStoreForConstellation(t)
	seedWeaverThread(t, db, "thread one")
	seedWeaverThread(t, db, "thread two")

	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		{Resp: &llm.ChatResponse{Content: "Noted."}},
		{Resp: &llm.ChatResponse{Content: "Noted."}},
	}}

	processed, err := BackfillConstellation(context.Background(), db, mock, 0)
	if err != nil {
		t.Fatalf("BackfillConstellation: %v", err)
	}
	if processed != 2 {
		t.Errorf("processed = %d, want 2 (every eligible thread)", processed)
	}
}
