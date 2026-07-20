package places

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewFoursquareClient_NilWithoutAPIKey(t *testing.T) {
	if c := NewFoursquareClient(""); c != nil {
		t.Errorf("NewFoursquareClient(\"\") = %v, want nil", c)
	}
	if c := NewFoursquareClient("a-key"); c == nil {
		t.Error("NewFoursquareClient(non-empty) = nil, want a client")
	}
}

func testFoursquareClient(t *testing.T, srv *httptest.Server) *FoursquareClient {
	t.Helper()
	return &FoursquareClient{apiKey: "test-key", baseURL: srv.URL, http: &http.Client{Timeout: 5 * time.Second}}
}

func TestSearchNearby(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"fsq_place_id":"1","name":"Blue Bottle Coffee","location":{"formatted_address":"123 Main St"},"categories":[{"name":"Coffee Shop"}],"distance":250,"latitude":47.61,"longitude":-122.33}
		]}`))
	}))
	defer srv.Close()

	client := testFoursquareClient(t, srv)
	places, err := client.SearchNearby(context.Background(), 47.6062, -122.3321, "coffee", 5000, 5)
	if err != nil {
		t.Fatalf("SearchNearby returned error: %v", err)
	}
	if len(places) != 1 || places[0].Name != "Blue Bottle Coffee" {
		t.Fatalf("places = %+v, want one result named Blue Bottle Coffee", places)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization header = %q, want Bearer test-key", gotAuth)
	}
}

func TestSearchNearby_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer srv.Close()

	client := testFoursquareClient(t, srv)
	if _, err := client.SearchNearby(context.Background(), 0, 0, "x", 1000, 5); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestFormatDistance(t *testing.T) {
	cases := []struct {
		meters int
		want   string
	}{
		{0, "nearby"},
		{-5, "nearby"},
		{500, "500m away"},
		{2000, "2.0km (~25 min walk)"},
		{50000, "50.0km away"},
	}
	for _, c := range cases {
		if got := FormatDistance(c.meters); got != c.want {
			t.Errorf("FormatDistance(%d) = %q, want %q", c.meters, got, c.want)
		}
	}
}

func TestJoinCategories(t *testing.T) {
	if got := JoinCategories(nil); got != "" {
		t.Errorf("JoinCategories(nil) = %q, want empty", got)
	}
	got := JoinCategories([]PlaceCategory{{Name: "Coffee Shop"}, {Name: "Cafe"}})
	if got != "Coffee Shop, Cafe" {
		t.Errorf("JoinCategories = %q, want %q", got, "Coffee Shop, Cafe")
	}
}

func TestPlaceAddress_PrefersFormattedAddress(t *testing.T) {
	p := Place{Location: PlaceLocation{Address: "123 St", FormattedAddress: "123 St, City, ST"}}
	if got := PlaceAddress(p); got != "123 St, City, ST" {
		t.Errorf("PlaceAddress = %q, want the formatted address", got)
	}
	p2 := Place{Location: PlaceLocation{Address: "123 St"}}
	if got := PlaceAddress(p2); got != "123 St" {
		t.Errorf("PlaceAddress (no formatted) = %q, want the plain address", got)
	}
}

func TestPlaceMapsURL(t *testing.T) {
	if got := PlaceMapsURL(Place{}); got != "" {
		t.Errorf("PlaceMapsURL(zero coords) = %q, want empty", got)
	}
	p := Place{Latitude: 47.6, Longitude: -122.3}
	if got := PlaceMapsURL(p); got == "" {
		t.Error("PlaceMapsURL with real coords should not be empty")
	}
}

func TestFilterByRelevance(t *testing.T) {
	places := []Place{
		{Name: "Downtown Coffee Shop"},
		{Name: "Joe's Pizzeria", Categories: []PlaceCategory{{Name: "Restaurant"}}},
	}
	filtered := FilterByRelevance(places, "coffee shop")
	if len(filtered) != 1 || filtered[0].Name != "Downtown Coffee Shop" {
		t.Errorf("filtered = %+v, want just the name/category match, not the pizzeria sharing no query words", filtered)
	}
}

func TestFilterByRelevance_NoMatchesReturnsOriginal(t *testing.T) {
	places := []Place{{Name: "Totally Unrelated"}}
	filtered := FilterByRelevance(places, "coffee")
	if len(filtered) != 1 {
		t.Errorf("filtered = %+v, want the original list back when nothing matches (a weak match beats no results)", filtered)
	}
}

func TestFormatPlaces_Empty(t *testing.T) {
	if got := FormatPlaces(nil); got != "No places found nearby." {
		t.Errorf("FormatPlaces(nil) = %q", got)
	}
}
