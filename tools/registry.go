// Package tools implements the agent's tool-use loop: think, web_search,
// web_read, nearby_search, youtube_transcript, weather, reference_lookup,
// github_repo, dictionary, music, books, and reply. Each tool self-registers
// via init(), mirroring her-go's tools/ package convention.
package tools

import (
	"polaris/llm"
	"polaris/logger"
)

var log = logger.WithPrefix("tools")

type HandlerFunc func(argsJSON string, ctx *Context, callID string) string

var registry = map[string]HandlerFunc{}

func Register(name string, fn HandlerFunc) {
	registry[name] = fn
}

func Dispatch(name, argsJSON string, ctx *Context, callID string) string {
	fn, ok := registry[name]
	if !ok {
		result := "error: unknown tool " + name
		log.Warn("model called unknown tool", "tool", name)
		ctx.Emit("tool_call", map[string]interface{}{"tool": name, "call_id": callID})
		ctx.Emit("tool_result", map[string]interface{}{"tool": name, "result": result, "call_id": callID})
		return result
	}
	return fn(argsJSON, ctx, callID)
}

// emitToolError reports a tool call that failed before doing any real work
// (bad JSON args, a missing required field) — emitting both "tool_call" and
// "tool_result" here, not just returning the error string, so it still
// reaches the durable event log the same way a call that failed partway
// through does (see gateway.logTurnEvent). Without this, an argument
// validation failure was invisible in the event trail: Dispatch's return
// value went straight back to the model with no record a call was ever
// attempted.
func emitToolError(ctx *Context, tool string, args map[string]interface{}, result string, callID string) string {
	ctx.Emit("tool_call", map[string]interface{}{"tool": tool, "args": args, "call_id": callID})
	ctx.Emit("tool_result", map[string]interface{}{"tool": tool, "result": result, "call_id": callID})
	return result
}

// toolDefsByName maps each catalogOrder name to its Go-literal ToolDef —
// the lookup Defs()/AllDefs() iterate over. Function.Description on each
// entry is a placeholder (see e.g. thinkDef) — callers overlay the
// catalog's current APIDescription on top of the copy they get back
// (llm.ToolDef is a plain struct, so byName[name] is already a copy,
// safe to mutate) rather than baking it in here, so an edit to a tool's
// api_description in tools/descriptions/*.yaml is reflected on the very
// next call instead of only at the process's original init() time.
func toolDefsByName() map[string]llm.ToolDef {
	return map[string]llm.ToolDef{
		"think": thinkDef, "calculator": calculatorDef, "current_time": currentTimeDef, "web_search": webSearchDef, "web_read": webReadDef,
		"nearby_search": nearbySearchDef, "youtube_transcript": youtubeTranscriptDef, "weather": weatherDef,
		"reference_lookup": referenceLookupDef, "github_repo": githubRepoDef, "github_activity": githubActivityDef, "dictionary": dictionaryDef,
		"music": musicDef, "books": booksDef, "movies": moviesDef, "code_exec": codeExecDef, "fetch_url": fetchURLDef,
		"image_search": imageSearchDef, "view_image": viewImageDef, "show": showDef, "highlight": highlightDef,
		"ask_user_question": askUserQuestionDef, "memory": memoryDef, "search_chats": searchChatsDef, "stars": starsDef, "spawn_researchers": spawnResearchersDef,
		"finalize_wizard_prompt": finalizeWizardPromptDef,
		"finalize_daily_items":   finalizeDailyItemsDef,
		"search_stars":           searchStarsDef, "read_star": readStarDef, "create_star": createStarDef,
		"update_star": updateStarDef, "link_stars": linkStarsDef,
		"compare_sources": compareSourcesDef, "save_to_field": saveToFieldDef,
		"show_map": showMapDef,
	}
}

// Defs returns the tool definitions offered to the model every turn,
// excluding any tool whose required API key isn't configured on ctx
// (currently music/movies — see catalog.go's catalogEntry.offered).
// There's no explicit "reply" tool — the loop runs with tool_choice
// "auto", so the model free-flows between calling tools and just
// answering directly once it has enough context.
func Defs(ctx *Context) []llm.ToolDef {
	catalog := loadCatalog()
	byName := toolDefsByName()
	defs := make([]llm.ToolDef, 0, len(catalogOrder))
	for _, name := range catalogOrder {
		entry := catalog[name]
		if !entry.offered(ctx) {
			continue
		}
		def := byName[name]
		def.Function.Description = entry.APIDescription
		defs = append(defs, def)
	}
	return defs
}

// AllDefs returns every tool definition, ungated — for agent/pseudocall.go's
// paramSchemaType, which has no per-request Context (it's a static-analysis
// path over pseudo-tool-call syntax, not a real per-turn tool offer).
func AllDefs() []llm.ToolDef {
	catalog := loadCatalog()
	byName := toolDefsByName()
	defs := make([]llm.ToolDef, 0, len(catalogOrder))
	for _, name := range catalogOrder {
		def := byName[name]
		def.Function.Description = catalog[name].APIDescription
		defs = append(defs, def)
	}
	return defs
}
