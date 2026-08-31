// memories.go backs the settings panel's Memory section: a plain CRUD
// view over store.Store's memories table, plus a scoped "tell it what to
// change" endpoint that lets a short natural-language instruction drive
// the same memory tool a normal agent turn would call, without spinning
// up a whole conversation thread for it.
package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"polaris/llm"
	"polaris/prompts"
	"polaris/store"
	"polaris/tools"
)

func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	memories, err := s.db.ListMemoriesFull()
	if err != nil {
		log.Warn("listing memories failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, memories)
}

func (s *Server) handleUpdateMemory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		Type        *string `json:"type"`
		Description *string `json:"description"`
		Content     *string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	memType := ""
	if req.Type != nil {
		if !validMemoryTypes[*req.Type] {
			http.Error(w, "type must be one of user, feedback, project, reference", http.StatusBadRequest)
			return
		}
		memType = *req.Type
	}
	description := ""
	if req.Description != nil {
		description = *req.Description
		if len(description) > tools.MaxMemoryDescriptionChars {
			http.Error(w, fmt.Sprintf("description must be %d characters or fewer", tools.MaxMemoryDescriptionChars), http.StatusBadRequest)
			return
		}
	}
	content := ""
	if req.Content != nil {
		content = *req.Content
	}

	if err := s.db.UpdateMemory(name, memType, description, content); err != nil {
		if errors.Is(err, store.ErrMemoryNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
		} else {
			log.Warn("updating memory failed", "name", name, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	s.db.LogEvent("", "info", "memory", "memory edited via settings panel", map[string]interface{}{"name": name}, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteMemory is a DELETE endpoint in name only — store.DeleteMemory
// soft-deletes (sets disabled = 1), same as handleDeleteThread does for
// threads, so a forgotten memory's record survives and its name is
// revivable later (see store/memory.go's CreateMemory) rather than being
// erased outright.
func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.db.DeleteMemory(name); err != nil {
		if errors.Is(err, store.ErrMemoryNotFound) {
			http.Error(w, "memory not found", http.StatusNotFound)
		} else {
			log.Warn("deleting memory failed", "name", name, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	s.db.LogEvent("", "info", "memory", "memory disabled (soft delete) via settings panel", map[string]interface{}{"name": name}, "")
	w.WriteHeader(http.StatusNoContent)
}

// validMemoryTypes mirrors tools.isValidMemoryType (unexported) — kept as
// its own small set here rather than exporting that function, since this
// is the only place outside the tools package that needs the same check.
var validMemoryTypes = map[string]bool{"user": true, "feedback": true, "project": true, "reference": true}

// maxMemoryChatToolTurns bounds handleMemoryChat's tool-call loop. Raised
// from an initial 4 after a live test showed a single instruction naming
// two unrelated facts ("I prefer metric units and I drink coffee every
// morning") getting merged into one memory instead of two separate write
// calls — the fix is mostly prompts.yaml's memory_chat_system now
// insisting on one call per distinct fact, but a compound instruction
// naming three or four things genuinely needs that many tool-call rounds
// before the final text reply, not just a stricter prompt with no room to
// act on it. Each round can itself carry more than one tool call (a model
// response's ToolCalls can have several entries — see the dispatch loop
// below), so this is turns, not a hard cap on total memory writes.
const maxMemoryChatToolTurns = 6

// handleMemoryChat is the settings panel's "Tell it what to change or
// remove" box — a single free-text instruction, resolved by giving a
// model just the memory tool (not the full agent catalog) and looping
// until it answers in plain text instead of calling the tool again. This
// deliberately isn't a persisted conversation/thread: each call is
// stateless, same as generateTitle/generateSuggestions elsewhere in this
// file's sibling turn.go — the settings panel has no chat history to show
// for it, just an instruction in and a confirmation + refreshed list out.
func (s *Server) handleMemoryChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Instruction string `json:"instruction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	instruction := strings.TrimSpace(req.Instruction)
	if instruction == "" {
		http.Error(w, "instruction is required", http.StatusBadRequest)
		return
	}

	// Same shutdown-draining registration handleTurn/handleAsk use — this
	// makes a real LLM call and mutates the DB, same as any other turn.
	if !s.TryStartTurn() {
		http.Error(w, "the server is restarting — please retry in a few seconds", http.StatusServiceUnavailable)
		return
	}
	defer s.FinishTurn()

	memCtx := &tools.Context{
		Ctx:          r.Context(),
		Emit:         func(string, map[string]interface{}) {},
		ListMemories: s.db.ListMemories,
		GetMemory:    s.db.GetMemory,
		WriteMemory:  s.db.CreateMemory,
		EditMemory:   s.db.UpdateMemory,
		ForgetMemory: s.db.DeleteMemory,
	}
	memoryOnlyDefs := memoryOnlyToolDefs(memCtx)

	cfg := s.liveConfig()
	modelCfg := cfg.ModelByID(s.effectiveDefaultModel(cfg))
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)}).
		WithReasoning(&llm.ReasoningParams{Enabled: boolPtr(false)})

	messages := []llm.ChatMessage{
		{Role: "system", Content: fmt.Sprintf(prompts.Get().Turn.MemoryChatSystem, tools.MemoryIndexPrompt(memCtx))},
		{Role: "user", Content: instruction},
	}

	var confirmation string
	for i := 0; i < maxMemoryChatToolTurns; i++ {
		resp, err := client.ChatCompletionWithTools(r.Context(), messages, memoryOnlyDefs, func(string) {}, nil)
		if err != nil {
			log.Warn("memory chat completion failed", "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(resp.ToolCalls) == 0 {
			confirmation = strings.TrimSpace(resp.Content)
			break
		}
		messages = append(messages, llm.ChatMessage{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			result := tools.Dispatch(tc.Function.Name, tc.Function.Arguments, memCtx)
			messages = append(messages, llm.ChatMessage{Role: "tool", ToolCallID: tc.ID, Content: result})
		}
	}
	if confirmation == "" {
		// Hit maxMemoryChatToolTurns without ever answering in plain text —
		// the requested changes (if any were valid) already landed via the
		// tool calls dispatched above, this is just a missing final
		// summary, not a failed instruction.
		confirmation = "Done."
	}

	memories, err := s.db.ListMemoriesFull()
	if err != nil {
		log.Warn("listing memories after memory chat failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.db.LogEvent("", "info", "memory", "memory changed via settings panel chat", map[string]interface{}{"instruction": instruction}, "")
	writeJSON(w, map[string]interface{}{"message": confirmation, "memories": memories})
}

// memoryOnlyToolDefs filters the normal tool catalog down to just the
// memory tool — tools.Defs(ctx) also returns "think" (it has no Requires
// gate, so it's always offered), which handleMemoryChat has no use for:
// this loop isn't a research turn, there's nothing to reason about other
// than which memory to touch.
func memoryOnlyToolDefs(ctx *tools.Context) []llm.ToolDef {
	var defs []llm.ToolDef
	for _, d := range tools.Defs(ctx) {
		if d.Function.Name == "memory" {
			defs = append(defs, d)
		}
	}
	return defs
}
