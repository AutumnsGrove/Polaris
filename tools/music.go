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
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"polaris/llm"
)

var musicDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "music",
		// Description is set in init() from tools/descriptions/music.yaml.
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

func init() {
	Register("music", handleMusic)
	musicDef.Function.Description = catalogDescription("music")
}

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

	// descriptionTruncateLen bounds how much of a wiki summary/overview
	// gets shown per recommendation line — fine to show in full once for
	// the source track/album/title, but stacked under every one of up to
	// 15 candidates it would swamp the actual result. 500 rather than a
	// tighter bound because most wiki summaries and TMDB overviews run
	// 1-3 sentences (150-400 chars) — a lower cap was cutting most of them
	// off mid-sentence, which read as more garbled than useful. Shared
	// with books.go's and movies.go's equivalent per-candidate truncation
	// (see truncateText).
	descriptionTruncateLen = 500
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
		"cards":     ctx.CardsSnapshot(),
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
	// Tags and description are both best-effort enrichment (grounding for
	// the model's "why these fit" reasoning, not the core result) — a
	// failure in either shouldn't sink an otherwise-successful
	// similar-tracks lookup.
	tags, tagErr := fetchTrackTags(ctx, resolvedArtist, resolvedTrack, 8)
	if tagErr != nil {
		log.Warn("music: fetching tags failed", "artist", resolvedArtist, "track", resolvedTrack, "err", tagErr)
	}
	description, descErr := fetchTrackWiki(ctx, resolvedArtist, resolvedTrack)
	if descErr != nil {
		log.Warn("music: fetching description failed", "artist", resolvedArtist, "track", resolvedTrack, "err", descErr)
	}

	citationURL := resolvedURL
	if citationURL == "" {
		citationURL = lastfmTrackURL(resolvedArtist, resolvedTrack)
	}
	ctx.AddCitation(Citation{
		Title:    fmt.Sprintf("Last.fm: %s – %s", resolvedArtist, resolvedTrack),
		URL:      citationURL,
		ImageURL: fetchDeezerCoverArt(ctx, "track", resolvedArtist, resolvedTrack),
	})

	// One card + one description per recommendation actually shown to the
	// user (same set as formatSimilarTrackResult's list, not some larger
	// internal set) — concurrent Deezer/Last.fm lookups so this stays fast
	// regardless of how many results there are, same shape as
	// aggregateSimilarTracks' fan-out.
	descriptions := make([]string, len(similar))
	for i, rec := range concurrentMap(trackFanoutConcurrency, similar, func(t lastfmSimilarTrack) (trackRecommendation, error) {
		cardURL := t.URL
		if cardURL == "" {
			cardURL = lastfmTrackURL(t.Artist.Name, t.Name)
		}
		wiki, _ := fetchTrackWiki(ctx, t.Artist.Name, t.Name)
		return trackRecommendation{
			Card: Card{
				Title:    t.Name,
				Subtitle: t.Artist.Name,
				ImageURL: fetchDeezerCoverArt(ctx, "track", t.Artist.Name, t.Name),
				URL:      cardURL,
			},
			Description: wiki,
		}, nil
	}) {
		ctx.AddCard(rec.Card)
		descriptions[i] = rec.Description
	}

	return formatSimilarTrackResult(resolvedArtist, resolvedTrack, tags, description, similar, descriptions), nil
}

// trackRecommendation pairs a candidate track's Card with its best-effort
// wiki description — concurrentMap returns one R per item, so a card-only
// return type would need a second, separately-ordered fan-out to also carry
// descriptions; bundling both in one round trip's result keeps them
// correctly paired by index without a second concurrent pass.
type trackRecommendation struct {
	Card        Card
	Description string
}

func formatSimilarTrackResult(artist, track string, tags []string, description string, similar []lastfmSimilarTrack, descriptions []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s - %s\n", artist, track)
	if len(tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(tags, ", "))
	}
	if description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", description)
	}
	sb.WriteString("\nSimilar tracks:\n")
	if len(similar) == 0 {
		sb.WriteString("(no similar tracks found)\n")
	}
	for i, t := range similar {
		fmt.Fprintf(&sb, "%d. %s - %s", i+1, t.Artist.Name, t.Name)
		if i < len(descriptions) && descriptions[i] != "" {
			fmt.Fprintf(&sb, " — %s", truncateText(descriptions[i], descriptionTruncateLen))
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// --- mode "album_tracks" ---

func lookupAlbumTracks(ctx *Context, artist, album string) (string, error) {
	canonicalArtist, albumURL, description, tracklist, err := fetchAlbumTracklist(ctx, artist, album)
	if err != nil {
		return "", err
	}

	ranked := rankSimilarTrackCandidates(aggregateSimilarTracks(ctx, canonicalArtist, tracklist))
	if len(ranked) > maxAlbumTrackResultsShown {
		ranked = ranked[:maxAlbumTrackResultsShown]
	}

	ctx.AddCitation(Citation{
		Title:    fmt.Sprintf("Last.fm: %s – %s", canonicalArtist, album),
		URL:      albumURL,
		ImageURL: fetchDeezerCoverArt(ctx, "album", canonicalArtist, album),
	})

	// One card + one description per recommendation actually shown — ranked
	// is already capped above, so this and the text list always describe
	// the same set. Concurrent Deezer/Last.fm lookups, same shape as
	// lookupSimilarTrack's.
	descriptions := make([]string, len(ranked))
	for i, rec := range concurrentMap(trackFanoutConcurrency, ranked, func(c *similarTrackCandidate) (trackRecommendation, error) {
		wiki, _ := fetchTrackWiki(ctx, c.Artist, c.Track)
		return trackRecommendation{
			Card: Card{
				Title:    c.Track,
				Subtitle: c.Artist,
				ImageURL: fetchDeezerCoverArt(ctx, "track", c.Artist, c.Track),
				URL:      lastfmTrackURL(c.Artist, c.Track),
			},
			Description: wiki,
		}, nil
	}) {
		ctx.AddCard(rec.Card)
		descriptions[i] = rec.Description
	}

	return formatAlbumTracksResult(canonicalArtist, album, description, ranked, descriptions), nil
}

func formatAlbumTracksResult(artist, album, description string, ranked []*similarTrackCandidate, descriptions []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Similar tracks to %s by %s:\n", album, artist)
	if description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", description)
	}
	sb.WriteString("\n")
	if len(ranked) == 0 {
		sb.WriteString("(no similar tracks found)\n")
	}
	for i, c := range ranked {
		fmt.Fprintf(&sb, "%d. %s - %s", i+1, c.Artist, c.Track)
		if c.Count > 1 {
			fmt.Fprintf(&sb, " (recommended by %d songs on the album)", c.Count)
		}
		if i < len(descriptions) && descriptions[i] != "" {
			fmt.Fprintf(&sb, " — %s", truncateText(descriptions[i], descriptionTruncateLen))
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// --- mode "similar_albums" ---

func lookupSimilarAlbums(ctx *Context, artist, album string) (string, error) {
	canonicalArtist, albumURL, description, tracklist, err := fetchAlbumTracklist(ctx, artist, album)
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

	if len(ranked) > maxSimilarAlbumsShown {
		ranked = ranked[:maxSimilarAlbumsShown]
	}

	ctx.AddCitation(Citation{
		Title:    fmt.Sprintf("Last.fm: %s – %s", canonicalArtist, album),
		URL:      albumURL,
		ImageURL: fetchDeezerCoverArt(ctx, "album", canonicalArtist, album),
	})

	// One card + one description per recommendation actually shown — ranked
	// is already capped above. Concurrent Deezer/Last.fm lookups, same
	// shape as the other two modes'. Each candidate costs an extra
	// album.getinfo call here (fetchAlbumWiki) on top of the track.getinfo
	// call fetchTrackAlbum already made to resolve its album name in the
	// first place — this mode is already documented as the expensive one.
	descriptions := make([]string, len(ranked))
	for i, rec := range concurrentMap(albumResolveConcurrency, ranked, func(c *similarAlbumCandidate) (trackRecommendation, error) {
		cardURL := c.URL
		if cardURL == "" {
			cardURL = lastfmAlbumURL(c.Artist, c.Album)
		}
		wiki, _ := fetchAlbumWiki(ctx, c.Artist, c.Album)
		return trackRecommendation{
			Card: Card{
				Title:    c.Album,
				Subtitle: c.Artist,
				ImageURL: fetchDeezerCoverArt(ctx, "album", c.Artist, c.Album),
				URL:      cardURL,
			},
			Description: wiki,
		}, nil
	}) {
		ctx.AddCard(rec.Card)
		descriptions[i] = rec.Description
	}

	return formatSimilarAlbumsResult(canonicalArtist, album, description, ranked, descriptions), nil
}

type similarAlbumCandidate struct {
	Artist string
	Album  string
	URL    string
	Score  float64
	Tracks map[string]bool // distinct contributing candidate tracks — len() is the cross-track agreement count
}

func formatSimilarAlbumsResult(artist, album, description string, ranked []*similarAlbumCandidate, descriptions []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Similar albums to %s by %s:\n", album, artist)
	if description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", description)
	}
	sb.WriteString("\n")
	if len(ranked) == 0 {
		sb.WriteString("(no similar albums found)\n")
	}
	for i, c := range ranked {
		fmt.Fprintf(&sb, "%d. %s - %s", i+1, c.Artist, c.Album)
		if len(c.Tracks) > 1 {
			fmt.Fprintf(&sb, " (%d tracks pointed here independently)", len(c.Tracks))
		}
		if i < len(descriptions) && descriptions[i] != "" {
			fmt.Fprintf(&sb, " — %s", truncateText(descriptions[i], descriptionTruncateLen))
		}
		sb.WriteString("\n")
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

	body, statusCode, err := httpGetJSON(ctx, lastfmBaseURL+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("last.fm status %d", statusCode)
	}
	var lfErr lastfmError
	if json.Unmarshal(body, &lfErr) == nil && lfErr.Error != 0 {
		return nil, fmt.Errorf("last.fm: %s", lfErr.Message)
	}
	return body, nil
}

// lastfmReadMoreLinkRe strips the "<a href=\"...\">Read more on Last.fm</a>."
// suffix Last.fm appends to every wiki summary (confirmed live for both
// track.getinfo and album.getinfo) — that link is meaningless once the text
// is folded into a tool result the model reads, not a rendered page.
var lastfmReadMoreLinkRe = regexp.MustCompile(`\s*<a\s+href="[^"]*">[^<]*</a>\.?\s*$`)

// cleanLastFMWiki strips the trailing "Read more on Last.fm" link, unescapes
// HTML entities (Last.fm's wiki text is user-submitted and escapes quotes/
// ampersands), and trims whitespace — turns the raw wiki.summary field into
// plain prose suitable for a tool result. Empty input (most candidate
// tracks/albums have no wiki entry at all — confirmed live) returns "".
func cleanLastFMWiki(raw string) string {
	cleaned := lastfmReadMoreLinkRe.ReplaceAllString(raw, "")
	return strings.TrimSpace(html.UnescapeString(cleaned))
}

// truncateText bounds s to max runes, breaking at the last space before the
// cutoff rather than mid-word, and appending "..." — used for per-candidate
// descriptions (see descriptionTruncateLen) in both music.go and books.go,
// where a full wiki/description paragraph is appropriate once for the
// source item but not stacked under every recommendation.
func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	// s[:max] is a byte-offset slice — for non-ASCII text (a foreign-film
	// TMDB overview, say) that can land mid-rune, producing invalid UTF-8.
	// Trim back byte-by-byte until the prefix is valid UTF-8 again, before
	// searching for a word boundary below — a run of multi-byte characters
	// with no space near the cutoff (nothing for LastIndex to find) can't
	// return a broken trailing byte sequence this way. Bounded by at most
	// utf8.UTFMax-1 iterations: s itself is assumed valid UTF-8 (it's
	// decoded JSON text), so cutting one byte at a time off an invalid
	// mid-rune prefix reaches a valid boundary in at most 3 steps.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "..."
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
	URL    string  `json:"url"`
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

// lastfmAlbumInfo is album.getinfo's parsed response — fetchAlbumInfo is the
// one place this file calls that endpoint; fetchAlbumTracklist and
// fetchAlbumWiki are both thin extractions over it rather than issuing their
// own separate requests, since a single album.getinfo call already carries
// everything either one needs (tracklist and wiki summary alike).
type lastfmAlbumInfo struct {
	Name   string
	Artist string
	URL    string
	Wiki   string
	Tracks []string
}

func fetchAlbumInfo(ctx *Context, artist, album string) (*lastfmAlbumInfo, error) {
	body, err := lastfmGet(ctx.Ctx, ctx.LastFMAPIKey, url.Values{
		"method": {"album.getinfo"},
		"artist": {artist},
		"album":  {album},
	})
	if err != nil {
		return nil, fmt.Errorf("fetching album: %w", err)
	}
	var resp struct {
		Album struct {
			Name   string `json:"name"`
			Artist string `json:"artist"`
			URL    string `json:"url"`
			Wiki   *struct {
				Summary string `json:"summary"`
			} `json:"wiki"`
			Tracks struct {
				Track []struct {
					Name string `json:"name"`
				} `json:"track"`
			} `json:"tracks"`
		} `json:"album"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing album response: %w", err)
	}

	names := make([]string, 0, len(resp.Album.Tracks.Track))
	for _, t := range resp.Album.Tracks.Track {
		names = append(names, t.Name)
	}
	wiki := ""
	if resp.Album.Wiki != nil {
		wiki = cleanLastFMWiki(resp.Album.Wiki.Summary)
	}
	return &lastfmAlbumInfo{Name: resp.Album.Name, Artist: resp.Album.Artist, URL: resp.Album.URL, Wiki: wiki, Tracks: names}, nil
}

// fetchAlbumTracklist resolves an album's canonical artist credit, Last.fm
// URL, and wiki description, plus its tracklist capped at
// maxAlbumTracklistSize.
func fetchAlbumTracklist(ctx *Context, artist, album string) (canonicalArtist, albumURL, wikiSummary string, tracks []string, err error) {
	info, err := fetchAlbumInfo(ctx, artist, album)
	if err != nil {
		return "", "", "", nil, err
	}
	if info.Name == "" {
		return "", "", "", nil, fmt.Errorf("no album found for %q by %q", album, artist)
	}
	if len(info.Tracks) == 0 {
		return "", "", "", nil, fmt.Errorf("%q by %q has no tracklist on last.fm", album, artist)
	}
	tracks = info.Tracks
	if len(tracks) > maxAlbumTracklistSize {
		tracks = tracks[:maxAlbumTracklistSize]
	}
	return info.Artist, info.URL, info.Wiki, tracks, nil
}

// fetchAlbumWiki is similar_albums mode's per-candidate description
// lookup — a second album.getinfo call per resolved candidate album, on top
// of fetchTrackAlbum's track.getinfo call that resolved its name in the
// first place (track.getinfo's own response has no album-level wiki field).
// Best-effort: errors are swallowed by the caller the same way
// fetchDeezerCoverArt's are, since a missing description shouldn't sink an
// otherwise-successful candidate.
func fetchAlbumWiki(ctx *Context, artist, album string) (string, error) {
	info, err := fetchAlbumInfo(ctx, artist, album)
	if err != nil {
		return "", err
	}
	return info.Wiki, nil
}

// fetchTrackWiki is track.getinfo's wiki summary, used both for a source
// track's own description (mode "track") and per-candidate descriptions
// across all three modes. Most candidate tracks have no wiki entry at all
// (confirmed live against niche tracks) — that's not an error, just an
// empty string the caller omits from its output.
func fetchTrackWiki(ctx *Context, artist, track string) (string, error) {
	body, err := lastfmGet(ctx.Ctx, ctx.LastFMAPIKey, url.Values{
		"method": {"track.getinfo"},
		"artist": {artist},
		"track":  {track},
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Track struct {
			Wiki *struct {
				Summary string `json:"summary"`
			} `json:"wiki"`
		} `json:"track"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Track.Wiki == nil {
		return "", nil
	}
	return cleanLastFMWiki(resp.Track.Wiki.Summary), nil
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

// lastfmAlbumURL is lastfmTrackURL's album-mode counterpart — used only
// as a citation/card fallback on the rare path where an API response
// didn't carry its own album URL.
func lastfmAlbumURL(artist, album string) string {
	return fmt.Sprintf("https://www.last.fm/music/%s/%s", url.PathEscape(artist), url.PathEscape(album))
}

// deezerBaseURL is a var (not a const) so tests can point it at a fake
// server, same pattern as lastfmBaseURL.
var deezerBaseURL = "https://api.deezer.com"

// fetchDeezerCoverArt is a best-effort enrichment, not part of the tool's
// core result — Deezer's field-scoped search (no key required) is keyless
// and, unlike Apple's/Last.fm's own art, reliably has cover art for both
// mainstream and niche releases alike (confirmed live for both Isaiah
// Rashad and Kendrick Lamar while designing this). kind is "track" or
// "album"; track results nest cover art under the parent album, album
// results carry it directly. Any failure or no-match returns "" — the
// citation this feeds just gets no thumbnail, never a broken/placeholder
// image, and never blocks the tool's actual result.
func fetchDeezerCoverArt(ctx *Context, kind, artist, title string) string {
	field := "track"
	if kind == "album" {
		field = "album"
	}
	q := fmt.Sprintf(`artist:"%s" %s:"%s"`, artist, field, title)

	req, err := http.NewRequestWithContext(ctx.Ctx, "GET",
		fmt.Sprintf("%s/search/%s?q=%s&limit=1", deezerBaseURL, field, url.QueryEscape(q)), nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}

	if kind == "album" {
		var out struct {
			Data []struct {
				CoverMedium string `json:"cover_medium"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &out) != nil || len(out.Data) == 0 {
			return ""
		}
		return out.Data[0].CoverMedium
	}

	var out struct {
		Data []struct {
			Album struct {
				CoverMedium string `json:"cover_medium"`
			} `json:"album"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil || len(out.Data) == 0 {
		return ""
	}
	return out.Data[0].Album.CoverMedium
}
