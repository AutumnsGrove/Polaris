// nearby_search finds real-world places (restaurants, pharmacies, parks,
// etc.) near a location. Foursquare gives structured results (distance,
// category, a Maps link surfaced as a citation); if Foursquare isn't
// configured, this falls back to a plain SearXNG search for
// "<query> near <location>" instead of failing outright.
package tools

import (
	"encoding/json"
	"fmt"

	"polaris/llm"
	"polaris/places"
)

var nearbySearchDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "nearby_search",
		Description: "Find real-world places near a location — restaurants, coffee shops, pharmacies, " +
			"parks, etc. Returns structured results (distance, category, map link) when Foursquare is " +
			"configured, otherwise falls back to a web search for the same query.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "What kind of place to find, e.g. 'coffee shop', 'pharmacy', 'bookstore'.",
				},
				"location": map[string]interface{}{
					"type": "string",
					"description": "Where to search near — an address, place name, or 'lat, lon'. " +
						"Omit to use the configured default location, if one is set.",
				},
				"radius_km": map[string]interface{}{
					"type":        "number",
					"description": "Search radius in kilometers (default 5, max 100).",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Max results to return (default 5, max 50).",
				},
			},
			"required": []string{"query"},
		},
	},
}

func init() { Register("nearby_search", handleNearbySearch) }

func handleNearbySearch(argsJSON string, ctx *Context) string {
	var args struct {
		Query    string  `json:"query"`
		Location string  `json:"location"`
		RadiusKM float64 `json:"radius_km"`
		Limit    int     `json:"limit"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "error: " + err.Error()
	}
	if args.Query == "" {
		return "error: query is required (e.g. 'coffee shop', 'pharmacy')"
	}
	if args.Limit <= 0 || args.Limit > 50 {
		args.Limit = 5
	}
	if args.RadiusKM <= 0 {
		args.RadiusKM = 5
	}
	radiusM := int(args.RadiusKM * 1000)

	locationQuery := args.Location
	if locationQuery == "" {
		locationQuery = ctx.DefaultLocation
	}
	if locationQuery == "" {
		return "error: no location given and no default_location configured — specify a location"
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "nearby_search",
		"args": map[string]interface{}{"query": args.Query, "location": locationQuery},
	})

	geo, err := places.Geocode(ctx.Ctx, locationQuery)
	if err != nil || geo == nil {
		result := fmt.Sprintf("error: couldn't resolve location %q", locationQuery)
		log.Warn("nearby_search: geocode failed", "location", locationQuery, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "nearby_search", "result": result})
		return result
	}

	if ctx.Foursquare != nil {
		matches, err := ctx.Foursquare.SearchNearby(ctx.Ctx, geo.Latitude, geo.Longitude, args.Query, radiusM, args.Limit)
		if err == nil {
			matches = places.FilterByRelevance(matches, args.Query)
			for _, p := range matches {
				if mapsURL := places.PlaceMapsURL(p); mapsURL != "" {
					ctx.AddCitation(Citation{Title: p.Name, URL: mapsURL})
				}
			}
			summary := "Searching near: " + geo.DisplayName + "\n\n" + places.FormatPlaces(matches)
			log.Info("nearby_search (foursquare)", "query", args.Query, "location", geo.DisplayName, "matches", len(matches))
			ctx.Emit("tool_result", map[string]interface{}{
				"tool": "nearby_search", "result": summary, "citations": ctx.Citations,
			})
			return summary
		}
		// Foursquare call failed — fall through to the SearXNG fallback below.
		log.Warn("nearby_search: foursquare failed, falling back to web search", "query", args.Query, "err", err)
	}

	// No Foursquare (or it errored): plain web search for the same intent.
	if ctx.SearXNG != nil {
		resp, err := ctx.SearXNG.Search(ctx.Ctx, args.Query+" near "+geo.DisplayName, args.Limit, "")
		if err == nil {
			var summary string
			for i, r := range resp.Results {
				summary += fmt.Sprintf("%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Content)
				ctx.AddCitation(Citation{Title: r.Title, URL: r.URL})
			}
			if summary == "" {
				summary = "No places found nearby."
			}
			log.Info("nearby_search (searxng fallback)", "query", args.Query, "location", geo.DisplayName, "results", len(resp.Results))
			ctx.Emit("tool_result", map[string]interface{}{
				"tool": "nearby_search", "result": summary, "citations": ctx.Citations,
			})
			return summary
		}
	}

	result := "error: nearby search failed (Foursquare not configured or unreachable, and web search fallback also failed)"
	log.Warn("nearby_search: all paths failed", "query", args.Query, "location", locationQuery)
	ctx.Emit("tool_result", map[string]interface{}{"tool": "nearby_search", "result": result})
	return result
}
