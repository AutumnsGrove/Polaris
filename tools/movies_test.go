package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleMovies_NotConfigured(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleMovies(`{"title":"The Martian","media_type":"movie"}`, ctx)
	if result == "" || result[:6] != "error:" || !strings.Contains(result, "aren't configured") {
		t.Errorf("result = %q, want a not-configured error", result)
	}
}

func TestHandleMovies_TitleRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, TMDBAPIKey: "key"}
	result := handleMovies(`{"media_type":"movie"}`, ctx)
	if result == "" || result[:6] != "error:" || !strings.Contains(result, "title is required") {
		t.Errorf("result = %q, want a title-required error", result)
	}
}

func TestHandleMovies_InvalidMediaType(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, TMDBAPIKey: "key"}
	result := handleMovies(`{"title":"The Martian","media_type":"book"}`, ctx)
	if result == "" || result[:6] != "error:" || !strings.Contains(result, "media_type") {
		t.Errorf("result = %q, want a media_type error", result)
	}
}

// fakeTMDB dispatches by request path, mirroring fakeLastFM's
// method-dispatch shape — TMDB's REST paths (not a query-param method
// field) are what distinguish one call site from another here.
func fakeTMDB(t *testing.T, handlers map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, ok := handlers[r.URL.Path]
		if !ok {
			t.Fatalf("unexpected tmdb path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	original := tmdbBaseURL
	tmdbBaseURL = srv.URL
	t.Cleanup(func() { tmdbBaseURL = original })
	return srv
}

func TestHandleMovies_Success(t *testing.T) {
	fakeTMDB(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/search/movie": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 286217, "title": "The Martian", "overview": "An astronaut is stranded on Mars.",
						"release_date": "2015-10-02", "poster_path": "/martian.jpg", "popularity": 50.0},
				},
			})
		},
		"/movie/286217": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"genres": []map[string]interface{}{{"id": 878, "name": "Science Fiction"}, {"id": 12, "name": "Adventure"}},
			})
		},
		"/movie/286217/recommendations": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 157336, "title": "Interstellar", "overview": "A team travels through a wormhole.",
						"release_date": "2014-11-05", "poster_path": "/interstellar.jpg", "popularity": 90.0},
					{"id": 62211, "title": "Gravity", "overview": "An astronaut struggles to survive.",
						"release_date": "2013-10-03", "poster_path": "/gravity.jpg", "popularity": 60.0},
				},
			})
		},
		"/movie/286217/similar": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{"results": []map[string]interface{}{}})
		},
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, TMDBAPIKey: "key"}
	result := handleMovies(`{"title":"The Martian","media_type":"movie"}`, ctx)

	if strings.HasPrefix(result, "error:") {
		t.Fatalf("unexpected error: %s", result)
	}
	if !strings.Contains(result, "The Martian (2015)") {
		t.Errorf("result missing resolved title/year: %s", result)
	}
	if !strings.Contains(result, "Science Fiction") {
		t.Errorf("result missing genres: %s", result)
	}
	if !strings.Contains(result, "Interstellar (2014)") || !strings.Contains(result, "Gravity (2013)") {
		t.Errorf("result missing recommendations: %s", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://www.themoviedb.org/movie/286217" {
		t.Errorf("citations = %+v, want one citation pointing at the resolved movie", ctx.Citations)
	}
	if ctx.Citations[0].ImageURL != "https://image.tmdb.org/t/p/w342/martian.jpg" {
		t.Errorf("citation image = %q", ctx.Citations[0].ImageURL)
	}
	if len(ctx.Cards) != 2 {
		t.Fatalf("cards = %+v, want 2", ctx.Cards)
	}
	if ctx.Cards[0].Title != "Interstellar" || ctx.Cards[0].Subtitle != "2014" {
		t.Errorf("cards[0] = %+v", ctx.Cards[0])
	}
}

func TestHandleMovies_TVMediaType(t *testing.T) {
	fakeTMDB(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/search/tv": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 1396, "name": "Breaking Bad", "overview": "A chemistry teacher turns to crime.",
						"first_air_date": "2008-01-20", "poster_path": "/bb.jpg", "popularity": 300.0},
				},
			})
		},
		"/tv/1396": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{"genres": []map[string]interface{}{{"id": 18, "name": "Drama"}}})
		},
		"/tv/1396/recommendations": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 60059, "name": "Better Call Saul", "overview": "A lawyer's origin story.",
						"first_air_date": "2015-02-08", "poster_path": "/bcs.jpg", "popularity": 200.0},
				},
			})
		},
		"/tv/1396/similar": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{"results": []map[string]interface{}{}})
		},
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, TMDBAPIKey: "key"}
	result := handleMovies(`{"title":"Breaking Bad","media_type":"tv"}`, ctx)

	if strings.HasPrefix(result, "error:") {
		t.Fatalf("unexpected error: %s", result)
	}
	if !strings.Contains(result, "Breaking Bad (2008)") || !strings.Contains(result, "Similar TV shows:") {
		t.Errorf("result = %s", result)
	}
	if !strings.Contains(result, "Better Call Saul (2015)") {
		t.Errorf("result missing tv recommendation: %s", result)
	}
}

func TestHandleMovies_NoResultsFound(t *testing.T) {
	fakeTMDB(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/search/movie": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{"results": []map[string]interface{}{}})
		},
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, TMDBAPIKey: "key"}
	result := handleMovies(`{"title":"Not A Real Movie Xyz","media_type":"movie"}`, ctx)
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "no movie found") {
		t.Errorf("result = %q, want a no-results error", result)
	}
}

func TestHandleMovies_ThinRecommendationsSupplementedBySimilar(t *testing.T) {
	fakeTMDB(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/search/movie": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 1, "title": "New Release", "release_date": "2026-01-01", "popularity": 5.0},
				},
			})
		},
		"/movie/1": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{"genres": []map[string]interface{}{}})
		},
		"/movie/1/recommendations": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{"results": []map[string]interface{}{}})
		},
		"/movie/1/similar": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]interface{}{
				"results": []map[string]interface{}{
					{"id": 2, "title": "Genre Match", "release_date": "2020-01-01", "popularity": 10.0},
				},
			})
		},
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, TMDBAPIKey: "key"}
	result := handleMovies(`{"title":"New Release","media_type":"movie"}`, ctx)

	if strings.HasPrefix(result, "error:") {
		t.Fatalf("unexpected error: %s", result)
	}
	if !strings.Contains(result, "Genre Match (2020)") {
		t.Errorf("result missing supplemented similar title: %s", result)
	}
	if !strings.Contains(result, "supplemented via genre/keyword-based") {
		t.Errorf("result missing supplemented note: %s", result)
	}
}

func TestTmdbPosterURL(t *testing.T) {
	if got := tmdbPosterURL(""); got != "" {
		t.Errorf("tmdbPosterURL(\"\") = %q, want empty", got)
	}
	if got := tmdbPosterURL("/abc.jpg"); got != "https://image.tmdb.org/t/p/w342/abc.jpg" {
		t.Errorf("tmdbPosterURL = %q", got)
	}
}

func TestMergeTMDBResults_Dedup(t *testing.T) {
	base := []tmdbTitle{{ID: 1, Popularity: 5}}
	extra := []tmdbTitle{{ID: 1, Popularity: 99}, {ID: 2, Popularity: 3}}
	merged := mergeTMDBResults(base, extra)
	if len(merged) != 2 {
		t.Fatalf("merged = %+v, want 2 entries (id 1 deduped)", merged)
	}
}
