package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractYouTubeID(t *testing.T) {
	cases := map[string]string{
		"dQw4w9WgXcQ": "dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":       "dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=43s": "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                      "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?si=abc123":            "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":        "dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ":         "dQw4w9WgXcQ",
		"https://www.youtube.com/live/dQw4w9WgXcQ":          "dQw4w9WgXcQ",
	}
	for input, want := range cases {
		got, err := extractYouTubeID(input)
		if err != nil {
			t.Errorf("extractYouTubeID(%q) returned error: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("extractYouTubeID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExtractYouTubeID_Invalid(t *testing.T) {
	for _, input := range []string{"", "not a url", "https://example.com/watch?v=nope"} {
		if _, err := extractYouTubeID(input); err == nil {
			t.Errorf("extractYouTubeID(%q) = nil error, want an error", input)
		}
	}
}

func TestPickCaptionTrack_PrefersHumanEnglishOverASR(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "en", Kind: "asr", BaseURL: "asr"},
		{LanguageCode: "en", Kind: "", BaseURL: "human"},
		{LanguageCode: "fr", Kind: "", BaseURL: "french"},
	}
	got := pickCaptionTrack(tracks)
	if got == nil || got.BaseURL != "human" {
		t.Errorf("pickCaptionTrack = %+v, want the human-uploaded English track", got)
	}
}

func TestPickCaptionTrack_FallsBackToASREnglish(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "fr", Kind: "", BaseURL: "french"},
		{LanguageCode: "en", Kind: "asr", BaseURL: "asr"},
	}
	got := pickCaptionTrack(tracks)
	if got == nil || got.BaseURL != "asr" {
		t.Errorf("pickCaptionTrack = %+v, want the ASR English track", got)
	}
}

func TestPickCaptionTrack_FallsBackToFirstAvailable(t *testing.T) {
	tracks := []captionTrack{
		{LanguageCode: "de", Kind: "", BaseURL: "german"},
		{LanguageCode: "fr", Kind: "", BaseURL: "french"},
	}
	got := pickCaptionTrack(tracks)
	if got == nil || got.BaseURL != "german" {
		t.Errorf("pickCaptionTrack = %+v, want the first track when no English is available", got)
	}
}

func TestPickCaptionTrack_NoTracks(t *testing.T) {
	if got := pickCaptionTrack(nil); got != nil {
		t.Errorf("pickCaptionTrack(nil) = %+v, want nil", got)
	}
}

func TestParseJSON3Transcript(t *testing.T) {
	body := `{"events":[{"segs":[{"utf8":"Hello "}]},{"segs":[{"utf8":"world."}]},{"aAppend":1}]}`
	text, err := parseJSON3Transcript(body)
	if err != nil {
		t.Fatalf("parseJSON3Transcript returned error: %v", err)
	}
	if text != "Hello world." {
		t.Errorf("text = %q, want %q", text, "Hello world.")
	}
}

func TestParseJSON3Transcript_Empty(t *testing.T) {
	if _, err := parseJSON3Transcript(`{"events":[]}`); err == nil {
		t.Error("expected an error for a transcript with no segments")
	}
}

// fakeYouTube serves a watch page embedding ytInitialPlayerResponse — its
// single caption track's baseUrl points back at this same server's
// /timedtext handler — plus the timedtext endpoint itself, enough to
// exercise fetchYouTubeTranscript end to end without touching the real
// youtube.com. withCaptions=false serves an empty caption track list, for
// exercising the no-captions-available path.
func fakeYouTube(t *testing.T, withCaptions bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		captions := "[]"
		if withCaptions {
			captions = fmt.Sprintf(`[{"baseUrl":"%s/timedtext","languageCode":"en","kind":""}]`, srv.URL)
		}
		page := fmt.Sprintf(`<html><body><script>var ytInitialPlayerResponse = `+
			`{"videoDetails":{"title":"Test Video"},"captions":{"playerCaptionsTracklistRenderer":`+
			`{"captionTracks":%s}}};var ytcfg = {};</script></body></html>`, captions)
		w.Write([]byte(page))
	})
	mux.HandleFunc("/timedtext", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"events":[{"segs":[{"utf8":"This is the transcript."}]}]}`))
	})

	return srv
}

func withYouTubeWatchBaseURL(t *testing.T, base string) {
	t.Helper()
	original := youtubeWatchBaseURL
	youtubeWatchBaseURL = base
	t.Cleanup(func() { youtubeWatchBaseURL = original })
}

func TestFetchYouTubeTranscript_Success(t *testing.T) {
	srv := fakeYouTube(t, true)
	withYouTubeWatchBaseURL(t, srv.URL+"/watch?v=")

	title, transcript, err := fetchYouTubeTranscript(context.Background(), "dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("fetchYouTubeTranscript returned error: %v", err)
	}
	if title != "Test Video" {
		t.Errorf("title = %q, want %q", title, "Test Video")
	}
	if !strings.Contains(transcript, "This is the transcript.") {
		t.Errorf("transcript = %q, want it to contain the timedtext content", transcript)
	}
}

func TestFetchYouTubeTranscript_NoCaptions(t *testing.T) {
	srv := fakeYouTube(t, false)
	withYouTubeWatchBaseURL(t, srv.URL+"/watch?v=")

	_, _, err := fetchYouTubeTranscript(context.Background(), "dQw4w9WgXcQ")
	if err == nil {
		t.Error("expected an error for a video with no caption tracks")
	}
}

func TestHandleYouTubeTranscript_URLRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleYouTubeTranscript(`{}`, ctx)
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want a url-required error", result)
	}
}

func TestHandleYouTubeTranscript_InvalidURL(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleYouTubeTranscript(`{"url":"not a youtube url"}`, ctx)
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an invalid-url error", result)
	}
}

func TestHandleYouTubeTranscript_Success(t *testing.T) {
	srv := fakeYouTube(t, true)
	withYouTubeWatchBaseURL(t, srv.URL+"/watch?v=")

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ"}`, ctx)
	if !strings.Contains(result, "This is the transcript.") {
		t.Errorf("result = %q, want it to contain the transcript", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].Title != "Test Video" {
		t.Errorf("citations = %+v, want one citation titled %q", ctx.Citations, "Test Video")
	}
}
