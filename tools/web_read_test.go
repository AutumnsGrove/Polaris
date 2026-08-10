package tools

import (
	"context"
	"encoding/base64"
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

	title, _, _, text, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil)
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

	_, siteName, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if siteName != "The Hollywood Reporter" {
		t.Errorf("siteName = %q, want %q", siteName, "The Hollywood Reporter")
	}
}

func TestFetchAndExtract_SiteNameEmptyWhenMissing(t *testing.T) {
	html := `<html><head><title>No meta tag here</title></head><body><article><p>content</p></article></body></html>`

	_, siteName, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil)
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

	_, _, imageURL, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil)
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
			_, _, imageURL, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil)
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
	_, _, _, text, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if !strings.Contains(text, "Just a plain page") {
		t.Errorf("text = %q, want the body content", text)
	}
}

func TestFetchAndExtract_TruncatesLongContent(t *testing.T) {
	huge := strings.Repeat("word ", 5000) // well over maxExtractedChars
	html := "<html><body><article>" + huge + "</article></body></html>"

	_, _, _, text, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL, nil)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error: %v", err)
	}
	if len(text) > maxExtractedChars+50 {
		t.Errorf("text length = %d, want it capped near maxExtractedChars (%d)", len(text), maxExtractedChars)
	}
	if !strings.HasSuffix(text, "[truncated]") {
		t.Errorf("text should end with a truncation marker, got suffix %q", text[max(0, len(text)-30):])
	}
}

func TestFetchAndExtract_NonOKStatus(t *testing.T) {
	_, _, _, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusNotFound, "not found").URL, nil)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
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

	_, _, _, _, err = fetchAndExtract(context.Background(), redirector.URL, bl)
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

	_, _, _, text, err := fetchAndExtract(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("fetchAndExtract returned error for a PDF: %v", err)
	}
	if !strings.Contains(text, "Hello World") {
		t.Errorf("text = %q, want it to contain the PDF's text content", text)
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
