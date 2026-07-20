package places

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeocode_EmptyQuery(t *testing.T) {
	geo, err := Geocode(context.Background(), "  ")
	if err != nil || geo != nil {
		t.Errorf("Geocode(empty) = (%+v, %v), want (nil, nil)", geo, err)
	}
}

func TestGeocode_RawCoordinatesSkipTheNetworkCall(t *testing.T) {
	// Point nominatimBaseURL somewhere that would fail any real request,
	// to prove the coordinate shortcut never reaches the network at all.
	old := nominatimBaseURL
	nominatimBaseURL = "http://127.0.0.1:1" // nothing listens here
	defer func() { nominatimBaseURL = old }()

	geo, err := Geocode(context.Background(), "40.7128, -74.0060")
	if err != nil {
		t.Fatalf("Geocode returned error: %v", err)
	}
	if geo == nil || geo.Latitude != 40.7128 || geo.Longitude != -74.0060 {
		t.Errorf("geo = %+v, want the parsed coordinates", geo)
	}
}

func TestGeocode_TextQueryHitsNominatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "Seattle, WA" {
			t.Errorf("query param = %q, want %q", got, "Seattle, WA")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"lat":"47.6062","lon":"-122.3321","display_name":"Seattle, WA, USA"}]`))
	}))
	defer srv.Close()

	old := nominatimBaseURL
	nominatimBaseURL = srv.URL
	defer func() { nominatimBaseURL = old }()

	geo, err := Geocode(context.Background(), "Seattle, WA")
	if err != nil {
		t.Fatalf("Geocode returned error: %v", err)
	}
	if geo == nil || geo.DisplayName != "Seattle, WA, USA" {
		t.Errorf("geo = %+v, want Seattle, WA, USA", geo)
	}
}

func TestGeocode_NoResultsReturnsNilNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	old := nominatimBaseURL
	nominatimBaseURL = srv.URL
	defer func() { nominatimBaseURL = old }()

	geo, err := Geocode(context.Background(), "a place that doesn't exist anywhere")
	if err != nil || geo != nil {
		t.Errorf("Geocode(unresolvable) = (%+v, %v), want (nil, nil)", geo, err)
	}
}

func TestGeocode_ServerErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	old := nominatimBaseURL
	nominatimBaseURL = srv.URL
	defer func() { nominatimBaseURL = old }()

	if _, err := Geocode(context.Background(), "somewhere"); err == nil {
		t.Fatal("expected an error for a 429 response")
	}
}
