package tools

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDescribeImageURL_FetchesAndDescribes(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	}))
	t.Cleanup(srv.Close)

	var gotB64, gotMime, gotInstr string
	ctx := &Context{
		Ctx: context.Background(),
		DescribeImage: func(_ context.Context, b64, mime, instr string) (string, float64, error) {
			gotB64, gotMime, gotInstr = b64, mime, instr
			return "a night-sky photo", 0.002, nil
		},
	}

	desc, cost, err := DescribeImageURL(ctx, srv.URL+"/a.png", "is it a photo?")
	if err != nil {
		t.Fatalf("DescribeImageURL: %v", err)
	}
	if desc != "a night-sky photo" || cost != 0.002 {
		t.Errorf("got (%q, %v), want the describer's own result and cost passed through", desc, cost)
	}
	if gotMime != "image/png" || gotInstr != "is it a photo?" {
		t.Errorf("describer got mime=%q instructions=%q, want image/png and the caller's instructions", gotMime, gotInstr)
	}
	if want := base64.StdEncoding.EncodeToString(png); gotB64 != want {
		t.Errorf("describer got different image bytes than the server served")
	}
}

func TestDescribeImageURL_NoDescriberConfigured(t *testing.T) {
	_, _, err := DescribeImageURL(&Context{Ctx: context.Background()}, "https://example.com/a.png", "")
	if err == nil || !strings.Contains(err.Error(), "no multimodal model") {
		t.Errorf("err = %v, want a no-multimodal-model error", err)
	}
}

func TestDescribeImageURL_FetchFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	called := false
	ctx := &Context{
		Ctx: context.Background(),
		DescribeImage: func(context.Context, string, string, string) (string, float64, error) {
			called = true
			return "", 0, nil
		},
	}
	if _, _, err := DescribeImageURL(ctx, srv.URL+"/a.png", ""); err == nil {
		t.Fatal("err = nil, want an error when the image host returns 403 (hotlink protection)")
	}
	if called {
		t.Error("describer ran on a failed fetch")
	}
}
