// movies finds real movie/TV show recommendations grounded in TMDB's
// audience-recommendation data — "users who watched this also watched" —
// instead of hoping a web_search hit on a review happens to mention a
// comparable title, the same failure mode music.go and books.go replace for
// their own domains. Requires a free TMDB API key (config.yaml's
// tmdb.api_key, https://www.themoviedb.org/settings/api) — like
// LastFMAPIKey, there's no unauthenticated fallback, so a missing key fails
// every call with a clear error rather than degrading silently.
//
// One tool, not two: media_type ("movie" or "tv") switches which of TMDB's
// parallel endpoint families a call uses, rather than this being split into
// separate movies/tv_shows tools — the underlying flow (resolve title →
// recommendations → format) is identical either way, and the model already
// picks the right mode/media_type reliably for music/books, so there's no
// real ambiguity risk to hedge against with two tool defs instead of one.
//
// Structurally simpler than music.go/books.go: TMDB's /recommendations
// endpoint already returns a ranked list for one title directly (its own
// collaborative-filtering algorithm), so there's no per-track/per-list
// fan-out and aggregation to do here — this is closer to music.go's single
// "track" mode than its more expensive album-level modes.
//
// /recommendations is genuinely thin for very new or very obscure titles
// (no viewing-behavior data to draw on yet) — /similar (genre/keyword
// based, a weaker but always-populated signal) supplements it when that
// happens, same fallback role Open Library's subject overlap plays for
// books.go when Hardcover's curated-list data is thin.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"polaris/llm"
)

var moviesDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "movies",
		Description: "Find real movie/TV show recommendations grounded in TMDB's actual audience-recommendation " +
			"data (\"people who watched this also watched\"), not guesswork or hoping a web search turns up a " +
			"\"movies like X\" listicle. Use media_type \"movie\" or \"tv\" depending on what the user named. " +
			"Pass year when the title could be ambiguous (a remake, a reboot, a common title).",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title": map[string]interface{}{
					"type":        "string",
					"description": "The movie or TV show's title.",
				},
				"media_type": map[string]interface{}{
					"type":        "string",
					"description": "Whether title names a movie or a TV show.",
					"enum":        []string{"movie", "tv"},
				},
				"year": map[string]interface{}{
					"type": "integer",
					"description": "The release year (movies) or first-air year (TV), if known — helps " +
						"disambiguate a title shared by a remake/reboot/unrelated production.",
				},
			},
			"required": []string{"title", "media_type"},
		},
	},
}

func init() { Register("movies", handleMovies) }

// tmdbBaseURL is a var (not a const) so tests can point it at a fake
// server, same pattern as lastfmBaseURL/hardcoverBaseURL.
var tmdbBaseURL = "https://api.themoviedb.org/3"

const (
	// minRecommendationCandidates below this, /similar is fetched too and
	// merged in — mirrors books.go's hardcoverMinCandidates threshold for
	// supplementing a thin primary signal with a weaker always-populated one.
	minRecommendationCandidates = 5
	maxMoviesResultsShown       = 10

	// tmdbPosterSize is TMDB's documented poster width buckets
	// (w92/w154/w185/w342/w500/w780/original) — w342 matches the card
	// carousel's 108px display width with room for retina density without
	// pulling the full "original" size for a thumbnail.
	tmdbPosterSize = "w342"
)

func handleMovies(argsJSON string, ctx *Context) string {
	var args struct {
		Title     string `json:"title"`
		MediaType string `json:"media_type"`
		Year      int    `json:"year"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "movies", nil, "error: "+err.Error())
	}
	if ctx.TMDBAPIKey == "" {
		return emitToolError(ctx, "movies", map[string]interface{}{"title": args.Title},
			"error: movies lookups aren't configured — set tmdb.api_key in config.yaml")
	}
	args.Title = strings.TrimSpace(args.Title)
	if args.Title == "" {
		return emitToolError(ctx, "movies", nil, "error: title is required")
	}
	if args.MediaType != "movie" && args.MediaType != "tv" {
		return emitToolError(ctx, "movies", map[string]interface{}{"title": args.Title, "media_type": args.MediaType},
			`error: media_type must be "movie" or "tv"`)
	}

	callArgs := map[string]interface{}{"title": args.Title, "media_type": args.MediaType}
	if args.Year != 0 {
		callArgs["year"] = args.Year
	}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "movies", "args": callArgs})

	result, err := lookupMovieRecommendations(ctx, args.Title, args.MediaType, args.Year)
	if err != nil {
		result = "error: " + err.Error()
		log.Warn("movies failed", "title", args.Title, "media_type", args.MediaType, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "movies", "result": result})
		return result
	}

	log.Info("movies", "title", args.Title, "media_type", args.MediaType)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "movies",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
		"cards":     ctx.CardsSnapshot(),
	})
	return result
}

func lookupMovieRecommendations(ctx *Context, title, mediaType string, year int) (string, error) {
	resolved, err := resolveTMDBTitle(ctx, mediaType, title, year)
	if err != nil {
		return "", err
	}

	// Genres and recommendations are independent lookups (both only need
	// resolved.ID) fired concurrently rather than sequentially — same
	// "independent calls run in parallel" shape as books.go's
	// lookupViaHardcover, just two goroutines instead of three since
	// movies.go has no Open-Library-equivalent third source to fetch.
	var genres []string
	var recs []tmdbTitle
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Best-effort grounding (the "why these fit" line), same role
		// music.go's tags/description play — a failure here shouldn't
		// sink an otherwise-successful lookup.
		var genreErr error
		genres, genreErr = fetchTMDBGenres(ctx, mediaType, resolved.ID)
		if genreErr != nil {
			log.Warn("movies: fetching genres failed", "title", resolved.displayTitle(mediaType), "err", genreErr)
		}
	}()
	go func() {
		defer wg.Done()
		var recsErr error
		recs, recsErr = fetchTMDBList(ctx, mediaType, resolved.ID, "recommendations")
		if recsErr != nil {
			log.Warn("movies: fetching recommendations failed", "title", resolved.displayTitle(mediaType), "err", recsErr)
			recs = nil
		}
	}()
	wg.Wait()

	supplemented := false
	if len(recs) < minRecommendationCandidates {
		similar, err := fetchTMDBList(ctx, mediaType, resolved.ID, "similar")
		if err == nil && len(similar) > 0 {
			before := len(recs)
			recs = mergeTMDBResults(recs, similar)
			if len(recs) > before {
				supplemented = true
			}
		}
	}
	if len(recs) > maxMoviesResultsShown {
		recs = recs[:maxMoviesResultsShown]
	}

	ctx.AddCitation(Citation{
		Title:    fmt.Sprintf("TMDB: %s", resolved.citationLabel(mediaType)),
		URL:      tmdbPageURL(mediaType, resolved.ID),
		ImageURL: tmdbPosterURL(resolved.PosterPath),
	})
	for _, r := range recs {
		ctx.AddCard(Card{
			Title:    r.displayTitle(mediaType),
			Subtitle: r.year(mediaType),
			ImageURL: tmdbPosterURL(r.PosterPath),
			URL:      tmdbPageURL(mediaType, r.ID),
		})
	}

	return formatMoviesResult(resolved, mediaType, genres, recs, supplemented), nil
}

func formatMoviesResult(source tmdbTitle, mediaType string, genres []string, recs []tmdbTitle, supplemented bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n", source.citationLabel(mediaType))
	if len(genres) > 0 {
		fmt.Fprintf(&sb, "Genres: %s\n", strings.Join(genres, ", "))
	}
	if overview := strings.TrimSpace(source.Overview); overview != "" {
		fmt.Fprintf(&sb, "Overview: %s\n", overview)
	}

	label := "Similar movies"
	if mediaType == "tv" {
		label = "Similar TV shows"
	}
	fmt.Fprintf(&sb, "\n%s:\n", label)
	if len(recs) == 0 {
		sb.WriteString("(none found)\n")
	}
	for i, r := range recs {
		fmt.Fprintf(&sb, "%d. %s", i+1, r.citationLabel(mediaType))
		if overview := strings.TrimSpace(r.Overview); overview != "" {
			fmt.Fprintf(&sb, " — %s", truncateText(overview, descriptionTruncateLen))
		}
		sb.WriteString("\n")
	}
	if supplemented {
		sb.WriteString("\n(TMDB had limited audience-recommendation data for this title — some results above " +
			"are supplemented via genre/keyword-based similar titles, a weaker \"same kind of thing\" signal " +
			"rather than \"people who watched this also watched\".)\n")
	}
	return strings.TrimSpace(sb.String())
}

// --- TMDB API ---

// tmdbTitle is one result row shared by /search, /recommendations, and
// /similar — all three endpoints return the same shape, just under
// different top-level keys, so one struct covers every call site in this
// file. Movie and TV fields coexist here rather than as two structs
// because a single mergeTMDBResults/formatMoviesResult pass needs to treat
// both uniformly; displayTitle/date/year pick the right field per call.
type tmdbTitle struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`          // movie
	Name          string  `json:"name"`           // tv
	OriginalTitle string  `json:"original_title"` // movie
	OriginalName  string  `json:"original_name"`  // tv
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date"`   // movie
	FirstAirDate  string  `json:"first_air_date"` // tv
	PosterPath    string  `json:"poster_path"`
	Popularity    float64 `json:"popularity"`
}

func (t tmdbTitle) displayTitle(mediaType string) string {
	if mediaType == "tv" {
		return t.Name
	}
	return t.Title
}

func (t tmdbTitle) originalTitle(mediaType string) string {
	if mediaType == "tv" {
		return t.OriginalName
	}
	return t.OriginalTitle
}

func (t tmdbTitle) date(mediaType string) string {
	if mediaType == "tv" {
		return t.FirstAirDate
	}
	return t.ReleaseDate
}

func (t tmdbTitle) year(mediaType string) string {
	d := t.date(mediaType)
	if len(d) >= 4 {
		return d[:4]
	}
	return ""
}

// citationLabel is "Title (year)", or bare "Title" when TMDB has no
// date on file yet (an unreleased/announced title — confirmed this
// happens live for titles still in production).
func (t tmdbTitle) citationLabel(mediaType string) string {
	if y := t.year(mediaType); y != "" {
		return fmt.Sprintf("%s (%s)", t.displayTitle(mediaType), y)
	}
	return t.displayTitle(mediaType)
}

// tmdbError is TMDB's API-level error shape — returned with a non-2xx
// status (unlike Last.fm's HTTP-200-with-error-field pattern), so this is
// only parsed after a non-OK status is already known, purely to surface a
// clearer message than the bare status code.
type tmdbError struct {
	StatusMessage string `json:"status_message"`
}

func tmdbGet(ctx context.Context, apiKey, path string, params url.Values) ([]byte, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("api_key", apiKey)

	body, statusCode, err := httpGetJSON(ctx, tmdbBaseURL+path+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		var tmdbErr tmdbError
		if json.Unmarshal(body, &tmdbErr) == nil && tmdbErr.StatusMessage != "" {
			return nil, fmt.Errorf("tmdb: %s", tmdbErr.StatusMessage)
		}
		return nil, fmt.Errorf("tmdb status %d", statusCode)
	}
	return body, nil
}

// resolveTMDBTitle turns a user-supplied title into TMDB's canonical
// entry via /search/{movie,tv}, preferring an exact case-insensitive
// title match first, then the highest-popularity match among those —
// same two-stage logic as resolveHardcoverBook's author filter followed
// by a usage-count comparison. Text match has to come first: TMDB's
// search is a loose text match, not an exact-title lookup, and querying
// "Atlanta" or "Minions" returns plenty of results that merely CONTAIN
// that word (confirmed live: "The Real Housewives of Atlanta" outranks
// the actual "Atlanta" series on raw popularity; "Minions & Monsters", a
// 2026 spinoff special, outranks the 2015 "Minions" film the same way).
// Ranking every loosely-matching result by popularity alone silently
// resolves to the wrong title in exactly these cases; restricting to
// exact matches before ranking fixes it, the same lesson resolveTrack's
// own doc comment describes for near-duplicate track titles. Falls back
// to TMDB's own relevance-ranked first result when nothing matches
// exactly (e.g. a query with a subtitle TMDB's canonical title omits),
// rather than failing outright.
//
// year, when given, first filters via TMDB's exact-match year parameter
// (primary_release_year for movies, first_air_date_year for TV) — this
// disambiguates remakes/reboots sharing a title far better than
// popularity alone could (a 2024 reboot might be less popular than the
// original it's named after). Falls back to an unfiltered search if the
// year filter finds nothing, rather than failing outright — the caller's
// year might just be slightly off (a show spanning multiple years,
// listed by a different year than the one asked about).
func resolveTMDBTitle(ctx *Context, mediaType, title string, year int) (tmdbTitle, error) {
	searchPath := "/search/movie"
	yearParam := "primary_release_year"
	if mediaType == "tv" {
		searchPath = "/search/tv"
		yearParam = "first_air_date_year"
	}

	search := func(withYear bool) ([]tmdbTitle, error) {
		params := url.Values{"query": {title}}
		if withYear && year != 0 {
			params.Set(yearParam, strconv.Itoa(year))
		}
		body, err := tmdbGet(ctx.Ctx, ctx.TMDBAPIKey, searchPath, params)
		if err != nil {
			return nil, fmt.Errorf("resolving title: %w", err)
		}
		var resp struct {
			Results []tmdbTitle `json:"results"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("parsing tmdb search response: %w", err)
		}
		return resp.Results, nil
	}

	results, err := search(true)
	if err != nil {
		return tmdbTitle{}, err
	}
	if len(results) == 0 && year != 0 {
		results, err = search(false)
		if err != nil {
			return tmdbTitle{}, err
		}
	}
	if len(results) == 0 {
		return tmdbTitle{}, fmt.Errorf("no %s found for %q on tmdb", mediaType, title)
	}

	var exact []tmdbTitle
	for _, r := range results {
		if strings.EqualFold(r.displayTitle(mediaType), title) || strings.EqualFold(r.originalTitle(mediaType), title) {
			exact = append(exact, r)
		}
	}
	candidates := results
	if len(exact) > 0 {
		candidates = exact
	}

	best := candidates[0]
	for _, r := range candidates[1:] {
		if r.Popularity > best.Popularity {
			best = r
		}
	}
	return best, nil
}

// fetchTMDBGenres fetches a title's genre names via its details endpoint
// (/movie/{id} or /tv/{id}) — /search and /recommendations/similar only
// carry numeric genre_ids, not names, so this is the one call in this
// file that needs the details endpoint rather than the shared search-like
// shape every other call site uses.
func fetchTMDBGenres(ctx *Context, mediaType string, id int) ([]string, error) {
	path := fmt.Sprintf("/movie/%d", id)
	if mediaType == "tv" {
		path = fmt.Sprintf("/tv/%d", id)
	}
	body, err := tmdbGet(ctx.Ctx, ctx.TMDBAPIKey, path, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(resp.Genres))
	for _, g := range resp.Genres {
		names = append(names, g.Name)
	}
	return names, nil
}

// fetchTMDBList fetches /{movie,tv}/{id}/{endpoint} — endpoint is
// "recommendations" or "similar", the two TMDB endpoints that share this
// exact response shape (see this file's package doc comment for what
// distinguishes them).
func fetchTMDBList(ctx *Context, mediaType string, id int, endpoint string) ([]tmdbTitle, error) {
	path := fmt.Sprintf("/%s/%d/%s", mediaType, id, endpoint)
	body, err := tmdbGet(ctx.Ctx, ctx.TMDBAPIKey, path, url.Values{"page": {"1"}})
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", endpoint, err)
	}
	var resp struct {
		Results []tmdbTitle `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing %s response: %w", endpoint, err)
	}
	return resp.Results, nil
}

// mergeTMDBResults appends extra's entries not already present in base
// (deduped by TMDB id) — used to supplement thin /recommendations results
// with /similar's, preserving recommendations' stronger-signal ordering
// first. Sorted by descending popularity within each source rather than
// left in whatever order TMDB returned, so a supplemented list still reads
// as "best matches first" across both sources rather than two
// separately-ordered runs concatenated together.
func mergeTMDBResults(base, extra []tmdbTitle) []tmdbTitle {
	sort.SliceStable(base, func(i, j int) bool { return base[i].Popularity > base[j].Popularity })
	sort.SliceStable(extra, func(i, j int) bool { return extra[i].Popularity > extra[j].Popularity })

	seen := make(map[int]bool, len(base))
	for _, r := range base {
		seen[r.ID] = true
	}
	merged := base
	for _, r := range extra {
		if !seen[r.ID] {
			seen[r.ID] = true
			merged = append(merged, r)
		}
	}
	return merged
}

// tmdbPageURL builds a title's canonical TMDB page URL, used for both the
// source citation and every recommendation card's link.
func tmdbPageURL(mediaType string, id int) string {
	return fmt.Sprintf("https://www.themoviedb.org/%s/%d", mediaType, id)
}

// tmdbPosterURL turns a bare poster_path (TMDB never returns a full URL,
// confirmed live — every response field is just "/abc123.jpg") into a
// real image URL via TMDB's image CDN. Unlike music.go's Deezer cover-art
// lookup, this needs no extra API call: the poster path comes back
// directly on every search/recommendations/similar result. Empty
// poster_path (uncommon but confirmed live for some very obscure/new
// titles) returns "", same "no thumbnail, never a broken one" contract as
// fetchDeezerCoverArt.
func tmdbPosterURL(posterPath string) string {
	if posterPath == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/" + tmdbPosterSize + posterPath
}
