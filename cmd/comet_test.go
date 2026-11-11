package cmd

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCometURL_EscapesParamsAndSkipsEmptyOnes(t *testing.T) {
	got := cometURL("/api/comet/classify", "limit", "5", "period", "", "x", "a&b=c")
	if !strings.Contains(got, "limit=5") || !strings.Contains(got, "x=a%26b%3Dc") {
		t.Errorf("cometURL = %q, want limit=5 and the x value query-escaped", got)
	}
	if strings.Contains(got, "period") {
		t.Errorf("cometURL = %q, want the empty period param omitted", got)
	}
	if bare := cometURL("/api/comet/generate", "period", ""); strings.Contains(bare, "?") {
		t.Errorf("cometURL with only empty params = %q, want no query string", bare)
	}
}

func TestCometAPIError_SurfacesThePlainTextBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusConflict,
		Body:       io.NopCloser(strings.NewReader("a comet run is already in progress\n")),
	}
	err := cometAPIError(resp, "classify")
	if err == nil || !strings.Contains(err.Error(), "already in progress") || !strings.Contains(err.Error(), "409") {
		t.Errorf("cometAPIError = %v, want the server's reason and status", err)
	}

	empty := &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(""))}
	if err := cometAPIError(empty, "backfill"); err == nil || !strings.Contains(err.Error(), "backfill failed (status 500)") {
		t.Errorf("cometAPIError with an empty body = %v, want the bare status fallback", err)
	}
}
