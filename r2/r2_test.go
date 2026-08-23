package r2

import (
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
	var gotMethod, gotPath, gotAuth, gotPayloadHash, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotPayloadHash = r.Header.Get("x-amz-content-sha256")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClientForTest("acct", "AKIATEST", "secretkey", "my-bucket", srv.URL)

	dir := t.TempDir()
	src := filepath.Join(dir, "polaris-20260823-101010.db")
	if err := os.WriteFile(src, []byte("fake sqlite contents"), 0o644); err != nil {
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
	if gotBody != "fake sqlite contents" {
		t.Errorf("body = %q, want the source file's contents", gotBody)
	}
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
