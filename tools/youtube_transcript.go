// youtube_transcript fetches a YouTube video's caption track and returns
// it as plain text, for research questions that link to a video instead
// of an article. No official API key needed and no OAuth: YouTube's Data
// API v3 caption download endpoint only works for videos you own, so
// (like every other "free youtube transcript" tool) this reads the same
// caption data the watch page itself loads — fetch the normal watch page,
// pull the caption track list out of its embedded ytInitialPlayerResponse
// JSON, then fetch that track's timedtext endpoint directly. No headless
// browser, same "stay light on the potato" constraint as web_read.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"polaris/llm"
)

var youtubeTranscriptDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "youtube_transcript",
		Description: "Fetch the transcript of a YouTube video, given its URL (youtube.com/watch, youtu.be, " +
			"shorts, etc.) or bare video ID. Use when the user shares a YouTube link or a web_search result " +
			"is a YouTube video worth reading. Fails if the video has no captions available.",
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

func handleYouTubeTranscript(argsJSON string, ctx *Context) string {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "youtube_transcript", nil, "error: "+err.Error())
	}
	if args.URL == "" {
		return emitToolError(ctx, "youtube_transcript", map[string]interface{}{"url": args.URL}, "error: url is required")
	}

	videoID, err := extractYouTubeID(args.URL)
	if err != nil {
		ctx.Emit("tool_call", map[string]interface{}{"tool": "youtube_transcript", "args": map[string]interface{}{"url": args.URL}})
		result := "error: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "youtube_transcript", "result": result})
		return result
	}
	watchURL := youtubeWatchBaseURL + videoID

	ctx.Emit("tool_call", map[string]interface{}{
		"tool": "youtube_transcript",
		"args": map[string]interface{}{"url": watchURL},
	})

	title, transcript, err := fetchYouTubeTranscript(ctx.Ctx, videoID)
	if err != nil {
		log.Warn("youtube_transcript failed", "video_id", videoID, "err", err)
		result := "error: " + err.Error()
		ctx.Emit("tool_result", map[string]interface{}{"tool": "youtube_transcript", "result": result})
		return result
	}

	log.Info("youtube_transcript", "video_id", videoID, "title", title, "chars", len(transcript))
	ctx.AddCitation(Citation{Title: title, URL: watchURL})
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "youtube_transcript",
		"result":    transcript,
		"citations": ctx.CitationsSnapshot(),
	})
	return transcript
}

// youtubeWatchBaseURL is a var (not a const) so tests can point it at an
// httptest server instead of the real youtube.com, same pattern as
// web_read.go's waybackAvailabilityAPI.
var youtubeWatchBaseURL = "https://www.youtube.com/watch?v="

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

// captionTrack is the subset of ytInitialPlayerResponse's caption track
// list this needs — YouTube's actual JSON has many more fields per track.
type captionTrack struct {
	BaseURL      string `json:"baseUrl"`
	LanguageCode string `json:"languageCode"`
	Kind         string `json:"kind"` // "asr" = auto-generated; empty = uploaded/human
}

// playerResponse is the subset of the embedded ytInitialPlayerResponse
// object this needs — decoded with encoding/json.Decoder rather than a
// regex capture, so it naturally stops at the end of the JSON value
// regardless of what follows it in the surrounding <script> tag (a
// trailing ";", more statements, etc.) instead of needing a fragile
// balanced-brace or non-greedy pattern to find the boundary itself.
type playerResponse struct {
	VideoDetails struct {
		Title string `json:"title"`
	} `json:"videoDetails"`
	Captions struct {
		PlayerCaptionsTracklistRenderer struct {
			CaptionTracks []captionTrack `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	} `json:"captions"`
}

// playerResponseMarker is where ytInitialPlayerResponse's JSON value
// starts in the watch page's raw HTML — everything before "{" is the
// "var ytInitialPlayerResponse = " (or equivalent) assignment prefix,
// which varies slightly across YouTube's rollouts and isn't worth
// matching exactly.
const playerResponseMarker = "ytInitialPlayerResponse"

// fetchYouTubeTranscript downloads the watch page, extracts the caption
// track list, picks the best available track, and fetches+flattens its
// timedtext JSON into plain text.
func fetchYouTubeTranscript(ctx context.Context, videoID string) (title, transcript string, err error) {
	client := &http.Client{Timeout: 15 * time.Second}

	body, err := httpGetBody(ctx, client, youtubeWatchBaseURL+videoID)
	if err != nil {
		return "", "", fmt.Errorf("fetching watch page: %w", err)
	}

	idx := strings.Index(body, playerResponseMarker)
	if idx == -1 {
		return "", "", fmt.Errorf("video unavailable or page structure changed (no player response found)")
	}
	jsonStart := strings.IndexByte(body[idx:], '{')
	if jsonStart == -1 {
		return "", "", fmt.Errorf("video unavailable or page structure changed (no player response found)")
	}

	var pr playerResponse
	dec := json.NewDecoder(strings.NewReader(body[idx+jsonStart:]))
	if err := dec.Decode(&pr); err != nil {
		return "", "", fmt.Errorf("parsing player response: %w", err)
	}

	tracks := pr.Captions.PlayerCaptionsTracklistRenderer.CaptionTracks
	track := pickCaptionTrack(tracks)
	if track == nil {
		return pr.VideoDetails.Title, "", fmt.Errorf("no captions available for this video")
	}

	transcriptURL, err := addQueryParam(track.BaseURL, "fmt", "json3")
	if err != nil {
		return pr.VideoDetails.Title, "", fmt.Errorf("parsing caption track url: %w", err)
	}
	transcriptBody, err := httpGetBody(ctx, client, transcriptURL)
	if err != nil {
		return pr.VideoDetails.Title, "", fmt.Errorf("fetching transcript: %w", err)
	}

	text, err := parseJSON3Transcript(transcriptBody)
	if err != nil {
		return pr.VideoDetails.Title, "", err
	}

	return pr.VideoDetails.Title, collapseWhitespace(text), nil
}

// pickCaptionTrack prefers a human-uploaded English track, then any
// English track (including auto-generated/"asr"), then just the first
// track available — better to return *a* transcript in whatever language
// the video actually has than to fail outright for a non-English video.
func pickCaptionTrack(tracks []captionTrack) *captionTrack {
	if len(tracks) == 0 {
		return nil
	}
	for i, t := range tracks {
		if t.LanguageCode == "en" && t.Kind != "asr" {
			return &tracks[i]
		}
	}
	for i, t := range tracks {
		if t.LanguageCode == "en" {
			return &tracks[i]
		}
	}
	return &tracks[0]
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

// addQueryParam adds a query parameter via net/url rather than raw string
// concatenation ("baseUrl+\"&fmt=json3\"") — the caption track's baseUrl
// happens to always carry existing query params (lang, v, etc.) on real
// YouTube responses, but building the query string properly means this
// doesn't silently break if that ever isn't true (e.g. producing
// ".../timedtext&fmt=json3" with no "?", which 404s).
func addQueryParam(rawURL, key, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// httpGetBody is a small shared helper for the two plain-GET fetches
// above — same User-Agent as web_read.go's fetchAndExtract, since
// YouTube's watch page (unlike the timedtext endpoint) does vary its
// response based on it.
func httpGetBody(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Polaris/1.0; +https://github.com/AutumnsGrove/Polaris)")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("url returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
