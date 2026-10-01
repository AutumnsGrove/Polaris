package gateway

import (
	"encoding/json"
	"testing"

	"polaris/tools"
)

// show_map's resolved card has to survive both hops out of the tool: the
// WebSocket event the browser renders live, and the persisted "tool call
// finished" row a reopened thread rebuilds the card from. Both copy fields by
// name (turn_emit.go), so a new payload field is silently dropped until it's
// added in each place — the gap this test pins down.
func TestTurnEmitter_CarriesShowMapPayloadLiveAndPersisted(t *testing.T) {
	h := newTestHarness(t, "http://unused")
	if err := h.db.CreateThread("t1", "maps", "mimo-pro", ""); err != nil {
		t.Fatal(err)
	}
	var sent []ServerEvent
	e := newTurnEmitter(h.srvObj, func(ev ServerEvent) { sent = append(sent, ev) }, "t1", "t1", "turn1")

	lat, lon := 47.62, -122.35
	card := &tools.MapPayload{
		ID: "map-abc123", Version: 1, Kind: "map", Title: "Coffee",
		Markers:  []tools.MapMarker{{ID: "a", Lat: &lat, Lon: &lon, Label: "A"}},
		Snapshot: "map-abc123.png",
	}
	e.emit("tool_result", map[string]interface{}{"tool": "show_map", "result": "ok", "call_id": "c1", "map": card})
	// A tool with no map must not grow a "map": null in its persisted row.
	e.emit("tool_result", map[string]interface{}{"tool": "think", "result": "ok", "call_id": "c2"})

	if len(sent) != 2 || sent[0].Map != card {
		t.Fatalf("live event Map = %+v, want the card passed through", sent[0].Map)
	}
	wire, _ := json.Marshal(sent[0])
	var onWire struct {
		Map struct {
			ID      string `json:"id"`
			Markers []struct {
				Label string `json:"label"`
			} `json:"markers"`
		} `json:"map"`
	}
	if err := json.Unmarshal(wire, &onWire); err != nil || onWire.Map.ID != "map-abc123" || len(onWire.Map.Markers) != 1 {
		t.Errorf("wire JSON = %s, want a nested map object with its markers", wire)
	}

	events, err := h.db.ListEvents("t1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawMap, sawNull bool
	for _, ev := range events {
		var data map[string]json.RawMessage
		json.Unmarshal([]byte(ev.Data), &data)
		switch ev.Source {
		case "tool.show_map":
			var persisted tools.MapPayload
			if raw, ok := data["map"]; ok && json.Unmarshal(raw, &persisted) == nil && persisted.ID == "map-abc123" && persisted.Snapshot == "map-abc123.png" && len(persisted.Markers) == 1 {
				sawMap = true
			}
		case "tool.think":
			_, sawNull = data["map"]
		}
	}
	if !sawMap {
		t.Error("persisted show_map event doesn't carry the full card — a reopened thread couldn't rebuild it")
	}
	if sawNull {
		t.Error("a non-map tool's persisted event grew a \"map\" key")
	}
}
