package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func fakeStaticFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                     {Data: []byte("<html>shell</html>")},
		"_app/immutable/chunk-abc123.js": {Data: []byte("console.log(1)")},
	}
}

// A client-routed path like /t/<uuid> isn't a real file in the build, so it
// must fall back to index.html directly — not a redirect. Regression test
// for a bug where the fallback went through http.FileServer with the
// request path rewritten to "/index.html": FileServer treats any request
// path ending in "/index.html" as needing a canonical redirect to strip
// that suffix, and the browser resolves that redirect against whatever it
// actually requested (e.g. "/t/<uuid>" -> "/t/"), which still isn't a real
// file — an infinite redirect loop the browser aborts with "too many
// redirects". This only ever surfaced once a second client route
// (routes/t/[id]) existed; "/" alone never rewrote its own path and so
// never tripped it.
func TestSpaHandler_ClientRouteFallsBackToIndexWithoutRedirecting(t *testing.T) {
	handler := spaHandler(fakeStaticFS())

	req := httptest.NewRequest("GET", "/t/9f8e7d6c-1234-5678-9abc-def012345678", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code >= 300 && rec.Code < 400 {
		t.Fatalf("got redirect status %d with Location %q, want a direct 200 serving index.html",
			rec.Code, rec.Header().Get("Location"))
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<html>shell</html>" {
		t.Errorf("body = %q, want the index.html shell", rec.Body.String())
	}
}

func TestSpaHandler_RootServesIndexDirectly(t *testing.T) {
	handler := spaHandler(fakeStaticFS())

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<html>shell</html>" {
		t.Errorf("body = %q, want the index.html shell", rec.Body.String())
	}
}

// A missing hashed asset must be a real 404, not the index.html SPA
// fallback — a 200 that serves an HTML document at a .js path would let a
// stale pre-self-update client (old index.html + cached old hashed files,
// or a long-lived browser session) keep running the previous build instead
// of failing loudly and reloading onto the current one. This is what stops
// an old bundle staying silently servable forever when its files are
// pruned. Client *routes* (/t/<id>) still correctly fall back to index.html;
// only build-artifact URLs under /_app/ 404.
func TestSpaHandler_MissingAssetIs404NotIndexFallback(t *testing.T) {
	handler := spaHandler(fakeStaticFS())

	req := httptest.NewRequest("GET", "/_app/immutable/old-hash-gone.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (not the index.html SPA fallback)", rec.Code)
	}
	if rec.Body.String() == "<html>shell</html>" {
		t.Fatalf("body served the index.html shell for a missing asset; want a real 404")
	}
}

func TestSpaHandler_RealAssetServedWithImmutableCache(t *testing.T) {
	handler := spaHandler(fakeStaticFS())

	req := httptest.NewRequest("GET", "/_app/immutable/chunk-abc123.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "console.log(1)" {
		t.Errorf("body = %q, want the real asset content", rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want immutable caching for a hashed asset", got)
	}
}
