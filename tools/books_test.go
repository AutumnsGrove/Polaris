package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHandleBooks_TitleRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleBooks(`{"title":""}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a title-required error", result)
	}
}

// TestHardcoverListDensity locks down the fix for a real quality bug caught
// during live verification: ranking curated lists by raw likes_count alone
// skewed recommendations toward generic "best books ever" lists (big reach,
// lots of likes spread across ~100 unrelated picks) over small lists a
// reader curated with real intent (few books, fewer but denser likes).
func TestHardcoverListDensity(t *testing.T) {
	// Raw likes_count alone would rank generalist above niche (500 > 50)
	// even though niche's likes are far more concentrated (5/book vs
	// 2.5/book) — density must invert that ordering.
	niche := hardcoverList{LikesCount: 50, BooksCount: 10}
	generalist := hardcoverList{LikesCount: 500, BooksCount: 200}
	if niche.LikesCount >= generalist.LikesCount {
		t.Fatalf("test fixture invalid: niche must have fewer raw likes than generalist to prove the point")
	}
	if niche.density() <= generalist.density() {
		t.Errorf("niche density %.3f should exceed generalist density %.3f despite fewer raw likes",
			niche.density(), generalist.density())
	}
	if got := (hardcoverList{LikesCount: 10, BooksCount: 0}).density(); got != 0 {
		t.Errorf("density() with zero BooksCount = %v, want 0 (avoid divide-by-zero)", got)
	}
}

// fakeHardcover dispatches every request by inspecting the GraphQL
// variables sent, not the query text, since fetchHardcoverLists and
// fetchListBooks both hit the same `list_books` root field — the variable
// shape ($bookID vs $listID vs $q) is what actually distinguishes them.
func fakeHardcover(t *testing.T, handler func(query string, variables map[string]interface{}) (interface{}, int)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding hardcover request: %v", err)
		}
		data, status := handler(body.Query, body.Variables)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		writeJSON(w, data)
	}))
	t.Cleanup(srv.Close)
	original := hardcoverBaseURL
	hardcoverBaseURL = srv.URL
	t.Cleanup(func() { hardcoverBaseURL = original })
}

// fakeOpenLibrary dispatches by path: /search.json for resolution,
// /subjects/*.json for the fallback/supplement aggregation fan-out.
func fakeOpenLibrary(t *testing.T, handler func(path string, query url.Values) (interface{}, int)) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, status := handler(r.URL.Path, r.URL.Query())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		writeJSON(w, data)
	}))
	t.Cleanup(srv.Close)
	original := openLibraryBaseURL
	openLibraryBaseURL = srv.URL
	t.Cleanup(func() { openLibraryBaseURL = original })
}

func hcSearchHit(id, title, slug string, usersReadCount int, authors, genres []string) map[string]interface{} {
	return map[string]interface{}{
		"document": map[string]interface{}{
			"id": id, "title": title, "slug": slug,
			"users_read_count": usersReadCount, "author_names": authors, "genres": genres,
		},
	}
}

func hcBookRow(id int, title, slug, author string) map[string]interface{} {
	return hcBookRowWithGenres(id, title, slug, author, nil)
}

// hcBookRowWithGenres mirrors the raw `books` type's nested cached_tags
// shape (category -> tags), not hcSearchHit's flat `genres` list — see
// hardcoverBookRow.genres()/hardcoverTag for why the two representations
// differ.
func hcBookRowWithGenres(id int, title, slug, author string, genres []string) map[string]interface{} {
	return map[string]interface{}{
		"book": map[string]interface{}{
			"id": id, "title": title, "slug": slug,
			"cached_contributors": []map[string]interface{}{{"author": map[string]interface{}{"name": author}}},
			"cached_tags":         map[string]interface{}{"Genre": hcGenreTags(genres...)},
		},
	}
}

// hcGenreTags builds cached_tags-shaped Genre entries at count=2 — above
// hardcoverGenreMinCount, so genres() doesn't filter them out as
// low-confidence noise (see hardcoverGenreMinCount's doc comment).
func hcGenreTags(genres ...string) []map[string]interface{} {
	tags := make([]map[string]interface{}, len(genres))
	for i, g := range genres {
		tags[i] = map[string]interface{}{"tag": g, "count": 2}
	}
	return tags
}

func TestHandleBooks_NoHardcoverKey_UsesOpenLibrary(t *testing.T) {
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		if path == "/search.json" {
			return map[string]interface{}{"docs": []map[string]interface{}{
				{"key": "/works/OL1W", "title": "Source Book", "author_name": []string{"Test Author"},
					"subject": []string{"Space Opera"}, "cover_i": 111},
			}}, http.StatusOK
		}
		return map[string]interface{}{"works": []map[string]interface{}{
			{"key": "/works/OL2W", "title": "Discovery Book", "cover_id": 222,
				"authors": []map[string]interface{}{{"name": "Other Author"}}},
		}}, http.StatusOK
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleBooks(`{"title":"Source Book","author":"Test Author"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted result (no key configured means open library directly)", result)
	}
	if !strings.Contains(result, "Discovery Book") {
		t.Errorf("result = %q, want the subject-overlap candidate", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://openlibrary.org/works/OL1W" {
		t.Errorf("Citations = %+v, want the open library source citation", ctx.Citations)
	}
	if len(ctx.Cards) != 1 || ctx.Cards[0].Title != "Discovery Book" || ctx.Cards[0].ImageURL == "" {
		t.Errorf("Cards = %+v, want one card with cover art for the discovery", ctx.Cards)
	}
}

func TestHandleBooks_HardcoverSuccess_AggregatesAcrossLists(t *testing.T) {
	// List 10 uniquely recommends X and Y; list 20 uniquely recommends Z and
	// W; both recommend Shared — five distinct candidates total, at
	// hardcoverMinCandidates, so the open-library supplement path never
	// triggers. Open Library is still fetched concurrently regardless (see
	// lookupViaHardcover) for corroboration scoring, so it's stubbed here
	// to return no match — isolating this test to Hardcover's own ranking
	// rather than also asserting on cross-source corroboration, and
	// avoiding a real network call to production Open Library.
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		return map[string]interface{}{"docs": []map[string]interface{}{}}, http.StatusOK
	})
	fakeHardcover(t, func(query string, vars map[string]interface{}) (interface{}, int) {
		switch {
		case vars["q"] != nil:
			return map[string]interface{}{"data": map[string]interface{}{"search": map[string]interface{}{
				"results": map[string]interface{}{"hits": []map[string]interface{}{
					hcSearchHit("1", "Source Book", "source-book", 100, []string{"Test Author"}, []string{"Science Fiction"}),
				}},
			}}}, http.StatusOK
		case vars["minBooks"] != nil: // fetchHardcoverLists — both it and fetchHardcoverBookGenres use $bookID, minBooks disambiguates
			return map[string]interface{}{"data": map[string]interface{}{"list_books": []map[string]interface{}{
				{"list": map[string]interface{}{"id": 10, "name": "List A", "likes_count": 5, "books_count": 5}},
				{"list": map[string]interface{}{"id": 20, "name": "List B", "likes_count": 5, "books_count": 5}},
			}}}, http.StatusOK
		case vars["bookID"] != nil: // fetchHardcoverBookGenres — empty cached_tags, falls back to the search doc's genres
			return map[string]interface{}{"data": map[string]interface{}{"books": []map[string]interface{}{
				{"cached_tags": map[string]interface{}{}},
			}}}, http.StatusOK
		case vars["listID"] != nil:
			listID := int(vars["listID"].(float64))
			books := []map[string]interface{}{hcBookRow(5, "Shared", "shared", "Shared Author")}
			if listID == 10 {
				books = append(books, hcBookRow(2, "X", "x", "X Author"), hcBookRow(3, "Y", "y", "Y Author"))
			} else {
				books = append(books, hcBookRow(6, "Z", "z", "Z Author"), hcBookRow(7, "W", "w", "W Author"))
			}
			return map[string]interface{}{"data": map[string]interface{}{"list_books": books}}, http.StatusOK
		}
		t.Fatalf("unexpected hardcover query, vars=%v", vars)
		return nil, http.StatusInternalServerError
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, HardcoverAPIKey: "key"}
	result := handleBooks(`{"title":"Source Book","author":"Test Author"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted result", result)
	}
	if !strings.Contains(result, "Shared") || !strings.Contains(result, "on 2 curated lists") {
		t.Errorf("result = %q, want Shared credited for both contributing lists", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://hardcover.app/books/source-book" {
		t.Errorf("Citations = %+v, want the hardcover source citation", ctx.Citations)
	}
	if len(ctx.Cards) != 5 {
		t.Errorf("Cards = %+v, want one card per unique candidate", ctx.Cards)
	}
}

// TestHandleBooks_GenreOverlap_OutranksRawListCount is the regression test
// for the real bug this rewrite fixes: a book that straddles a "literary
// canon" and a genre identity (confirmed live for The Count of Monte
// Cristo, see this file's package doc comment) had its list-based
// candidates dominated by other canon staples with more raw list
// agreement, drowning out genuinely genre-similar picks with less. Canon
// Pick appears on all 3 lists (highest possible Count) but shares no genre
// with the source book; Genre Match appears on only 1 list but shares the
// source book's genre — it must still rank first.
func TestHandleBooks_GenreOverlap_OutranksRawListCount(t *testing.T) {
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		return map[string]interface{}{"docs": []map[string]interface{}{}}, http.StatusOK
	})
	fakeHardcover(t, func(query string, vars map[string]interface{}) (interface{}, int) {
		switch {
		case vars["q"] != nil:
			return map[string]interface{}{"data": map[string]interface{}{"search": map[string]interface{}{
				"results": map[string]interface{}{"hits": []map[string]interface{}{
					hcSearchHit("1", "Source Book", "source-book", 100, []string{"Test Author"}, []string{"Adventure"}),
				}},
			}}}, http.StatusOK
		case vars["minBooks"] != nil: // fetchHardcoverLists
			return map[string]interface{}{"data": map[string]interface{}{"list_books": []map[string]interface{}{
				{"list": map[string]interface{}{"id": 10, "name": "Canon List A", "likes_count": 5, "books_count": 5}},
				{"list": map[string]interface{}{"id": 20, "name": "Canon List B", "likes_count": 5, "books_count": 5}},
			}}}, http.StatusOK
		case vars["bookID"] != nil: // fetchHardcoverBookGenres — the source book's real (count-filtered) genres
			return map[string]interface{}{"data": map[string]interface{}{"books": []map[string]interface{}{
				{"cached_tags": map[string]interface{}{"Genre": hcGenreTags("Adventure", "Mystery")}},
			}}}, http.StatusOK
		case vars["listID"] != nil:
			listID := int(vars["listID"].(float64))
			// Canon Pick: no genre overlap, but on both lists (Count=2).
			// Genre Match: overlaps on both source genres, but only on one
			// list (Count=1) — despite the weaker list signal, it must
			// still outrank Canon Pick: (1+2)²*(1+1)=18 vs (1+0)²*(1+2)=3.
			books := []map[string]interface{}{
				hcBookRowWithGenres(5, "Canon Pick", "canon-pick", "Canon Author", []string{"Literary Fiction"}),
			}
			if listID == 10 {
				books = append(books, hcBookRowWithGenres(2, "Genre Match", "genre-match", "Genre Author", []string{"Adventure", "Mystery"}))
			}
			return map[string]interface{}{"data": map[string]interface{}{"list_books": books}}, http.StatusOK
		}
		t.Fatalf("unexpected hardcover query, vars=%v", vars)
		return nil, http.StatusInternalServerError
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, HardcoverAPIKey: "key"}
	result := handleBooks(`{"title":"Source Book","author":"Test Author"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted result", result)
	}
	if strings.Index(result, "Genre Match") > strings.Index(result, "Canon Pick") {
		t.Errorf("result = %q, want Genre Match (shares both the source book's genres, on fewer lists) to rank "+
			"above Canon Pick (no genre overlap, on more lists) — the multiplicative score must let strong genre "+
			"overlap outweigh weaker list-count consensus", result)
	}
}

func TestHandleBooks_HardcoverAuthError_FallsBackToOpenLibrary(t *testing.T) {
	fakeHardcover(t, func(query string, vars map[string]interface{}) (interface{}, int) {
		// The real shape Hardcover returns for an invalid/expired token —
		// HTTP 401 with a top-level "error" field, not the standard GraphQL
		// "errors" array (confirmed live).
		return map[string]interface{}{"error": "Unable to verify token"}, http.StatusUnauthorized
	})
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		if path == "/search.json" {
			return map[string]interface{}{"docs": []map[string]interface{}{
				{"key": "/works/OL1W", "title": "Source Book", "author_name": []string{"Test Author"},
					"subject": []string{"Space Opera"}, "cover_i": 111},
			}}, http.StatusOK
		}
		return map[string]interface{}{"works": []map[string]interface{}{
			{"key": "/works/OL2W", "title": "Fallback Discovery", "cover_id": 222,
				"authors": []map[string]interface{}{{"name": "Other Author"}}},
		}}, http.StatusOK
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, HardcoverAPIKey: "expired-key"}
	result := handleBooks(`{"title":"Source Book","author":"Test Author"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want the tool to degrade to open library, not fail outright", result)
	}
	if !strings.Contains(result, "Fallback Discovery") {
		t.Errorf("result = %q, want the open library fallback candidate", result)
	}
	if len(ctx.Citations) != 1 || !strings.Contains(ctx.Citations[0].Title, "Open Library") {
		t.Errorf("Citations = %+v, want only the open library citation (the failed hardcover attempt cites nothing)", ctx.Citations)
	}
}

// TestHardcoverAuthError_SurfacesAccountInactiveDescription locks down a
// real gap found live: a token well within its own validity window can
// still fail if Hardcover deactivates the *account* behind it — same
// resp.StatusCode/parsed.Error shape as a plain bad token, indistinguishable
// without reading error_description too. Before this, hardcoverAuthError
// discarded that field entirely, so the log just said "invalid_token" for
// both cases and there was no way to tell "get a new token" from "check
// your account status" without a live curl probe against Hardcover's API.
func TestHardcoverAuthError_SurfacesAccountInactiveDescription(t *testing.T) {
	err := &hardcoverAuthError{message: "invalid_token", description: "User account is not active"}
	msg := err.Error()
	if !strings.Contains(msg, "User account is not active") {
		t.Errorf("Error() = %q, want it to include the account-inactive description, not just the bare error code", msg)
	}
	if !isHardcoverAuthError(err) {
		t.Error("isHardcoverAuthError() = false, want true — this must still be treated as an auth failure so the caller falls back to Open Library")
	}
}

// TestHardcoverQuery_ParsesErrorDescriptionFromWire covers the actual JSON
// response Hardcover sends for a deactivated account (confirmed live), not
// just the hardcoverAuthError struct in isolation — makes sure
// error_description survives the real unmarshal path in hardcoverQuery.
func TestHardcoverQuery_ParsesErrorDescriptionFromWire(t *testing.T) {
	fakeHardcover(t, func(query string, vars map[string]interface{}) (interface{}, int) {
		return map[string]interface{}{
			"error":             "invalid_token",
			"error_description": "User account is not active",
		}, http.StatusUnauthorized
	})

	ctx := &Context{Ctx: context.Background(), HardcoverAPIKey: "well-formed-but-account-deactivated"}
	_, err := hardcoverQuery(ctx, "query { me { id } }", nil)
	if err == nil {
		t.Fatal("hardcoverQuery() error = nil, want an auth error")
	}
	if !strings.Contains(err.Error(), "User account is not active") {
		t.Errorf("error = %q, want error_description to survive the real parse path, not just be discarded", err.Error())
	}
	if !isHardcoverAuthError(err) {
		t.Error("isHardcoverAuthError() = false, want true")
	}
}

func TestHandleBooks_ThinHardcoverData_SupplementedWithOpenLibrary(t *testing.T) {
	fakeHardcover(t, func(query string, vars map[string]interface{}) (interface{}, int) {
		switch {
		case vars["q"] != nil:
			return map[string]interface{}{"data": map[string]interface{}{"search": map[string]interface{}{
				"results": map[string]interface{}{"hits": []map[string]interface{}{
					hcSearchHit("1", "Obscure Book", "obscure-book", 2, []string{"Test Author"}, []string{"Fantasy"}),
				}},
			}}}, http.StatusOK
		case vars["minBooks"] != nil: // fetchHardcoverLists
			return map[string]interface{}{"data": map[string]interface{}{"list_books": []map[string]interface{}{
				{"list": map[string]interface{}{"id": 10, "name": "Tiny List", "likes_count": 1, "books_count": 3}},
			}}}, http.StatusOK
		case vars["bookID"] != nil: // fetchHardcoverBookGenres
			return map[string]interface{}{"data": map[string]interface{}{"books": []map[string]interface{}{
				{"cached_tags": map[string]interface{}{}},
			}}}, http.StatusOK
		case vars["listID"] != nil:
			return map[string]interface{}{"data": map[string]interface{}{"list_books": []map[string]interface{}{
				hcBookRow(2, "Lone Hardcover Pick", "lone-pick", "Pick Author"),
			}}}, http.StatusOK
		}
		t.Fatalf("unexpected hardcover query, vars=%v", vars)
		return nil, http.StatusInternalServerError
	})
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		if path == "/search.json" {
			return map[string]interface{}{"docs": []map[string]interface{}{
				{"key": "/works/OL1W", "title": "Obscure Book", "author_name": []string{"Test Author"},
					"subject": []string{"High Fantasy"}, "cover_i": 111},
			}}, http.StatusOK
		}
		return map[string]interface{}{"works": []map[string]interface{}{
			{"key": "/works/OL2W", "title": "Supplement Discovery", "cover_id": 222,
				"authors": []map[string]interface{}{{"name": "Supp Author"}}},
		}}, http.StatusOK
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, HardcoverAPIKey: "key"}
	result := handleBooks(`{"title":"Obscure Book","author":"Test Author"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted result", result)
	}
	if !strings.Contains(result, "Lone Hardcover Pick") {
		t.Errorf("result = %q, want the primary hardcover candidate kept", result)
	}
	if !strings.Contains(result, "Supplement Discovery") {
		t.Errorf("result = %q, want the open library supplement appended", result)
	}
	if !strings.Contains(result, "supplemented via Open Library") {
		t.Errorf("result = %q, want the supplemented-data disclosure note", result)
	}
	if strings.Index(result, "Lone Hardcover Pick") > strings.Index(result, "Supplement Discovery") {
		t.Errorf("result = %q, want the primary hardcover candidate ranked above the fallback supplement", result)
	}
	if len(ctx.Citations) != 1 || !strings.Contains(ctx.Citations[0].Title, "Hardcover") {
		t.Errorf("Citations = %+v, want the hardcover citation (primary signal did succeed, just thinly)", ctx.Citations)
	}
}

func TestHandleBooks_HardcoverNoLists_FallsBackToOpenLibrary(t *testing.T) {
	fakeHardcover(t, func(query string, vars map[string]interface{}) (interface{}, int) {
		switch {
		case vars["q"] != nil:
			return map[string]interface{}{"data": map[string]interface{}{"search": map[string]interface{}{
				"results": map[string]interface{}{"hits": []map[string]interface{}{
					hcSearchHit("1", "No Lists Book", "no-lists-book", 5, []string{"Test Author"}, []string{"Drama"}),
				}},
			}}}, http.StatusOK
		case vars["minBooks"] != nil: // fetchHardcoverLists — empty means lookupViaHardcover bails before ever reaching fetchHardcoverBookGenres
			return map[string]interface{}{"data": map[string]interface{}{"list_books": []map[string]interface{}{}}}, http.StatusOK
		}
		t.Fatalf("unexpected hardcover query, vars=%v", vars)
		return nil, http.StatusInternalServerError
	})
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		if path == "/search.json" {
			return map[string]interface{}{"docs": []map[string]interface{}{
				{"key": "/works/OL1W", "title": "No Lists Book", "author_name": []string{"Test Author"},
					"subject": []string{"Family Drama"}, "cover_i": 111},
			}}, http.StatusOK
		}
		return map[string]interface{}{"works": []map[string]interface{}{
			{"key": "/works/OL2W", "title": "OL Only Discovery", "cover_id": 222,
				"authors": []map[string]interface{}{{"name": "OL Author"}}},
		}}, http.StatusOK
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, HardcoverAPIKey: "key"}
	result := handleBooks(`{"title":"No Lists Book","author":"Test Author"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want fallback to open library to succeed", result)
	}
	if !strings.Contains(result, "OL Only Discovery") {
		t.Errorf("result = %q, want the open library candidate", result)
	}
}

// TestHandleBooks_HardcoverCandidateDescription confirms a list-sourced
// candidate's description comes straight from Hardcover's own `books` row
// (fetchListBooks' query now selects it) with no extra network call — the
// zero-extra-calls path documented on bookCandidate.Description.
func TestHandleBooks_HardcoverCandidateDescription(t *testing.T) {
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		return map[string]interface{}{"docs": []map[string]interface{}{}}, http.StatusOK
	})
	fakeHardcover(t, func(query string, vars map[string]interface{}) (interface{}, int) {
		switch {
		case vars["q"] != nil:
			return map[string]interface{}{"data": map[string]interface{}{"search": map[string]interface{}{
				"results": map[string]interface{}{"hits": []map[string]interface{}{
					hcSearchHit("1", "Source Book", "source-book", 100, []string{"Test Author"}, []string{"Science Fiction"}),
				}},
			}}}, http.StatusOK
		case vars["minBooks"] != nil: // fetchHardcoverLists
			return map[string]interface{}{"data": map[string]interface{}{"list_books": []map[string]interface{}{
				{"list": map[string]interface{}{"id": 10, "name": "List A", "likes_count": 5, "books_count": 5}},
			}}}, http.StatusOK
		case vars["bookID"] != nil: // fetchHardcoverBookGenres
			return map[string]interface{}{"data": map[string]interface{}{"books": []map[string]interface{}{
				{"cached_tags": map[string]interface{}{}},
			}}}, http.StatusOK
		case vars["listID"] != nil:
			// Five candidates total (at hardcoverMinCandidates) so the
			// thin-data Open Library supplement path never triggers.
			books := []map[string]interface{}{
				{"book": map[string]interface{}{
					"id": 2, "title": "Candidate Book", "slug": "candidate-book",
					"description":         "A tense tale of revenge on the high seas.",
					"cached_contributors": []map[string]interface{}{{"author": map[string]interface{}{"name": "Candidate Author"}}},
				}},
			}
			for i := 3; i <= 6; i++ {
				books = append(books, hcBookRow(i, fmt.Sprintf("Filler %d", i), fmt.Sprintf("filler-%d", i), "Filler Author"))
			}
			return map[string]interface{}{"data": map[string]interface{}{"list_books": books}}, http.StatusOK
		}
		t.Fatalf("unexpected hardcover query, vars=%v", vars)
		return nil, http.StatusInternalServerError
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}, HardcoverAPIKey: "key"}
	result := handleBooks(`{"title":"Source Book","author":"Test Author"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted result", result)
	}
	if !strings.Contains(result, "Candidate Book by Candidate Author — A tense tale of revenge on the high seas.") {
		t.Errorf("result = %q, want the candidate's description shown inline with its recommendation", result)
	}
}

// TestHandleBooks_OpenLibraryCandidateDescription confirms the pure Open
// Library path (no Hardcover key) fetches each shown candidate's
// description via one extra /works/{key}.json call — /subjects/*.json's
// listing shape (used to surface the candidate itself) carries no
// description field at all, so this is the one path in the tool that
// actually costs a network round trip beyond what resolution already needed.
func TestHandleBooks_OpenLibraryCandidateDescription(t *testing.T) {
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		switch path {
		case "/search.json":
			return map[string]interface{}{"docs": []map[string]interface{}{
				{"key": "/works/OL1W", "title": "Source Book", "author_name": []string{"Test Author"},
					"subject": []string{"Space Opera"}, "cover_i": 111, "description": "The source book's own blurb."},
			}}, http.StatusOK
		case "/subjects/space_opera.json":
			return map[string]interface{}{"works": []map[string]interface{}{
				{"key": "/works/OL2W", "title": "Discovery Book", "cover_id": 222,
					"authors": []map[string]interface{}{{"name": "Other Author"}}},
			}}, http.StatusOK
		case "/works/OL2W.json":
			// The flexible {type,value} shape, distinct from search.json's
			// flattened plain-string form used above for the source book —
			// see openLibraryDescription's doc comment.
			return map[string]interface{}{"description": map[string]interface{}{
				"type": "/type/text", "value": "Discovery Book's own detailed description.",
			}}, http.StatusOK
		}
		t.Fatalf("unexpected open library path %q", path)
		return nil, http.StatusInternalServerError
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleBooks(`{"title":"Source Book","author":"Test Author"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted result", result)
	}
	if !strings.Contains(result, "Description: The source book's own blurb.") {
		t.Errorf("result = %q, want the source book's description shown once, in full", result)
	}
	if !strings.Contains(result, "Discovery Book by Other Author") || !strings.Contains(result, "Discovery Book's own detailed description.") {
		t.Errorf("result = %q, want the candidate's description fetched from its {type,value} work-detail shape", result)
	}
}

func TestHandleBooks_NotFoundOnEitherSource(t *testing.T) {
	fakeOpenLibrary(t, func(path string, q url.Values) (interface{}, int) {
		return map[string]interface{}{"docs": []map[string]interface{}{}}, http.StatusOK
	})

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleBooks(`{"title":"Zzxqvblorp Nonexistent Title"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a clear not-found error, not a silent empty success", result)
	}
}

// TestRankHardcoverCandidates_CorroborationBonusRequiresAuthorMatch guards
// against a real ranking bug: the Open Library corroboration bonus (see
// rankHardcoverCandidates' doc comment) is meant to reward a Hardcover
// candidate that Open Library's independent subject-overlap signal ALSO
// surfaced — i.e. two sources agreeing on the same book. Keying that check
// by title alone (the original bug) let an unrelated Open Library
// candidate that merely shares a title with a different author (a common
// public-domain title, a "Study Guide" companion edition, ...) grant the
// bonus anyway, since matching only by title+author (as every other
// dedup/match key in this file does) is what actually distinguishes one
// book from another sharing its title.
func TestRankHardcoverCandidates_CorroborationBonusRequiresAuthorMatch(t *testing.T) {
	agg := map[string]*bookCandidate{
		"same-book":      {Title: "Frankenstein", Author: "Mary Shelley", Count: 1},
		"different-book": {Title: "Frankenstein", Author: "Some Other Author", Count: 1},
	}
	// Open Library only actually corroborated the Mary Shelley edition.
	openLibraryTitleAuthors := map[string]bool{
		"frankenstein|mary shelley": true,
	}

	ranked := rankHardcoverCandidates(agg, nil, openLibraryTitleAuthors)
	if len(ranked) != 2 {
		t.Fatalf("got %d candidates, want 2", len(ranked))
	}
	// Both start with equal (1+0)*(1+1)=2 base score; only the genuinely
	// corroborated one should out-rank the other via the +2 bonus.
	if ranked[0].Author != "Mary Shelley" {
		t.Errorf("top candidate = %q by %q, want the Mary Shelley edition ranked first "+
			"(the only one Open Library actually corroborated)", ranked[0].Title, ranked[0].Author)
	}
}
