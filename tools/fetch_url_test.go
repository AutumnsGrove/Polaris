package tools

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"polaris/search"
)

func fakeCSVServer(t *testing.T, body string, contentType string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHandleFetchURL_FilenameRequired(t *testing.T) {
	ctx := newTestContext()
	result := handleFetchURL(`{"url":"https://example.com/x"}`, ctx, "call-1")
	if !strings.Contains(result, "filename is required") {
		t.Errorf("result = %q, want a filename-required error", result)
	}
}

func TestHandleFetchURL_FilenameWithSlashRejected(t *testing.T) {
	ctx := newTestContext()
	result := handleFetchURL(`{"url":"https://example.com/x","filename":"../evil.csv"}`, ctx, "call-1")
	if !strings.Contains(result, "no directories") {
		t.Errorf("result = %q, want a no-directories error", result)
	}
}

func TestHandleFetchURL_NeitherURLNorCardIndex(t *testing.T) {
	ctx := newTestContext()
	result := handleFetchURL(`{"filename":"x.csv"}`, ctx, "call-1")
	if !strings.Contains(result, "pass exactly one of url or card_index") {
		t.Errorf("result = %q, want an exactly-one-of error", result)
	}
}

func TestHandleFetchURL_BothURLAndCardIndex(t *testing.T) {
	ctx := newTestContext()
	result := handleFetchURL(`{"url":"https://example.com/x","card_index":1,"filename":"x.csv"}`, ctx, "call-1")
	if !strings.Contains(result, "pass exactly one of url or card_index") {
		t.Errorf("result = %q, want an exactly-one-of error", result)
	}
}

func TestHandleFetchURL_NoWorkspaceConfigured(t *testing.T) {
	ctx := newTestContext() // CodeExecWorkspaceDir/ThreadID left unset
	ctx.AddCitation(Citation{Title: "x", URL: "https://example.com/x"})
	result := handleFetchURL(`{"url":"https://example.com/x","filename":"x.csv"}`, ctx, "call-1")
	if !strings.Contains(result, "no code-execution workspace configured") {
		t.Errorf("result = %q, want a no-workspace-configured error", result)
	}
}

func TestHandleFetchURL_URLNotACitationRejected(t *testing.T) {
	fetched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.Write([]byte("a,b\n1,2\n"))
	}))
	t.Cleanup(srv.Close)

	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "thread-1"
	// Note: srv.URL deliberately never added as a citation.
	result := handleFetchURL(`{"url":"`+srv.URL+`","filename":"data.csv"}`, ctx, "call-1")
	if !strings.Contains(result, "hasn't appeared as a citation") {
		t.Errorf("result = %q, want a not-a-citation error", result)
	}
	if fetched {
		t.Error("handleFetchURL fetched a non-citation URL instead of rejecting it up front")
	}
}

func TestHandleFetchURL_BlockedSourceRejectedWithoutFetching(t *testing.T) {
	fetched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.Write([]byte("a,b\n1,2\n"))
	}))
	t.Cleanup(srv.Close)

	parsedURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}
	bl, err := search.LoadBlocklist(writeBlocklistFile(t, parsedURL.Hostname()+"\n"))
	if err != nil {
		t.Fatalf("LoadBlocklist returned error: %v", err)
	}

	ctx := newTestContext()
	ctx.Blocklist = bl
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "thread-1"
	ctx.AddCitation(Citation{Title: "x", URL: srv.URL})

	result := handleFetchURL(`{"url":"`+srv.URL+`","filename":"data.csv"}`, ctx, "call-1")
	if !strings.Contains(result, "blocked") {
		t.Errorf("result = %q, want a blocked-source error", result)
	}
	if fetched {
		t.Error("handleFetchURL fetched a blocklisted URL instead of rejecting it up front")
	}
}

func TestHandleFetchURL_URLSuccessWritesWorkspaceFile(t *testing.T) {
	srv := fakeCSVServer(t, "a,b\n1,2\n", "text/csv")

	workspaceRoot := t.TempDir()
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = workspaceRoot
	ctx.ThreadID = "thread-1"
	ctx.AddCitation(Citation{Title: "data", URL: srv.URL})

	result := handleFetchURL(`{"url":"`+srv.URL+`","filename":"data.csv"}`, ctx, "call-1")
	if !strings.Contains(result, `fetched`) || !strings.Contains(result, `"data.csv"`) {
		t.Errorf("result = %q, want a success message naming data.csv", result)
	}

	written, err := os.ReadFile(filepath.Join(workspaceRoot, "thread-1", "data.csv"))
	if err != nil {
		t.Fatalf("expected data.csv to exist in the workspace: %v", err)
	}
	if string(written) != "a,b\n1,2\n" {
		t.Errorf("written file = %q, want the fetched body verbatim", string(written))
	}
}

func TestHandleFetchURL_CardIndexOutOfRange(t *testing.T) {
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "thread-1"
	ctx.AddCard(Card{Title: "one", URL: "https://example.com/1", ImageURL: "https://example.com/1.png", Kind: "image"})
	result := handleFetchURL(`{"card_index":5,"filename":"photo.jpg"}`, ctx, "call-1")
	if !strings.Contains(result, "out of range") {
		t.Errorf("result = %q, want an out-of-range error", result)
	}
}

func TestHandleFetchURL_CardIndexSuccessWritesWorkspaceFile(t *testing.T) {
	srv := fakeImageServer(t)
	workspaceRoot := t.TempDir()
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = workspaceRoot
	ctx.ThreadID = "thread-1"
	ctx.AddCard(Card{Title: "a red bicycle", URL: "https://example.com/1", FullImageURL: srv.URL, Kind: "image"})

	result := handleFetchURL(`{"card_index":1,"filename":"photo.png"}`, ctx, "call-1")
	if !strings.Contains(result, `"photo.png"`) {
		t.Errorf("result = %q, want a success message naming photo.png", result)
	}

	written, err := os.ReadFile(filepath.Join(workspaceRoot, "thread-1", "photo.png"))
	if err != nil {
		t.Fatalf("expected photo.png to exist in the workspace: %v", err)
	}
	if string(written) != "fake-png-bytes" {
		t.Errorf("written file = %q, want the fetched image bytes verbatim", string(written))
	}
}

func TestHandleFetchURL_DisallowedContentTypeRejected(t *testing.T) {
	srv := fakeCSVServer(t, "MZ\x90\x00fake-exe-bytes", "application/x-msdownload")
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "thread-1"
	ctx.AddCitation(Citation{Title: "x", URL: srv.URL})

	result := handleFetchURL(`{"url":"`+srv.URL+`","filename":"thing.exe"}`, ctx, "call-1")
	if !strings.Contains(result, "isn't in the allowed set") {
		t.Errorf("result = %q, want a disallowed-content-type error", result)
	}
	if _, err := os.Stat(filepath.Join(ctx.CodeExecWorkspaceDir, "thread-1", "thing.exe")); err == nil {
		t.Error("disallowed content should not have been written to the workspace")
	}
}

func TestHandleFetchURL_OctetStreamAllowedForKnownExtension(t *testing.T) {
	srv := fakeCSVServer(t, "fake-parquet-bytes", "application/octet-stream")
	workspaceRoot := t.TempDir()
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = workspaceRoot
	ctx.ThreadID = "thread-1"
	ctx.AddCitation(Citation{Title: "x", URL: srv.URL})

	result := handleFetchURL(`{"url":"`+srv.URL+`","filename":"data.parquet"}`, ctx, "call-1")
	if !strings.Contains(result, `"data.parquet"`) {
		t.Errorf("result = %q, want a success message naming data.parquet", result)
	}
}

func TestHandleFetchURL_OctetStreamRejectedForUnknownExtension(t *testing.T) {
	srv := fakeCSVServer(t, "unknown-binary-bytes", "application/octet-stream")
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "thread-1"
	ctx.AddCitation(Citation{Title: "x", URL: srv.URL})

	result := handleFetchURL(`{"url":"`+srv.URL+`","filename":"data.bin"}`, ctx, "call-1")
	if !strings.Contains(result, "isn't in the allowed set") {
		t.Errorf("result = %q, want a disallowed-content-type error", result)
	}
}

func TestHandleFetchURL_SizeCapExceeded(t *testing.T) {
	huge := strings.Repeat("a", maxFetchURLBytes+1)
	srv := fakeCSVServer(t, huge, "text/plain")
	ctx := newTestContext()
	ctx.CodeExecWorkspaceDir = t.TempDir()
	ctx.ThreadID = "thread-1"
	ctx.AddCitation(Citation{Title: "x", URL: srv.URL})

	result := handleFetchURL(`{"url":"`+srv.URL+`","filename":"huge.txt"}`, ctx, "call-1")
	if !strings.Contains(result, "byte limit") {
		t.Errorf("result = %q, want a size-limit error", result)
	}
}
