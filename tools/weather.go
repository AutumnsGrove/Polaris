// weather fetches current conditions and a short forecast for a location
// via Open-Meteo — free, keyless, no rate-limit headaches for personal use
// (https://open-meteo.com), the same "no API key required" bar as
// web_search/web_read's free paths. Reuses places.Geocode for location
// resolution, same as nearby_search.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"polaris/llm"
	"polaris/places"
)

var weatherDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "weather",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/weather.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"location": map[string]interface{}{
					"type": "string",
					"description": "Where to get weather for — an address, place name, or 'lat, lon'. " +
						"Omit to use the configured default location, if one is set.",
				},
				"forecast_days": map[string]interface{}{
					"type":        "integer",
					"description": "How many days ahead to forecast, including today (default 3, max 7).",
				},
				"include_hourly": map[string]interface{}{
					"type": "boolean",
					"description": "Set true to also return an hour-by-hour forecast (temperature, " +
						"precipitation chance, conditions) for the next 24 hours. Default false — most " +
						"questions only need the daily summary.",
				},
			},
			"required": []string{},
		},
	},
}

func init() { Register("weather", handleWeather) }

// openMeteoBaseURL is a var (not a const) so tests can point it at a fake
// server, same pattern as places.nominatimBaseURL and web_read.go's
// waybackAvailabilityAPI.
var openMeteoBaseURL = "https://api.open-meteo.com/v1/forecast"

func handleWeather(argsJSON string, ctx *Context) string {
	var args struct {
		Location      string `json:"location"`
		ForecastDays  int    `json:"forecast_days"`
		IncludeHourly bool   `json:"include_hourly"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "weather", nil, "error: "+err.Error())
	}
	if args.ForecastDays <= 0 || args.ForecastDays > 7 {
		args.ForecastDays = 3
	}

	locationQuery := ctx.ResolveLocation(args.Location)
	if locationQuery == "" {
		return emitToolError(ctx, "weather", map[string]interface{}{"location": args.Location},
			"error: no location given and no default_location configured — specify a location")
	}

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "weather",
		"args": map[string]interface{}{"location": locationQuery},
	})

	geo, err := places.Geocode(ctx.Ctx, locationQuery)
	if err != nil || geo == nil {
		result := fmt.Sprintf("error: couldn't resolve location %q", locationQuery)
		log.Warn("weather: geocode failed", "location", locationQuery, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "weather", "result": result})
		return result
	}

	forecast, err := fetchWeather(ctx.Ctx, geo.Latitude, geo.Longitude, args.ForecastDays, args.IncludeHourly)
	if err != nil {
		result := "error: " + err.Error()
		log.Warn("weather: fetch failed", "location", geo.DisplayName, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "weather", "result": result})
		return result
	}

	summary := formatWeather(geo.DisplayName, forecast)
	ctx.AddCitation(Citation{Title: "Open-Meteo forecast", URL: "https://open-meteo.com/"})
	setWeatherChart(ctx, geo.DisplayName, forecast)
	log.Info("weather", "location", geo.DisplayName, "forecast_days", args.ForecastDays, "include_hourly", args.IncludeHourly)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "weather",
		"result":    summary,
		"citations": ctx.CitationsSnapshot(),
		"chart":     ctx.ChartSnapshot(),
	})
	return summary
}

// setWeatherChart is Tier 1's only v1 source (see
// docs/plans/visualize-and-image-search.md) — a deterministic chart
// attached from data formatWeather already has in scope, no extra fetch
// and no model round-trip. Only fires for a multi-day forecast; a
// single-day/current-only call has nothing worth plotting.
//
// Kind is "range", not "line" — live-tested against a real multi-day
// forecast and a plain two-line High/Low chart genuinely didn't work for
// this: no hover/tooltip in a static SVG made the compressed axis hard to
// read at a glance, and the values were only inferrable, not readable.
// "range" is a Tier-1-only kind: never in visualize's own kind enum (see
// visualize.go), so it needs no schema/model-facing change — ChartCard.
// svelte just renders whatever Kind string arrives, and only this
// function ever sets "range". Series[0] must be High, Series[1] Low, same
// order/count — that positional contract lives here and in ChartCard.
// svelte's "range" case, not enforced by the type system.
func setWeatherChart(ctx *Context, displayName string, f *openMeteoResponse) {
	if len(f.Daily.Time) <= 1 {
		return
	}
	highs := make([]ChartPoint, len(f.Daily.Time))
	lows := make([]ChartPoint, len(f.Daily.Time))
	icons := make([]string, len(f.Daily.Time))
	for i, day := range f.Daily.Time {
		highs[i] = ChartPoint{X: day, Y: f.Daily.TempMax[i]}
		lows[i] = ChartPoint{X: day, Y: f.Daily.TempMin[i]}
		icons[i] = weatherCodeIcon(f.Daily.WeatherCode[i])
	}
	ctx.SetChart(ChartSpec{
		Kind:   "range",
		Title:  fmt.Sprintf("Forecast for %s", displayName),
		XLabel: "Date",
		YLabel: "°F",
		Series: []ChartSeries{
			{Label: "High", Points: highs},
			{Label: "Low", Points: lows},
		},
		Icons: icons,
	})
}

// openMeteoResponse is the subset of Open-Meteo's forecast response this
// needs. Units are requested as imperial (fahrenheit/mph/inch) via query
// params rather than parsed from a units block, since the request always
// asks for the same ones.
type openMeteoResponse struct {
	Current struct {
		Temperature   float64 `json:"temperature_2m"`
		FeelsLike     float64 `json:"apparent_temperature"`
		Humidity      float64 `json:"relative_humidity_2m"`
		Precipitation float64 `json:"precipitation"`
		WeatherCode   int     `json:"weather_code"`
		WindSpeed     float64 `json:"wind_speed_10m"`
	} `json:"current"`
	Daily struct {
		Time          []string  `json:"time"`
		WeatherCode   []int     `json:"weather_code"`
		TempMax       []float64 `json:"temperature_2m_max"`
		TempMin       []float64 `json:"temperature_2m_min"`
		PrecipProbMax []float64 `json:"precipitation_probability_max"`
	} `json:"daily"`
	// Hourly is only populated when fetchWeather is called with
	// includeHourly true — Open-Meteo omits the whole "hourly" object
	// from its response when the request never asked for it, so this is
	// naturally empty (not just unused) on a normal daily-only call.
	Hourly struct {
		Time        []string  `json:"time"`
		WeatherCode []int     `json:"weather_code"`
		Temperature []float64 `json:"temperature_2m"`
		PrecipProb  []float64 `json:"precipitation_probability"`
	} `json:"hourly"`
}

// hourlyForecastHours caps the hourly block at the next 24 hours,
// independent of forecastDays — Open-Meteo's own "forecast_hours" param
// (separate from "forecast_days", which drives the daily block) returns
// exactly the next N hours starting from the current hour, rather than
// every hour across every requested day (7 days of hourly rows would be
// 168 lines, far more than any answer needs "hour by hour" to mean).
const hourlyForecastHours = 24

func fetchWeather(ctx context.Context, lat, lon float64, forecastDays int, includeHourly bool) (*openMeteoResponse, error) {
	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", lat))
	q.Set("longitude", fmt.Sprintf("%.4f", lon))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,precipitation,weather_code,wind_speed_10m")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max")
	q.Set("temperature_unit", "fahrenheit")
	q.Set("wind_speed_unit", "mph")
	q.Set("precipitation_unit", "inch")
	q.Set("timezone", "auto")
	q.Set("forecast_days", fmt.Sprintf("%d", forecastDays))
	if includeHourly {
		q.Set("hourly", "temperature_2m,precipitation_probability,weather_code")
		q.Set("forecast_hours", fmt.Sprintf("%d", hourlyForecastHours))
	}

	endpoint := openMeteoBaseURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating weather request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching weather: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading weather response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open-meteo error (status %d): %s", resp.StatusCode, string(body))
	}

	var out openMeteoResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parsing weather response: %w", err)
	}
	return &out, nil
}

func formatWeather(displayName string, f *openMeteoResponse) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Weather for %s:\n\n", displayName)
	fmt.Fprintf(&sb, "Now: %.0f°F (feels like %.0f°F), %s, %.0f%% humidity, wind %.0f mph\n",
		f.Current.Temperature, f.Current.FeelsLike, weatherCodeDescription(f.Current.WeatherCode),
		f.Current.Humidity, f.Current.WindSpeed)

	if len(f.Daily.Time) > 0 {
		sb.WriteString("\nForecast:\n")
		for i := range f.Daily.Time {
			fmt.Fprintf(&sb, "- %s: %s, high %.0f°F / low %.0f°F, %.0f%% chance of precipitation\n",
				f.Daily.Time[i], weatherCodeDescription(f.Daily.WeatherCode[i]),
				f.Daily.TempMax[i], f.Daily.TempMin[i], f.Daily.PrecipProbMax[i])
		}
	}

	if len(f.Hourly.Time) > 0 {
		sb.WriteString("\nHourly (next 24h):\n")
		for i := range f.Hourly.Time {
			// Open-Meteo's hourly "time" is a local ISO datetime
			// (2026-08-03T14:00) with timezone=auto already applied — just
			// the "T" swapped for a space reads naturally without needing
			// to parse and reformat it.
			ts := strings.Replace(f.Hourly.Time[i], "T", " ", 1)
			fmt.Fprintf(&sb, "- %s: %.0f°F, %s, %.0f%% chance of precipitation\n",
				ts, f.Hourly.Temperature[i], weatherCodeDescription(f.Hourly.WeatherCode[i]), f.Hourly.PrecipProb[i])
		}
	}

	return sb.String()
}

// weatherCodeDescription maps WMO weather interpretation codes (the
// scheme Open-Meteo uses) to plain text.
// https://open-meteo.com/en/docs#weathervariables
func weatherCodeDescription(code int) string {
	switch {
	case code == 0:
		return "clear sky"
	case code == 1:
		return "mainly clear"
	case code == 2:
		return "partly cloudy"
	case code == 3:
		return "overcast"
	case code == 45 || code == 48:
		return "fog"
	case code >= 51 && code <= 55:
		return "drizzle"
	case code == 56 || code == 57:
		return "freezing drizzle"
	case code >= 61 && code <= 65:
		return "rain"
	case code == 66 || code == 67:
		return "freezing rain"
	case code >= 71 && code <= 75:
		return "snow"
	case code == 77:
		return "snow grains"
	case code == 80 || code == 81 || code == 82:
		return "rain showers"
	case code == 85 || code == 86:
		return "snow showers"
	case code == 95:
		return "thunderstorm"
	case code == 96 || code == 99:
		return "thunderstorm with hail"
	default:
		return "unknown conditions"
	}
}

// weatherCodeIcon maps the same WMO codes weatherCodeDescription reads to
// a small, fixed vocabulary of icon keys — ChartCard.svelte's iconFor
// maps each one to a Lucide icon component. A closed vocabulary
// (deliberately not "return the raw code and let the frontend figure it
// out"): keeping the code -> category mapping in one place (here) means
// the frontend only ever needs a lookup table, never its own copy of
// Open-Meteo's WMO code ranges.
func weatherCodeIcon(code int) string {
	switch {
	case code == 0 || code == 1:
		return "clear"
	case code == 2:
		return "partly-cloudy"
	case code == 3:
		return "cloudy"
	case code == 45 || code == 48:
		return "fog"
	case code >= 51 && code <= 57:
		return "drizzle"
	case (code >= 61 && code <= 67) || (code >= 80 && code <= 82):
		return "rain"
	case (code >= 71 && code <= 77) || code == 85 || code == 86:
		return "snow"
	case code == 95 || code == 96 || code == 99:
		return "thunderstorm"
	default:
		return "cloudy"
	}
}
