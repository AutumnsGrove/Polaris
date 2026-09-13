// code_exec runs model-generated Python in a locked-down, ephemeral
// sandbox container — Docker-only (see catalog.go's "docker_only"
// Requires case, gated on Context.CodeExecEnabled). This process never
// touches the Docker socket directly: it hands a request off to a
// host-side script (compose/watcher/codeexec.sh, triggered by a systemd
// path unit) via a plain request/result-file pair, the same handoff
// shape gateway/docker_update.go's update-signal/ already uses — see
// docs/plans/code-execution.md's "How Polaris's own container reaches
// Docker" for why that's a deliberate security boundary, not just an
// implementation convenience.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"polaris/llm"
)

// codeExecGlobalLock serializes every code_exec call across the whole
// process, regardless of thread — see docs/plans/code-execution.md's
// "Concurrency": there's no memory headroom on the potato for two
// sandbox containers running at once. A second call arriving while one
// is in flight simply blocks here rather than racing the host-side
// signal files, which are a single fixed request/result pair per call,
// not a queue.
var codeExecGlobalLock sync.Mutex

// codeExecPollInterval/codeExecResultGracePeriod govern how long
// runCodeExecRequest waits for the host-side script to produce a
// result file — same poll-loop shape as
// gateway/docker_ci_status.go's waitForPublishWorkflow. The grace
// period on top of the caller's own timeout gives the host script's
// `timeout --kill-after` backstop room to actually finish killing the
// container and write a result before this gives up and reports its
// own generic timeout instead of the real one.
const (
	codeExecPollInterval      = 300 * time.Millisecond
	codeExecResultGracePeriod = 10 * time.Second
)

var codeExecDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "code_exec",
		// Description is populated at call time from
		// tools/descriptions/code_exec.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"code": map[string]interface{}{"type": "string",
					"description": "Python source to run. stdout, stderr, and the exit code are returned. " +
						"Files written to the current directory persist across calls within this conversation."},
			},
			"required": []string{"code"},
		},
	},
}

func init() { Register("code_exec", handleCodeExec) }

// codeExecRequest/codeExecResult are the exact JSON shape written to
// and read from Context.CodeExecSignalDir — compose/watcher/codeexec.sh
// parses codeExecRequest's fields and writes codeExecResult's fields by
// hand (it's a bash script, not Go), so a change to either struct's
// JSON tags must be mirrored there too.
type codeExecRequest struct {
	ID               string `json:"id"`
	Code             string `json:"code"`
	HostWorkspaceDir string `json:"host_workspace_dir"`
	MemoryLimitMB    int    `json:"memory_limit_mb"`
	PidsLimit        int    `json:"pids_limit"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
}

type codeExecResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	OOMKilled bool   `json:"oom_killed"`
	// Error is set only when the host script itself failed to run the
	// sandbox at all (e.g. the sandbox image is missing) — distinct from
	// a nonzero ExitCode, which is the executed Python script's own
	// failure and gets reported as a normal result, not this field.
	Error string `json:"error"`
}

func handleCodeExec(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "code_exec", nil, "error: "+err.Error(), callID)
	}
	if args.Code == "" {
		return emitToolError(ctx, "code_exec", nil, "error: code is required", callID)
	}

	ctx.Emit("tool_call", map[string]interface{}{"tool": "code_exec", "args": map[string]interface{}{"code": args.Code}, "call_id": callID})

	if !ctx.CodeExecEnabled {
		// Defense in depth: catalog.go's "docker_only" Requires case
		// should already keep the model from ever calling this on a
		// bare-metal install, but a handler shouldn't trust that it's
		// the only thing standing between a model and this branch.
		result := "error: code execution requires a Docker install — this deployment has no container boundary for running generated code safely"
		ctx.Emit("tool_result", map[string]interface{}{"tool": "code_exec", "result": result, "call_id": callID})
		return result
	}
	if ctx.CodeExecSignalDir == "" || ctx.CodeExecHostWorkspaceDir == "" || ctx.ThreadID == "" {
		result := "error: code execution isn't fully configured on this deployment"
		log.Warn("code_exec enabled but required Context fields are missing",
			"signal_dir", ctx.CodeExecSignalDir, "host_workspace_dir", ctx.CodeExecHostWorkspaceDir, "thread_id", ctx.ThreadID)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "code_exec", "result": result, "call_id": callID})
		return result
	}

	codeExecGlobalLock.Lock()
	defer codeExecGlobalLock.Unlock()

	if err := os.MkdirAll(filepath.Join(ctx.CodeExecWorkspaceDir, ctx.ThreadID), 0o755); err != nil {
		result := "error: couldn't prepare the workspace directory: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "code_exec", "result": result, "call_id": callID})
		return result
	}

	req := codeExecRequest{
		ID:               uuid.NewString(),
		Code:             args.Code,
		HostWorkspaceDir: filepath.Join(ctx.CodeExecHostWorkspaceDir, ctx.ThreadID),
		MemoryLimitMB:    ctx.CodeExecMemoryLimitMB,
		PidsLimit:        ctx.CodeExecPidsLimit,
		TimeoutSeconds:   ctx.CodeExecTimeoutSeconds,
	}

	resultText, err := runCodeExecRequest(ctx.Ctx, ctx.CodeExecSignalDir, req)
	if err != nil {
		result := "error: " + err.Error()
		log.Warn("code_exec failed", "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "code_exec", "result": result, "call_id": callID})
		return result
	}

	ctx.Emit("tool_result", map[string]interface{}{"tool": "code_exec", "result": resultText, "call_id": callID})
	return resultText
}

// runCodeExecRequest writes req to signalDir/requested-<id>.json via a
// temp-file-then-rename (same atomicity reasoning as
// gateway/docker_update.go's writeUpdateSignal — the host-side path
// unit fires the instant a file appears in the directory, so a
// half-written file could otherwise be read mid-write), then polls for
// signalDir/result-<id>.json up to req.TimeoutSeconds plus
// codeExecResultGracePeriod. Returns a plain string, not a structured
// type, since the only consumer is a tool result headed straight back
// to the model.
func runCodeExecRequest(ctx context.Context, signalDir string, req codeExecRequest) (string, error) {
	reqPath := filepath.Join(signalDir, "requested-"+req.ID+".json")
	resultPath := filepath.Join(signalDir, "result-"+req.ID+".json")
	tmpPath := reqPath + ".tmp"

	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encoding sandbox request: %w", err)
	}
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing sandbox request: %w", err)
	}
	if err := os.Rename(tmpPath, reqPath); err != nil {
		return "", fmt.Errorf("committing sandbox request: %w", err)
	}
	// Best-effort: the host script also removes its own copy once
	// picked up, so this only matters if the script never ran at all
	// (e.g. the watcher unit isn't installed/enabled).
	defer os.Remove(reqPath)

	deadline := time.Now().Add(time.Duration(req.TimeoutSeconds)*time.Second + codeExecResultGracePeriod)
	for {
		if data, readErr := os.ReadFile(resultPath); readErr == nil {
			os.Remove(resultPath)
			var result codeExecResult
			if err := json.Unmarshal(data, &result); err != nil {
				return "", fmt.Errorf("parsing sandbox result: %w", err)
			}
			return formatCodeExecResult(result), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for the sandbox runner — is the host-side watcher (compose/watcher/polaris-codeexec.path) running?")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(codeExecPollInterval):
		}
	}
}

// formatCodeExecResult turns a codeExecResult into the plain-text
// summary handed back to the model — a limit hit is framed as an
// ordinary, actionable tool result ("simplify your code"), not an
// error the model should treat as a system failure, per
// docs/plans/code-execution.md's "Other open questions".
func formatCodeExecResult(r codeExecResult) string {
	if r.Error != "" {
		return "error: " + r.Error
	}
	if r.TimedOut {
		return fmt.Sprintf("your code didn't finish within the time limit and was stopped — simplify it or reduce the data size.\nstdout so far:\n%s\nstderr so far:\n%s", r.Stdout, r.Stderr)
	}
	if r.OOMKilled {
		return fmt.Sprintf("your code used too much memory and was stopped — reduce the data size or simplify the computation.\nstdout so far:\n%s\nstderr so far:\n%s", r.Stdout, r.Stderr)
	}
	return fmt.Sprintf("exit code: %d\nstdout:\n%s\nstderr:\n%s", r.ExitCode, r.Stdout, r.Stderr)
}
