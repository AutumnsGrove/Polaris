package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/search"
	"polaris/tavily"
)

// TestMain swaps out safeDialContext for a plain, unrestricted dialer for
// the whole test binary. Every test in this file fetches from httptest
// servers, which bind to loopback on purpose — safeDialContext exists to
// block exactly that in production, so it would refuse every fake server
// here otherwise.
func TestMain(m *testing.M) {
	dialContext = (&net.Dialer{}).DialContext
	os.Exit(m.Run())
}

func fakeHTMLPage(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeJSONServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withWaybackAPI points fetchFromWayback's archive.org call at a fake
// server for the duration of the test, restoring the real URL after.
func withWaybackAPI(t *testing.T, availabilityURL string) {
	t.Helper()
	original := waybackAvailabilityAPI
	waybackAvailabilityAPI = availabilityURL
	t.Cleanup(func() { waybackAvailabilityAPI = original })
}

func TestFetchAndExtract_PrefersArticleOverChrome(t *testing.T) {
	html := `<html><head><title>My Article</title></head><body>
		<nav>site nav links</nav>
		<script>console.log('should be dropped')</script>
		<article><p>The actual article content.</p></article>
		<footer>copyright footer</footer>
	</body></html>`

	title, _, _, text, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil, 0)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if title != "My Article" {
		t.Errorf("title = %q, want %q", title, "My Article")
	}
	if !strings.Contains(text, "The actual article content.") {
		t.Errorf("text = %q, want it to contain the article body", text)
	}
	if strings.Contains(text, "site nav") || strings.Contains(text, "dropped") || strings.Contains(text, "copyright") {
		t.Errorf("text = %q, want nav/script/footer stripped out", text)
	}
}

func TestFetchAndExtract_ExtractsSiteName(t *testing.T) {
	html := `<html><head><title>The Bard, Explained</title>
		<meta property="og:site_name" content="The Hollywood Reporter"></head>
		<body><article><p>content</p></article></body></html>`

	_, siteName, _, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil, 0)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if siteName != "The Hollywood Reporter" {
		t.Errorf("siteName = %q, want %q", siteName, "The Hollywood Reporter")
	}
}

func TestFetchAndExtract_SiteNameEmptyWhenMissing(t *testing.T) {
	html := `<html><head><title>No meta tag here</title></head><body><article><p>content</p></article></body></html>`

	_, siteName, _, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil, 0)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if siteName != "" {
		t.Errorf("siteName = %q, want empty when the page sets no og:site_name", siteName)
	}
}

func TestFetchAndExtract_ExtractsImageURL(t *testing.T) {
	html := `<html><head><title>An Article With Art</title>
		<meta property="og:image" content="https://example.com/lead-image.jpg"></head>
		<body><article><p>content</p></article></body></html>`

	_, _, imageURL, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil, 0)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if imageURL != "https://example.com/lead-image.jpg" {
		t.Errorf("imageURL = %q, want %q", imageURL, "https://example.com/lead-image.jpg")
	}
}

func TestFetchAndExtract_ImageURLEmptyWhenMissingOrRelative(t *testing.T) {
	for name, html := range map[string]string{
		"missing":  `<html><head><title>No og:image</title></head><body><article><p>content</p></article></body></html>`,
		"relative": `<html><head><title>Relative og:image</title><meta property="og:image" content="/img/lead.jpg"></head><body><article><p>content</p></article></body></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, imageURL, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil, 0)
			if err != nil {
				t.Fatalf("fetchAndExtract returned error: %v", err)
			}
			if imageURL != "" {
				t.Errorf("imageURL = %q, want empty (a relative/missing og:image must never reach the frontend as a broken thumbnail)", imageURL)
			}
		})
	}
}

func TestFetchAndExtract_FallsBackToBodyWithoutArticleOrMain(t *testing.T) {
	html := `<html><body><p>Just a plain page with no article or main tag.</p></body></html>`
	_, _, _, text, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil, 0)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if !strings.Contains(text, "Just a plain page") {
		t.Errorf("text = %q, want the body content", text)
	}
}

// TestFetchAndExtract_DoesNotTruncate confirms fetchAndExtract itself
// returns the full, uncapped text — length capping (and, for HTML, offset
// pagination) is applied later by windowText in handleWebRead, not here.
// Truncating in fetchAndExtract would silently defeat pagination: the
// filter-pass and paywall/empty-page heuristics both need the full text,
// and offset pagination needs something bigger than maxExtractedChars to
// page through in the first place.
func TestFetchAndExtract_DoesNotTruncate(t *testing.T) {
	huge := strings.Repeat("word ", 5000) // well over maxExtractedChars
	html := "<html><body><article>" + huge + "</article></body></html>"

	_, _, _, text, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil, 0)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if len(text) <= maxExtractedChars {
		t.Errorf("text length = %d, want it to exceed maxExtractedChars (%d) — fetchAndExtract must not truncate", len(text), maxExtractedChars)
	}
	if strings.Contains(text, "[truncated]") {
		t.Error("fetchAndExtract's text contains a truncation marker — truncation belongs in windowText, not here")
	}
}

func TestWindowText_TruncatesAndReportsNextOffset(t *testing.T) {
	full := strings.Repeat("a", maxExtractedChars+500)

	windowed := windowText(full, 0)
	if len(windowed) <= maxExtractedChars {
		t.Fatalf("windowText should still return roughly maxExtractedChars of content plus a marker, got len %d", len(windowed))
	}
	if !strings.Contains(windowed, fmt.Sprintf("offset: %d", maxExtractedChars)) {
		t.Errorf("windowed = %q, want it to name the next offset (%d)", windowed, maxExtractedChars)
	}

	rest := windowText(full, maxExtractedChars)
	if len(rest) != 500 {
		t.Errorf("windowText(full, maxExtractedChars) length = %d, want the remaining 500 chars with no further truncation", len(rest))
	}
	if strings.Contains(rest, "call web_read again") {
		t.Errorf("rest = %q, want no continuation hint once everything has been read", rest)
	}
}

func TestFetchAndExtract_NonOKStatus(t *testing.T) {
	_, _, _, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusNotFound, "not found").URL, nil, 0)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}

func TestFetchAndExtract_OversizedResponseRejected(t *testing.T) {
	huge := strings.Repeat("a", maxResponseBytes+1)
	html := "<html><body><article>" + huge + "</article></body></html>"

	_, _, _, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil, 0)
	if err == nil {
		t.Fatal("expected an error for a response over the size limit")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %q, want it to mention the size limit", err.Error())
	}
}

// TestSafeDialContext_BlocksInternalAddresses exercises the real
// safeDialContext (not the TestMain-overridden dialContext var) directly,
// since every other test in this file deliberately bypasses it to reach
// httptest's loopback servers.
func TestSafeDialContext_BlocksInternalAddresses(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "169.254.169.254:80", "10.0.0.5:443", "localhost:80"} {
		t.Run(addr, func(t *testing.T) {
			_, err := safeDialContext(context.Background(), "tcp", addr)
			if err == nil {
				t.Fatalf("safeDialContext(%q) = nil error, want it refused as an internal address", addr)
			}
			if !strings.Contains(err.Error(), "refusing to connect") {
				t.Errorf("err = %q, want a refusing-to-connect error", err.Error())
			}
		})
	}
}

func TestIsDisallowedIP_AllowsPublicAddress(t *testing.T) {
	// 93.184.216.34 is example.com's well-known static IP — a genuine
	// public address that must not trip the internal-address check.
	if isDisallowedIP(net.ParseIP("93.184.216.34")) {
		t.Error("isDisallowedIP(93.184.216.34) = true, want false for a public address")
	}
}

// A URL that isn't itself on the blocklist but redirects to one must still
// be rejected — otherwise the blocklist only ever protects against direct
// links, not shorteners/old domains/anything that 302s onward. handleWebRead
// only checks the requested URL up front; fetchAndExtract's CheckRedirect is
// what has to catch a blocked *destination*.
func TestFetchAndExtract_RejectsRedirectToBlockedDomain(t *testing.T) {
	blocked := fakeHTMLPage(t, http.StatusOK, "<html><body>should never be seen</body></html>")
	blockedURL, err := url.Parse(blocked.URL)
	if err != nil {
		t.Fatalf("parsing blocked server URL: %v", err)
	}

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blocked.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	bl, err := search.LoadBlocklist(writeBlocklistFile(t, blockedURL.Hostname()+"\n"))
	if err != nil {
		t.Fatalf("LoadBlocklist returned error: %v", err)
	}

	_, _, _, _, _, err = fetchAndExtract(context.Background(), redirector.URL, bl, 0)
	if err == nil {
		t.Fatal("expected an error: redirect target is on the blocklist")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("err = %q, want it to mention the redirect was blocked", err.Error())
	}
}

func writeBlocklistFile(t *testing.T, contents string) string {
	t.Helper()
	path := t.TempDir() + "/blocked_sources.txt"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing blocklist file: %v", err)
	}
	return path
}

func TestHandleWebRead_URLRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebRead(`{}`, ctx)
	if result != "error: url is required" {
		t.Errorf("result = %q, want the url-required error", result)
	}
}

func TestHandleWebRead_BlockedDomainRejectedWithoutFetching(t *testing.T) {
	fetched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.WriteHeader(http.StatusOK)
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

	ctx := &Context{Ctx: context.Background(), Blocklist: bl, Emit: func(string, map[string]interface{}) {}}
	result := handleWebRead(`{"url":"`+srv.URL+`"}`, ctx)

	if !strings.Contains(result, "blocked") {
		t.Errorf("result = %q, want a blocked-source error", result)
	}
	if fetched {
		t.Error("handleWebRead fetched a blocked URL instead of rejecting it upfront")
	}
}

func TestHandleWebRead_WithoutInstructions_ReturnsFullText(t *testing.T) {
	html := `<html><head><title>Page</title></head><body><article>Full extracted content here.</article></body></html>`
	srv := fakeHTMLPage(t, http.StatusOK, html)

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebRead(`{"url":"`+srv.URL+`"}`, ctx)

	if !strings.Contains(result, "Full extracted content here.") {
		t.Errorf("result = %q, want the extracted text unfiltered", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].Title != "Page" {
		t.Errorf("Citations = %+v, want one citation titled Page", ctx.Citations)
	}
}

// TestHandleWebRead_CitationIncludesImageURL confirms og:image reaches the
// citation through the full handleWebRead path, not just the lower-level
// fetchAndExtract unit tests above — this is what the frontend's citation
// thumbnail actually depends on.
func TestHandleWebRead_CitationIncludesImageURL(t *testing.T) {
	html := `<html><head><title>Page With Art</title>
		<meta property="og:image" content="https://example.com/lead.jpg"></head>
		<body><article>content</article></body></html>`
	srv := fakeHTMLPage(t, http.StatusOK, html)

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	handleWebRead(`{"url":"`+srv.URL+`"}`, ctx)

	if len(ctx.Citations) != 1 || ctx.Citations[0].ImageURL != "https://example.com/lead.jpg" {
		t.Errorf("Citations = %+v, want the og:image URL carried through", ctx.Citations)
	}
}

func TestHandleWebRead_WithInstructions_AppliesFilterPass(t *testing.T) {
	html := `<html><body><article>A long page with prices: $10, $20, and $30 scattered around with lots of other text.</article></body></html>`
	srv := fakeHTMLPage(t, http.StatusOK, html)

	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{
			{Resp: &llm.ChatResponse{Content: "$10, $20, $30"}},
		},
	}
	ctx := &Context{Ctx: context.Background(), LLM: mock, Emit: func(string, map[string]interface{}) {}}

	result := handleWebRead(`{"url":"`+srv.URL+`","instructions":"just the prices"}`, ctx)
	if result != "$10, $20, $30" {
		t.Errorf("result = %q, want the filtered content from the mock, not the full page", result)
	}
	if mock.CallCount() != 1 {
		t.Errorf("CallCount = %d, want exactly 1 (the filter pass)", mock.CallCount())
	}
}

func TestHandleWebRead_OffsetContinuesReading(t *testing.T) {
	huge := strings.Repeat("a", maxExtractedChars) + "SECOND_CHUNK_MARKER" + strings.Repeat("b", 100)
	html := "<html><body><article>" + huge + "</article></body></html>"
	srv := fakeHTMLPage(t, http.StatusOK, html)

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	first := handleWebRead(`{"url":"`+srv.URL+`"}`, ctx)
	if strings.Contains(first, "SECOND_CHUNK_MARKER") {
		t.Errorf("first read should stop before the marker, got a result containing it")
	}
	if !strings.Contains(first, fmt.Sprintf("offset: %d", maxExtractedChars)) {
		t.Errorf("first = %q, want a continuation hint naming offset %d", first, maxExtractedChars)
	}

	ctx2 := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	second := handleWebRead(fmt.Sprintf(`{"url":"%s","offset":%d}`, srv.URL, maxExtractedChars), ctx2)
	if !strings.Contains(second, "SECOND_CHUNK_MARKER") {
		t.Errorf("second = %q, want it to contain the marker past the first chunk", second)
	}
}

// TestHandleWebRead_InstructionsSeeBeyondDisplayWindow guards against the
// exact bug this pagination feature was built to fix: the filter pass used
// to run on the already-truncated (maxExtractedChars) text, so "extract
// just X" could never find an X that only appeared later in a long page.
func TestHandleWebRead_InstructionsSeeBeyondDisplayWindow(t *testing.T) {
	huge := strings.Repeat("a", maxExtractedChars+500) + "DEEP_TARGET_VALUE"
	html := "<html><body><article>" + huge + "</article></body></html>"
	srv := fakeHTMLPage(t, http.StatusOK, html)

	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{{Resp: &llm.ChatResponse{Content: "DEEP_TARGET_VALUE"}}},
	}
	ctx := &Context{Ctx: context.Background(), LLM: mock, Emit: func(string, map[string]interface{}) {}}
	handleWebRead(`{"url":"`+srv.URL+`","instructions":"find the target value"}`, ctx)

	if len(mock.Calls) != 1 {
		t.Fatalf("CallCount = %d, want exactly 1", len(mock.Calls))
	}
	sent := mock.Calls[0].Messages[len(mock.Calls[0].Messages)-1].Content
	if !strings.Contains(sent, "DEEP_TARGET_VALUE") {
		t.Errorf("filter pass input didn't contain content past maxExtractedChars — the double-RAG truncation bug regressed")
	}
}

func TestHandleWebRead_FilterFailureFallsBackToFullText(t *testing.T) {
	html := `<html><body><article>Full content, filter will fail so this should still come back.</article></body></html>`
	srv := fakeHTMLPage(t, http.StatusOK, html)

	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{{Err: context.DeadlineExceeded}},
	}
	ctx := &Context{Ctx: context.Background(), LLM: mock, Emit: func(string, map[string]interface{}) {}}

	result := handleWebRead(`{"url":"`+srv.URL+`","instructions":"anything"}`, ctx)
	if !strings.Contains(result, "Full content, filter will fail") {
		t.Errorf("result = %q, want the full extracted text as a fallback", result)
	}
}

func TestFilterExtractedText(t *testing.T) {
	mock := &llmtest.MockClient{
		Responses: []llmtest.Response{{Resp: &llm.ChatResponse{Content: "extracted answer"}}},
	}
	result, err := filterExtractedText(context.Background(), mock, "page text", "an instruction")
	if err != nil {
		t.Fatalf("filterExtractedText returned error: %v", err)
	}
	if result != "extracted answer" {
		t.Errorf("result = %q, want %q", result, "extracted answer")
	}
}

func TestLooksLikePaywall_DetectsKnownMarker(t *testing.T) {
	text := "Some intro text. Subscribe to continue reading this exclusive report."
	if !looksLikePaywall(text) {
		t.Errorf("looksLikePaywall(%q) = false, want true", text)
	}
}

func TestLooksLikePaywall_IgnoresUnrelatedText(t *testing.T) {
	text := "Sign up for our free weekly newsletter to get updates on new articles."
	if looksLikePaywall(text) {
		t.Errorf("looksLikePaywall(%q) = true, want false (newsletter CTA isn't a paywall)", text)
	}
}

func TestLooksEmpty(t *testing.T) {
	if !looksEmpty("short") {
		t.Error("looksEmpty(\"short\") = false, want true")
	}
	if looksEmpty(strings.Repeat("word ", 100)) {
		t.Error("looksEmpty(long text) = true, want false")
	}
}

func TestIsPDF_ByContentType(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"application/pdf"}}}
	if !isPDF(resp, "https://example.com/download") {
		t.Error("isPDF() = false, want true for application/pdf content-type")
	}
}

func TestIsPDF_ByExtension(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Content-Type": []string{"application/octet-stream"}}}
	if !isPDF(resp, "https://example.com/papers/2301.00234.pdf") {
		t.Error("isPDF() = false, want true for a .pdf URL suffix")
	}
	if isPDF(resp, "https://example.com/papers/2301.00234") {
		t.Error("isPDF() = true, want false for a plain octet-stream response with no .pdf suffix")
	}
}

// minimalTestPDFBase64 is a byte-accurate minimal single-page PDF
// ("Hello World") — its xref table's offsets must match its object
// bytes exactly, so it's generated programmatically (not hand-typed)
// and stored pre-encoded rather than risking a subtly-wrong literal.
const minimalTestPDFBase64 = "JVBERi0xLjEKJcKlwrHDqwoKMSAwIG9iago8PCAvVHlwZSAvQ2F0YWxvZyAvUGFnZXMgMiAwIFIgPj4KZW5kb2JqCgoyIDAgb2JqCjw8IC9UeXBlIC9QYWdlcyAvS2lkcyBbMyAwIFJdIC9Db3VudCAxIC9NZWRpYUJveCBbMCAwIDMwMCAxNDRdID4+CmVuZG9iagoKMyAwIG9iago8PCAvVHlwZSAvUGFnZSAvUGFyZW50IDIgMCBSIC9SZXNvdXJjZXMgPDwgL0ZvbnQgPDwgL0YxIDw8IC9UeXBlIC9Gb250IC9TdWJ0eXBlIC9UeXBlMSAvQmFzZUZvbnQgL1RpbWVzLVJvbWFuID4+ID4+ID4+IC9Db250ZW50cyA0IDAgUiA+PgplbmRvYmoKCjQgMCBvYmoKPDwgL0xlbmd0aCAzOSA+PgpzdHJlYW0KQlQgL0YxIDE4IFRmIDAgMCBUZCAoSGVsbG8gV29ybGQpIFRqIEVUCmVuZHN0cmVhbQplbmRvYmoKCnhyZWYKMCA1CjAwMDAwMDAwMDAgNjU1MzUgZiAKMDAwMDAwMDAxOCAwMDAwMCBuIAowMDAwMDAwMDY4IDAwMDAwIG4gCjAwMDAwMDAxNTAgMDAwMDAgbiAKMDAwMDAwMDMwNCAwMDAwMCBuIAp0cmFpbGVyCjw8IC9Sb290IDEgMCBSIC9TaXplIDUgPj4Kc3RhcnR4cmVmCjM5NAolJUVPRg=="

func TestFetchAndExtract_PDF(t *testing.T) {
	pdfBytes, err := base64.StdEncoding.DecodeString(minimalTestPDFBase64)
	if err != nil {
		t.Fatalf("decoding test fixture: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		w.Write(pdfBytes)
	}))
	t.Cleanup(srv.Close)

	_, _, _, text, totalPages, err := fetchAndExtract(context.Background(), srv.URL, nil, 0)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error for a PDF: %v", err)
	}
	if !strings.Contains(text, "Hello World") {
		t.Errorf("text = %q, want it to contain the PDF's text content", text)
	}
	if totalPages != 1 {
		t.Errorf("totalPages = %d, want 1 for a single-page PDF", totalPages)
	}
}

// twoPageTestPDFBase64 is a byte-accurate two-page PDF ("Page One Text" /
// "Page Two Text"), generated the same programmatic way as
// minimalTestPDFBase64 above (see its comment) — verified against
// ledongthuc/pdf directly before being pasted in here.
const twoPageTestPDFBase64 = "JVBERi0xLjEKJcKlwrHDqwoKMSAwIG9iago8PCAvVHlwZSAvQ2F0YWxvZyAvUGFnZXMgMiAwIFIgPj4KZW5kb2JqCgoyIDAgb2JqCjw8IC9UeXBlIC9QYWdlcyAvS2lkcyBbMyAwIFIgNSAwIFJdIC9Db3VudCAyIC9NZWRpYUJveCBbMCAwIDMwMCAxNDRdID4+CmVuZG9iagoKMyAwIG9iago8PCAvVHlwZSAvUGFnZSAvUGFyZW50IDIgMCBSIC9SZXNvdXJjZXMgPDwgL0ZvbnQgPDwgL0YxIDw8IC9UeXBlIC9Gb250IC9TdWJ0eXBlIC9UeXBlMSAvQmFzZUZvbnQgL1RpbWVzLVJvbWFuID4+ID4+ID4+IC9Db250ZW50cyA0IDAgUiA+PgplbmRvYmoKCjQgMCBvYmoKPDwgL0xlbmd0aCA0MSA+PgpzdHJlYW0KQlQgL0YxIDE4IFRmIDAgMCBUZCAoUGFnZSBPbmUgVGV4dCkgVGogRVQKZW5kc3RyZWFtCmVuZG9iagoKNSAwIG9iago8PCAvVHlwZSAvUGFnZSAvUGFyZW50IDIgMCBSIC9SZXNvdXJjZXMgPDwgL0ZvbnQgPDwgL0YxIDw8IC9UeXBlIC9Gb250IC9TdWJ0eXBlIC9UeXBlMSAvQmFzZUZvbnQgL1RpbWVzLVJvbWFuID4+ID4+ID4+IC9Db250ZW50cyA2IDAgUiA+PgplbmRvYmoKCjYgMCBvYmoKPDwgL0xlbmd0aCA0MSA+PgpzdHJlYW0KQlQgL0YxIDE4IFRmIDAgMCBUZCAoUGFnZSBUd28gVGV4dCkgVGogRVQKZW5kc3RyZWFtCmVuZG9iagoKeHJlZgowIDcKMDAwMDAwMDAwMCA2NTUzNSBmIAowMDAwMDAwMDE4IDAwMDAwIG4gCjAwMDAwMDAwNjggMDAwMDAgbiAKMDAwMDAwMDE1NiAwMDAwMCBuIAowMDAwMDAwMzEwIDAwMDAwIG4gCjAwMDAwMDA0MDIgMDAwMDAgbiAKMDAwMDAwMDU1NiAwMDAwMCBuIAp0cmFpbGVyCjw8IC9Sb290IDEgMCBSIC9TaXplIDcgPj4Kc3RhcnR4cmVmCjY0OAolJUVPRg=="

func twoPageTestPDFServer(t *testing.T) *httptest.Server {
	t.Helper()
	pdfBytes, err := base64.StdEncoding.DecodeString(twoPageTestPDFBase64)
	if err != nil {
		t.Fatalf("decoding test fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		w.Write(pdfBytes)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExtractPDFPage_DefaultsToFirstPage(t *testing.T) {
	pdfBytes, err := base64.StdEncoding.DecodeString(twoPageTestPDFBase64)
	if err != nil {
		t.Fatalf("decoding test fixture: %v", err)
	}

	title, text, totalPages, err := ExtractPDFPage(pdfBytes, 0)
	if err != nil {
		t.Fatalf("ExtractPDFPage returned error: %v", err)
	}
	if totalPages != 2 {
		t.Errorf("totalPages = %d, want 2", totalPages)
	}
	if !strings.Contains(text, "Page One Text") {
		t.Errorf("text = %q, want page 1's content when page is unset", text)
	}
	if strings.Contains(text, "Page Two Text") {
		t.Errorf("text = %q, want only page 1's content, not page 2's", text)
	}
	if title != "Page One Text" {
		t.Errorf("title = %q, want the first line of page 1's text", title)
	}
	if !strings.Contains(text, "page: 2") {
		t.Errorf("text = %q, want a continuation hint pointing at page 2", text)
	}
}

func TestExtractPDFPage_SecondPage(t *testing.T) {
	pdfBytes, err := base64.StdEncoding.DecodeString(twoPageTestPDFBase64)
	if err != nil {
		t.Fatalf("decoding test fixture: %v", err)
	}

	_, text, totalPages, err := ExtractPDFPage(pdfBytes, 2)
	if err != nil {
		t.Fatalf("ExtractPDFPage returned error: %v", err)
	}
	if totalPages != 2 {
		t.Errorf("totalPages = %d, want 2", totalPages)
	}
	if !strings.Contains(text, "Page Two Text") {
		t.Errorf("text = %q, want page 2's content", text)
	}
	if strings.Contains(text, "Page One Text") {
		t.Errorf("text = %q, want only page 2's content, not page 1's", text)
	}
	if !strings.Contains(text, "last page") {
		t.Errorf("text = %q, want a last-page note since there's nothing after page 2", text)
	}
}

func TestExtractPDFPage_OutOfRangeClampsToLastPage(t *testing.T) {
	pdfBytes, err := base64.StdEncoding.DecodeString(twoPageTestPDFBase64)
	if err != nil {
		t.Fatalf("decoding test fixture: %v", err)
	}

	_, text, _, err := ExtractPDFPage(pdfBytes, 99)
	if err != nil {
		t.Fatalf("ExtractPDFPage returned error: %v", err)
	}
	if !strings.Contains(text, "Page Two Text") {
		t.Errorf("text = %q, want page 99 clamped down to the last real page (2)", text)
	}
}

func TestHandleWebRead_PDFPageParam(t *testing.T) {
	srv := twoPageTestPDFServer(t)

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebRead(fmt.Sprintf(`{"url":"%s","page":2}`, srv.URL), ctx)

	if !strings.Contains(result, "Page Two Text") {
		t.Errorf("result = %q, want page 2's content when page:2 is requested", result)
	}
	if strings.Contains(result, "Page One Text") {
		t.Errorf("result = %q, want only page 2's content", result)
	}
}

func TestHandleWebRead_PDFIgnoresOffsetPagination(t *testing.T) {
	// offset is the HTML pagination knob; a PDF paginates by page instead,
	// so an offset alone (no page) must not truncate/window a PDF's text.
	srv := twoPageTestPDFServer(t)

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebRead(fmt.Sprintf(`{"url":"%s","offset":5}`, srv.URL), ctx)

	if !strings.Contains(result, "Page One Text") {
		t.Errorf("result = %q, want page 1's full content, unaffected by offset", result)
	}
}

func TestHandleWebRead_DeadLinkFallsBackToArchiveOrg(t *testing.T) {
	deadServer := fakeHTMLPage(t, http.StatusNotFound, "not found")
	snapshotServer := fakeHTMLPage(t, http.StatusOK,
		`<html><head><title>Archived</title></head><body><article>Content recovered from the archive.</article></body></html>`)

	waybackJSON := `{"archived_snapshots":{"closest":{"available":true,"url":"` + snapshotServer.URL + `"}}}`
	waybackServer := fakeJSONServer(t, http.StatusOK, waybackJSON)
	withWaybackAPI(t, waybackServer.URL)

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebRead(`{"url":"`+deadServer.URL+`"}`, ctx)

	if !strings.Contains(result, "Content recovered from the archive.") {
		t.Errorf("result = %q, want the archive.org snapshot's content", result)
	}
}

func TestHandleWebRead_NoArchiveSnapshotFallsBackToTavily(t *testing.T) {
	deadServer := fakeHTMLPage(t, http.StatusNotFound, "not found")

	waybackJSON := `{"archived_snapshots":{}}`
	waybackServer := fakeJSONServer(t, http.StatusOK, waybackJSON)
	withWaybackAPI(t, waybackServer.URL)

	tavilyJSON := `{"results":[{"url":"x","raw_content":"` + strings.Repeat("Rendered by Tavily. ", 20) + `"}]}`
	tavilyServer := fakeJSONServer(t, http.StatusOK, tavilyJSON)

	ctx := &Context{
		Ctx:    context.Background(),
		Emit:   func(string, map[string]interface{}) {},
		Tavily: tavily.NewClientForTest("test-key", tavilyServer.URL),
	}
	result := handleWebRead(`{"url":"`+deadServer.URL+`"}`, ctx)

	if !strings.Contains(result, "Rendered by Tavily.") {
		t.Errorf("result = %q, want Tavily's content once archive.org has no snapshot", result)
	}
}

func TestHandleWebRead_EmptyBodyFallsBackToTavilyWithoutArchiveOrg(t *testing.T) {
	// Simulates a JS-rendered SPA: HTTP 200, valid but empty HTML shell —
	// looksEmpty(text) is true, but err is nil, so archive.org (which
	// would just return the same empty shell) should never be tried.
	jsRenderedServer := fakeHTMLPage(t, http.StatusOK, `<html><body><div id="root"></div></body></html>`)

	waybackCalled := false
	waybackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		waybackCalled = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"archived_snapshots":{}}`))
	}))
	t.Cleanup(waybackServer.Close)
	withWaybackAPI(t, waybackServer.URL)

	tavilyJSON := `{"results":[{"url":"x","raw_content":"` + strings.Repeat("Full SPA content via Tavily. ", 20) + `"}]}`
	tavilyServer := fakeJSONServer(t, http.StatusOK, tavilyJSON)

	ctx := &Context{
		Ctx:    context.Background(),
		Emit:   func(string, map[string]interface{}) {},
		Tavily: tavily.NewClientForTest("test-key", tavilyServer.URL),
	}
	result := handleWebRead(`{"url":"`+jsRenderedServer.URL+`"}`, ctx)

	if !strings.Contains(result, "Full SPA content via Tavily.") {
		t.Errorf("result = %q, want Tavily's rendered content", result)
	}
	if waybackCalled {
		t.Error("archive.org was called for a JS-render case (err == nil) — it should only be tried on dead links/paywalls")
	}
}

func TestHandleWebRead_NoFallbackConfigured_ReturnsError(t *testing.T) {
	deadServer := fakeHTMLPage(t, http.StatusNotFound, "not found")
	waybackServer := fakeJSONServer(t, http.StatusOK, `{"archived_snapshots":{}}`)
	withWaybackAPI(t, waybackServer.URL)

	// No ctx.Tavily configured at all — mirrors a deployment that hasn't
	// set TAVILY_API_KEY.
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebRead(`{"url":"`+deadServer.URL+`"}`, ctx)

	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error when every fallback is unavailable", result)
	}
}
