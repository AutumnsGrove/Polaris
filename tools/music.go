// music finds real music recommendations grounded in Last.fm's
// community-scrobble similarity data, instead of hoping a web_search hit
// on an album review happens to mention a comparable song — the failure
// mode this tool replaces (see prompt.md/PRODUCT.md's general "ground
// every fact in researched text" principle, applied here to taste, not
// facts). Requires a free Last.fm API key (config.yaml's lastfm.api_key,
// https://www.last.fm/api/account/create) — unlike github_repo's optional
// token, there's no unauthenticated fallback path, so a missing key fails
// every call with a clear error rather than degrading silently.
//
// Three modes:
//
//   - "track": one song → more songs like it. Resolves the given
//     artist/track to Last.fm's canonical entry first (see resolveTrack's
//     doc comment for why that resolution step is load-bearing, not
//     cosmetic), then returns similar tracks plus community tags.
//   - "album_tracks": one album → song-level recommendations. Fans out a
//     similar-tracks lookup across the album's tracklist concurrently and
//     aggregates the results — a candidate recommended by more than one
//     song on the album ranks above one recommended by only one.
//   - "similar_albums": one album → other whole ALBUMS like it. Last.fm
//     has no direct album-to-album similarity endpoint, so this reuses
//     album_tracks' aggregated candidates, resolves each top candidate to
//     ITS OWN album, and re-aggregates at the album level. Noticeably
//     more expensive (an extra API call per candidate track) than the
//     other two modes — the tool description steers the model toward it
//     only when album-level recommendations were actually asked for.
//
// album_tracks and similar_albums exclude the source artist's own catalog
// from their aggregated results — the point there is finding music beyond
// the album already in hand, and Last.fm's own similarity data otherwise
// surfaces plenty of same-artist tracks/albums that would just crowd out
// genuine discoveries. track mode does NOT apply this filter: a same-
// artist result is often exactly right for "more songs like this one"
// (confirmed against live data — Radiohead's own "Airbag" was Last.fm's
// #1 real similar-track result for "Paranoid Android", not a false
// positive to filter out).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"polaris/llm"
)

var musicDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "music",
		Description: "Find real music recommendations grounded in actual listening/similarity data " +
			"(Last.fm), not guesswork or hoping a web search turns up a review that happens to mention " +
			"comparable songs. Use mode \"track\" when the user names one song and wants more like it. Use " +
			"\"album_tracks\" when they want song-level recommendations based on a whole album. Use " +
			"\"similar_albums\" only when they specifically want other ALBUMS similar to a given album, not " +
			"individual songs — this mode makes many more API calls than the other two, so reach for it only " +
			"when album-level recommendations were actually asked for.",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"mode": map[string]interface{}{
					"type":        "string",
					"description": "Which kind of lookup to run.",
					"enum":        []string{"track", "album_tracks", "similar_albums"},
				},
				"artist": map[string]interface{}{
					"type":        "string",
					"description": "The artist's name.",
				},
				"track": map[string]interface{}{
					"type":        "string",
					"description": "The track title. Required for mode \"track\", ignored otherwise.",
				},
				"album": map[string]interface{}{
					"type": "string",
					"description": "The album title. Required for modes \"album_tracks\" and " +
						"\"similar_albums\", ignored otherwise.",
				},
			},
			"required": []string{"mode", "artist"},
		},
	},
}

func init() { Register("music", handleMusic) }

// lastfmBaseURL is a var (not a const) so tests can point it at a fake
// server, same pattern as openMeteoBaseURL/wikipediaAPIBaseURL.
var lastfmBaseURL = "https://ws.audioscrobbler.com/2.0/"

// Tuning constants — see the package doc comment for why similar_albums
// is the expensive mode these mostly exist to bound.
const (
	maxAlbumTracklistSize     = 15 // tracklist fan-out cap — a deluxe-edition album shouldn't balloon the call count
	trackFanoutConcurrency    = 5  // concurrent track.getsimilar calls, polite to Last.fm's free-tier rate limit
	albumResolveConcurrency   = 8  // concurrent track.getinfo calls when resolving similar_albums candidates
	maxSimilarAlbumCandidate  = 40 // how many top aggregated candidate tracks get resolved to albums
	maxTrackResultsShown      = 10
	maxAlbumTrackResultsShown = 15
	maxSimilarAlbumsShown     = 10
)

func handleMusic(argsJSON string, ctx *Context) string {
	var args struct {
		Mode   string `json:"mode"`
		Artist string `json:"artist"`
		Track  string `json:"track"`
		Album  string `json:"album"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "music", nil, "error: "+err.Error())
	}
	if ctx.LastFMAPIKey == "" {
		return emitToolError(ctx, "music", map[string]interface{}{"mode": args.Mode},
			"error: music lookups aren't configured — set lastfm.api_key in config.yaml")
	}
	args.Artist = strings.TrimSpace(args.Artist)
	if args.Artist == "" {
		return emitToolError(ctx, "music", map[string]interface{}{"mode": args.Mode}, "error: artist is required")
	}

	callArgs := map[string]interface{}{"mode": args.Mode, "artist": args.Artist}
	if args.Track != "" {
		callArgs["track"] = args.Track
	}
	if args.Album != "" {
		callArgs["album"] = args.Album
	}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "music", "args": callArgs})

	var result string
	var err error
	switch args.Mode {
	case "track":
		if strings.TrimSpace(args.Track) == "" {
			err = fmt.Errorf(`track is required for mode "track"`)
		} else {
			result, err = lookupSimilarTrack(ctx, args.Artist, args.Track)
		}
	case "album_tracks":
		if strings.TrimSpace(args.Album) == "" {
			err = fmt.Errorf(`album is required for mode "album_tracks"`)
		} else {
			result, err = lookupAlbumTracks(ctx, args.Artist, args.Album)
		}
	case "similar_albums":
		if strings.TrimSpace(args.Album) == "" {
			err = fmt.Errorf(`album is required for mode "similar_albums"`)
		} else {
			result, err = lookupSimilarAlbums(ctx, args.Artist, args.Album)
		}
	default:
		err = fmt.Errorf(`unknown mode %q (must be "track", "album_tracks", or "similar_albums")`, args.Mode)
	}

	if err != nil {
		result = "error: " + err.Error()
		log.Warn("music failed", "mode", args.Mode, "artist", args.Artist, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "music", "result": result})
		return result
	}

	log.Info("music", "mode", args.Mode, "artist", args.Artist)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "music",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
	})
	return result
}

// --- mode "track" ---

func lookupSimilarTrack(ctx *Context, artist, track string) (string, error) {
	resolvedArtist, resolvedTrack, resolvedURL, err := resolveTrack(ctx, artist, track)
	if err != nil {
		return "", err
	}

	similar, err := fetchSimilarTracks(ctx, resolvedArtist, resolvedTrack, maxTrackResultsShown)
	if err != nil {
		return "", err
	}
	// Tags are best-effort enrichment (grounding for the model's "why
	// these fit" reasoning, not the core result) — a failure here
	// shouldn't sink an otherwise-successful similar-tracks lookup.
	tags, tagErr := fetchTrackTags(ctx, resolvedArtist, resolvedTrack, 8)
	if tagErr != nil {
		log.Warn("music: fetching tags failed", "artist", resolvedArtist, "track", resolvedTrack, "err", tagErr)
	}

	citationURL := resolvedURL
	if citationURL == "" {
		citationURL = lastfmTrackURL(resolvedArtist, resolvedTrack)
	}
	ctx.AddCitation(Citation{Title: fmt.Sprintf("Last.fm: %s – %s", resolvedArtist, resolvedTrack), URL: citationURL})

	return formatSimilarTrackResult(resolvedArtist, resolvedTrack, tags, similar), nil
}

func formatSimilarTrackResult(artist, track string, tags []string, similar []lastfmSimilarTrack) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s - %s\n", artist, track)
	if len(tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(tags, ", "))
	}
	sb.WriteString("\nSimilar tracks:\n")
	if len(similar) == 0 {
		sb.WriteString("(no similar tracks found)\n")
	}
	for i, t := range similar {
		fmt.Fprintf(&sb, "%d. %s - %s\n", i+1, t.Artist.Name, t.Name)
	}
	return strings.TrimSpace(sb.String())
}

// --- mode "album_tracks" ---

func lookupAlbumTracks(ctx *Context, artist, album string) (string, error) {
	canonicalArtist, albumURL, tracklist, err := fetchAlbumTracklist(ctx, artist, album)
	if err != nil {
		return "", err
	}

	ranked := rankSimilarTrackCandidates(aggregateSimilarTracks(ctx, canonicalArtist, tracklist))

	ctx.AddCitation(Citation{Title: fmt.Sprintf("Last.fm: %s – %s", canonicalArtist, album), URL: albumURL})
	return formatAlbumTracksResult(canonicalArtist, album, ranked), nil
}

func formatAlbumTracksResult(artist, album string, ranked []*similarTrackCandidate) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Similar tracks to %s by %s:\n\n", album, artist)
	if len(ranked) == 0 {
		sb.WriteString("(no similar tracks found)\n")
	}
	for i, c := range ranked {
		if i >= maxAlbumTrackResultsShown {
			break
		}
		if c.Count > 1 {
			fmt.Fprintf(&sb, "%d. %s - %s (recommended by %d songs on the album)\n", i+1, c.Artist, c.Track, c.Count)
		} else {
			fmt.Fprintf(&sb, "%d. %s - %s\n", i+1, c.Artist, c.Track)
		}
	}
	return strings.TrimSpace(sb.String())
}

// --- mode "similar_albums" ---

func lookupSimilarAlbums(ctx *Context, artist, album string) (string, error) {
	canonicalArtist, albumURL, tracklist, err := fetchAlbumTracklist(ctx, artist, album)
	if err != nil {
		return "", err
	}

	trackCandidates := rankSimilarTrackCandidates(aggregateSimilarTracks(ctx, canonicalArtist, tracklist))
	if len(trackCandidates) > maxSimilarAlbumCandidate {
		trackCandidates = trackCandidates[:maxSimilarAlbumCandidate]
	}

	albumAgg := map[string]*similarAlbumCandidate{}
	var aggMu sync.Mutex
	concurrentMap(albumResolveConcurrency, trackCandidates, func(c *similarTrackCandidate) (struct{}, error) {
		albumArtist, albumTitle, resolvedAlbumURL, err := fetchTrackAlbum(ctx, c.Artist, c.Track)
		if err != nil || albumTitle == "" || strings.EqualFold(albumArtist, canonicalArtist) {
			return struct{}{}, nil
		}
		key := strings.ToLower(albumArtist) + "|" + strings.ToLower(albumTitle)
		aggMu.Lock()
		defer aggMu.Unlock()
		entry, ok := albumAgg[key]
		if !ok {
			entry = &similarAlbumCandidate{Artist: albumArtist, Album: albumTitle, URL: resolvedAlbumURL, Tracks: map[string]bool{}}
			albumAgg[key] = entry
		}
		entry.Score += c.Score
		entry.Tracks[strings.ToLower(c.Artist)+"|"+strings.ToLower(c.Track)] = true
		return struct{}{}, nil
	})

	ranked := make([]*similarAlbumCandidate, 0, len(albumAgg))
	for _, v := range albumAgg {
		ranked = append(ranked, v)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if len(ranked[i].Tracks) != len(ranked[j].Tracks) {
			return len(ranked[i].Tracks) > len(ranked[j].Tracks)
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Artist < ranked[j].Artist
	})

	ctx.AddCitation(Citation{Title: fmt.Sprintf("Last.fm: %s – %s", canonicalArtist, album), URL: albumURL})
	return formatSimilarAlbumsResult(canonicalArtist, album, ranked), nil
}

type similarAlbumCandidate struct {
	Artist string
	Album  string
	URL    string
	Score  float64
	Tracks map[string]bool // distinct contributing candidate tracks — len() is the cross-track agreement count
}

func formatSimilarAlbumsResult(artist, album string, ranked []*similarAlbumCandidate) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Similar albums to %s by %s:\n\n", album, artist)
	if len(ranked) == 0 {
		sb.WriteString("(no similar albums found)\n")
	}
	for i, c := range ranked {
		if i >= maxSimilarAlbumsShown {
			break
		}
		if len(c.Tracks) > 1 {
			fmt.Fprintf(&sb, "%d. %s - %s (%d tracks pointed here independently)\n", i+1, c.Artist, c.Album, len(c.Tracks))
		} else {
			fmt.Fprintf(&sb, "%d. %s - %s\n", i+1, c.Artist, c.Album)
		}
	}
	return strings.TrimSpace(sb.String())
}

// --- shared aggregation ---

type similarTrackCandidate struct {
	Artist string
	Track  string
	Score  float64
	Count  int
}

// aggregateSimilarTracks fans out track.getsimilar across trackTitles
// concurrently and aggregates by (artist, track), excluding sourceArtist's
// own catalog — a candidate recommended by more than one source track
// gets both a higher Count and a summed Score, which rankSimilarTrackCandidates
// uses as (count, score) — cross-track agreement beats one track's high
// match score, confirmed against real data before this was built (a
// candidate independently surfaced by two different album tracks is a
// stronger signal than one track's single highest-match result).
func aggregateSimilarTracks(ctx *Context, sourceArtist string, trackTitles []string) map[string]*similarTrackCandidate {
	perTrack := concurrentMap(trackFanoutConcurrency, trackTitles, func(title string) ([]lastfmSimilarTrack, error) {
		return fetchSimilarTracks(ctx, sourceArtist, title, maxTrackResultsShown)
	})

	agg := map[string]*similarTrackCandidate{}
	for _, sims := range perTrack {
		for _, t := range sims {
			if strings.EqualFold(t.Artist.Name, sourceArtist) {
				continue
			}
			key := strings.ToLower(t.Artist.Name) + "|" + strings.ToLower(t.Name)
			entry, ok := agg[key]
			if !ok {
				entry = &similarTrackCandidate{Artist: t.Artist.Name, Track: t.Name}
				agg[key] = entry
			}
			entry.Score += t.Match
			entry.Count++
		}
	}
	return agg
}

func rankSimilarTrackCandidates(agg map[string]*similarTrackCandidate) []*similarTrackCandidate {
	ranked := make([]*similarTrackCandidate, 0, len(agg))
	for _, v := range agg {
		ranked = append(ranked, v)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Artist < ranked[j].Artist
	})
	return ranked
}

// concurrentMap runs fn over items with at most maxWorkers goroutines in
// flight, returning results in the same order as items. A failed item
// (fn returns a non-nil error) contributes its type's zero value rather
// than aborting the batch — callers filter/skip zero values themselves,
// since "one candidate's lookup failed" shouldn't sink an aggregate
// spanning a dozen others that succeeded.
func concurrentMap[T any, R any](maxWorkers int, items []T, fn func(T) (R, error)) []R {
	results := make([]R, len(items))
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, item T) {
			defer wg.Done()
			defer func() { <-sem }()
			if r, err := fn(item); err == nil {
				results[i] = r
			}
		}(i, item)
	}
	wg.Wait()
	return results
}

// --- Last.fm API calls ---

// lastfmError is the shape of a Last.fm API-level error — these come back
// as HTTP 200 with an "error" field, not a 4xx/5xx status, so every call
// site checks for this after a successful HTTP round trip rather than
// relying on the status code alone.
type lastfmError struct {
	Error   int    `json:"error"`
	Message string `json:"message"`
}

func lastfmGet(ctx context.Context, apiKey string, params url.Values) ([]byte, error) {
	params.Set("api_key", apiKey)
	params.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, "GET", lastfmBaseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Polaris/1.0 (personal search assistant)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("last.fm status %d", resp.StatusCode)
	}
	var lfErr lastfmError
	if json.Unmarshal(body, &lfErr) == nil && lfErr.Error != 0 {
		return nil, fmt.Errorf("last.fm: %s", lfErr.Message)
	}
	return body, nil
}

// resolveTrack turns a user-supplied artist/track into Last.fm's
// canonical entry via track.search, picking the same-artist match with
// the most listeners. This is load-bearing, not cosmetic: track.getsimilar
// matches on the exact title string, and a real song can exist on Last.fm
// under several near-duplicate title variants with wildly different
// scrobble counts (e.g. a bare title with 767 listeners vs. the real
// "(feat. X)" version with 229,000) — calling getsimilar on the wrong
// variant silently returns an empty/thin result for a song that actually
// has plenty of similarity data, indistinguishable from a genuine gap.
// Falls back to the caller's original strings verbatim if search finds no
// same-artist match at all, rather than failing outright — getsimilar
// might still resolve it directly.
func resolveTrack(ctx *Context, artist, track string) (resolvedArtist, resolvedTrack, resolvedURL string, err error) {
	body, err := lastfmGet(ctx.Ctx, ctx.LastFMAPIKey, url.Values{
		"method": {"track.search"},
		"track":  {track},
		"artist": {artist},
		"limit":  {"15"},
	})
	if err != nil {
		return "", "", "", fmt.Errorf("resolving track: %w", err)
	}

	var resp struct {
		Results struct {
			TrackMatches struct {
				Track []struct {
					Name string `json:"name"`
					// Artist/Listeners/URL are quoted strings in this
					// endpoint's response — Last.fm's API is inconsistent
					// about numeric field typing across endpoints (confirmed
					// against the live API; track.getsimilar's "match"/
					// "playcount" are real JSON numbers, this "listeners"
					// isn't), so Listeners is parsed with strconv, not
					// unmarshaled as a number.
					Artist    string `json:"artist"`
					Listeners string `json:"listeners"`
					URL       string `json:"url"`
				} `json:"track"`
			} `json:"trackmatches"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", "", fmt.Errorf("parsing last.fm search response: %w", err)
	}

	bestListeners := -1
	for _, m := range resp.Results.TrackMatches.Track {
		if !strings.EqualFold(m.Artist, artist) {
			continue
		}
		listeners, _ := strconv.Atoi(m.Listeners)
		if listeners > bestListeners {
			bestListeners = listeners
			resolvedArtist, resolvedTrack, resolvedURL = m.Artist, m.Name, m.URL
		}
	}
	if resolvedTrack == "" {
		return artist, track, "", nil
	}
	return resolvedArtist, resolvedTrack, resolvedURL, nil
}

type lastfmSimilarTrack struct {
	Name   string  `json:"name"`
	Match  float64 `json:"match"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
}

func fetchSimilarTracks(ctx *Context, artist, track string, limit int) ([]lastfmSimilarTrack, error) {
	body, err := lastfmGet(ctx.Ctx, ctx.LastFMAPIKey, url.Values{
		"method": {"track.getsimilar"},
		"artist": {artist},
		"track":  {track},
		"limit":  {strconv.Itoa(limit)},
	})
	if err != nil {
		return nil, fmt.Errorf("fetching similar tracks for %q: %w", track, err)
	}
	var resp struct {
		SimilarTracks struct {
			Track []lastfmSimilarTrack `json:"track"`
		} `json:"similartracks"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing similar-tracks response: %w", err)
	}
	return resp.SimilarTracks.Track, nil
}

func fetchTrackTags(ctx *Context, artist, track string, limit int) ([]string, error) {
	body, err := lastfmGet(ctx.Ctx, ctx.LastFMAPIKey, url.Values{
		"method": {"track.gettoptags"},
		"artist": {artist},
		"track":  {track},
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		TopTags struct {
			Tag []struct {
				Name string `json:"name"`
			} `json:"tag"`
		} `json:"toptags"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	names := make([]string, 0, limit)
	for i, t := range resp.TopTags.Tag {
		if i >= limit {
			break
		}
		names = append(names, t.Name)
	}
	return names, nil
}

// fetchAlbumTracklist resolves an album's canonical artist credit and
// Last.fm URL, plus its tracklist capped at maxAlbumTracklistSize.
func fetchAlbumTracklist(ctx *Context, artist, album string) (canonicalArtist, albumURL string, tracks []string, err error) {
	body, err := lastfmGet(ctx.Ctx, ctx.LastFMAPIKey, url.Values{
		"method": {"album.getinfo"},
		"artist": {artist},
		"album":  {album},
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("fetching album: %w", err)
	}
	var resp struct {
		Album struct {
			Name   string `json:"name"`
			Artist string `json:"artist"`
			URL    string `json:"url"`
			Tracks struct {
				Track []struct {
					Name string `json:"name"`
				} `json:"track"`
			} `json:"tracks"`
		} `json:"album"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", nil, fmt.Errorf("parsing album response: %w", err)
	}
	if resp.Album.Name == "" {
		return "", "", nil, fmt.Errorf("no album found for %q by %q", album, artist)
	}

	names := make([]string, 0, len(resp.Album.Tracks.Track))
	for _, t := range resp.Album.Tracks.Track {
		names = append(names, t.Name)
	}
	if len(names) == 0 {
		return "", "", nil, fmt.Errorf("%q by %q has no tracklist on last.fm", album, artist)
	}
	if len(names) > maxAlbumTracklistSize {
		names = names[:maxAlbumTracklistSize]
	}
	return resp.Album.Artist, resp.Album.URL, names, nil
}

// fetchTrackAlbum resolves which album a candidate track belongs to —
// the extra call similar_albums mode needs since track.getsimilar's
// results don't carry album info, and Last.fm has no album.getsimilar
// endpoint at all (confirmed against the live API: "Invalid Method").
func fetchTrackAlbum(ctx *Context, artist, track string) (albumArtist, albumTitle, albumURL string, err error) {
	body, err := lastfmGet(ctx.Ctx, ctx.LastFMAPIKey, url.Values{
		"method": {"track.getinfo"},
		"artist": {artist},
		"track":  {track},
	})
	if err != nil {
		return "", "", "", err
	}
	var resp struct {
		Track struct {
			Album *struct {
				Artist string `json:"artist"`
				Title  string `json:"title"`
				URL    string `json:"url"`
			} `json:"album"`
		} `json:"track"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", "", err
	}
	if resp.Track.Album == nil {
		return "", "", "", nil
	}
	return resp.Track.Album.Artist, resp.Track.Album.Title, resp.Track.Album.URL, nil
}

// lastfmTrackURL builds a track's canonical Last.fm page URL — used only
// as a citation fallback on the rare path where resolveTrack found no
// exact-artist search match (so has no URL from the API response itself).
func lastfmTrackURL(artist, track string) string {
	return fmt.Sprintf("https://www.last.fm/music/%s/_/%s", url.PathEscape(artist), url.PathEscape(track))
}
