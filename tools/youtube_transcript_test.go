package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
	t.Cleanup(func() { ytDlpPath = original })
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
