package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandleReferenceLookup_QueryRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleReferenceLookup(`{"source":"wikipedia"}`, ctx, "test-call")
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a query-required error", result)
	}
}

func TestHandleReferenceLookup_UnknownSource(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleReferenceLookup(`{"source":"bing","query":"go"}`, ctx, "test-call")
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want an unknown-source error", result)
	}
}

func TestHandleReferenceLookup_Wikipedia(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":{"pages":{"123":{
			"title":"Go (programming language)",
			"extract":"Go is a statically typed, compiled programming language.",
			"fullurl":"https://en.wikipedia.org/wiki/Go_(programming_language)",
			"thumbnail":{"source":"https://upload.wikimedia.org/go-gopher.png","width":500,"height":300}
		}}}}`))
	}))
	t.Cleanup(srv.Close)
	original := wikipediaAPIBaseURL
	wikipediaAPIBaseURL = srv.URL
	t.Cleanup(func() { wikipediaAPIBaseURL = original })

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleReferenceLookup(`{"source":"wikipedia","query":"golang"}`, ctx, "test-call")
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted summary", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://en.wikipedia.org/wiki/Go_(programming_language)" {
		t.Errorf("Citations = %+v, want the article added", ctx.Citations)
	}
	if ctx.Citations[0].ImageURL != "https://upload.wikimedia.org/go-gopher.png" {
		t.Errorf("Citations[0].ImageURL = %q, want the article's own lead thumbnail", ctx.Citations[0].ImageURL)
	}
}

func TestHandleReferenceLookup_WikipediaNoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":{"pages":{}}}`))
	}))
	t.Cleanup(srv.Close)
	original := wikipediaAPIBaseURL
	wikipediaAPIBaseURL = srv.URL
	t.Cleanup(func() { wikipediaAPIBaseURL = original })

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleReferenceLookup(`{"source":"wikipedia","query":"asdkjqwe123nonsense"}`, ctx, "test-call")
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a not-found error", result)
	}
}

func TestHandleReferenceLookup_Arxiv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <id>http://arxiv.org/abs/1234.5678v1</id>
    <title>Attention Is All You Need</title>
    <summary>The dominant sequence transduction models are based on complex recurrent
    or convolutional neural networks.</summary>
  </entry>
</feed>`))
	}))
	t.Cleanup(srv.Close)
	original := arxivAPIBaseURL
	arxivAPIBaseURL = srv.URL
	t.Cleanup(func() { arxivAPIBaseURL = original })

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleReferenceLookup(`{"source":"arxiv","query":"transformers"}`, ctx, "test-call")
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want formatted paper results", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "http://arxiv.org/abs/1234.5678v1" {
		t.Errorf("Citations = %+v, want the paper added", ctx.Citations)
	}
	if ctx.Citations[0].ImageURL != arxivLogoURL {
		t.Errorf("Citations[0].ImageURL = %q, want the shared arXiv source badge", ctx.Citations[0].ImageURL)
	}
}

// arXiv throttles with 429 "Rate exceeded" (seen live, 2026-10); one spaced
// retry should ride that out, but a persistent 429 must still surface.
func TestHandleReferenceLookup_ArxivRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "Rate exceeded.", http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>http://arxiv.org/abs/1</id><title>T</title><summary>S</summary></entry></feed>`))
	}))
	t.Cleanup(srv.Close)
	origURL, origDelay := arxivAPIBaseURL, arxivRetryDelay
	arxivAPIBaseURL, arxivRetryDelay = srv.URL, time.Millisecond
	t.Cleanup(func() { arxivAPIBaseURL, arxivRetryDelay = origURL, origDelay })

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleReferenceLookup(`{"source":"arxiv","query":"x"}`, ctx, "c")
	if strings.HasPrefix(result, "error:") || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("result=%q calls=%d, want success after exactly one retry", result, calls)
	}
}

func TestHandleReferenceLookup_ArxivPersistent429Surfaces(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "Rate exceeded.", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	origURL, origDelay := arxivAPIBaseURL, arxivRetryDelay
	arxivAPIBaseURL, arxivRetryDelay = srv.URL, time.Millisecond
	t.Cleanup(func() { arxivAPIBaseURL, arxivRetryDelay = origURL, origDelay })

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleReferenceLookup(`{"source":"arxiv","query":"x"}`, ctx, "c")
	if !strings.Contains(result, "status 429") || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("result=%q calls=%d, want 429 error after 2 attempts", result, calls)
	}
}
