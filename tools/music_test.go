package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleMusic_NotConfigured(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleMusic(`{"mode":"track","artist":"Radiohead","track":"Airbag"}`, ctx)
	if result == "" || result[:6] != "error:" || !strings.Contains(result, "aren't configured") {
		t.Errorf("result = %q, want a not-configured error", result)
	}
}

func TestHandleMusic_ArtistRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, LastFMAPIKey: "key"}
	result := handleMusic(`{"mode":"track","track":"Airbag"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want an artist-required error", result)
	}
}

func TestHandleMusic_UnknownMode(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, LastFMAPIKey: "key"}
	result := handleMusic(`{"mode":"vibes","artist":"Radiohead"}`, ctx)
	if result == "" || result[:6] != "error:" || !strings.Contains(result, "unknown mode") {
		t.Errorf("result = %q, want an unknown-mode error", result)
	}
}

func TestHandleMusic_TrackModeRequiresTrack(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, LastFMAPIKey: "key"}
	result := handleMusic(`{"mode":"track","artist":"Radiohead"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a track-required error", result)
	}
}

func TestHandleMusic_AlbumTracksModeRequiresAlbum(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, LastFMAPIKey: "key"}
	result := handleMusic(`{"mode":"album_tracks","artist":"Radiohead"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want an album-required error", result)
	}
}

// fakeLastFM dispatches by the "method" query param, same shape every
// real Last.fm call uses regardless of which of the tool's HTTP helpers
// issued it — one handler covers every mode's fake server needs.
func fakeLastFM(t *testing.T, handlers map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Query().Get("method")
		h, ok := handlers[method]
		if !ok {
			t.Fatalf("unexpected last.fm method %q", method)
		}
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	original := lastfmBaseURL
	lastfmBaseURL = srv.URL
	t.Cleanup(func() { lastfmBaseURL = original })
	return srv
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	b, _ := json.Marshal(v)
	w.Write(b)
}

// fakeDeezer stubs deezerBaseURL for fetchDeezerCoverArt's enrichment
// call — every success-path test needs this stubbed (even ones not
// asserting on ImageURL) so it never makes a real network call to
// production Deezer during `go test`. Returns coverURL for both
// /search/track and /search/album regardless of the query, which is fine
// since these tests only ever resolve one track/album per case.
func fakeDeezer(t *testing.T, coverURL string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/search/album") {
			writeJSON(w, map[string]interface{}{"data": []map[string]interface{}{{"cover_medium": coverURL}}})
			return
		}
		writeJSON(w, map[string]interface{}{
			"data": []map[string]interface{}{{"album": map[string]interface{}{"cover_medium": coverURL}}},
		})
	}))
	t.Cleanup(srv.Close)
	original := deezerBaseURL
	deezerBaseURL = srv.URL
	t.Cleanup(func() { deezerBaseURL = original })
}

func TestHandleMusic_TrackMode_ResolvesToHighestListenerVariant(t *testing.T) {
	// Mirrors the real Isaiah Rashad "Claymore" incident this design was
	// built to handle: a bare title with almost no listeners vs. the real
	// "(feat. X)" variant with far more — resolveTrack must pick the
	// latter, not whichever the search API happens to rank first.
	fakeLastFM(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"track.search": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"results": map[string]interface{}{
					"trackmatches": map[string]interface{}{
						"track": []map[string]interface{}{
							{"name": "Song", "artist": "Test Artist", "listeners": "5", "url": "https://last.fm/thin"},
							{"name": "Song (feat. X)", "artist": "Test Artist", "listeners": "50000", "url": "https://last.fm/real"},
							{"name": "Song", "artist": "Different Artist", "listeners": "999999", "url": "https://last.fm/wrong-artist"},
						},
					},
				},
			})
		},
		"track.getsimilar": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("track") != "Song (feat. X)" {
				t.Errorf("track.getsimilar called with track=%q, want the resolved high-listener variant", r.URL.Query().Get("track"))
			}
			writeJSON(w, map[string]interface{}{
				"similartracks": map[string]interface{}{
					"track": []map[string]interface{}{
						{"name": "Cool Track", "match": 0.9, "artist": map[string]interface{}{"name": "Other Artist"}},
					},
				},
			})
		},
		"track.gettoptags": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"toptags": map[string]interface{}{"tag": []map[string]interface{}{{"name": "rap"}}},
			})
		},
	})
	fakeDeezer(t, "https://cdn.deezer.example/cover.jpg")

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, LastFMAPIKey: "key"}
	result := handleMusic(`{"mode":"track","artist":"Test Artist","track":"Song"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted similar-tracks result", result)
	}
	if !strings.Contains(result, "Cool Track") {
		t.Errorf("result = %q, want it to include the similar track", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://last.fm/real" {
		t.Errorf("Citations = %+v, want the resolved (high-listener) track's URL", ctx.Citations)
	}
	if ctx.Citations[0].ImageURL != "https://cdn.deezer.example/cover.jpg" {
		t.Errorf("Citations[0].ImageURL = %q, want the Deezer cover art enrichment", ctx.Citations[0].ImageURL)
	}
	if len(ctx.Cards) != 1 || ctx.Cards[0].Title != "Cool Track" || ctx.Cards[0].Subtitle != "Other Artist" {
		t.Errorf("Cards = %+v, want one card for the similar track", ctx.Cards)
	}
	if ctx.Cards[0].ImageURL != "https://cdn.deezer.example/cover.jpg" {
		t.Errorf("Cards[0].ImageURL = %q, want the Deezer cover art enrichment", ctx.Cards[0].ImageURL)
	}
}

func TestHandleMusic_AlbumTracksMode_AggregatesAcrossTracklist(t *testing.T) {
	fakeLastFM(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"album.getinfo": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"album": map[string]interface{}{
					"name":   "Test Album",
					"artist": "Test Artist",
					"url":    "https://last.fm/album",
					"tracks": map[string]interface{}{
						"track": []map[string]interface{}{
							{"name": "Track One"},
							{"name": "Track Two"},
						},
					},
				},
			})
		},
		"track.getsimilar": func(w http.ResponseWriter, r *http.Request) {
			// Both source tracks recommend "Shared Hit" — it should rank
			// above a candidate only one track recommends, and the source
			// artist's own tracks must be excluded entirely.
			writeJSON(w, map[string]interface{}{
				"similartracks": map[string]interface{}{
					"track": []map[string]interface{}{
						{"name": "Shared Hit", "match": 0.5, "artist": map[string]interface{}{"name": "Discovery Artist"}},
						{"name": "Own Deep Cut", "match": 0.99, "artist": map[string]interface{}{"name": "Test Artist"}},
					},
				},
			})
		},
	})
	fakeDeezer(t, "https://cdn.deezer.example/cover.jpg")

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, LastFMAPIKey: "key"}
	result := handleMusic(`{"mode":"album_tracks","artist":"Test Artist","album":"Test Album"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted aggregated result", result)
	}
	if strings.Contains(result, "Own Deep Cut") {
		t.Errorf("result = %q, must not include the source artist's own tracks", result)
	}
	if !strings.Contains(result, "Shared Hit") || !strings.Contains(result, "recommended by 2 songs") {
		t.Errorf("result = %q, want Shared Hit credited for both contributing tracks", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://last.fm/album" {
		t.Errorf("Citations = %+v, want the album's own page added", ctx.Citations)
	}
	if ctx.Citations[0].ImageURL != "https://cdn.deezer.example/cover.jpg" {
		t.Errorf("Citations[0].ImageURL = %q, want the Deezer cover art enrichment", ctx.Citations[0].ImageURL)
	}
	if len(ctx.Cards) != 1 || ctx.Cards[0].Title != "Shared Hit" || ctx.Cards[0].Subtitle != "Discovery Artist" {
		t.Errorf("Cards = %+v, want one card for the shared-hit recommendation, none for the excluded same-artist track", ctx.Cards)
	}
}

func TestHandleMusic_SimilarAlbumsMode_ResolvesCandidatesToAlbums(t *testing.T) {
	fakeLastFM(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"album.getinfo": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"album": map[string]interface{}{
					"name":   "Test Album",
					"artist": "Test Artist",
					"url":    "https://last.fm/album",
					"tracks": map[string]interface{}{
						"track": []map[string]interface{}{{"name": "Track One"}},
					},
				},
			})
		},
		"track.getsimilar": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"similartracks": map[string]interface{}{
					"track": []map[string]interface{}{
						{"name": "Candidate Track", "match": 0.8, "artist": map[string]interface{}{"name": "Discovery Artist"}},
					},
				},
			})
		},
		"track.getinfo": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"track": map[string]interface{}{
					"album": map[string]interface{}{
						"artist": "Discovery Artist",
						"title":  "Discovery Album",
						"url":    "https://last.fm/discovery-album",
					},
				},
			})
		},
	})
	fakeDeezer(t, "https://cdn.deezer.example/cover.jpg")

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, LastFMAPIKey: "key"}
	result := handleMusic(`{"mode":"similar_albums","artist":"Test Artist","album":"Test Album"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted similar-albums result", result)
	}
	if !strings.Contains(result, "Discovery Album") {
		t.Errorf("result = %q, want the resolved album included", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].ImageURL != "https://cdn.deezer.example/cover.jpg" {
		t.Errorf("Citations = %+v, want the Deezer cover art enrichment on the source album citation", ctx.Citations)
	}
	if len(ctx.Cards) != 1 || ctx.Cards[0].Title != "Discovery Album" || ctx.Cards[0].Subtitle != "Discovery Artist" {
		t.Errorf("Cards = %+v, want one card for the resolved similar album", ctx.Cards)
	}
	if ctx.Cards[0].URL != "https://last.fm/discovery-album" {
		t.Errorf("Cards[0].URL = %q, want the resolved album's own URL", ctx.Cards[0].URL)
	}
}

// TestFetchDeezerCoverArt_NoMatchReturnsEmpty confirms the enrichment is
// truly best-effort — a Deezer response with no results (bad/obscure
// query, or Deezer itself down) must degrade to "" silently, never error
// or panic, since a citation missing a thumbnail is a non-event, not a
// tool failure.
func TestFetchDeezerCoverArt_NoMatchReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(w, map[string]interface{}{"data": []map[string]interface{}{}})
	}))
	t.Cleanup(srv.Close)
	original := deezerBaseURL
	deezerBaseURL = srv.URL
	t.Cleanup(func() { deezerBaseURL = original })

	ctx := &Context{Ctx: context.Background()}
	if got := fetchDeezerCoverArt(ctx, "track", "Nobody", "Nothing"); got != "" {
		t.Errorf("fetchDeezerCoverArt() = %q, want empty on no match", got)
	}
}

func TestHandleMusic_LastFMAPIError(t *testing.T) {
	fakeLastFM(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"track.search": func(w http.ResponseWriter, r *http.Request) {
			// Last.fm reports its own errors as HTTP 200 with an "error"
			// field, not a 4xx/5xx status — this must still be treated as
			// a failure, not parsed as a (mostly-empty) success.
			writeJSON(w, map[string]interface{}{"error": 6, "message": "Track not found"})
		},
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, LastFMAPIKey: "key"}
	result := handleMusic(`{"mode":"track","artist":"Nobody","track":"Nothing"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want an error surfaced from last.fm's error field", result)
	}
}
