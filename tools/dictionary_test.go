package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleDictionary_WordRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleDictionary(`{}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a word-required error", result)
	}
}

func TestHandleDictionary_PrimarySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"word": "ephemeral",
			"phonetic": "/əˈfɛmərəl/",
			"meanings": [{
				"partOfSpeech": "adjective",
				"definitions": [
					{"definition": "Lasting for a short period of time.", "example": "ephemeral popularity"}
				]
			}],
			"sourceUrls": ["https://en.wiktionary.org/wiki/ephemeral"]
		}]`))
	}))
	t.Cleanup(srv.Close)
	original := dictionaryAPIDevBaseURL
	dictionaryAPIDevBaseURL = srv.URL
	t.Cleanup(func() { dictionaryAPIDevBaseURL = original })

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleDictionary(`{"word":"ephemeral"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want a formatted definition", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://en.wiktionary.org/wiki/ephemeral" {
		t.Errorf("Citations = %+v, want the wiktionary page added", ctx.Citations)
	}
}

func TestHandleDictionary_PrimaryNotFoundFallsBackToSecondary(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"title":"No Definitions Found"}`))
	}))
	t.Cleanup(primary.Close)
	originalPrimary := dictionaryAPIDevBaseURL
	dictionaryAPIDevBaseURL = primary.URL
	t.Cleanup(func() { dictionaryAPIDevBaseURL = originalPrimary })

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"word": "quixotic",
			"entries": [{
				"partOfSpeech": "adjective",
				"pronunciations": [{"text": "/kwɪkˈsɒtɪk/"}],
				"senses": [{"definition": "Exceedingly idealistic.", "examples": []}]
			}],
			"source": {"url": "https://en.wiktionary.org/wiki/quixotic"}
		}`))
	}))
	t.Cleanup(fallback.Close)
	originalFallback := freeDictionaryAPIBaseURL
	freeDictionaryAPIBaseURL = fallback.URL
	t.Cleanup(func() { freeDictionaryAPIBaseURL = originalFallback })

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleDictionary(`{"word":"quixotic"}`, ctx)
	if result == "" || result[:6] == "error:" {
		t.Fatalf("result = %q, want the fallback source's formatted definition", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].URL != "https://en.wiktionary.org/wiki/quixotic" {
		t.Errorf("Citations = %+v, want the fallback source's page added", ctx.Citations)
	}
}

func TestHandleDictionary_BothSourcesFail(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(primary.Close)
	originalPrimary := dictionaryAPIDevBaseURL
	dictionaryAPIDevBaseURL = primary.URL
	t.Cleanup(func() { dictionaryAPIDevBaseURL = originalPrimary })

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"word": "asdkjqwe123nonsense", "entries": []}`))
	}))
	t.Cleanup(fallback.Close)
	originalFallback := freeDictionaryAPIBaseURL
	freeDictionaryAPIBaseURL = fallback.URL
	t.Cleanup(func() { freeDictionaryAPIBaseURL = originalFallback })

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleDictionary(`{"word":"asdkjqwe123nonsense"}`, ctx)
	if result == "" || result[:6] != "error:" {
		t.Errorf("result = %q, want a not-found error", result)
	}
}
