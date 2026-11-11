package r2

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewClientNilOnMissingField(t *testing.T) {
	cases := []struct {
		name, accountID, accessKey, secretKey, bucket string
	}{
		{"missing account", "", "key", "secret", "bucket"},
		{"missing access key", "acct", "", "secret", "bucket"},
		{"missing secret key", "acct", "key", "", "bucket"},
		{"missing bucket", "acct", "key", "secret", ""},
		{"all missing", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NewClient(c.accountID, c.accessKey, c.secretKey, c.bucket); got != nil {
				t.Fatalf("expected nil client, got %+v", got)
			}
		})
	}
}

func TestNewClientNonNilWhenFullyConfigured(t *testing.T) {
	c := NewClient("acct", "key", "secret", "bucket")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.endpoint != "https://acct.r2.cloudflarestorage.com" {
		t.Errorf("unexpected endpoint: %s", c.endpoint)
	}
}

func TestUploadSignsAndSendsRequest(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotPayloadHash, gotEncoding string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotPayloadHash = r.Header.Get("x-amz-content-sha256")
		gotEncoding = r.Header.Get("Content-Encoding")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClientForTest("acct", "AKIATEST", "secretkey", "my-bucket", srv.URL)

	dir := t.TempDir()
	src := filepath.Join(dir, "polaris-20260823-101010.db")
	contents := strings.Repeat("fake sqlite contents ", 100)
	if err := os.WriteFile(src, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := c.Upload(context.Background(), "polaris-20260823-101010.db", src); err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/my-bucket/polaris-20260823-101010.db" {
		t.Errorf("path = %s, want /my-bucket/polaris-20260823-101010.db", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIATEST/") {
		t.Errorf("Authorization header = %q, missing expected prefix", gotAuth)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("Authorization header = %q, missing expected SignedHeaders", gotAuth)
	}
	if gotPayloadHash != unsignedPayload {
		t.Errorf("x-amz-content-sha256 = %s, want %s", gotPayloadHash, unsignedPayload)
	}

	// The object key stays plain (no .gz) so backup.parseTime and
	// PruneRemote keep working; compression lives in the body.
	if gotEncoding != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", gotEncoding)
	}
	if len(gotBody) >= len(contents) {
		t.Errorf("uploaded body = %d bytes, want fewer than the %d-byte source (not compressed)", len(gotBody), len(contents))
	}
	ungzipped, err := gunzip(gotBody)
	if err != nil {
		t.Fatalf("uploaded body is not valid gzip: %v", err)
	}
	if string(ungzipped) != contents {
		t.Error("decompressed upload body doesn't match the source file")
	}

	// No leftover temp file from the compress step.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(src) {
			t.Errorf("unexpected leftover file in backup dir: %s", e.Name())
		}
	}
}

// TestUploadSweepsStaleTempFiles covers the crash case documented on
// sweepStaleTemps: a container recreate mid-upload strands a temp file that
// nothing else will ever clean up (backup.List and backup.Prune both skip
// names that aren't backups). A fresh temp must survive, since it may belong
// to a concurrent upload still compressing.
func TestUploadSweepsStaleTempFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClientForTest("acct", "key", "secret", "bucket", srv.URL)
	dir := t.TempDir()

	stale := filepath.Join(dir, tempPrefix+"stranded"+tempSuffix)
	if err := os.WriteFile(stale, []byte("stale gzip bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleTempAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	fresh := filepath.Join(dir, tempPrefix+"inflight"+tempSuffix)
	if err := os.WriteFile(fresh, []byte("in-flight gzip bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	unrelated := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dir, "polaris-20260823-101010.db")
	if err := os.WriteFile(src, []byte(strings.Repeat("pages ", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Upload(context.Background(), "polaris-20260823-101010.db", src); err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp file survived the sweep, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp file was swept, should have been left alone: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated file was removed, stat err = %v", err)
	}
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

func TestUploadNonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("access denied"))
	}))
	defer srv.Close()

	c := NewClientForTest("acct", "key", "secret", "bucket", srv.URL)
	dir := t.TempDir()
	src := filepath.Join(dir, "backup.db")
	os.WriteFile(src, []byte("data"), 0o644)

	err := c.Upload(context.Background(), "backup.db", src)
	if err == nil {
		t.Fatal("expected error on 403 response")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error = %v, want it to include the response body", err)
	}
}

func TestDownloadWritesFileAtomically(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("downloaded contents"))
	}))
	defer srv.Close()

	c := NewClientForTest("acct", "key", "secret", "bucket", srv.URL)
	dir := t.TempDir()
	dest := filepath.Join(dir, "restored.db")

	if err := c.Download(context.Background(), "polaris-20260823-101010.db", dest); err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "downloaded contents" {
		t.Errorf("downloaded file contents = %q", string(got))
	}

	// No leftover .downloading temp file after a clean finish.
	if _, err := os.Stat(dest + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("expected .downloading temp file to be gone, stat err = %v", err)
	}
}

// TestDownloadDecompressesGzipObject covers the shape every object written by
// Upload now has. The bucket also still holds plain objects from before
// compression existed — TestDownloadWritesFileAtomically above is that case,
// and it must keep passing.
func TestDownloadDecompressesGzipObject(t *testing.T) {
	want := strings.Repeat("SQLite format 3\x00 and some pages ", 500)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(want)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	compressed := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirrors how R2 returns an object stored with
		// Content-Encoding: gzip. Go's transport may decompress this
		// before Download sees it, which must also work — that path is
		// covered by the plain-body test above plus this same sniff.
		w.Header().Set("Content-Encoding", "identity")
		w.Write(compressed)
	}))
	defer srv.Close()

	c := NewClientForTest("acct", "key", "secret", "bucket", srv.URL)
	dest := filepath.Join(t.TempDir(), "restored.db")
	if err := c.Download(context.Background(), "polaris-20260823-101010.db", dest); err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("downloaded %d bytes, want the decompressed %d bytes", len(got), len(want))
	}
}

// TestDownloadCorruptGzipLeavesNoFile guards the reason Download writes to a
// .downloading temp file at all: a truncated object must not land at destPath
// looking like a restorable backup.
func TestDownloadCorruptGzipLeavesNoFile(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(strings.Repeat("pages ", 1000)))
	zw.Close()
	truncated := buf.Bytes()[:buf.Len()/2]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(truncated)
	}))
	defer srv.Close()

	c := NewClientForTest("acct", "key", "secret", "bucket", srv.URL)
	dest := filepath.Join(t.TempDir(), "restored.db")
	if err := c.Download(context.Background(), "polaris-20260823-101010.db", dest); err == nil {
		t.Fatal("expected an error for a truncated gzip object")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("expected no file at destPath, stat err = %v", err)
	}
	if _, err := os.Stat(dest + ".downloading"); !os.IsNotExist(err) {
		t.Errorf("expected the .downloading temp file to be removed, stat err = %v", err)
	}
}

func TestDeleteAcceptsOKAndNoContent(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		var gotMethod string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			w.WriteHeader(status)
		}))
		c := NewClientForTest("acct", "key", "secret", "bucket", srv.URL)
		if err := c.Delete(context.Background(), "polaris-20260823-101010.db"); err != nil {
			t.Errorf("status %d: Delete failed: %v", status, err)
		}
		if gotMethod != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", gotMethod)
		}
		srv.Close()
	}
}

func TestListParsesObjectsAndPaginates(t *testing.T) {
	pageOne := `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <IsTruncated>true</IsTruncated>
  <NextContinuationToken>token-2</NextContinuationToken>
  <Contents>
    <Key>polaris-20260101-000000.db</Key>
    <Size>1024</Size>
    <LastModified>2026-01-01T00:00:00.000Z</LastModified>
  </Contents>
</ListBucketResult>`
	pageTwo := `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>polaris-20260102-000000.db</Key>
    <Size>2048</Size>
    <LastModified>2026-01-02T00:00:00.000Z</LastModified>
  </Contents>
</ListBucketResult>`

	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("list-type") != "2" {
			t.Errorf("missing list-type=2 query param, got %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("continuation-token") == "token-2" {
			w.Write([]byte(pageTwo))
		} else {
			w.Write([]byte(pageOne))
		}
	}))
	defer srv.Close()

	c := NewClientForTest("acct", "key", "secret", "bucket", srv.URL)
	objs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if requests != 2 {
		t.Errorf("expected 2 requests (pagination), got %d", requests)
	}
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(objs))
	}
	if objs[0].Key != "polaris-20260101-000000.db" || objs[0].SizeBytes != 1024 {
		t.Errorf("unexpected first object: %+v", objs[0])
	}
	if objs[1].Key != "polaris-20260102-000000.db" || objs[1].SizeBytes != 2048 {
		t.Errorf("unexpected second object: %+v", objs[1])
	}
	wantTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !objs[0].LastModified.Equal(wantTime) {
		t.Errorf("LastModified = %v, want %v", objs[0].LastModified, wantTime)
	}
}
