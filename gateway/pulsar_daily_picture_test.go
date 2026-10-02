package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"polaris/llm"
	"polaris/llm/llmtest"
	"polaris/search"
	"polaris/tools"
)

// fakeImageSearXNG answers SearXNG's images category with whatever the
// per-query table says, so a test can make one query return junk and a
// retry return something good.
func fakeImageSearXNG(t *testing.T, byQuery map[string][]map[string]interface{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"results": byQuery[r.URL.Query().Get("q")]})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pictureTestContext(srv *httptest.Server) *tools.Context {
	return &tools.Context{
		Ctx:     context.Background(),
		SearXNG: search.NewSearXNGClient(srv.URL, nil),
		Emit:    func(string, map[string]interface{}) {},
	}
}

func imageResult(title, thumb string) map[string]interface{} {
	return map[string]interface{}{"title": title, "url": "https://example.com/page", "thumbnail": thumb, "img_src": thumb}
}

func queryResponse(q string) llmtest.Response {
	return llmtest.Response{Resp: &llm.ChatResponse{Content: q}}
}

// The regression this vetting exists for: the top hit for a vague query was
// a leggings ad. The judge rejects it, the retry (told why) finds a real
// photo, and that's what ships — with the judge's caption, not the junk
// search title.
func TestGenerateDailyPictureBlock_RejectionTriggersRetryWithFeedback(t *testing.T) {
	srv := fakeImageSearXNG(t, map[string][]map[string]interface{}{
		"abstract flowing curves": {imageResult("Women's OneForm Seamless High Waisted", "https://cdn.example/leggings.jpg")},
		"aurora over mountains":   {imageResult("Aurora borealis over a ridge", "https://cdn.example/aurora.jpg")},
	})
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		queryResponse("abstract flowing curves"),
		{Resp: toolCallResponse("pick_picture", `{"choice":0,"reasoning":"a leggings product shot, not a photograph"}`)},
		queryResponse("aurora over mountains"),
		{Resp: toolCallResponse("pick_picture", `{"choice":1,"caption":"Green aurora above a snowy ridge","reasoning":"real night-sky photo"}`)},
	}}

	content, imageURL, _, err := generateDailyPictureBlock(context.Background(), mock, pictureTestContext(srv), "SPACE PHOTOS")
	if err != nil {
		t.Fatalf("generateDailyPictureBlock: %v", err)
	}
	if imageURL != "https://cdn.example/aurora.jpg" {
		t.Errorf("imageURL = %q, want the aurora photo, not the rejected leggings ad", imageURL)
	}
	if content != "Green aurora above a snowy ridge" {
		t.Errorf("content = %q, want the judge's caption rather than the search title", content)
	}

	retryPrompt := mock.Calls[2].Messages[len(mock.Calls[2].Messages)-1].Content
	if !strings.Contains(retryPrompt, "abstract flowing curves") || !strings.Contains(retryPrompt, "leggings product shot") {
		t.Errorf("retry query prompt = %q, want the rejected query and the judge's reason fed back", retryPrompt)
	}
}

// Nothing qualifying ever must error (Stage D drops the block) rather than
// fall back to "just take the top hit".
func TestGenerateDailyPictureBlock_AllRejectedErrors(t *testing.T) {
	srv := fakeImageSearXNG(t, map[string][]map[string]interface{}{
		"q1": {imageResult("Shop leggings", "https://cdn.example/a.jpg")},
		"q2": {imageResult("Buy shorts", "https://cdn.example/b.jpg")},
	})
	mock := &llmtest.MockClient{Responses: []llmtest.Response{
		queryResponse("q1"),
		{Resp: toolCallResponse("pick_picture", `{"choice":0,"reasoning":"ad"}`)},
		queryResponse("q2"),
		{Resp: toolCallResponse("pick_picture", `{"choice":0,"reasoning":"ad again"}`)},
	}}

	_, imageURL, _, err := generateDailyPictureBlock(context.Background(), mock, pictureTestContext(srv), "")
	if err == nil {
		t.Fatalf("err = nil, imageURL = %q; want an error once every query's candidates were rejected", imageURL)
	}
	if imageURL != "" {
		t.Errorf("imageURL = %q, want empty on failure", imageURL)
	}
}

// A judge that answers in prose, or picks a number that doesn't exist, must
// count as a rejection — never as "take the first one".
func TestDailyPictureVet_UnusableJudgeAnswerIsRejection(t *testing.T) {
	cands := []tools.Card{{Title: "t", Subtitle: "example.com", ImageURL: "https://cdn.example/a.jpg", Kind: "image"}}
	cases := map[string]llmtest.Response{
		"prose instead of the tool": {Resp: &llm.ChatResponse{Content: "I like number 1"}},
		"choice out of range":       {Resp: toolCallResponse("pick_picture", `{"choice":7,"reasoning":"x"}`)},
		"garbage arguments":         {Resp: toolCallResponse("pick_picture", `not json`)},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			mock := &llmtest.MockClient{Responses: []llmtest.Response{resp}}
			pick, _, err := dailyPictureVet(context.Background(), mock, &tools.Context{Ctx: context.Background()}, "", cands)
			if err != nil {
				t.Fatalf("dailyPictureVet: %v", err)
			}
			if pick.index != -1 {
				t.Errorf("index = %d, want -1 (rejected)", pick.index)
			}
		})
	}
}

// With a vision model wired, a candidate whose image can't be fetched is
// never offered to the judge blind. (The gateway test binary keeps tools'
// SSRF-safe dialer, so a loopback image URL is exactly such an unfetchable
// one — and with all candidates dropped, the judge isn't even called.)
func TestDailyPictureVet_UndescribableCandidatesAreNotOfferedBlind(t *testing.T) {
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	t.Cleanup(img.Close)

	described := false
	ctx := &tools.Context{
		Ctx: context.Background(),
		DescribeImage: func(context.Context, string, string, string) (string, float64, error) {
			described = true
			return "a photo", 0, nil
		},
	}
	mock := &llmtest.MockClient{}
	cands := []tools.Card{{Title: "t", Subtitle: "example.com", ImageURL: img.URL + "/a.jpg", Kind: "image"}}

	pick, _, err := dailyPictureVet(context.Background(), mock, ctx, "", cands)
	if err != nil {
		t.Fatalf("dailyPictureVet: %v", err)
	}
	if pick.index != -1 {
		t.Errorf("index = %d, want -1 when nothing could be described", pick.index)
	}
	if described {
		t.Error("DescribeImage ran, but the fetch should have been refused by the SSRF-safe dialer first")
	}
	if len(mock.Calls) != 0 {
		t.Errorf("judge was called %d time(s), want 0 — nothing was left to judge", len(mock.Calls))
	}
}
