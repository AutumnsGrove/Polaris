package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"polaris/tavily"
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

func TestPickYtDlpLanguage_PrefersHumanEnglishOverASR(t *testing.T) {
	info := &ytDlpInfo{
		Subtitles:         map[string][]ytDlpSubFormat{"en": {{Ext: "json3"}}},
		AutomaticCaptions: map[string][]ytDlpSubFormat{"en": {{Ext: "json3"}}},
	}
	lang, isAuto, ok := pickYtDlpLanguage(info)
	if !ok || lang != "en" || isAuto {
		t.Errorf("pickYtDlpLanguage = (%q, %v, %v), want (\"en\", false, true)", lang, isAuto, ok)
	}
}

func TestPickYtDlpLanguage_FallsBackToASREnglish(t *testing.T) {
	info := &ytDlpInfo{
		Subtitles:         map[string][]ytDlpSubFormat{"fr": {{Ext: "json3"}}},
		AutomaticCaptions: map[string][]ytDlpSubFormat{"en": {{Ext: "json3"}}},
	}
	lang, isAuto, ok := pickYtDlpLanguage(info)
	if !ok || lang != "en" || !isAuto {
		t.Errorf("pickYtDlpLanguage = (%q, %v, %v), want (\"en\", true, true)", lang, isAuto, ok)
	}
}

func TestPickYtDlpLanguage_FallsBackToFirstAvailable(t *testing.T) {
	info := &ytDlpInfo{
		Subtitles: map[string][]ytDlpSubFormat{"de": {{Ext: "json3"}}, "fr": {{Ext: "json3"}}},
	}
	lang, isAuto, ok := pickYtDlpLanguage(info)
	if !ok || lang != "de" || isAuto {
		t.Errorf("pickYtDlpLanguage = (%q, %v, %v), want the alphabetically-first manual track (\"de\", false, true)", lang, isAuto, ok)
	}
}

func TestPickYtDlpLanguage_NoTracks(t *testing.T) {
	if _, _, ok := pickYtDlpLanguage(&ytDlpInfo{}); ok {
		t.Error("pickYtDlpLanguage on an empty info = ok, want not ok")
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

// writeFakeYtDlp writes a shell script standing in for the real yt-dlp
// binary and points ytDlpPath at it for the duration of the test — same
// idea as the old httptest-server stub, but at the process-exec boundary
// instead of HTTP, since fetchYouTubeTranscript now shells out rather
// than making its own requests. script receives yt-dlp's own argv (via
// "$@") and must handle both invocations this package makes: `-j
// --skip-download <url>` (info) and `--sub-langs ... -o <path> <url>`
// (the actual subtitle download).
func writeFakeYtDlp(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "yt-dlp")
	if runtime.GOOS == "windows" {
		t.Skip("fake yt-dlp shell script stub isn't set up for windows")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("writing fake yt-dlp: %v", err)
	}
	original := ytDlpPath
	ytDlpPath = path
	// The cache, gap, and backoffs are process-wide state: without this
	// reset, one test's cached success would satisfy the next test's fetch
	// of the same video ID, and the real 2s gap/8s backoff would make the
	// suite crawl.
	originalGap, originalBackoffs := ytDlpMinGap, ytRateLimitBackoffs
	ytDlpMinGap, ytRateLimitBackoffs = 0, []time.Duration{time.Millisecond, time.Millisecond}
	ytTranscriptCache.reset()
	t.Cleanup(func() {
		ytDlpPath = original
		ytDlpMinGap, ytRateLimitBackoffs = originalGap, originalBackoffs
		ytTranscriptCache.reset()
	})
}

// fakeYtDlpScript recognizes the two invocation shapes fetchYouTubeTranscript
// makes by checking for "-j" (the info call) vs. "-o" (the subtitle-download
// call, whose destination template is the argument right after "-o").
const fakeYtDlpScript = `
for arg in "$@"; do
	if [ "$arg" = "-j" ]; then
		echo '{"title":"Test Video","subtitles":{},"automatic_captions":{"en":[{"ext":"json3"}]}}'
		exit 0
	fi
done
prev=""
for arg in "$@"; do
	if [ "$prev" = "-o" ]; then
		# yt-dlp's own -o template is "<dir>/%(id)s" — the real binary
		# substitutes the id; this stub just writes straight into that
		# same directory under a fixed name, since there's only ever one
		# video id in play in these tests.
		dir=$(dirname "$arg")
		echo '{"events":[{"segs":[{"utf8":"This is the transcript."}]}]}' > "$dir/dQw4w9WgXcQ.en.json3"
		exit 0
	fi
	prev="$arg"
done
exit 1
`

func TestFetchYouTubeTranscript_Success(t *testing.T) {
	writeFakeYtDlp(t, fakeYtDlpScript)

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
	writeFakeYtDlp(t, `
for arg in "$@"; do
	if [ "$arg" = "-j" ]; then
		echo '{"title":"Test Video","subtitles":{},"automatic_captions":{}}'
		exit 0
	fi
done
exit 1
`)

	_, _, err := fetchYouTubeTranscript(context.Background(), "dQw4w9WgXcQ")
	if err == nil {
		t.Error("expected an error for a video with no caption tracks")
	}
}

// rateLimitedThenOKScript fails the subtitle download with yt-dlp's real
// 429 stderr text for its first failN calls, then behaves like
// fakeYtDlpScript. The counter lives next to the stub ($0's directory)
// since each invocation is a fresh process. Every subtitle call, success or
// not, also appends a line to calls so tests can count real attempts.
func rateLimitedThenOKScript(failN int) string {
	return `
for arg in "$@"; do
	if [ "$arg" = "-j" ]; then
		echo '{"title":"Test Video","subtitles":{},"automatic_captions":{"en":[{"ext":"json3"}]}}'
		exit 0
	fi
done
d=$(dirname "$0")
echo x >> "$d/calls"
n=$(wc -l < "$d/calls")
if [ "$n" -le ` + fmt.Sprint(failN) + ` ]; then
	echo "ERROR: Unable to download video subtitles for 'en': HTTP Error 429: Too Many Requests" >&2
	exit 1
fi
prev=""
for arg in "$@"; do
	if [ "$prev" = "-o" ]; then
		echo '{"events":[{"segs":[{"utf8":"This is the transcript."}]}]}' > "$(dirname "$arg")/dQw4w9WgXcQ.en.json3"
		exit 0
	fi
	prev="$arg"
done
exit 1
`
}

func TestFetchYouTubeTranscript_RetriesRateLimit(t *testing.T) {
	writeFakeYtDlp(t, rateLimitedThenOKScript(2))

	_, transcript, err := fetchYouTubeTranscript(context.Background(), "dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("expected recovery after two 429s, got error: %v", err)
	}
	if !strings.Contains(transcript, "This is the transcript.") {
		t.Errorf("transcript = %q", transcript)
	}
}

func TestFetchYouTubeTranscript_RateLimitExhaustedGivesClearError(t *testing.T) {
	writeFakeYtDlp(t, rateLimitedThenOKScript(100))

	_, _, err := fetchYouTubeTranscript(context.Background(), "dQw4w9WgXcQ")
	if err == nil || !strings.Contains(err.Error(), "rate-limiting") {
		t.Errorf("err = %v, want a clear rate-limit error", err)
	}
}

// Concurrent threads asking for the same video must cost one real fetch:
// the first through the gate fills the cache, the rest hit it.
func TestFetchYouTubeTranscript_ConcurrentSameVideoFetchesOnce(t *testing.T) {
	writeFakeYtDlp(t, rateLimitedThenOKScript(0))
	dir := filepath.Dir(ytDlpPath)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := fetchYouTubeTranscript(context.Background(), "dQw4w9WgXcQ"); err != nil {
				t.Errorf("fetch failed: %v", err)
			}
		}()
	}
	wg.Wait()

	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if n := strings.Count(string(calls), "x"); n != 1 {
		t.Errorf("subtitle downloads = %d, want 1 (rest should come from cache)", n)
	}
}

// A cancelled context must stop a thread waiting behind the gate rather
// than leaving it queued.
func TestFetchYouTubeTranscript_CancelledWhileWaitingForGate(t *testing.T) {
	writeFakeYtDlp(t, fakeYtDlpScript)
	ytDlpGate <- struct{}{}
	defer func() { <-ytDlpGate }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := fetchYouTubeTranscript(ctx, "dQw4w9WgXcQ")
	if err == nil {
		t.Error("expected a context error while the gate was held")
	}
}

func TestFetchYouTubeTranscript_YtDlpMissing(t *testing.T) {
	original := ytDlpPath
	ytDlpPath = filepath.Join(t.TempDir(), "no-such-binary")
	t.Cleanup(func() { ytDlpPath = original })

	_, _, err := fetchYouTubeTranscript(context.Background(), "dQw4w9WgXcQ")
	if err == nil || !strings.Contains(err.Error(), "yt-dlp is not installed") {
		t.Errorf("err = %v, want a clear yt-dlp-not-installed error", err)
	}
}

func TestHandleYouTubeTranscript_URLRequired(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleYouTubeTranscript(`{}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want a url-required error", result)
	}
}

func TestHandleYouTubeTranscript_InvalidURL(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleYouTubeTranscript(`{"url":"not a youtube url"}`, ctx, "test-call")
	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result = %q, want an invalid-url error", result)
	}
}

func TestHandleYouTubeTranscript_Success(t *testing.T) {
	writeFakeYtDlp(t, fakeYtDlpScript)

	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ"}`, ctx, "test-call")
	if !strings.Contains(result, "This is the transcript.") {
		t.Errorf("result = %q, want it to contain the transcript", result)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].Title != "Test Video" {
		t.Errorf("citations = %+v, want one citation titled %q", ctx.Citations, "Test Video")
	}
}

// tavilyWatchPage is shaped like the real Tavily Extract output captured in
// the 2026-10-04 spike: title first line, metadata, a description that
// itself contains a "Transcript of ..." line, then the real "Transcript"
// section with a one-word cue that stripBoilerplateLines-style filtering
// would have eaten.
const tavilyWatchPage = "Test Video Title\nChannel: Someone (verified)\nDescription\n" +
	"Transcript of the address:\nTranscript\n" +
	"[0:07] Hello there.\n[0:10] Ah\n[1:02:03] Last line."

func tavilyExtractJSON(t *testing.T, rawContent string) string {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{
		"results": []map[string]string{{"url": "x", "raw_content": rawContent}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseTavilyYouTubeTranscript(t *testing.T) {
	title, transcript, ok := parseTavilyYouTubeTranscript(tavilyWatchPage, "fallback")
	if !ok {
		t.Fatal("expected a transcript")
	}
	if title != "Test Video Title" {
		t.Errorf("title = %q", title)
	}
	if transcript != "Hello there. Ah Last line." {
		t.Errorf("transcript = %q, want timestamps stripped and short cue kept", transcript)
	}
}

func TestParseTavilyYouTubeTranscript_NoTranscriptSection(t *testing.T) {
	// Tavily returns the page with no error and no Transcript section for a
	// video without captions — absence is the only signal.
	_, _, ok := parseTavilyYouTubeTranscript("Some Video\nChannel: x\nDescription\nTranscript of nothing here", "fb")
	if ok {
		t.Error("ok = true for a page with no Transcript section")
	}
}

// tavilyCtx builds a Context wired to a fake Tavily server, with ytDlpPath
// pointed at a nonexistent binary so any accidental yt-dlp use fails loudly.
func tavilyCtx(t *testing.T, serverURL string, used int, incremented *int) *Context {
	t.Helper()
	ytTranscriptCache.reset()
	t.Cleanup(ytTranscriptCache.reset)
	original := ytDlpPath
	ytDlpPath = filepath.Join(t.TempDir(), "no-such-binary")
	t.Cleanup(func() { ytDlpPath = original })
	return &Context{
		Ctx:                  context.Background(),
		Emit:                 func(string, map[string]interface{}) {},
		Tavily:               tavily.NewClientForTest("test-key", serverURL),
		TavilyUsageThisMonth: func() (int, error) { return used, nil },
		IncrementTavilyUsage: func() error { *incremented++; return nil },
	}
}

func TestHandleYouTubeTranscript_TavilyProvider(t *testing.T) {
	server := fakeJSONServer(t, http.StatusOK, tavilyExtractJSON(t, tavilyWatchPage))
	incremented := 0
	ctx := tavilyCtx(t, server.URL, 0, &incremented)

	result := handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ","provider":"tavily"}`, ctx, "c")
	if result != "Hello there. Ah Last line." {
		t.Errorf("result = %q", result)
	}
	if incremented != 1 {
		t.Errorf("tavily usage incremented %d times, want 1", incremented)
	}
	if len(ctx.Citations) != 1 || ctx.Citations[0].Title != "Test Video Title" {
		t.Errorf("citations = %+v", ctx.Citations)
	}

	// A second ask for the same video is served from cache: no new credit.
	handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ","provider":"tavily"}`, ctx, "c2")
	if incremented != 1 {
		t.Errorf("cached repeat spent another credit (incremented = %d)", incremented)
	}
}

func TestHandleYouTubeTranscript_TavilyNoCaptionsCountsCreditNoHint(t *testing.T) {
	server := fakeJSONServer(t, http.StatusOK, tavilyExtractJSON(t, "Captionless\nChannel: x\nDescription\nnothing"))
	incremented := 0
	ctx := tavilyCtx(t, server.URL, 0, &incremented)

	result := handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ","provider":"tavily"}`, ctx, "c")
	if !strings.HasPrefix(result, "error:") || !strings.Contains(result, "no captions") {
		t.Errorf("result = %q, want a no-captions error", result)
	}
	if strings.Contains(result, `provider="tavily"`) {
		t.Errorf("result = %q, must not re-suggest tavily", result)
	}
	if incremented != 1 {
		t.Errorf("incremented = %d, want 1 (the credit was spent)", incremented)
	}
}

func TestHandleYouTubeTranscript_TavilyCapReached(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	t.Cleanup(server.Close)
	incremented := 0
	ctx := tavilyCtx(t, server.URL, tavilyMonthlyCap, &incremented)

	result := handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ","provider":"tavily"}`, ctx, "c")
	if !strings.Contains(result, "monthly cap") {
		t.Errorf("result = %q, want a cap error", result)
	}
	if called {
		t.Error("Tavily was called despite the cap being reached")
	}
}

func TestHandleYouTubeTranscript_TavilyNotConfigured(t *testing.T) {
	ytTranscriptCache.reset()
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ","provider":"tavily"}`, ctx, "c")
	if !strings.Contains(result, "requires Tavily to be configured") {
		t.Errorf("result = %q", result)
	}
}

func TestHandleYouTubeTranscript_UnknownProvider(t *testing.T) {
	ctx := &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}
	result := handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ","provider":"bing"}`, ctx, "c")
	if !strings.Contains(result, "unknown provider") {
		t.Errorf("result = %q", result)
	}
}

// The hint is what lets the model reach the opt-in provider at all, so
// when it appears matters as much as the provider itself: only for "we
// couldn't get it" failures, only with Tavily configured, never for a
// genuinely caption-less video.
func TestHandleYouTubeTranscript_TavilyHint(t *testing.T) {
	tavilyConfigured := func() *Context {
		return &Context{
			Ctx:    context.Background(),
			Emit:   func(string, map[string]interface{}) {},
			Tavily: tavily.NewClientForTest("k", "http://unused.invalid"),
		}
	}
	const blocked = `
echo "ERROR: Video unavailable" >&2
exit 1
`
	const noCaptions = `
for arg in "$@"; do
	if [ "$arg" = "-j" ]; then
		echo '{"title":"T","subtitles":{},"automatic_captions":{}}'
		exit 0
	fi
done
exit 1
`
	cases := []struct {
		name     string
		script   string
		ctx      *Context
		wantHint bool
	}{
		{"blocked, tavily configured", blocked, tavilyConfigured(), true},
		{"blocked, tavily not configured", blocked, &Context{Ctx: context.Background(), Emit: func(string, map[string]interface{}) {}}, false},
		{"no captions, tavily configured", noCaptions, tavilyConfigured(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFakeYtDlp(t, c.script)
			result := handleYouTubeTranscript(`{"url":"dQw4w9WgXcQ"}`, c.ctx, "c")
			if !strings.HasPrefix(result, "error:") {
				t.Fatalf("result = %q, want an error", result)
			}
			if got := strings.Contains(result, `provider="tavily"`); got != c.wantHint {
				t.Errorf("hint present = %v, want %v (result %q)", got, c.wantHint, result)
			}
		})
	}
}
