// memory lets the model persist durable facts about the user or ongoing
// work across threads — deliberately modeled on Claude Code's own
// file-based memory system (a short always-in-context index backing
// full content fetched on demand), just backed by store.Store's memories
// table instead of markdown files, since Polaris already has a
// per-install SQLite database and no equivalent of a hand-editable, git-
// tracked memory directory. One tool with three actions (write/edit/view)
// plus forget, rather than four separate tools, keeps the model's tool
// menu from growing by three entries for one feature.
package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"polaris/llm"
	"polaris/store"
)

var memoryDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "memory",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/memory.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"write", "edit", "view", "forget"},
					"description": "write: create a new memory. edit: update an existing one. view: list every memory's name/type/description, or read one in full when name is given. forget: delete one.",
				},
				"name": map[string]interface{}{
					"type": "string",
					"description": "Short kebab-case slug identifying the memory (e.g. \"user-timezone\", \"feedback-commit-style\"). " +
						"Required for write/edit/forget, and for view when reading one memory in full. Omitted on view to list everything.",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"user", "feedback", "project", "reference"},
					"description": "user: who they are, their role/preferences. feedback: guidance on how to approach work, corrections or confirmed approaches. project: facts about ongoing work/goals not obvious from context. reference: pointers to where more detail lives. Required for write; optional for edit (omit to leave unchanged).",
				},
				"description": map[string]interface{}{
					"type": "string",
					"description": fmt.Sprintf("One line summarizing this memory — sent to you every turn as part of the always-visible index, so keep it short and specific (max %d characters). Required for write; optional for edit (omit to leave unchanged).", maxMemoryDescriptionChars),
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "The full memory body, only fetched on demand via view. For feedback/project memories, lead with the fact or rule, then a Why: line and a How to apply: line. Required for write; optional for edit (omit to leave unchanged).",
				},
			},
			"required": []string{"action"},
		},
	},
}

func init() { Register("memory", handleMemory) }

// memoryNameRe enforces the kebab-case-slug shape the tool description
// asks the model for — not strictly necessary for correctness (any string
// works as a SQLite primary key), but a consistent naming scheme is what
// keeps the always-injected index readable as it grows, and catches a
// model accidentally passing a whole sentence as the name early rather
// than storing it and discovering the mess later.
var memoryNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxMemoryDescriptionChars bounds description, not content — description
// is what MemoryIndexPrompt replays into the system prompt on every single
// future turn, in every thread, forever, so an unbounded one is a standing
// per-turn cost that only grows; content is only ever fetched on demand via
// memory(action=view). 2000 is deliberately generous — a few sentences of
// real context (see the write/edit "Why:"/"How to apply:" convention this
// project's own memory files use), not a one-line-only limit — the tool
// description's own "keep it short" guidance is what should keep the common
// case far under this in practice, not the cap itself.
const maxMemoryDescriptionChars = 2000

func handleMemory(argsJSON string, ctx *Context) string {
	var args struct {
		Action      string `json:"action"`
		Name        string `json:"name"`
		Type        string `json:"type"`
		Description string `json:"description"`
		Content     string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "memory", nil, "error: "+err.Error())
	}

	logArgs := map[string]interface{}{"action": args.Action, "name": args.Name}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "memory", "args": logArgs})

	result := dispatchMemoryAction(ctx, args.Action, args.Name, args.Type, args.Description, args.Content)
	ctx.Emit("tool_result", map[string]interface{}{"tool": "memory", "result": result})
	return result
}

func dispatchMemoryAction(ctx *Context, action, name, memType, description, content string) string {
	switch action {
	case "write":
		return handleMemoryWrite(ctx, name, memType, description, content)
	case "edit":
		return handleMemoryEdit(ctx, name, memType, description, content)
	case "view":
		return handleMemoryView(ctx, name)
	case "forget":
		return handleMemoryForget(ctx, name)
	default:
		return "error: unknown action " + action + " — must be one of write, edit, view, forget"
	}
}

func handleMemoryWrite(ctx *Context, name, memType, description, content string) string {
	if ctx.WriteMemory == nil {
		return "error: memory is not available in this context"
	}
	if !memoryNameRe.MatchString(name) {
		return "error: name must be a kebab-case slug (lowercase letters, digits, hyphens), e.g. \"user-timezone\""
	}
	if !isValidMemoryType(memType) {
		return "error: type must be one of user, feedback, project, reference"
	}
	if description == "" || content == "" {
		return "error: description and content are required to write a memory"
	}
	if len(description) > maxMemoryDescriptionChars {
		return fmt.Sprintf("error: description is %d characters, must be %d or fewer — it's shown in full on every future turn, so keep it short and put detail in content instead", len(description), maxMemoryDescriptionChars)
	}
	if err := ctx.WriteMemory(name, memType, description, content); err != nil {
		if err == store.ErrMemoryExists {
			return fmt.Sprintf("error: a memory named %q already exists — use action=edit to update it", name)
		}
		return "error: " + err.Error()
	}
	return fmt.Sprintf("saved memory %q", name)
}

// handleMemoryEdit passes name/memType/description/content straight through
// to ctx.EditMemory with no read-modify-write in between — see
// store.UpdateMemory's doc comment for why a Go-side pre-fetch here would
// reintroduce exactly the concurrent-edit race that a single atomic UPDATE
// avoids. An empty memType/description/content is EditMemory's own signal
// for "leave this field alone", so there's nothing here to merge.
func handleMemoryEdit(ctx *Context, name, memType, description, content string) string {
	if ctx.EditMemory == nil {
		return "error: memory is not available in this context"
	}
	if name == "" {
		return "error: name is required to edit a memory"
	}
	if memType != "" && !isValidMemoryType(memType) {
		return "error: type must be one of user, feedback, project, reference"
	}
	if len(description) > maxMemoryDescriptionChars {
		return fmt.Sprintf("error: description is %d characters, must be %d or fewer — it's shown in full on every future turn, so keep it short and put detail in content instead", len(description), maxMemoryDescriptionChars)
	}
	if err := ctx.EditMemory(name, memType, description, content); err != nil {
		if err == store.ErrMemoryNotFound {
			return fmt.Sprintf("error: no memory named %q — use action=write to create it", name)
		}
		return "error: " + err.Error()
	}
	return fmt.Sprintf("updated memory %q", name)
}

func handleMemoryView(ctx *Context, name string) string {
	if name != "" {
		if ctx.GetMemory == nil {
			return "error: memory is not available in this context"
		}
		m, err := ctx.GetMemory(name)
		if err != nil {
			if err == store.ErrMemoryNotFound {
				return fmt.Sprintf("no memory named %q", name)
			}
			return "error: " + err.Error()
		}
		return fmt.Sprintf("[%s] %s — %s\n\n%s", m.Type, m.Name, m.Description, m.Content)
	}

	if ctx.ListMemories == nil {
		return "error: memory is not available in this context"
	}
	entries, err := ctx.ListMemories()
	if err != nil {
		return "error: " + err.Error()
	}
	if len(entries) == 0 {
		return "no memories saved yet"
	}
	return formatMemoryIndex(entries)
}

func handleMemoryForget(ctx *Context, name string) string {
	if ctx.ForgetMemory == nil {
		return "error: memory is not available in this context"
	}
	if name == "" {
		return "error: name is required to forget a memory"
	}
	if err := ctx.ForgetMemory(name); err != nil {
		if err == store.ErrMemoryNotFound {
			return fmt.Sprintf("no memory named %q", name)
		}
		return "error: " + err.Error()
	}
	return fmt.Sprintf("forgot memory %q", name)
}

func isValidMemoryType(t string) bool {
	switch t {
	case "user", "feedback", "project", "reference":
		return true
	default:
		return false
	}
}

// MemoryIndexPrompt renders the always-injected {memories} placeholder's
// replacement text — the same per-entry format as memory(action=view)'s
// list-all output (see formatMemoryIndex), or a short "nothing yet" line
// when there are none, so prompt.md never has to special-case an empty
// memory store. Mirrors tools.ToolsPrompt's shape/rationale exactly.
// Returns "" (and callers should render nothing at all) when memory isn't
// wired into ctx, e.g. the benchmark harness's isolated Context — see
// agent/driver.go's applyMemoriesPlaceholder.
func MemoryIndexPrompt(ctx *Context) string {
	if ctx.ListMemories == nil {
		return ""
	}
	entries, err := ctx.ListMemories()
	if err != nil {
		log.Warn("loading memory index for system prompt failed", "err", err)
		return ""
	}
	if len(entries) == 0 {
		return "(none saved yet)"
	}
	return formatMemoryIndex(entries)
}

// formatMemoryIndex renders one "- [type] name: description" line per
// entry — the single formatting used both by the always-injected
// {memories} prompt block (MemoryIndexPrompt) and by memory(action=view)'s
// list-all output (handleMemoryView), so a model that's learned to read one
// never sees the same data reformatted differently when it double-checks
// via the other.
func formatMemoryIndex(entries []store.MemoryIndexEntry) string {
	var sb strings.Builder
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "- [%s] %s: %s", e.Type, e.Name, e.Description)
	}
	return sb.String()
}
