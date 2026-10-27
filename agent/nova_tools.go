package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"

	"github.com/AutumnsGrove/nova/tool"

	"polaris/llm"
	"polaris/tools"
)

// polarisTool is the one generic wrapper that puts every Polaris tool behind
// Nova's tool.Tool. There is no per-tool code: the handler registry in
// tools/registry.go stays the dispatch table, and the wrapper only adapts the
// call shape (docs/plans/nova-loop.md §5.3).
//
// Parameters is deliberately empty, which tool.ValidateArgs treats as "accept
// any object". Polaris's handlers validate their own arguments and emit a
// tool_call/tool_result pair for a bad call (tools.emitToolError); letting
// Nova reject it first would swallow the card the UI correlates by call_id.
// The schema the *model* sees is separate — see novaToolDefs.
type polarisTool struct {
	name string
	info tool.Info
}

func (t polarisTool) Info() tool.Info { return t.info }

func (t polarisTool) Run(_ context.Context, args json.RawMessage, tctx *tool.Context) (tool.Result, error) {
	pc, _ := tctx.Data.(*tools.Context)
	return tool.Result{Content: dispatchGuarded(t.name, string(args), pc, tctx.CallID)}, nil
}

// dispatchGuarded is tools.Dispatch with the same per-call recover the bespoke
// loop's dispatchToolCallsConcurrently gave every call. Nova's registry already
// recovers a panic inside Run; this keeps the bespoke wording for it, and also
// covers the pseudo-tool-call path, which dispatches from a hook rather than
// through the registry.
func dispatchGuarded(name, argsJSON string, pc *tools.Context, callID string) (result string) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic in tool dispatch", "tool", name, "panic", r, "stack", string(debug.Stack()))
			result = fmt.Sprintf("error: internal error running %s", name)
		}
	}()
	return tools.Dispatch(name, argsJSON, pc, callID)
}

// novaToolRegistry builds this run's registry from the tools Polaris offers it
// (tools.Defs already applies the configured-key gating), in catalogOrder.
// Built per run, not cached: descriptions are the catalog overlay and an edit
// to tools/descriptions/*.yaml must show up on the very next turn.
//
// Every tool is parallel except code_exec: Nova makes a non-parallel call a
// barrier, which is how the bespoke loop's "code_exec runs one at a time, on
// its own goroutine" rule is expressed with no per-tool special case.
func novaToolRegistry(defs []llm.ToolDef) *tool.Registry {
	reg := tool.NewRegistry()
	for _, d := range defs {
		reg.MustRegister(polarisTool{
			name: d.Function.Name,
			info: tool.Info{
				Name:        d.Function.Name,
				Description: d.Function.Description,
				Parallel:    d.Function.Name != "code_exec",
			},
		})
	}
	// Handlers with no catalog row are never offered but stay dispatchable (see
	// tools.UncatalogedHandlers); catalog tools Defs withheld are not here, so
	// a call to a disabled or unconfigured tool is refused.
	for _, name := range tools.UncatalogedHandlers() {
		reg.MustRegister(polarisTool{name: name, info: tool.Info{Name: name, Parallel: true}})
	}
	return reg
}
