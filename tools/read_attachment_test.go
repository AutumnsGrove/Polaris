package tools

import (
	"encoding/base64"
	"strings"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
)

// twoPagePDFBytes decodes the fixture shared with web_read_test.go
// ("Page One Text" / "Page Two Text") — same package, so the const is
// already in scope.
func twoPagePDFBytes(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(twoPageTestPDFBase64)
	if err != nil {
		t.Fatalf("decoding test fixture: %v", err)
	}
	return data
}

func TestHandleReadAttachment_NoAttachmentErrors(t *testing.T) {
	ctx := newTestContext()
	result := handleReadAttachment(`{}`, ctx)
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an error with no AttachmentData", result)
	}
}

func TestHandleReadAttachment_DefaultsToFirstPage(t *testing.T) {
	ctx := newTestContext()
	ctx.AttachmentData = twoPagePDFBytes(t)

	result := handleReadAttachment(`{}`, ctx)
	if !strings.Contains(result, "Page One Text") {
		t.Errorf("result = %q, want page 1's content when page is unset", result)
	}
	if strings.Contains(result, "Page Two Text") {
		t.Errorf("result = %q, want only page 1's content", result)
	}
	if !strings.Contains(result, "page: 2") {
		t.Errorf("result = %q, want a continuation hint pointing at page 2", result)
	}
}

func TestHandleReadAttachment_SpecificPage(t *testing.T) {
	ctx := newTestContext()
	ctx.AttachmentData = twoPagePDFBytes(t)

	result := handleReadAttachment(`{"page":2}`, ctx)
	if !strings.Contains(result, "Page Two Text") {
		t.Errorf("result = %q, want page 2's content", result)
	}
	if !strings.Contains(result, "last page") {
		t.Errorf("result = %q, want a last-page note", result)
	}
}

func TestHandleReadAttachment_InstructionsRunFilterPass(t *testing.T) {
	ctx := newTestContext()
	ctx.AttachmentData = twoPagePDFBytes(t)
	ctx.LLM = &llmtest.MockClient{
		Responses: []llmtest.Response{{Resp: &llm.ChatResponse{Content: "filtered result"}}},
	}

	result := handleReadAttachment(`{"instructions":"just the total"}`, ctx)
	if !strings.Contains(result, "filtered result") {
		t.Errorf("result = %q, want the filter pass's output", result)
	}
}

func TestHandleReadAttachment_QueryFindsMatchingPage(t *testing.T) {
	ctx := newTestContext()
	ctx.AttachmentData = twoPagePDFBytes(t)

	result := handleReadAttachment(`{"query":"Page Two"}`, ctx)
	if !strings.Contains(result, "[page 2]") {
		t.Errorf("result = %q, want it to report the match on page 2", result)
	}
	if strings.Contains(result, "[page 1]") {
		t.Errorf("result = %q, want no match reported on page 1", result)
	}
}

func TestHandleReadAttachment_QueryWithNoMatchesSaysSo(t *testing.T) {
	ctx := newTestContext()
	ctx.AttachmentData = twoPagePDFBytes(t)

	result := handleReadAttachment(`{"query":"nonexistent term xyz"}`, ctx)
	if !strings.Contains(result, "no matches") {
		t.Errorf("result = %q, want a clear no-matches message", result)
	}
}

func TestSearchPDFPages_ReturnsSnippetWithContext(t *testing.T) {
	matches, totalPages, err := searchPDFPages(twoPagePDFBytes(t), "page one")
	if err != nil {
		t.Fatalf("searchPDFPages returned error: %v", err)
	}
	if totalPages != 2 {
		t.Errorf("totalPages = %d, want 2", totalPages)
	}
	if len(matches) != 1 || matches[0].Page != 1 {
		t.Fatalf("matches = %+v, want exactly one match on page 1", matches)
	}
	if !strings.Contains(strings.ToLower(matches[0].Snippet), "page one text") {
		t.Errorf("snippet = %q, want it to contain the matched text", matches[0].Snippet)
	}
}

func TestSearchPDFPages_IsCaseInsensitive(t *testing.T) {
	matches, _, err := searchPDFPages(twoPagePDFBytes(t), "PAGE TWO TEXT")
	if err != nil {
		t.Fatalf("searchPDFPages returned error: %v", err)
	}
	if len(matches) != 1 || matches[0].Page != 2 {
		t.Fatalf("matches = %+v, want exactly one case-insensitive match on page 2", matches)
	}
}

func TestSearchPDFPages_NoMatchReturnsEmpty(t *testing.T) {
	matches, totalPages, err := searchPDFPages(twoPagePDFBytes(t), "not in this document")
	if err != nil {
		t.Fatalf("searchPDFPages returned error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("matches = %+v, want none", matches)
	}
	if totalPages != 2 {
		t.Errorf("totalPages = %d, want 2", totalPages)
	}
}

func TestReadAttachment_OfferedOnlyWithAttachmentData(t *testing.T) {
	withoutAttachment := newTestContext()
	for _, d := range Defs(withoutAttachment) {
		if d.Function.Name == "read_attachment" {
			t.Error("Defs() offered read_attachment with no AttachmentData set")
		}
	}

	withAttachment := newTestContext()
	withAttachment.AttachmentData = twoPagePDFBytes(t)
	found := false
	for _, d := range Defs(withAttachment) {
		if d.Function.Name == "read_attachment" {
			found = true
		}
	}
	if !found {
		t.Error("Defs() did not offer read_attachment despite AttachmentData being set")
	}
}
