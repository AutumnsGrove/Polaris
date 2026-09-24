// current_time gives the model the exact time of day on demand. The system
// prompt's preamble deliberately carries only today's date (see
// agent.currentContextPreamble): the time to the minute used to sit at
// byte ~30 of every request, so no two turns more than a minute apart ever
// shared a cacheable prefix — every follow-up paid full price for its
// entire history. A date changes once a day; the rare question that truly
// needs "right now" (is X open, how long until Y) calls this instead. See
// docs/plans/verbatim-turn-transcripts.md.
package tools

import (
	"time"

	"polaris/llm"
)

var currentTimeDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "current_time",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/current_time.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	},
}

func init() { Register("current_time", handleCurrentTime) }

// now is swappable for tests; production always reads the real clock.
var currentTimeNow = time.Now

func handleCurrentTime(argsJSON string, ctx *Context, callID string) string {
	ctx.Emit("tool_call", map[string]interface{}{"tool": "current_time", "args": map[string]interface{}{}, "call_id": callID})
	result := formatCurrentTime(currentTimeNow())
	ctx.Emit("tool_result", map[string]interface{}{"tool": "current_time", "result": result, "call_id": callID})
	return result
}

// formatCurrentTime includes the zone name and its UTC offset: the name
// alone ("Local", or an abbreviation like "EDT") isn't always enough for
// the model to convert into another city's time correctly.
func formatCurrentTime(t time.Time) string {
	return t.Format("Monday, January 2, 2006, 15:04:05") + " (timezone: " + t.Location().String() + ", UTC" + t.Format("-07:00") + ")"
}
