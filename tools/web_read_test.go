package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
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

func TestFetchAndExtract_PrefersArticleOverChrome(t *testing.T) {
	html := `<html><head><title>My Article</title></head><body>
		<nav>site nav links</nav>
		<script>console.log('should be dropped')</script>
		<article><p>The actual article content.</p></article>
		<footer>copyright footer</footer>
	</body></html>`

	title, text, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL)
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

func TestFetchAndExtract_FallsBackToBodyWithoutArticleOrMain(t *testing.T) {
	html := `<html><body><p>Just a plain page with no article or main tag.</p></body></html>`
	_, text, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL)
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

	_, text, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusOK, html).URL)
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
	_, _, err := fetchAndExtract(context.Background(), fakeHTMLPage(t, http.StatusNotFound, "not found").URL)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}

func TestHandleWebRead_URLRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleWebRead(`{}`, ctx)
	if result != "error: url is required" {
		t.Errorf("result = %q, want the url-required error", result)
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
