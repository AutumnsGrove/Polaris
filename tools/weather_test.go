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

func TestHandleWeather_MultiDayForecastAttachesChart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"current": {"temperature_2m": 68, "apparent_temperature": 66, "relative_humidity_2m": 50, "precipitation": 0, "weather_code": 1, "wind_speed_10m": 5},
			"daily": {"time": ["2026-08-03", "2026-08-04", "2026-08-05"], "weather_code": [2, 1, 0],
				"temperature_2m_max": [72, 75, 80], "temperature_2m_min": [58, 60, 62],
				"precipitation_probability_max": [10, 5, 0]}
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
	handleWeather(`{"forecast_days": 3}`, ctx)

	if ctx.Chart == nil {
		t.Fatal("Chart is nil, want a Tier-1 auto-attached chart for a multi-day forecast")
	}
	if ctx.Chart.Kind != "range" {
		t.Errorf("Chart.Kind = %q, want range", ctx.Chart.Kind)
	}
	if len(ctx.Chart.Series) != 2 {
		t.Fatalf("Chart.Series = %+v, want High and Low series", ctx.Chart.Series)
	}
	if len(ctx.Chart.Series[0].Points) != 3 || ctx.Chart.Series[0].Points[2].Y != 80 {
		t.Errorf("Chart.Series[0].Points = %+v, want 3 points ending at 80", ctx.Chart.Series[0].Points)
	}
}

func TestHandleWeather_SingleDayForecastSetsNoChart(t *testing.T) {
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
		DefaultLocation: "47.6062, -122.3321",
		Emit:            func(string, map[string]interface{}) {},
	}
	handleWeather(`{"forecast_days": 1}`, ctx)

	if ctx.Chart != nil {
		t.Errorf("Chart = %+v, want nil — a single day has nothing worth plotting", ctx.Chart)
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
