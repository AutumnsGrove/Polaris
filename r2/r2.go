// Package r2 is a minimal Cloudflare R2 (S3-compatible) client, hand-signing
// requests with AWS Signature Version 4 rather than pulling in
// aws-sdk-go-v2 — that SDK's dependency tree (smithy-go plus its own
// retry/config/credential-chain machinery) is disproportionate to the four
// operations backup mirroring actually needs (put/list/get/delete a single
// object), and this project already prefers small, hand-rolled HTTP clients
// for third-party APIs over pulling in their SDKs — see brave/, parallel/,
// tavily/. See backup.go's Mirror/PruneRemote/Fetch for how this is used.
//
// Objects are stored gzip-compressed: Upload compresses on the way out and
// Download decompresses on the way in, transparently to callers, who always
// hand Upload and receive from Download a plain file. R2 does no compression
// at rest of its own, and a VACUUM INTO snapshot of Polaris's database
// compresses to roughly 40% of its raw size (measured against a live
// production backup: 108.7MB -> 44.9MB at gzip's default level), so uploading
// raw bytes is pure waste of bucket space and of the potato's uplink on every
// daily backup. Keys are deliberately left unchanged (polaris-<stamp>.db, no
// .gz suffix) so backup.parseTime, PruneRemote, and `backup list --remote`
// keep working untouched — and so the plain objects already in the bucket
// from before compression existed stay readable: Download sniffs for gzip's
// magic bytes rather than assuming, see its own comment.
package r2

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// region/service are fixed by R2 itself — it has no regions of its own,
// "auto" is Cloudflare's documented SigV4 region for every R2 bucket
// regardless of where it's actually stored, and the S3-compatible API is
// always signed as service "s3".
const (
	region  = "auto"
	service = "s3"
)

// unsignedPayload marks every request body as unsigned in the SigV4
// signature (x-amz-content-sha256: UNSIGNED-PAYLOAD) — a documented,
// supported option for S3/R2's API specifically so a streaming upload
// doesn't have to be buffered into memory (or read twice) just to compute
// its content hash first. Backups are whole-database VACUUM INTO
// snapshots, easily hundreds of MB on a real install, so avoiding that
// buffering matters here in a way it wouldn't for the small JSON bodies
// brave/parallel/tavily's clients send.
const unsignedPayload = "UNSIGNED-PAYLOAD"

// maxResponseBytes bounds how much of an error body or list response this
// package ever buffers in memory — same local-constant convention as
// brave/parallel/tavily's own copies. Deliberately not applied to
// Download's actual object body (see that method's own streaming-to-disk
// comment) — this only covers the small, bounded-by-nature responses:
// R2's own error messages and a bucket listing's XML.
const maxResponseBytes = 10 << 20 // 10MB

// Client talks to a single R2 bucket over its S3-compatible API. Construct
// with NewClient, which returns nil if any required field is empty —
// callers check for nil to know whether R2 mirroring is configured at all,
// the same optional-dependency pattern as brave.NewClient/tavily.NewClient.
type Client struct {
	accountID string
	accessKey string
	secretKey string
	bucket    string
	endpoint  string // scheme://host, no trailing slash
	http      *http.Client
}

// NewClient returns nil if accountID, accessKeyID, secretAccessKey, or
// bucket is empty, so the zero value of config.Config's R2 section (the
// default — nobody has to opt in) disables mirroring entirely rather than
// making every call site check four fields itself.
func NewClient(accountID, accessKeyID, secretAccessKey, bucket string) *Client {
	if accountID == "" || accessKeyID == "" || secretAccessKey == "" || bucket == "" {
		return nil
	}
	return newClient(accountID, accessKeyID, secretAccessKey, bucket,
		fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID))
}

// NewClientForTest builds a Client against a custom endpoint — exported
// solely so this package's own tests (and backup_test.go's) can point it at
// an httptest server instead of the real R2 API. Not used outside tests.
func NewClientForTest(accountID, accessKeyID, secretAccessKey, bucket, endpoint string) *Client {
	return newClient(accountID, accessKeyID, secretAccessKey, bucket, endpoint)
}

func newClient(accountID, accessKeyID, secretAccessKey, bucket, endpoint string) *Client {
	return &Client{
		accountID: accountID,
		accessKey: accessKeyID,
		secretKey: secretAccessKey,
		bucket:    bucket,
		endpoint:  strings.TrimSuffix(endpoint, "/"),
		// Backup files move over the potato's residential/tailnet uplink,
		// not a quick API call — brave/tavily/parallel's 15s timeouts
		// would abort a slow-but-succeeding multi-hundred-MB transfer
		// partway through.
		http: &http.Client{Timeout: 10 * time.Minute},
	}
}

// Object describes one item already in the bucket, as returned by List.
// JSON tags match backup.Info's naming convention — gateway/backup.go's
// remote-list endpoint serializes these directly.
type Object struct {
	Key string `json:"key"`
	// SizeBytes is the object's stored size. For backups written by
	// Upload that's the *compressed* size, so it's noticeably smaller
	// than the matching local file's — `backup list --remote` compares
	// this against `backup list`, and the mismatch is expected, not a
	// truncated upload.
	SizeBytes    int64     `json:"size_bytes"`
	LastModified time.Time `json:"last_modified"`
}

// Temp files gzipToTemp writes, and therefore the names Upload is allowed to
// sweep up as its own leftovers (see sweepStaleTemps).
const (
	tempPrefix = ".r2-upload-"
	tempSuffix = ".gz"
)

// staleTempAge is how long a temp file has to sit untouched before Upload
// treats it as abandoned by a dead process rather than still in flight.
// Compression of the biggest backup this project produces takes seconds, so an
// hour is enormously conservative.
const staleTempAge = time.Hour

// Upload gzips the file at path and PUTs the result into the bucket under
// key. It streams from (and to) disk rather than reading the whole backup
// into memory, which matters on the potato: a backup is ~100MB there and the
// SBC has 2GB of RAM total, shared with everything else.
func (c *Client) Upload(ctx context.Context, key, path string) error {
	sweepStaleTemps(filepath.Dir(path))

	gzPath, gzSize, err := gzipToTemp(path)
	if err != nil {
		return err
	}
	defer os.Remove(gzPath)

	f, err := os.Open(gzPath)
	if err != nil {
		return fmt.Errorf("opening compressed %s: %w", gzPath, err)
	}
	defer f.Close()

	req, err := c.newRequest(ctx, http.MethodPut, c.objectPath(key), nil, f)
	if err != nil {
		return err
	}
	req.ContentLength = gzSize
	// Unsigned on purpose: canonicalizeHeaders only signs host plus the two
	// x-amz-* headers, and S3/R2 don't require every header to be signed.
	// This is purely a hint for anyone reading the bucket with wrangler/curl/
	// rclone — Download decides from the bytes, not from this header, so an
	// object stored before compression existed still reads back correctly.
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("r2 upload request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return fmt.Errorf("r2 upload failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// Download GETs the object at key into a file at destPath, decompressing it
// if it's gzip-compressed, and streaming straight to disk for the same
// reason Upload streams from it. destPath's parent directory must already
// exist.
func (c *Client) Download(ctx context.Context, key, destPath string) error {
	req, err := c.newRequest(ctx, http.MethodGet, c.objectPath(key), nil, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("r2 download request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return fmt.Errorf("r2 download failed (status %d): %s", resp.StatusCode, string(body))
	}

	// Whether this object is compressed is decided from the bytes, not from
	// the Content-Encoding we stored or from a filename suffix: the bucket
	// still holds plain objects from before Upload gzipped anything, so
	// either shape has to work indefinitely. gzip's two-byte magic (0x1f
	// 0x8b) can't collide with a real SQLite file, which starts with
	// "SQLite format 3\x00". Note that net/http may already have silently
	// decompressed the body (it auto-decompresses a response carrying
	// Content-Encoding: gzip, because it adds its own Accept-Encoding), in
	// which case the peek below just sees a plain file and copies it — the
	// same path a legacy object takes.
	br := bufio.NewReader(resp.Body)
	var src io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return fmt.Errorf("reading compressed %s: %w", key, err)
		}
		defer zr.Close()
		src = zr
	}

	tmp := destPath + ".downloading"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	// A gzip stream's checksum is verified only when it's read through to
	// EOF, which io.Copy does — a truncated or corrupted object surfaces
	// here as an error rather than as a half-valid .db.
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()

	// Rename into place only once the full body has landed on disk, so a
	// download killed partway through never leaves a truncated file sitting
	// at destPath looking like a complete backup.
	if err := os.Rename(tmp, destPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("finalizing download: %w", err)
	}
	return nil
}

// gzipToTemp compresses path into a temp file in the same directory and
// returns that file's path and compressed size. Callers are responsible for
// removing it.
//
// Same-directory is deliberate (one filesystem, and the backing volume is
// already sized for a backups folder) and so is the dot-prefixed name: it
// must not look like a polaris-YYYYMMDD-HHMMSS.db backup, or backup.List
// would report a half-written temp file as a recoverable backup.
//
// Compressing to a file rather than streaming through an io.Pipe is the
// consequence of R2 needing a real Content-Length on the PUT while gzip's
// output length isn't known until its stream ends. The extra write+read is
// of the *compressed* size (~40MB for a ~100MB backup) and is far cheaper
// than buffering the whole thing in memory.
func gzipToTemp(path string) (string, int64, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("opening %s: %w", path, err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"*"+tempSuffix)
	if err != nil {
		return "", 0, fmt.Errorf("creating temp file for %s: %w", path, err)
	}
	zw := gzip.NewWriter(tmp)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		tmp.Close()
		os.Remove(tmp.Name())
		return "", 0, fmt.Errorf("compressing %s: %w", path, err)
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", 0, fmt.Errorf("finishing compressed %s: %w", path, err)
	}
	fi, err := tmp.Stat()
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", 0, fmt.Errorf("stat'ing compressed %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", 0, fmt.Errorf("closing compressed %s: %w", tmp.Name(), err)
	}
	return tmp.Name(), fi.Size(), nil
}

// sweepStaleTemps removes abandoned compression temp files from dir.
//
// It exists because there is a real way for one to be left behind: the Docker
// update watcher recreates the container with --force-recreate, which kills
// this process outright rather than draining it, and nothing holds a busy flag
// for a backup being mirrored (handleServerBusy only covers chat turns,
// shooting-star runs, Daily/Comet generations, and the embedding backfill). A
// killed upload therefore strands a ~40MB file in the data volume that
// backup.List and backup.Prune both skip forever — List because the name isn't
// one of ours, Prune because it only deletes names List returned. Best-effort
// by design: this is janitorial, and a failure here must never fail the upload
// it's preceding.
func sweepStaleTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, tempSuffix) {
			continue
		}
		fi, err := e.Info()
		// Only stale ones: a file this fresh may be a concurrent Upload's
		// in-flight compression, which must not be deleted out from under it.
		if err != nil || time.Since(fi.ModTime()) < staleTempAge {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// Delete removes the object at key. A key that doesn't exist is not an
// error — S3-compatible DELETE is idempotent, and callers (PruneRemote)
// only ever delete keys they just listed.
func (c *Client) Delete(ctx context.Context, key string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, c.objectPath(key), nil, nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("r2 delete request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		return fmt.Errorf("r2 delete failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// List returns every object in the bucket, paginating through
// ListObjectsV2's continuation token until exhausted. Retention windows
// keep the real object count small (dozens, not thousands), but a
// deployment mirroring for months shouldn't silently see only the first
// page.
func (c *Client) List(ctx context.Context) ([]Object, error) {
	var all []Object
	token := ""
	for {
		query := url.Values{"list-type": {"2"}}
		if token != "" {
			query.Set("continuation-token", token)
		}
		req, err := c.newRequest(ctx, http.MethodGet, c.bucketPath(), query, nil)
		if err != nil {
			return nil, err
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("r2 list request failed: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading r2 list response: %w", err)
		}
		if len(body) > maxResponseBytes {
			return nil, fmt.Errorf("r2 list response exceeds %d byte limit", maxResponseBytes)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("r2 list failed (status %d): %s", resp.StatusCode, string(body))
		}

		var parsed listBucketResult
		if err := xml.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("parsing r2 list response: %w", err)
		}
		for _, item := range parsed.Contents {
			all = append(all, Object{Key: item.Key, SizeBytes: item.Size, LastModified: item.LastModified})
		}

		if !parsed.IsTruncated || parsed.NextContinuationToken == "" {
			break
		}
		token = parsed.NextContinuationToken
	}
	return all, nil
}

type listBucketResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string    `xml:"Key"`
		Size         int64     `xml:"Size"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"Contents"`
}

func (c *Client) bucketPath() string {
	return "/" + c.bucket + "/"
}

func (c *Client) objectPath(key string) string {
	return "/" + c.bucket + "/" + key
}

// newRequest builds an HTTP request against path (already bucket-prefixed
// by bucketPath/objectPath — R2's S3 endpoint is path-style, not
// virtual-hosted, so the bucket lives in the URL path rather than the
// hostname) with query already canonically ordered, and signs it with
// SigV4 before returning.
func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Request, error) {
	u := c.endpoint + path
	if len(query) > 0 {
		u += "?" + canonicalQueryString(query)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("building r2 request: %w", err)
	}
	c.sign(req)
	return req, nil
}

// sign attaches SigV4 headers (x-amz-date, x-amz-content-sha256,
// Authorization) to req in place, per
// https://docs.aws.amazon.com/general/latest/gr/sigv4-signing-aws-requests.html
// — R2 implements the same algorithm S3 does.
func (c *Client) sign(req *http.Request) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", unsignedPayload)
	req.Header.Set("Host", req.URL.Host)

	canonicalHeaders, signedHeaders := canonicalizeHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.Path,
		req.URL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		unsignedPayload,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, region, service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hashHex(canonicalRequest),
	}, "\n")

	signingKey := c.deriveSigningKey(dateStamp)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.accessKey, credentialScope, signedHeaders, signature))
}

// canonicalizeHeaders returns SigV4's canonical-headers block (sorted
// "name:value\n" pairs) and the matching semicolon-joined signed-headers
// list. Only host + the two x-amz-* headers sign's already set are
// included — S3/R2 don't require every header to be signed, and keeping
// this to exactly what sign() itself sets avoids any risk of the
// http.Client's own default headers (User-Agent, etc.) drifting the
// signature from what's actually sent.
func canonicalizeHeaders(req *http.Request) (canonical, signed string) {
	headers := map[string]string{
		"host":                 req.Header.Get("Host"),
		"x-amz-content-sha256": req.Header.Get("x-amz-content-sha256"),
		"x-amz-date":           req.Header.Get("x-amz-date"),
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strings.TrimSpace(headers[name]))
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// canonicalQueryString renders query as SigV4's canonical query string:
// keys sorted, both keys and values percent-encoded per RFC 3986's
// unreserved set (net/url.Values.Encode does this correctly already,
// including sorting — Go's own encoder matches AWS's rules here, unlike
// path encoding, which needs "/" left unescaped).
func canonicalQueryString(query url.Values) string {
	return query.Encode()
}

func (c *Client) deriveSigningKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+c.secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hashHex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}
