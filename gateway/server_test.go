package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCsrfProtect_SameOriginAlwaysPasses covers the normal production
// case: browser and server share one origin (the embedded frontend), so
// Origin's host equals r.Host and the request goes through regardless of
// devMode.
func TestCsrfProtect_SameOriginAlwaysPasses(t *testing.T) {
	for _, devMode := range []bool{false, true} {
		next := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}), devMode)

		req := httptest.NewRequest(http.MethodPost, "http://polaris.example/api/settings", nil)
		req.Header.Set("Origin", "http://polaris.example")
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("devMode=%v: status = %d, want 200 for a same-origin request", devMode, rec.Code)
		}
	}
}

// TestCsrfProtect_CrossOriginRejectedInProduction guards the whole point
// of this middleware: a hostile page's browser-originated mutating
// request must be rejected outright when not running the vite dev-proxy
// split (devMode=false).
func TestCsrfProtect_CrossOriginRejectedInProduction(t *testing.T) {
	next := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), false)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8899/api/settings", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a genuinely cross-origin request", rec.Code)
	}
}

// TestCsrfProtect_DevModeTrustsViteDevServerOrigin is the fix this test
// file exists for: `polaris run --dev` splits the frontend (vite on its
// pinned dev port, see web/vite.config.ts) and backend (this server) into
// two ports on purpose — a legitimate browser request proxied through
// vite always carries Origin: http://localhost:45173 while r.Host here is
// whatever this backend actually listens on (e.g. localhost:8899), which
// is exactly the mismatch this middleware exists to reject. Without a
// dev-mode exception, every mutating route (uploads included — this is
// the bug issue #71's live testing surfaced) 403s in bare-metal dev via
// `pnpm run dev`, even though the request came from the developer's own
// browser, not a hostile page.
func TestCsrfProtect_DevModeTrustsViteDevServerOrigin(t *testing.T) {
	next := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), true)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8899/api/upload", nil)
	req.Header.Set("Origin", "http://localhost:45173")
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — the vite dev server's own origin must be trusted in dev mode", rec.Code)
	}
}

// TestCsrfProtect_DevModeStillRejectsOtherOrigins confirms the dev-mode
// exception is narrowly scoped to vite's own known port, not a blanket
// disable of the check whenever --dev is passed — a hostile page is just
// as dangerous while developing as in production.
func TestCsrfProtect_DevModeStillRejectsOtherOrigins(t *testing.T) {
	next := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), true)

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8899/api/upload", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 — dev mode must not trust an arbitrary origin", rec.Code)
	}
}

// TestCsrfProtect_GetAlwaysPasses covers the existing (unchanged)
// same-origin-browser-check scope: GET/HEAD are never state-changing, so
// they're never subject to this check regardless of Origin or devMode —
// this is what lets a WS upgrade handshake (always GET) through.
func TestCsrfProtect_GetAlwaysPasses(t *testing.T) {
	next := csrfProtect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), false)

	req := httptest.NewRequest(http.MethodGet, "http://localhost:8899/ws", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — GET is never subject to this check", rec.Code)
	}
}
