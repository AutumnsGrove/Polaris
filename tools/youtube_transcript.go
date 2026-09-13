// youtube_transcript fetches a YouTube video's caption track and returns
// it as plain text, for research questions that link to a video instead
// of an article. No official API key needed and no OAuth: YouTube's Data
// API v3 caption download endpoint only works for videos you own, so
// (like every other "free youtube transcript" tool) this reads the same
// caption data the watch page itself loads.
//
// This used to do that by hand — fetch the watch page, pull the caption
// track list out of its embedded ytInitialPlayerResponse JSON, then GET
// that track's timedtext endpoint directly with a plain net/http client.
// That stopped working: YouTube now gates the timedtext endpoint behind a
// "PO Token" (proof-of-origin) check for requests that don't look like a
// real browser session, and instead of an error it returns a *valid*
// `200 OK` with `Content-Length: 0` — no signal to react to, just silent
// empty output, which surfaced as a confusing "unexpected end of JSON
// input" error with no indication anything upstream had changed.
// Confirmed live from two unrelated networks (the potato and a laptop on
// a residential connection) that this is a global YouTube change, not an
// IP-reputation problem specific to either host.
//
// Shelling out to yt-dlp instead of hand-rolling the HTTP calls is a
// deliberate dependency tradeoff: yt-dlp is actively maintained against
// exactly this kind of anti-bot change (it rotates through several
// client-impersonation strategies YouTube hasn't closed off yet), where a
// bespoke implementation here would need to be re-fixed by hand every
// time YouTube adjusts its defenses again. This is the first external
// binary dependency anywhere in tools/ — see README's "Docker install"
// and bare-metal setup instructions for how it gets installed on either
// deployment model. checkYtDlpAvailable degrades this to a clear,
// actionable error instead of a crash when it's missing, same pattern as
// the Books tool's Hardcover-token-expiry fallback.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"polaris/llm"
)

var youtubeTranscriptDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "youtube_transcript",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/youtube_transcript.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "A YouTube video URL or bare 11-character video ID.",
				},
			},
			"required": []string{"url"},
		},
	},
}

func init() { Register("youtube_transcript", handleYouTubeTranscript) }

func handleYouTubeTranscript(argsJSON string, ctx *Context, callID string) string {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "youtube_transcript", nil, "error: "+err.Error(), callID)
	}
	if args.URL == "" {
		return emitToolError(ctx, "youtube_transcript", map[string]interface{}{"url": args.URL}, "error: url is required", callID)
	}

	videoID, err := extractYouTubeID(args.URL)
	if err != nil {
		ctx.Emit("tool_call", map[string]interface{}{"tool": "youtube_transcript", "args": map[string]interface{}{"url": args.URL}, "call_id": callID})
		result := "error: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "youtube_transcript", "result": result, "call_id": callID})
		return result
	}
	watchURL := youtubeWatchBaseURL + videoID

	ctx.Emit("tool_call", map[string]interface{}{
		"tool":    "youtube_transcript",
		"args":    map[string]interface{}{"url": watchURL},
		"call_id": callID,
	})

	title, transcript, err := fetchYouTubeTranscript(ctx.Ctx, videoID)
	if err != nil {
		log.Warn("youtube_transcript failed", "video_id", videoID, "err", err)
		result := "error: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "youtube_transcript", "result": result, "call_id": callID})
		return result
	}

	log.Info("youtube_transcript", "video_id", videoID, "title", title, "chars", len(transcript))
	ctx.AddCitation(Citation{Title: title, URL: watchURL})
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "youtube_transcript",
		"result":    transcript,
		"citations": ctx.CitationsSnapshot(),
		"call_id":   callID,
	})
	return transcript
}

// youtubeWatchBaseURL builds the URL used for the citation this tool
// attaches — not fetched directly anymore (yt-dlp resolves the video
// itself from the bare ID), but kept as the canonical link shown to the
// user, same as before.
const youtubeWatchBaseURL = "https://www.youtube.com/watch?v="

// youtubeIDPattern matches a bare video ID on its own, and is also used to
// validate an ID pulled out of a URL — YouTube IDs are always exactly 11
// URL-safe base64-alphabet characters.
var youtubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// youtubePathIDPattern catches every URL shape that puts the ID directly
// in the path rather than a query param: youtu.be/<id>, /shorts/<id>,
// /embed/<id>, /live/<id>.
var youtubePathIDPattern = regexp.MustCompile(`(?:youtu\.be/|/shorts/|/embed/|/live/)([A-Za-z0-9_-]{11})`)

// extractYouTubeID accepts a full YouTube URL in any of its common forms,
// or a bare 11-character video ID, and returns just the ID.
func extractYouTubeID(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if youtubeIDPattern.MatchString(trimmed) {
		return trimmed, nil
	}

	if u, err := url.Parse(trimmed); err == nil {
		if v := u.Query().Get("v"); youtubeIDPattern.MatchString(v) {
			return v, nil
		}
	}

	if m := youtubePathIDPattern.FindStringSubmatch(trimmed); m != nil {
		return m[1], nil
	}

	return "", fmt.Errorf("couldn't find a YouTube video ID in %q", input)
}

// ytDlpPath is a var (not a const) so tests can point it at a stub script
// instead of a real yt-dlp binary — same pattern as youtubeWatchBaseURL
// used to be for an httptest server.
var ytDlpPath = "yt-dlp"

// ytDlpTimeout bounds both yt-dlp invocations below. 30s is generous for
// two small metadata/subtitle fetches — this isn't downloading any video
// or audio (--skip-download) — but the potato's weak CPU plus a possibly
// slow network makes a short fixed timeout the wrong call here.
const ytDlpTimeout = 30 * time.Second

// ytDlpInfo is the subset of `yt-dlp -j`'s dump-json output this needs.
// subtitles is manually-uploaded tracks; automatic_captions is YouTube's
// own ASR output plus, for popular videos, dozens of auto-*translated*
// languages — deliberately not requested in bulk (see pickYtDlpLanguage),
// since downloading every one of those would be slow and mostly useless.
type ytDlpInfo struct {
	Title             string                      `json:"title"`
	Subtitles         map[string][]ytDlpSubFormat `json:"subtitles"`
	AutomaticCaptions map[string][]ytDlpSubFormat `json:"automatic_captions"`
}

type ytDlpSubFormat struct {
	Ext string `json:"ext"`
}

// fetchYouTubeTranscript resolves a video's metadata and caption-track
// listing in one yt-dlp call, picks the best available track the same way
// the old hand-rolled version did (prefer a human-uploaded English track,
// then any English track, then just something — a transcript in the
// video's own language beats no transcript at all), then makes a second,
// narrowly-targeted yt-dlp call to actually fetch just that one track.
func fetchYouTubeTranscript(ctx context.Context, videoID string) (title, transcript string, err error) {
	if err := checkYtDlpAvailable(ctx); err != nil {
		return "", "", err
	}

	info, err := fetchYtDlpInfo(ctx, videoID)
	if err != nil {
		return "", "", err
	}

	lang, isAuto, ok := pickYtDlpLanguage(info)
	if !ok {
		return info.Title, "", fmt.Errorf("no captions available for this video")
	}

	transcriptBody, err := fetchYtDlpSubtitle(ctx, videoID, lang, isAuto)
	if err != nil {
		return info.Title, "", err
	}

	text, err := parseJSON3Transcript(transcriptBody)
	if err != nil {
		return info.Title, "", err
	}

	return info.Title, collapseWhitespace(text), nil
}

// checkYtDlpAvailable gives a clear, actionable error the first time this
// tool is used on a deployment that never installed yt-dlp, instead of
// exec.CommandContext's raw "executable file not found in $PATH" — which
// says nothing about *why* a video-transcript tool needs an external
// binary at all or what to do about it. See this file's package doc
// comment for why yt-dlp is required in the first place.
func checkYtDlpAvailable(ctx context.Context) error {
	if _, err := exec.LookPath(ytDlpPath); err != nil {
		return fmt.Errorf("yt-dlp is not installed (or not on PATH) — required for youtube_transcript since YouTube blocks the old direct-fetch approach; see README's setup instructions for installing it on this deployment model")
	}
	return nil
}

// fetchYtDlpInfo runs `yt-dlp -j --skip-download` to get a video's title
// and full caption-track listing without downloading anything — a fast,
// simulate-mode-only call (no --no-simulate needed: nothing here writes
// a file).
func fetchYtDlpInfo(ctx context.Context, videoID string) (*ytDlpInfo, error) {
	runCtx, cancel := context.WithTimeout(ctx, ytDlpTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ytDlpPath, "--skip-download", "-j", youtubeWatchBaseURL+videoID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("yt-dlp couldn't fetch this video's info: %s", firstNonEmptyLine(stderr.String(), err.Error()))
	}

	var info ytDlpInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("parsing yt-dlp's video info: %w", err)
	}
	return &info, nil
}

// pickYtDlpLanguage mirrors the old pickCaptionTrack's priority order —
// human-uploaded English, then auto-generated English, then just
// whatever's available — but over yt-dlp's subtitles/automatic_captions
// maps instead of YouTube's own caption-track list. Go map iteration
// order is random, so the "just whatever's available" fallback sorts
// language codes first for a deterministic (if arbitrary) pick, rather
// than the old version's YouTube-list-order pick — a difference without
// a meaningful consequence, since neither ordering carries real meaning
// for a non-English fallback.
func pickYtDlpLanguage(info *ytDlpInfo) (lang string, isAuto bool, ok bool) {
	for _, code := range []string{"en", "en-US", "en-GB", "en-orig", "en-US-orig"} {
		if _, found := info.Subtitles[code]; found {
			return code, false, true
		}
	}
	for _, code := range []string{"en", "en-US", "en-GB", "en-orig", "en-US-orig"} {
		if _, found := info.AutomaticCaptions[code]; found {
			return code, true, true
		}
	}
	if lang, ok := firstSortedKey(info.Subtitles); ok {
		return lang, false, true
	}
	if lang, ok := firstSortedKey(info.AutomaticCaptions); ok {
		return lang, true, true
	}
	return "", false, false
}

func firstSortedKey(m map[string][]ytDlpSubFormat) (string, bool) {
	if len(m) == 0 {
		return "", false
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0], true
}

// fetchYtDlpSubtitle downloads exactly one caption track to a temp file
// and returns its raw json3 body. A second yt-dlp invocation (rather than
// asking for the subtitle in the same call as fetchYtDlpInfo above) keeps
// the common case cheap: most videos have far more automatic_captions
// entries (every YouTube auto-translate language, easily 100+) than
// anyone would ever want downloaded, so this always requests exactly the
// one language pickYtDlpLanguage already chose instead of "all".
func fetchYtDlpSubtitle(ctx context.Context, videoID, lang string, isAuto bool) (string, error) {
	tmpDir, err := os.MkdirTemp("", "polaris-yt-transcript-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	runCtx, cancel := context.WithTimeout(ctx, ytDlpTimeout)
	defer cancel()

	args := []string{
		"--skip-download",
		// --no-simulate: --print's own presence elsewhere in this codebase's
		// yt-dlp invocations is unrelated here, but --skip-download alone
		// still implies simulate mode for the subtitle writers specifically
		// — confirmed live, omitting this flag silently logs "Downloading
		// subtitles: ..." and exits 0 without ever writing the file.
		"--no-simulate",
		"--sub-langs", lang,
		"--sub-format", "json3",
		"-o", filepath.Join(tmpDir, "%(id)s"),
	}
	if isAuto {
		args = append(args, "--write-auto-sub")
	} else {
		args = append(args, "--write-subs")
	}
	args = append(args, youtubeWatchBaseURL+videoID)

	cmd := exec.CommandContext(runCtx, ytDlpPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("yt-dlp couldn't fetch the transcript: %s", firstNonEmptyLine(stderr.String(), err.Error()))
	}

	matches, err := filepath.Glob(filepath.Join(tmpDir, "*.json3"))
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("yt-dlp reported success but wrote no subtitle file")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return "", fmt.Errorf("reading transcript file: %w", err)
	}
	return string(data), nil
}

// firstNonEmptyLine pulls the last non-empty line out of yt-dlp's stderr
// (its actual "ERROR: ..." line, if any — everything before it is usually
// debug/warning noise) to surface as this tool's own error, falling back
// to fallback when stderr had nothing useful.
func firstNonEmptyLine(stderrOutput, fallback string) string {
	lines := strings.Split(strings.TrimSpace(stderrOutput), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return fallback
}

// json3Transcript is the shape of YouTube's timedtext endpoint when
// requested with &fmt=json3 — much simpler to parse than the default XML
// response. Events with no Segs are non-text cue points (e.g. position
// markers) and are simply skipped.
type json3Transcript struct {
	Events []struct {
		Segs []struct {
			UTF8 string `json:"utf8"`
		} `json:"segs"`
	} `json:"events"`
}

func parseJSON3Transcript(body string) (string, error) {
	var t json3Transcript
	if err := json.Unmarshal([]byte(body), &t); err != nil {
		return "", fmt.Errorf("parsing transcript: %w", err)
	}
	var sb strings.Builder
	for _, evt := range t.Events {
		for _, seg := range evt.Segs {
			sb.WriteString(seg.UTF8)
		}
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("transcript track was empty")
	}
	return sb.String(), nil
}
