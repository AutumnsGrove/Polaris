// show_map displays an interactive map (real tiles, pins, drawn-on shapes) or an
// annotated image inline in the conversation. See docs/plans/interactive-maps.md
// for the design; this file is the tool half (validation, geocoding, caps,
// update-by-id merging, the event payload). The frontend half draws it.
//
// The model emits coordinates as data and never places anything in pixels itself.
// Everything the card needs is resolved here, in the handler — a place name is
// geocoded, an update is merged onto the prior card — and sent as one complete
// `map` object on the tool_result, so a card is always reconstructible from its
// own persisted event (a reopened thread never re-runs this code). The card
// renders from that `map` object, not from the call's args.
//
// Like show, a purely display action: it never puts anything into the model's
// context beyond a text description of what the user now sees.
package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"

	"github.com/google/uuid"

	"polaris/llm"
	"polaris/places"
)

const (
	// Per-turn and per-call caps. Heavier than show (geocoding now, tiles and a
	// render once the snapshot lands), and — because store.truncateEventStrings
	// only trims top-level string values — the marker/shape caps are the only
	// thing bounding how large a nested `map` object gets in the events table.
	showMapMaxPerTurn = 3
	showMapMaxMarkers = 50
	showMapMaxShapes  = 30
	showMapMaxLayers  = 8

	showMapDefaultZoom = 15
	showMapMaxZoom     = 19
	// A radius_m past this is a typo (metres vs km), not a real request: the
	// largest thing a "within N of here" question plausibly draws is a few
	// hundred km.
	showMapMaxRadiusM = 500000

	showMapAttribution = "© OpenStreetMap contributors"
)

// MapMarker is one pin, used both as the model's input and the resolved output.
// A "map" card uses Lat/Lon; an "image" card uses X/Y in the image's own pixel
// space, so zooming and panning can never misalign it.
type MapMarker struct {
	ID     string   `json:"id"`
	Lat    *float64 `json:"lat,omitempty"`
	Lon    *float64 `json:"lon,omitempty"`
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	Label  string   `json:"label"`
	Kind   string   `json:"kind,omitempty"` // "place" (default), "landmark", "muted"
	Detail string   `json:"detail,omitempty"`
}

// MapShape is one drawn annotation. Circles use Lat/Lon (+RadiusM) or X/Y
// (+RadiusPx); polylines/polygons/arrows use Points as [lat, lon] pairs on a map
// card and [x, y] pairs on an image card.
type MapShape struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"` // circle | polyline | polygon | arrow
	Lat      *float64    `json:"lat,omitempty"`
	Lon      *float64    `json:"lon,omitempty"`
	X        *float64    `json:"x,omitempty"`
	Y        *float64    `json:"y,omitempty"`
	RadiusM  float64     `json:"radius_m,omitempty"`
	RadiusPx float64     `json:"radius_px,omitempty"`
	Points   [][]float64 `json:"points,omitempty"`
	Label    string      `json:"label,omitempty"`
	Layer    string      `json:"layer"`
}

// MapLayer is what a toggle chip binds to; every shape names one.
type MapLayer struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	DefaultOn bool   `json:"default_on"`
}

// MapView is where the card opens. Everything is optional: with none set the
// frontend frames all non-muted markers and shapes itself.
type MapView struct {
	Center    []float64   `json:"center,omitempty"` // [lat, lon]
	Zoom      int         `json:"zoom,omitempty"`
	Bounds    [][]float64 `json:"bounds,omitempty"` // [[lat, lon], [lat, lon]]
	PlaceName string      `json:"place_name,omitempty"`
}

// MapImage is the base of an "image" card.
type MapImage struct {
	URL  string `json:"url"`
	Path string `json:"path,omitempty"` // set when the source is a workspace file
}

// MapPayload is the complete, resolved state of one card — the `map` field on
// show_map's tool_result.
type MapPayload struct {
	ID          string      `json:"id"`
	Version     int         `json:"version"`
	Kind        string      `json:"kind"`
	Title       string      `json:"title,omitempty"`
	View        MapView     `json:"view"`
	Image       *MapImage   `json:"image,omitempty"`
	Markers     []MapMarker `json:"markers"`
	Shapes      []MapShape  `json:"shapes"`
	Layers      []MapLayer  `json:"layers"`
	Attribution string      `json:"attribution,omitempty"`
	// Snapshot is the workspace-relative filename of the server-rendered static
	// image for this version of the card, when one was made (see
	// show_map_snapshot.go). Empty when snapshots are off or failed.
	Snapshot string `json:"snapshot,omitempty"`
}

// showMapState is a turn's show_map bookkeeping. Tool calls in one model
// response run concurrently (see agent's dispatchToolCallsConcurrently), so
// both the call counter and the card store need a lock.
type showMapState struct {
	mu    sync.Mutex
	calls int
	maps  map[string]*MapPayload
}

var showMapDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "show_map",
		// Description is populated at call time from
		// tools/descriptions/show_map.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"kind": map[string]interface{}{
					"type": "string", "enum": []string{"map", "image"},
					"description": "\"map\" for a real geographic map; \"image\" to annotate a picture (a game map, a screenshot, a diagram). Required unless update is set.",
				},
				"update": map[string]interface{}{
					"type":        "string",
					"description": "The id of a map you already showed THIS turn, to change it instead of showing a second one. Markers/shapes with an existing id are replaced, new ids are added; list ids to delete in remove_ids.",
				},
				"title": map[string]interface{}{"type": "string", "description": "Short title shown above the card."},
				"center": map[string]interface{}{
					"type":        "string",
					"description": "kind=map: where to open — a place name or \"lat, lon\". Optional: omit to frame the markers.",
				},
				"bounds": map[string]interface{}{
					"type": "array", "items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}},
					"description": "kind=map: [[south, west], [north, east]] to frame instead of center/zoom.",
				},
				"zoom": map[string]interface{}{"type": "integer", "description": fmt.Sprintf("kind=map: 1-%d, only with center.", showMapMaxZoom)},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "kind=image: a file in your workspace. Pass exactly one of path or url.",
				},
				"url": map[string]interface{}{
					"type":        "string",
					"description": "kind=image: a remote http(s) image URL. Pass exactly one of path or url.",
				},
				"markers": map[string]interface{}{
					"type":        "array",
					"description": fmt.Sprintf("Up to %d pins. kind=map uses lat/lon; kind=image uses x/y pixels in the image's own coordinates.", showMapMaxMarkers),
					"items": map[string]interface{}{
						"type":     "object",
						"required": []string{"label"},
						"properties": map[string]interface{}{
							"id":     map[string]interface{}{"type": "string", "description": "Optional stable id, so a later update can replace this pin."},
							"lat":    map[string]interface{}{"type": "number"},
							"lon":    map[string]interface{}{"type": "number"},
							"x":      map[string]interface{}{"type": "number"},
							"y":      map[string]interface{}{"type": "number"},
							"label":  map[string]interface{}{"type": "string"},
							"kind":   map[string]interface{}{"type": "string", "enum": []string{"place", "landmark", "muted"}, "description": "landmark = the reference point; muted = out of range / de-emphasised."},
							"detail": map[string]interface{}{"type": "string", "description": "One short line shown when the pin is selected, e.g. \"4 min walk\"."},
						},
					},
				},
				"shapes": map[string]interface{}{
					"type":        "array",
					"description": fmt.Sprintf("Up to %d drawn annotations, each on a layer.", showMapMaxShapes),
					"items": map[string]interface{}{
						"type":     "object",
						"required": []string{"type"},
						"properties": map[string]interface{}{
							"id":        map[string]interface{}{"type": "string"},
							"type":      map[string]interface{}{"type": "string", "enum": []string{"circle", "polyline", "polygon", "arrow"}},
							"lat":       map[string]interface{}{"type": "number", "description": "circle centre (kind=map)"},
							"lon":       map[string]interface{}{"type": "number", "description": "circle centre (kind=map)"},
							"x":         map[string]interface{}{"type": "number", "description": "circle centre (kind=image)"},
							"y":         map[string]interface{}{"type": "number", "description": "circle centre (kind=image)"},
							"radius_m":  map[string]interface{}{"type": "number", "description": "circle radius in metres (kind=map)"},
							"radius_px": map[string]interface{}{"type": "number", "description": "circle radius in pixels (kind=image)"},
							"points": map[string]interface{}{
								"type": "array", "items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "number"}},
								"description": "polyline/polygon/arrow vertices: [lat, lon] pairs (kind=map) or [x, y] pairs (kind=image). An arrow takes exactly 2.",
							},
							"label": map[string]interface{}{"type": "string"},
							"layer": map[string]interface{}{"type": "string", "description": "Which layer toggle this belongs to, e.g. \"radius\" or \"route\". Defaults to \"drawings\"."},
						},
					},
				},
				"layers": map[string]interface{}{
					"type":        "array",
					"description": "Optional toggle definitions: [{id, label, default_on}]. A layer a shape names but you don't declare is created on by default.",
					"items": map[string]interface{}{
						"type":     "object",
						"required": []string{"id", "label"},
						"properties": map[string]interface{}{
							"id": map[string]interface{}{"type": "string"}, "label": map[string]interface{}{"type": "string"},
							"default_on": map[string]interface{}{"type": "boolean"},
						},
					},
				},
				"remove_ids": map[string]interface{}{
					"type": "array", "items": map[string]interface{}{"type": "string"},
					"description": "With update: ids of markers or shapes to delete.",
				},
			},
		},
	},
}

func init() { Register("show_map", handleShowMap) }

type showMapArgs struct {
	Kind      string      `json:"kind"`
	Update    string      `json:"update"`
	Title     string      `json:"title"`
	Center    string      `json:"center"`
	Bounds    [][]float64 `json:"bounds"`
	Zoom      int         `json:"zoom"`
	Path      string      `json:"path"`
	URL       string      `json:"url"`
	Markers   []MapMarker `json:"markers"`
	Shapes    []MapShape  `json:"shapes"`
	Layers    []MapLayer  `json:"layers"`
	RemoveIDs []string    `json:"remove_ids"`
}

func handleShowMap(argsJSON string, ctx *Context, callID string) string {
	var args showMapArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "show_map", nil, "error: "+err.Error(), callID)
	}

	// Only a summary goes in the persisted tool_call: the full markers/shapes
	// live in the tool_result's resolved `map`, and storing them twice would
	// double every card's footprint in the events table for no benefit (the
	// card never renders from args).
	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "show_map",
		"args": map[string]interface{}{
			"kind": args.Kind, "title": args.Title, "update": args.Update,
			"markers": len(args.Markers), "shapes": len(args.Shapes),
		},
		"call_id": callID,
	})

	// Reserve the slot up front so two concurrent calls in one response can't
	// both pass the cap check; give it back if this call doesn't produce a card.
	if !ctx.showMap.reserve() {
		return showMapError(ctx, fmt.Sprintf("already showed %d maps this turn — update one of them (update: its id) or describe the rest in text", showMapMaxPerTurn), callID)
	}
	ok := false
	defer func() {
		if !ok {
			ctx.showMap.release()
		}
	}()

	payload, err := buildMapPayload(ctx, &args)
	if err != nil {
		return showMapError(ctx, err.Error(), callID)
	}
	// Rendered synchronously, before the card is stored/emitted, so the file
	// exists by the model's next step and payload.Snapshot is final when it's
	// shared. Never fails the call: the interactive card works without it.
	snapshot, snapshotNote := snapshotMap(ctx, payload)
	payload.Snapshot = snapshot
	ctx.showMap.store(payload)
	ok = true

	result := describeMap(payload, args.Update != "", snapshotNote)
	log.Info("show_map", "id", payload.ID, "kind", payload.Kind, "version", payload.Version,
		"markers", len(payload.Markers), "shapes", len(payload.Shapes), "thread_id", ctx.ThreadID)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool": "show_map", "result": result, "call_id": callID, "map": payload,
	})
	return result
}

// showMapError reports a failure after the tool_call was already emitted —
// result only, same reasoning as showError.
func showMapError(ctx *Context, msg, callID string) string {
	result := "error: " + msg
	ctx.Emit("tool_result", map[string]interface{}{"tool": "show_map", "result": result, "call_id": callID})
	return result
}

func buildMapPayload(ctx *Context, args *showMapArgs) (*MapPayload, error) {
	var p *MapPayload
	if args.Update != "" {
		prev, ok := ctx.showMap.get(args.Update)
		if !ok {
			return nil, fmt.Errorf("no map %q to update — ids only last for the turn they were shown in; show a new one instead", args.Update)
		}
		if args.Kind != "" && args.Kind != prev.Kind {
			return nil, fmt.Errorf("can't change a %q map into a %q one — show a new map instead", prev.Kind, args.Kind)
		}
		p = prev // already a private copy (see get)
		p.Version++
		if args.Title != "" {
			p.Title = args.Title
		}
	} else {
		if args.Kind != "map" && args.Kind != "image" {
			return nil, fmt.Errorf("kind must be \"map\" or \"image\"")
		}
		// 6 hex chars of a UUID, not a per-turn counter: the id later names the
		// snapshot file in the thread's workspace, and a counter restarting at 1
		// each turn would have turn 2's map overwrite turn 1's.
		p = &MapPayload{ID: "map-" + uuid.NewString()[:6], Version: 1, Kind: args.Kind, Title: args.Title, Attribution: showMapAttribution}
	}

	// Source / view only apply when creating, or when an update re-sets them.
	if p.Kind == "image" {
		if args.Update == "" || args.Path != "" || args.URL != "" {
			img, err := resolveMapImage(ctx, args.Path, args.URL)
			if err != nil {
				return nil, err
			}
			p.Image = img
		}
	} else if args.Center != "" || len(args.Bounds) > 0 {
		view, err := resolveMapView(ctx, args)
		if err != nil {
			return nil, err
		}
		p.View = view
	}

	if err := mergeMapContent(p, args); err != nil {
		return nil, err
	}
	if err := validateMapContent(p); err != nil {
		return nil, err
	}
	if p.Kind == "map" && args.Update == "" && p.View.Center == nil && p.View.Bounds == nil && len(p.Markers) == 0 && len(p.Shapes) == 0 {
		return nil, fmt.Errorf("a map needs a center, bounds, or at least one marker/shape to show")
	}
	return p, nil
}

func resolveMapImage(ctx *Context, path, rawURL string) (*MapImage, error) {
	if (path == "") == (rawURL == "") {
		return nil, fmt.Errorf("kind=image needs exactly one of path or url")
	}
	if path != "" {
		if _, err := resolveWorkspaceFilePath(ctx, path); err != nil {
			return nil, err
		}
		return &MapImage{URL: fmt.Sprintf("/api/workspace/%s/%s", ctx.ThreadID, path), Path: path}, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("url must be an absolute http(s) URL")
	}
	if ctx.Blocklist.Blocked(rawURL) {
		return nil, fmt.Errorf("this image's source is blocked and cannot be shown")
	}
	return &MapImage{URL: rawURL}, nil
}

func resolveMapView(ctx *Context, args *showMapArgs) (MapView, error) {
	var v MapView
	if len(args.Bounds) > 0 {
		if len(args.Bounds) != 2 || len(args.Bounds[0]) != 2 || len(args.Bounds[1]) != 2 {
			return v, fmt.Errorf("bounds must be [[south, west], [north, east]]")
		}
		for _, c := range args.Bounds {
			if err := checkLatLon(c[0], c[1]); err != nil {
				return v, fmt.Errorf("bounds: %w", err)
			}
		}
		v.Bounds = args.Bounds
		return v, nil
	}
	geo, err := places.Geocode(ctx.Ctx, args.Center)
	if err != nil {
		return v, fmt.Errorf("couldn't look up %q: %v", args.Center, err)
	}
	if geo == nil {
		return v, fmt.Errorf("couldn't find a place matching %q — try a more specific name, or pass \"lat, lon\"", args.Center)
	}
	zoom := args.Zoom
	if zoom == 0 {
		zoom = showMapDefaultZoom
	}
	if zoom < 1 || zoom > showMapMaxZoom {
		return v, fmt.Errorf("zoom must be 1-%d", showMapMaxZoom)
	}
	v.Center = []float64{geo.Latitude, geo.Longitude}
	v.Zoom = zoom
	if !geo.FromRawCoordinates {
		v.PlaceName = geo.DisplayName
	}
	return v, nil
}

// mergeMapContent applies args' markers/shapes/layers/remove_ids onto p:
// an existing id is replaced in place (keeping its position, so numbered pins
// don't reshuffle), a new or missing id is appended.
func mergeMapContent(p *MapPayload, args *showMapArgs) error {
	if len(args.RemoveIDs) > 0 && args.Update == "" {
		return fmt.Errorf("remove_ids only applies with update")
	}
	removed := map[string]bool{}
	for _, id := range args.RemoveIDs {
		removed[id] = true
	}
	if len(removed) > 0 {
		var ms []MapMarker
		for _, m := range p.Markers {
			if !removed[m.ID] {
				ms = append(ms, m)
			}
		}
		p.Markers = ms
		var ss []MapShape
		for _, s := range p.Shapes {
			if !removed[s.ID] {
				ss = append(ss, s)
			}
		}
		p.Shapes = ss
	}

	seen := map[string]bool{}
	for _, m := range args.Markers {
		if m.ID != "" {
			if seen[m.ID] {
				return fmt.Errorf("duplicate marker id %q in one call", m.ID)
			}
			seen[m.ID] = true
		}
		if m.ID == "" {
			m.ID = fmt.Sprintf("m%d", len(p.Markers)+1)
			for hasMapID(p, m.ID) {
				m.ID += "x"
			}
		}
		replaced := false
		for i := range p.Markers {
			if p.Markers[i].ID == m.ID {
				p.Markers[i] = m
				replaced = true
				break
			}
		}
		if !replaced {
			p.Markers = append(p.Markers, m)
		}
	}
	for _, s := range args.Shapes {
		if s.ID != "" {
			if seen[s.ID] {
				return fmt.Errorf("duplicate id %q in one call", s.ID)
			}
			seen[s.ID] = true
		}
		if s.ID == "" {
			s.ID = fmt.Sprintf("s%d", len(p.Shapes)+1)
			for hasMapID(p, s.ID) {
				s.ID += "x"
			}
		}
		if s.Layer == "" {
			s.Layer = "drawings"
		}
		replaced := false
		for i := range p.Shapes {
			if p.Shapes[i].ID == s.ID {
				p.Shapes[i] = s
				replaced = true
				break
			}
		}
		if !replaced {
			p.Shapes = append(p.Shapes, s)
		}
	}

	for _, l := range args.Layers {
		replaced := false
		for i := range p.Layers {
			if p.Layers[i].ID == l.ID {
				p.Layers[i] = l
				replaced = true
				break
			}
		}
		if !replaced {
			p.Layers = append(p.Layers, l)
		}
	}
	// A shape naming a layer nobody declared gets one, on by default — failing
	// the whole call over a missing toggle definition would just cost the model
	// a retry for something it clearly meant.
	for _, s := range p.Shapes {
		declared := false
		for _, l := range p.Layers {
			if l.ID == s.Layer {
				declared = true
				break
			}
		}
		if !declared {
			label := strings.ToUpper(s.Layer[:1]) + s.Layer[1:]
			p.Layers = append(p.Layers, MapLayer{ID: s.Layer, Label: label, DefaultOn: true})
		}
	}
	if p.Markers == nil {
		p.Markers = []MapMarker{}
	}
	if p.Shapes == nil {
		p.Shapes = []MapShape{}
	}
	if p.Layers == nil {
		p.Layers = []MapLayer{}
	}
	return nil
}

func hasMapID(p *MapPayload, id string) bool {
	for _, m := range p.Markers {
		if m.ID == id {
			return true
		}
	}
	for _, s := range p.Shapes {
		if s.ID == id {
			return true
		}
	}
	return false
}

func checkLatLon(lat, lon float64) error {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return fmt.Errorf("%v, %v is not a valid lat, lon", lat, lon)
	}
	return nil
}

// validateMapContent checks the merged card as a whole (so an update that
// pushes it over a cap, or leaves a stale-coordinate marker, is caught the same
// as a fresh call). Messages tell the model exactly what to change.
func validateMapContent(p *MapPayload) error {
	if len(p.Markers) > showMapMaxMarkers {
		return fmt.Errorf("too many markers (%d) — a map holds at most %d. Trim it to the strongest and call again", len(p.Markers), showMapMaxMarkers)
	}
	if len(p.Shapes) > showMapMaxShapes {
		return fmt.Errorf("too many shapes (%d) — a map holds at most %d. Trim it down and call again", len(p.Shapes), showMapMaxShapes)
	}
	if len(p.Layers) > showMapMaxLayers {
		return fmt.Errorf("too many layers (%d) — at most %d", len(p.Layers), showMapMaxLayers)
	}
	geo := p.Kind == "map"
	// point validates one coordinate pair in the card's own space.
	point := func(what string, a, b *float64, wantGeo bool) error {
		if wantGeo {
			if a == nil || b == nil {
				return fmt.Errorf("%s needs lat and lon (this is a geographic map)", what)
			}
			return wrapErr(what, checkLatLon(*a, *b))
		}
		if a == nil || b == nil {
			return fmt.Errorf("%s needs x and y in image pixels (this is an image)", what)
		}
		if *a < 0 || *b < 0 {
			return fmt.Errorf("%s: x and y are pixel positions and can't be negative", what)
		}
		return nil
	}
	for _, m := range p.Markers {
		what := fmt.Sprintf("marker %q", m.ID)
		if strings.TrimSpace(m.Label) == "" {
			return fmt.Errorf("%s needs a label", what)
		}
		switch m.Kind {
		case "", "place", "landmark", "muted":
		default:
			return fmt.Errorf("%s: kind must be place, landmark, or muted", what)
		}
		var err error
		if geo {
			err = point(what, m.Lat, m.Lon, true)
		} else {
			err = point(what, m.X, m.Y, false)
		}
		if err != nil {
			return err
		}
	}
	for _, s := range p.Shapes {
		what := fmt.Sprintf("shape %q", s.ID)
		switch s.Type {
		case "circle":
			var err error
			if geo {
				err = point(what, s.Lat, s.Lon, true)
				if err == nil && (s.RadiusM <= 0 || s.RadiusM > showMapMaxRadiusM) {
					err = fmt.Errorf("%s: radius_m must be between 1 and %d metres", what, showMapMaxRadiusM)
				}
			} else {
				err = point(what, s.X, s.Y, false)
				if err == nil && s.RadiusPx <= 0 {
					err = fmt.Errorf("%s: radius_px must be positive", what)
				}
			}
			if err != nil {
				return err
			}
		case "polyline", "polygon", "arrow":
			min := map[string]int{"polyline": 2, "polygon": 3, "arrow": 2}[s.Type]
			if len(s.Points) < min || (s.Type == "arrow" && len(s.Points) != 2) {
				return fmt.Errorf("%s: a %s needs %s points", what, s.Type, map[string]string{"polyline": "at least 2", "polygon": "at least 3", "arrow": "exactly 2"}[s.Type])
			}
			for _, pt := range s.Points {
				if len(pt) != 2 {
					return fmt.Errorf("%s: every point is a [%s] pair", what, map[bool]string{true: "lat, lon", false: "x, y"}[geo])
				}
				a, b := pt[0], pt[1]
				if err := point(what, &a, &b, geo); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%s: type must be circle, polyline, polygon, or arrow", what)
		}
	}
	return nil
}

func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

// describeMap is the model-facing result: what the user now sees. The model
// can't look at a live map, so it needs the ids (to update) and the numbering
// (to refer to pins correctly in its prose).
func describeMap(p *MapPayload, updated bool, snapshotNote string) string {
	var b strings.Builder
	verb := "now showing"
	if updated {
		verb = fmt.Sprintf("updated (v%d) —", p.Version)
	}
	fmt.Fprintf(&b, "%s an interactive %s inline as %q", verb, p.Kind, p.ID)
	if p.Title != "" {
		fmt.Fprintf(&b, " (%q)", p.Title)
	}
	b.WriteString(".")
	if p.View.PlaceName != "" {
		fmt.Fprintf(&b, " Centered on %s.", p.View.PlaceName)
	}
	if len(p.Markers) > 0 {
		b.WriteString("\nPins, in the order the user sees them:")
		n := 0
		for _, m := range p.Markers {
			if m.Kind == "landmark" {
				fmt.Fprintf(&b, "\n- ★ %s [%s]", m.Label, m.ID)
				continue
			}
			n++
			fmt.Fprintf(&b, "\n- %d. %s [%s]", n, m.Label, m.ID)
		}
	}
	if len(p.Shapes) > 0 {
		b.WriteString("\nDrawn on it:")
		for _, s := range p.Shapes {
			fmt.Fprintf(&b, "\n- %s [%s] on layer %q", s.Type, s.ID, s.Layer)
		}
	}
	if snapshotNote != "" {
		b.WriteString("\n" + snapshotNote)
	}
	b.WriteString("\nThe user can pan, zoom, tap pins and toggle layers. Don't re-list the pins in prose beyond what adds something; to change it this turn call show_map with update: \"" + p.ID + "\".")
	return b.String()
}

func (s *showMapState) reserve() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls >= showMapMaxPerTurn {
		return false
	}
	s.calls++
	return true
}

func (s *showMapState) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls--
}

func (s *showMapState) store(p *MapPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maps == nil {
		s.maps = map[string]*MapPayload{}
	}
	s.maps[p.ID] = p
}

// get returns a private copy of the stored card, so an update that fails
// validation halfway through never leaves the stored version half-merged.
func (s *showMapState) get(id string) (*MapPayload, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.maps[id]
	if !ok {
		return nil, false
	}
	cp := *p
	cp.Markers = append([]MapMarker(nil), p.Markers...)
	cp.Shapes = append([]MapShape(nil), p.Shapes...)
	cp.Layers = append([]MapLayer(nil), p.Layers...)
	return &cp, true
}
