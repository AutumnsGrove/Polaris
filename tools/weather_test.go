package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleWeather_NoLocationAndNoDefault(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWeather(`{}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want an error for a missing location with no default configured", result)
	}
}

func TestHandleWeather_UsesDefaultLocationAndFormatsForecast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"current": {"temperature_2m": 68, "apparent_temperature": 66, "relative_humidity_2m": 50, "precipitation": 0, "weather_code": 1, "wind_speed_10m": 5},
			"daily": {"time": ["2026-08-03"], "weather_code": [2], "temperature_2m_max": [72], "temperature_2m_min": [58], "precipitation_probability_max": [10]}
		}`))
	}))
	t.Cleanup(srv.Close)
	original := openMeteoBaseURL
	openMeteoBaseURL = srv.URL
	t.Cleanup(func() { openMeteoBaseURL = original })

	ctx := &Context{
		Ctx:             context.Background(),
		DefaultLocation: "47.6062, -122.3321", // coordinate pair skips the Nominatim network call
		Emit:            func(string, map[string]interface{}) {},
	}
	result := handleWeather(`{}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted forecast", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://open-meteo.com/" {
		t.Errorf("Citations = %+v, want the Open-Meteo attribution added", ctx.Citations)
	}
}

func TestHandleWeather_IncludeHourlyAddsHourlyBlock(t *testing.T) {
	var requestedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"current": {"temperature_2m": 68, "apparent_temperature": 66, "relative_humidity_2m": 50, "precipitation": 0, "weather_code": 1, "wind_speed_10m": 5},
			"daily": {"time": ["2026-08-03"], "weather_code": [2], "temperature_2m_max": [72], "temperature_2m_min": [58], "precipitation_probability_max": [10]},
			"hourly": {"time": ["2026-08-03T14:00", "2026-08-03T15:00"], "weather_code": [1, 2], "temperature_2m": [70, 71], "precipitation_probability": [5, 10]}
		}`))
	}))
	t.Cleanup(srv.Close)
	original := openMeteoBaseURL
	openMeteoBaseURL = srv.URL
	t.Cleanup(func() { openMeteoBaseURL = original })

	ctx := &Context{
		Ctx:             context.Background(),
		DefaultLocation: "47.6062, -122.3321",
		Emit:            func(string, map[string]interface{}) {},
	}
	result := handleWeather(`{"include_hourly": true}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted forecast", result)
	}
	if !strings.Contains(result, "Hourly (next 24h):") {
		t.Errorf("result = %q, want an hourly section when include_hourly is true", result)
	}
	if !strings.Contains(result, "2026-08-03 14:00: 70°F") {
		t.Errorf("result = %q, want a formatted hourly row", result)
	}
	if !strings.Contains(requestedQuery, "forecast_hours=24") || !strings.Contains(requestedQuery, "hourly=") {
		t.Errorf("requested query = %q, want hourly and forecast_hours params set", requestedQuery)
	}
}

func TestHandleWeather_OmitsHourlyByDefault(t *testing.T) {
	var requestedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"current": {"temperature_2m": 68, "apparent_temperature": 66, "relative_humidity_2m": 50, "precipitation": 0, "weather_code": 1, "wind_speed_10m": 5},
			"daily": {"time": ["2026-08-03"], "weather_code": [2], "temperature_2m_max": [72], "temperature_2m_min": [58], "precipitation_probability_max": [10]}
		}`))
	}))
	t.Cleanup(srv.Close)
	original := openMeteoBaseURL
	openMeteoBaseURL = srv.URL
	t.Cleanup(func() { openMeteoBaseURL = original })

	ctx := &Context{
		Ctx:             context.Background(),
		DefaultLocation: "47.6062, -122.3321",
		Emit:            func(string, map[string]interface{}) {},
	}
	result := handleWeather(`{}`, ctx)
	if strings.Contains(result, "Hourly") {
		t.Errorf("result = %q, want no hourly section when include_hourly is omitted", result)
	}
	if strings.Contains(requestedQuery, "hourly=") {
		t.Errorf("requested query = %q, want no hourly param when include_hourly is omitted", requestedQuery)
	}
}

func TestWeatherCodeDescription(t *testing.T) {
	cases := map[int]string{
		0:  "clear sky",
		2:  "partly cloudy",
		61: "rain",
		95: "thunderstorm",
		-1: "unknown conditions",
	}
	for code, want := range cases {
		if got := weatherCodeDescription(code); got != want {
			t.Errorf("weatherCodeDescription(%d) = %q, want %q", code, got, want)
		}
	}
}
