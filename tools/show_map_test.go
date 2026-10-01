package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/places"
)

// showMapCapture runs handleShowMap and returns its result plus the emitted
// tool_call / tool_result payloads.
func showMapCapture(t *testing.T, ctx *Context, args string) (result string, call, res map[string]interface{}) {
	t.Helper()
	ctx.Emit = func(event string, payload map[string]interface{}) {
		switch event {
		case "tool_call":
			call = payload
		case "tool_result":
			res = payload
		}
	}
	return handleShowMap(args, ctx, "call-1"), call, res
}

func mapOf(t *testing.T, res map[string]interface{}) *MapPayload {
	t.Helper()
	p, ok := res["map"].(*MapPayload)
	if !ok {
		t.Fatalf("tool_result has no *MapPayload under \"map\": %+v", res)
	}
	return p
}

func TestShowMap_KindRequired(t *testing.T) {
	result, _, _ := showMapCapture(t, newTestContext(), `{}`)
	if !strings.Contains(result, `kind must be "map" or "image"`) {
		t.Errorf("result = %q, want a kind error", result)
	}
}

func TestShowMap_MapWithMarkersAndShapes(t *testing.T) {
	ctx := newTestContext()
	result, call, res := showMapCapture(t, ctx, `{
		"kind":"map","title":"Coffee",
		"markers":[
			{"id":"needle","lat":47.6205,"lon":-122.3493,"label":"Space Needle","kind":"landmark"},
			{"lat":47.6226,"lon":-122.3517,"label":"Storyville","detail":"4 min walk"}
		],
		"shapes":[{"type":"circle","lat":47.6205,"lon":-122.3493,"radius_m":800,"layer":"radius"}]
	}`)
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("result = %q, want success", result)
	}
	p := mapOf(t, res)
	if p.Kind != "map" || p.Version != 1 || !strings.HasPrefix(p.ID, "map-") || p.Attribution == "" {
		t.Errorf("payload = %+v", p)
	}
	if len(p.Markers) != 2 || p.Markers[1].ID == "" {
		t.Errorf("markers = %+v, want 2 with a generated id on the unnamed one", p.Markers)
	}
	// The undeclared "radius" layer is created on rather than failing the call.
	if len(p.Layers) != 1 || p.Layers[0].ID != "radius" || !p.Layers[0].DefaultOn {
		t.Errorf("layers = %+v, want an auto-created default-on radius layer", p.Layers)
	}
	// The persisted tool_call carries counts only, never the full markers.
	args := call["args"].(map[string]interface{})
	if args["markers"] != 2 || args["shapes"] != 1 {
		t.Errorf("tool_call args = %+v, want marker/shape counts only", args)
	}
	for _, want := range []string{p.ID, "★ Space Needle", "1. Storyville", "update:"} {
		if !strings.Contains(result, want) {
			t.Errorf("result = %q, want it to contain %q", result, want)
		}
	}
}

func TestShowMap_CenterIsGeocoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"lat":"47.6205","lon":"-122.3493","display_name":"Space Needle, Seattle"}]`)
	}))
	defer srv.Close()
	defer places.SetNominatimBaseURLForTesting(srv.URL)()

	_, _, res := showMapCapture(t, newTestContext(), `{"kind":"map","center":"Space Needle","zoom":16}`)
	p := mapOf(t, res)
	if len(p.View.Center) != 2 || p.View.Center[0] != 47.6205 || p.View.Zoom != 16 || p.View.PlaceName != "Space Needle, Seattle" {
		t.Errorf("view = %+v, want the geocoded center, requested zoom, and place name", p.View)
	}
}

func TestShowMap_UnresolvableCenter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) }))
	defer srv.Close()
	defer places.SetNominatimBaseURLForTesting(srv.URL)()

	result, _, _ := showMapCapture(t, newTestContext(), `{"kind":"map","center":"zzzxqv"}`)
	if !strings.Contains(result, "couldn't find a place") {
		t.Errorf("result = %q, want a not-found error", result)
	}
}

func TestShowMap_RawLatLonCenterNeedsNoNetwork(t *testing.T) {
	_, _, res := showMapCapture(t, newTestContext(), `{"kind":"map","center":"47.6, -122.3"}`)
	p := mapOf(t, res)
	if p.View.Center[0] != 47.6 || p.View.PlaceName != "" {
		t.Errorf("view = %+v, want parsed coordinates and no place name", p.View)
	}
}

func TestShowMap_EmptyMapRejected(t *testing.T) {
	result, _, _ := showMapCapture(t, newTestContext(), `{"kind":"map"}`)
	if !strings.Contains(result, "needs a center, bounds, or at least one marker") {
		t.Errorf("result = %q", result)
	}
}

func TestShowMap_PerTurnCapAndFailuresDontBurnIt(t *testing.T) {
	ctx := newTestContext()
	const ok = `{"kind":"map","markers":[{"lat":1,"lon":2,"label":"a"}]}`
	// Failed calls hand their slot back, so errors alone can't exhaust the cap.
	for i := 0; i < 5; i++ {
		showMapCapture(t, ctx, `{"kind":"map","markers":[{"lat":999,"lon":2,"label":"bad"}]}`)
	}
	for i := 0; i < showMapMaxPerTurn; i++ {
		if r, _, _ := showMapCapture(t, ctx, ok); strings.HasPrefix(r, "error:") {
			t.Fatalf("call %d: %q, want success", i+1, r)
		}
	}
	r, _, _ := showMapCapture(t, ctx, ok)
	if !strings.Contains(r, "already showed 3 maps this turn") {
		t.Errorf("4th call = %q, want the cap error", r)
	}
}

func TestShowMap_PayloadCaps(t *testing.T) {
	var ms []string
	for i := 0; i <= showMapMaxMarkers; i++ {
		ms = append(ms, fmt.Sprintf(`{"lat":1,"lon":2,"label":"m%d"}`, i))
	}
	r, _, _ := showMapCapture(t, newTestContext(), `{"kind":"map","markers":[`+strings.Join(ms, ",")+`]}`)
	if !strings.Contains(r, "too many markers (51)") {
		t.Errorf("result = %q, want the marker cap error", r)
	}

	var ss []string
	for i := 0; i <= showMapMaxShapes; i++ {
		ss = append(ss, `{"type":"circle","lat":1,"lon":2,"radius_m":10}`)
	}
	r, _, _ = showMapCapture(t, newTestContext(), `{"kind":"map","center":"1, 2","shapes":[`+strings.Join(ss, ",")+`]}`)
	if !strings.Contains(r, "too many shapes (31)") {
		t.Errorf("result = %q, want the shape cap error", r)
	}
}

func TestShowMap_Validation(t *testing.T) {
	cases := map[string]struct{ args, want string }{
		"bad latitude":         {`{"kind":"map","markers":[{"lat":91,"lon":0,"label":"x"}]}`, "not a valid lat, lon"},
		"no label":             {`{"kind":"map","markers":[{"lat":1,"lon":2}]}`, "needs a label"},
		"map marker as xy":     {`{"kind":"map","markers":[{"x":1,"y":2,"label":"x"}]}`, "needs lat and lon"},
		"bad marker kind":      {`{"kind":"map","markers":[{"lat":1,"lon":2,"label":"x","kind":"star"}]}`, "kind must be place, landmark, or muted"},
		"huge radius":          {`{"kind":"map","center":"1, 2","shapes":[{"type":"circle","lat":1,"lon":2,"radius_m":9000000}]}`, "radius_m must be between"},
		"short polyline":       {`{"kind":"map","center":"1, 2","shapes":[{"type":"polyline","points":[[1,2]]}]}`, "at least 2 points"},
		"three-pt arrow":       {`{"kind":"map","center":"1, 2","shapes":[{"type":"arrow","points":[[1,2],[3,4],[5,6]]}]}`, "exactly 2 points"},
		"bad point":            {`{"kind":"map","center":"1, 2","shapes":[{"type":"polyline","points":[[1,2],[3]]}]}`, "pair"},
		"unknown shape":        {`{"kind":"map","center":"1, 2","shapes":[{"type":"star"}]}`, "type must be circle"},
		"image marker latlon":  {`{"kind":"image","url":"https://example.com/a.png","markers":[{"lat":1,"lon":2,"label":"x"}]}`, "needs x and y in image pixels"},
		"negative pixel":       {`{"kind":"image","url":"https://example.com/a.png","markers":[{"x":-5,"y":2,"label":"x"}]}`, "can't be negative"},
		"duplicate ids":        {`{"kind":"map","markers":[{"id":"a","lat":1,"lon":2,"label":"x"},{"id":"a","lat":1,"lon":2,"label":"y"}]}`, "duplicate marker id"},
		"remove_ids no update": {`{"kind":"map","center":"1, 2","remove_ids":["a"]}`, "remove_ids only applies with update"},
		"bad bounds":           {`{"kind":"map","bounds":[[1,2]]}`, "bounds must be"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, _, _ := showMapCapture(t, newTestContext(), tc.args)
			if !strings.HasPrefix(r, "error:") || !strings.Contains(r, tc.want) {
				t.Errorf("result = %q, want an error containing %q", r, tc.want)
			}
		})
	}
}

func TestShowMap_ImageKind(t *testing.T) {
	_, _, res := showMapCapture(t, newTestContext(), `{
		"kind":"image","url":"https://example.com/gw2.png",
		"markers":[{"x":120,"y":340,"label":"Meta"}],
		"shapes":[{"type":"circle","x":120,"y":340,"radius_px":60,"layer":"zone"}]}`)
	p := mapOf(t, res)
	if p.Image == nil || p.Image.URL != "https://example.com/gw2.png" || p.Attribution == "" {
		t.Errorf("payload = %+v, want an image source", p)
	}
	for _, bad := range []string{"javascript:alert(1)", "/api/workspace/x/y.png", "ftp://example.com/a.png"} {
		r, _, _ := showMapCapture(t, newTestContext(), `{"kind":"image","url":"`+bad+`"}`)
		if !strings.Contains(r, "absolute http(s) URL") {
			t.Errorf("url %q: result = %q, want it rejected", bad, r)
		}
	}
	r, _, _ := showMapCapture(t, newTestContext(), `{"kind":"image","url":"https://a.com/x.png","path":"x.png"}`)
	if !strings.Contains(r, "exactly one of path or url") {
		t.Errorf("both sources: result = %q", r)
	}
	r, _, _ = showMapCapture(t, newTestContext(), `{"kind":"image"}`)
	if !strings.Contains(r, "exactly one of path or url") {
		t.Errorf("no source: result = %q", r)
	}
}

func TestShowMap_ImagePathTraversalRejected(t *testing.T) {
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "t1"
	r, _, _ := showMapCapture(t, ctx, `{"kind":"image","path":"../../etc/passwd"}`)
	if !strings.HasPrefix(r, "error:") {
		t.Errorf("result = %q, want a traversal error", r)
	}
}

func TestShowMap_UpdateByID(t *testing.T) {
	ctx := newTestContext()
	_, _, res := showMapCapture(t, ctx, `{"kind":"map","markers":[
		{"id":"a","lat":1,"lon":2,"label":"A"},{"id":"b","lat":3,"lon":4,"label":"B"}],
		"shapes":[{"id":"ring","type":"circle","lat":1,"lon":2,"radius_m":100,"layer":"radius"}]}`)
	id := mapOf(t, res).ID

	// Replace a (keeping its position), add c, drop b and the ring.
	result, _, res := showMapCapture(t, ctx, fmt.Sprintf(`{"update":%q,"title":"New",
		"markers":[{"id":"a","lat":9,"lon":9,"label":"A2"},{"id":"c","lat":5,"lon":6,"label":"C"}],
		"remove_ids":["b","ring"]}`, id))
	if strings.HasPrefix(result, "error:") {
		t.Fatalf("update: %q", result)
	}
	p := mapOf(t, res)
	if p.ID != id || p.Version != 2 || p.Title != "New" {
		t.Errorf("payload = %+v, want same id, version 2, new title", p)
	}
	if len(p.Markers) != 2 || p.Markers[0].Label != "A2" || p.Markers[1].ID != "c" || len(p.Shapes) != 0 {
		t.Errorf("markers/shapes = %+v / %+v, want [A2, C] and no shapes", p.Markers, p.Shapes)
	}
	if !strings.Contains(result, "updated (v2)") {
		t.Errorf("result = %q, want it to say updated", result)
	}
}

func TestShowMap_UpdateFailureLeavesStoredCardUntouched(t *testing.T) {
	ctx := newTestContext()
	_, _, res := showMapCapture(t, ctx, `{"kind":"map","markers":[{"id":"a","lat":1,"lon":2,"label":"A"}]}`)
	id := mapOf(t, res).ID

	r, _, _ := showMapCapture(t, ctx, fmt.Sprintf(`{"update":%q,"markers":[{"id":"z","lat":999,"lon":0,"label":"bad"}]}`, id))
	if !strings.HasPrefix(r, "error:") {
		t.Fatalf("result = %q, want a validation error", r)
	}
	stored, _ := ctx.showMap.get(id)
	if len(stored.Markers) != 1 || stored.Version != 1 {
		t.Errorf("stored card = %+v, want it unchanged after a failed update", stored)
	}
}

func TestShowMap_UpdateRejections(t *testing.T) {
	ctx := newTestContext()
	r, _, _ := showMapCapture(t, ctx, `{"update":"map-nope","markers":[]}`)
	if !strings.Contains(r, `no map "map-nope" to update`) {
		t.Errorf("unknown id: %q", r)
	}
	_, _, res := showMapCapture(t, ctx, `{"kind":"map","markers":[{"lat":1,"lon":2,"label":"A"}]}`)
	r, _, _ = showMapCapture(t, ctx, fmt.Sprintf(`{"update":%q,"kind":"image"}`, mapOf(t, res).ID))
	if !strings.Contains(r, "can't change") {
		t.Errorf("kind change: %q", r)
	}
}

func TestShowMap_IsRegisteredLastInCatalogAndOffered(t *testing.T) {
	if got := catalogOrder[len(catalogOrder)-1]; got != "show_map" {
		t.Errorf("last catalog entry = %q, want show_map (mid-list inserts break prompt-prefix caching)", got)
	}
	found := false
	for _, d := range Defs(newTestContext()) {
		if d.Function.Name == "show_map" {
			found = true
			if d.Function.Description == "" {
				t.Error("show_map offered with an empty description")
			}
		}
	}
	if !found {
		t.Error("show_map not offered on an ordinary context")
	}
	ctx := newTestContext()
	ctx.DisabledTools = map[string]bool{"show_map": true}
	for _, d := range Defs(ctx) {
		if d.Function.Name == "show_map" {
			t.Error("show_map offered despite being disabled")
		}
	}
}
