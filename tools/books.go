// books finds book recommendations grounded in real signals, blended
// rather than tried one-at-a-time, instead of guesswork:
//
//   - Hardcover.app's user-curated lists (when hardcover.api_key is
//     configured): books that turn up on the same curated lists as the
//     source book — "Lasers Go Pew Pew", "The Esquire 75 Best Sci-Fi Books
//     of All Time" — the book equivalent of music.go's
//     aggregateSimilarTracks cross-track agreement signal. On its own this
//     is NOT enough: a book that straddles two identities (The Count of
//     Monte Cristo is both canonical 19th-century literature AND an
//     adventure/revenge genre novel) gets its list ecosystem dominated by
//     whichever identity has more active list-curators — confirmed live,
//     Monte Cristo has 936 qualifying public lists, nearly all "greatest
//     classics"/canon meta-lists, while its handful of genre-specific
//     lists ("Adventure") have zero likes each, so cross-list agreement
//     alone surfaced other canon staples (Catch-22, To the Lighthouse)
//     instead of anything resembling its actual plot.
//   - Hardcover's own crowdsourced genre tags, both for the source book
//     (via its search index) and for every list-sourced candidate (via
//     cached_tags) — genre overlap with the source book is the PRIMARY
//     re-ranking signal on the Hardcover path (see
//     rankHardcoverCandidates), specifically because list co-occurrence
//     alone failed the case above. List agreement still matters as a
//     tiebreaker, just not as the deciding factor.
//   - Open Library subject-tag overlap: fetched CONCURRENTLY alongside
//     Hardcover's list data, not sequentially as a fallback-of-last-resort.
//     It does three jobs at once: corroborates Hardcover candidates (a
//     modest ranking bonus when an independent signal agrees), backstops
//     thin Hardcover list data (new/obscure titles — see
//     hardcoverMinCandidates), and stands in entirely when Hardcover isn't
//     configured, its auth is failing for any reason (see
//     hardcoverAuthError), or the title can't be resolved there at all.
//
// Unlike music.go's LastFMAPIKey, HardcoverAPIKey is optional (see
// tools/registry.go's Context.HardcoverAPIKey doc comment). Hardcover's key
// format itself isn't stable either: it originally issued personal-account
// JWTs with roughly a one-year expiry, then switched to `hc_pat_...`
// personal access tokens with a configurable (including non-expiring)
// lifetime — same `Bearer <token>` auth scheme either way, confirmed live,
// so no code change was needed for the format switch itself. What isn't
// stable is the *account* behind the token: confirmed live, a token can be
// well within its own validity window and still fail with
// error_description "User account is not active" if Hardcover deactivates
// the account server-side — indistinguishable from a bad token without
// reading error_description (see hardcoverQuery's doc comment). Either
// failure — token or account — degrades the tool to Open Library's weaker
// signal instead of breaking it outright, which is exactly why the Open
// Library path exists independently.
package tools

import (
	"bytes"
	"encoding/json"
	"errors"
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

var booksDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "books",
		// Description is populated at call time by Defs()/AllDefs() from
		// tools/descriptions/books.yaml — see tools/catalog.go.
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"title": map[string]interface{}{
					"type":        "string",
					"description": "The book's title.",
				},
				"author": map[string]interface{}{
					"type":        "string",
					"description": "The author's name, if known — helps disambiguate books with common titles.",
				},
			},
			"required": []string{"title"},
		},
	},
}

func init() { Register("books", handleBooks) }

// hardcoverBaseURL/openLibraryBaseURL are vars (not consts) so tests can
// point them at a fake server, same pattern as music.go's lastfmBaseURL.
var hardcoverBaseURL = "https://api.hardcover.app/v1/graphql"
var openLibraryBaseURL = "https://openlibrary.org"

const (
	hardcoverListMinBooks    = 3   // exclude tiny throwaway/personal-shelf lists
	hardcoverListMaxBooks    = 100 // exclude "TBR 2000+" mega-lists that dilute the signal
	hardcoverListPoolSize    = 25  // likes-ranked candidate pool fetched before re-ranking by density
	hardcoverListFanout      = 8   // top N curated lists (by density, see hardcoverList.density) to fan out across
	hardcoverListConcurrency = 5
	hardcoverMinCandidates   = 5 // below this, supplement with Open Library
	hardcoverTagLimit        = 8

	openLibrarySubjectFanout      = 5
	openLibrarySubjectConcurrency = 5
	openLibrarySubjectWorkLimit   = 25

	maxBooksResultsShown = 10
)

const (
	candidateSourceList    = "list"    // Hardcover curated-list co-occurrence
	candidateSourceSubject = "subject" // Open Library subject overlap
)

func handleBooks(argsJSON string, ctx *Context) string {
	var args struct {
		Title  string `json:"title"`
		Author string `json:"author"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return emitToolError(ctx, "books", nil, "error: "+err.Error())
	}
	args.Title = strings.TrimSpace(args.Title)
	if args.Title == "" {
		return emitToolError(ctx, "books", nil, "error: title is required")
	}
	args.Author = strings.TrimSpace(args.Author)

	callArgs := map[string]interface{}{"title": args.Title}
	if args.Author != "" {
		callArgs["author"] = args.Author
	}
	ctx.Emit("tool_call", map[string]interface{}{"tool": "books", "args": callArgs})

	result, err := lookupSimilarBooks(ctx, args.Title, args.Author)
	if err != nil {
		result = "error: " + err.Error()
		log.Warn("books failed", "title", args.Title, "err", err)
		ctx.Emit("tool_result", map[string]interface{}{"tool": "books", "result": result})
		return result
	}

	log.Info("books", "title", args.Title)
	ctx.Emit("tool_result", map[string]interface{}{
		"tool":      "books",
		"result":    result,
		"citations": ctx.CitationsSnapshot(),
	})
	return result
}

func lookupSimilarBooks(ctx *Context, title, author string) (string, error) {
	if ctx.HardcoverAPIKey != "" {
		result, err := lookupViaHardcover(ctx, title, author)
		if err == nil {
			return result, nil
		}
		if isHardcoverAuthError(err) {
			log.Warn("books: hardcover auth failed, falling back to open library", "err", err)
		} else {
			log.Warn("books: hardcover lookup failed, falling back to open library", "title", title, "err", err)
		}
	}
	return lookupViaOpenLibrary(ctx, title, author)
}

// --- shared candidate type ---

// bookCandidate is a recommended book, tagged with which signal surfaced it
// and how many independent sources within that signal agreed (distinct
// curated lists, or distinct shared subjects) — the same (source, count)
// shape music.go's similarTrackCandidate uses for cross-track agreement.
type bookCandidate struct {
	Title    string
	Author   string
	URL      string
	CoverURL string
	Count    int
	Source   string // candidateSourceList or candidateSourceSubject
	// Genres is only populated for candidateSourceList candidates (each
	// list-sourced book's own Hardcover genre tags) — used by
	// rankHardcoverCandidates to score genre overlap against the source
	// book, not shown directly to the user.
	Genres []string
	// Description is populated directly from Hardcover's response for
	// candidateSourceList candidates (its `books` GraphQL type carries
	// description right alongside title/slug/contributors, confirmed live
	// — no extra query needed). candidateSourceSubject candidates have no
	// description in the /subjects/{slug}.json listing that surfaces them,
	// so it's left empty here and filled in afterward, only for the
	// capped/shown set, by enrichSubjectDescriptions.
	Description string
	// Key is the Open Library work key (e.g. "/works/OL2W"), populated
	// only for candidateSourceSubject candidates — enrichSubjectDescriptions
	// uses it to fetch the one extra per-candidate detail call description
	// requires on this path.
	Key string
}

// addBookCards converts ranked candidates (already capped to
// maxBooksResultsShown by the caller) into the frontend's recommendation
// carousel, same shape/rationale as music.go's Card population — Cards are
// a UI presentation of the same ranked list already in the text result, not
// a second, independent computation.
func addBookCards(ctx *Context, ranked []*bookCandidate) {
	for _, c := range ranked {
		ctx.AddCard(Card{Title: c.Title, Subtitle: c.Author, ImageURL: c.CoverURL, URL: c.URL})
	}
}

func rankBookCandidates(agg map[string]*bookCandidate) []*bookCandidate {
	ranked := make([]*bookCandidate, 0, len(agg))
	for _, v := range agg {
		ranked = append(ranked, v)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}
		return ranked[i].Title < ranked[j].Title
	})
	return ranked
}

// genreOverlapCount counts genres two books share, case-insensitively —
// the primary re-ranking signal on the Hardcover path (see
// rankHardcoverCandidates's doc comment for why cross-list agreement alone
// isn't enough: it lets a book's "literary canon" cluster drown out
// genuinely genre-similar candidates for books that straddle both a canon
// and a genre identity).
// genericHardcoverGenres are tags applied broadly enough across unrelated
// books to add noise rather than signal to overlap scoring — confirmed
// live: The Count of Monte Cristo's own genre tags include "Fiction" and
// "Classics", and Heart of Darkness (tagged "Fantasy", "Fiction", "Murder",
// "Romance" — crowdsourced tagging is noisy) shared enough of those with
// it to outrank genuinely adventure-themed candidates on overlap alone,
// purely because "Fiction" means nothing as a similarity signal between
// two 19th-century novels.
var genericHardcoverGenres = map[string]bool{
	"fiction": true, "nonfiction": true, "classics": true, "literary fiction": true,
}

func genreOverlapCount(a, b []string) int {
	set := make(map[string]bool, len(b))
	for _, g := range b {
		g = strings.ToLower(g)
		if !genericHardcoverGenres[g] {
			set[g] = true
		}
	}
	count := 0
	for _, g := range a {
		g = strings.ToLower(g)
		if !genericHardcoverGenres[g] && set[g] {
			count++
		}
	}
	return count
}

// rankHardcoverCandidates re-ranks aggregateListBooks' output primarily by
// genre overlap with the source book (weighted far above cross-list
// agreement — see genreOverlapCount), with a secondary bonus when Open
// Library's independent subject-overlap signal also surfaced the same
// candidate (fetched concurrently alongside the Hardcover list fan-out in
// lookupViaHardcover, not as a sequential fallback — see this file's
// package doc comment). Cross-list Count still breaks ties, it's just no
// longer the deciding factor on its own: that's what let The Count of
// Monte Cristo's 936 "greatest classics" meta-lists outrank its own
// (unliked, but genre-accurate) "Adventure" lists before this existed.
func rankHardcoverCandidates(agg map[string]*bookCandidate, sourceGenres []string, openLibraryTitleAuthors map[string]bool) []*bookCandidate {
	ranked := make([]*bookCandidate, 0, len(agg))
	for _, v := range agg {
		ranked = append(ranked, v)
	}
	// Multiplicative, not additive — confirmed live against real Monte
	// Cristo data that an additive score (genre overlap weighted far above
	// Count) over-corrects: a single-list candidate matching on two
	// generic-ish tags (a fantasy/vampire romance sharing "Historical
	// Fiction"+"Romance" with Monte Cristo) beat real 4-list community
	// consensus (Catch-22) outright.
	//
	// Overlap is squared, not linear — (1+overlap)²*(1+count), not
	// (1+overlap)*(1+count) — found live to be a real, separate gap from
	// the additive-vs-multiplicative question above: the plain linear
	// version still let list-count alone dominate on books whose real
	// curated-list ecosystem is itself dominated by "greatest classics"
	// meta-lists (Monte Cristo again — pulling from a 100-list pool instead
	// of 25 changed nothing, confirmed live, so this isn't a
	// not-enough-lists problem). Concretely: Lonesome Dove (shares 3 of
	// Monte Cristo's own genres, including the specific "Adventure" tag,
	// but only 1 curated list) scored 8 under the linear formula while
	// Catch-22 (shares only 1 genre, "Historical Fiction", but 4 lists)
	// scored 10 — count alone won despite being the weaker content match.
	// Squaring flips that (32 vs 20) without reintroducing the additive
	// scheme's own regression above — Catch-22 (20) still comfortably
	// outranks the vampire-romance case (18), re-verified against the same
	// real data before this changed. A candidate with genuinely neither
	// signal (Heart of Darkness / To Kill a Mockingbird: 0 overlap, Count
	// only from those same generic meta-lists) still sinks to the bottom —
	// (1+0)² is still just 1, unchanged from before.
	score := func(c *bookCandidate) int {
		overlap := 1 + genreOverlapCount(c.Genres, sourceGenres)
		s := overlap * overlap * (1 + c.Count)
		// Keyed by title+author, not title alone — matching every other
		// dedup/match key in this file (see aggregateListBooks,
		// aggregateOpenLibrarySubjects, openLibraryExtras). Title-only
		// matching let an unrelated Open Library candidate that merely
		// SHARES a title with a Hardcover candidate (a common public-domain
		// title, a "Study Guide" companion edition, ...) grant this
		// cross-source corroboration bonus even though Open Library never
		// actually corroborated THIS book — silently promoting the wrong
		// candidate in the final ranking.
		if openLibraryTitleAuthors[strings.ToLower(c.Title)+"|"+strings.ToLower(c.Author)] {
			s += 2
		}
		return s
	}
	sort.Slice(ranked, func(i, j int) bool {
		if si, sj := score(ranked[i]), score(ranked[j]); si != sj {
			return si > sj
		}
		if ranked[i].Count != ranked[j].Count {
			return ranked[i].Count > ranked[j].Count
		}
		return ranked[i].Title < ranked[j].Title
	})
	return ranked
}

// capBookCandidates trims to maxBooksResultsShown before both the text
// result and the Card carousel are built from it, so the two always
// describe the same set — same rationale as music.go's "ranked is already
// capped above" comment on its own Card-population loops.
func capBookCandidates(ranked []*bookCandidate) []*bookCandidate {
	if len(ranked) > maxBooksResultsShown {
		return ranked[:maxBooksResultsShown]
	}
	return ranked
}

// formatBooksResult assumes ranked is already capped (see
// capBookCandidates) — callers pass the same capped slice to addBookCards,
// so the text list and the Card carousel always describe the same set.
func formatBooksResult(title, author, description string, tags []string, ranked []*bookCandidate, supplemented bool) string {
	var sb strings.Builder
	if author != "" {
		fmt.Fprintf(&sb, "%s by %s\n", title, author)
	} else {
		fmt.Fprintf(&sb, "%s\n", title)
	}
	if len(tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(tags, ", "))
	}
	if description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", description)
	}
	sb.WriteString("\nSimilar books:\n")
	if len(ranked) == 0 {
		sb.WriteString("(no similar books found)\n")
	}
	for i, c := range ranked {
		label := ""
		switch {
		case c.Source == candidateSourceList && c.Count > 1:
			label = fmt.Sprintf(" (on %d curated lists with this book)", c.Count)
		case c.Source == candidateSourceSubject && c.Count > 1:
			label = fmt.Sprintf(" (shares %d subjects with this book)", c.Count)
		case c.Source == candidateSourceSubject:
			label = " (via shared subjects)"
		}
		if c.Author == "" {
			fmt.Fprintf(&sb, "%d. %s%s", i+1, c.Title, label)
		} else {
			fmt.Fprintf(&sb, "%d. %s by %s%s", i+1, c.Title, c.Author, label)
		}
		if c.Description != "" {
			fmt.Fprintf(&sb, " — %s", c.Description)
		}
		sb.WriteString("\n")
	}
	if supplemented {
		sb.WriteString("\n(Hardcover had limited curated-list data for this book — some results above are " +
			"supplemented via Open Library's shared-subject data, a weaker \"same genre\" signal rather than " +
			"\"readers curated these together\".)\n")
	}
	return strings.TrimSpace(sb.String())
}

// --- Hardcover path ---

// hardcoverAuthError distinguishes "token missing/invalid/expired" from any
// other Hardcover failure (network error, bad query, book not found) — only
// this specific case gets logged as an auth problem pointing at
// hardcover.app's account settings; the caller falls back to Open Library
// either way; see lookupSimilarBooks.
//
// description carries Hardcover's error_description field, which turned out
// to matter more than the bare "error" code once seen live: a still-valid,
// non-expired token started failing with error="invalid_token", and without
// description that's indistinguishable from an actually-expired token —
// description was the only thing that said error_description="User account
// is not active", i.e. the Hardcover *account* itself had been deactivated
// server-side, not a token problem at all. Both fields are captured now so
// that distinction shows up in the logs instead of requiring a live curl
// probe to discover, same as it did the first time.
type hardcoverAuthError struct{ message, description string }

func (e *hardcoverAuthError) Error() string {
	switch {
	case e.message == "":
		return "hardcover: authorization failed (token missing, invalid, or expired)"
	case e.description != "":
		return fmt.Sprintf("hardcover: %s (%s) — check both the token's validity and the account's status at hardcover.app, they fail independently", e.message, e.description)
	default:
		return "hardcover: " + e.message
	}
}

func isHardcoverAuthError(err error) bool {
	var authErr *hardcoverAuthError
	return errors.As(err, &authErr)
}

// hardcoverQuery POSTs a GraphQL query and returns its "data" field.
// Hardcover has two distinct error shapes, both handled here rather than at
// each call site: a top-level `{"error": "...", "error_description": "..."}`
// for auth failures (seen live: `{"error":"Unable to verify token"}` for a
// malformed/bad token, and separately `{"error":"invalid_token",
// "error_description":"User account is not active"}` for a perfectly valid,
// non-expired token whose *account* had been deactivated server-side —
// error_description is the field that actually distinguishes "get a new
// token" from "check your account status", not the bare error code), and
// the standard GraphQL `{"errors": [...]}` array for query-level failures
// (HTTP 200).
func hardcoverQuery(ctx *Context, query string, variables map[string]interface{}) (json.RawMessage, error) {
	payload, err := json.Marshal(map[string]interface{}{"query": query, "variables": variables})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx.Ctx, "POST", hardcoverBaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ctx.HardcoverAPIKey)

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

	var parsed struct {
		Error            string          `json:"error"`
		ErrorDescription string          `json:"error_description"`
		Data             json.RawMessage `json:"data"`
		Errors           []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parsing hardcover response: %w", err)
	}
	if parsed.Error != "" || resp.StatusCode == http.StatusUnauthorized {
		return nil, &hardcoverAuthError{message: parsed.Error, description: parsed.ErrorDescription}
	}
	if len(parsed.Errors) > 0 {
		return nil, fmt.Errorf("hardcover: %s", parsed.Errors[0].Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hardcover status %d", resp.StatusCode)
	}
	return parsed.Data, nil
}

// hardcoverBookRow is the shape of one `books` row from the list-member
// queries (fetchListBooks) — the raw `books` GraphQL type, distinct from
// hardcoverSearchDoc below (the Typesense-backed `search` query's result
// shape, used only for resolution).
type hardcoverBookRow struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Image       *struct {
		URL string `json:"url"`
	} `json:"image"`
	CachedContributors []struct {
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"cached_contributors"`
	// CachedTags is the raw `books` type's nested tag shape (categories:
	// Genre, Mood, Content Warning, etc.) — distinct from
	// hardcoverSearchDoc's flat `genres []string`, which comes from a
	// different underlying representation (the Typesense search index).
	// Only the "Genre" category is used here, via genres() below.
	CachedTags map[string][]hardcoverTag `json:"cached_tags"`
}

type hardcoverTag struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"` // how many members independently applied this tag
}

// hardcoverGenreMinCount excludes single-vote (and near-single-vote) genre
// tags from overlap scoring — confirmed live: a book's cached_tags Genre
// category routinely includes stray count=1 entries ("Comics", "General",
// "History" on The Count of Monte Cristo; "Comics" again, oddly, on an
// obscure Heart of Darkness edition) that are essentially noise, one
// person's mistag or edge-case categorization rather than a real signal —
// and because they're rare, two unrelated books coincidentally sharing one
// is far more likely to be noise-matching-noise than genuine similarity.
const hardcoverGenreMinCount = 2

func (r hardcoverBookRow) authorNames() []string {
	names := make([]string, 0, len(r.CachedContributors))
	for _, c := range r.CachedContributors {
		if c.Author.Name != "" {
			names = append(names, c.Author.Name)
		}
	}
	return names
}

func (r hardcoverBookRow) hasAuthor(author string) bool {
	for _, n := range r.authorNames() {
		if strings.Contains(strings.ToLower(n), strings.ToLower(author)) {
			return true
		}
	}
	return false
}

func (r hardcoverBookRow) genres() []string {
	tags, ok := r.CachedTags["Genre"]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t.Count >= hardcoverGenreMinCount {
			out = append(out, t.Tag)
		}
	}
	return out
}

func hardcoverBookURL(slug string) string {
	if slug == "" {
		return ""
	}
	return "https://hardcover.app/books/" + slug
}

type hardcoverBook struct {
	ID          int
	Title       string
	Author      string
	Slug        string
	ImageURL    string
	Genres      []string
	Description string
}

// hardcoverSearchDoc is one hit's `document` from Hardcover's `search` query
// — a Typesense index, not the raw `books` SQL type fetchListBooks queries.
// Resolution goes through this rather than `books(where: {title: {_ilike:
// ...}})` because Hardcover's public API tier rejects _ilike outright
// (confirmed live: "ilike and related operations are not permitted on this
// server") — `search` is the tier-appropriate way to do a fuzzy title
// lookup, and it conveniently already flattens contributors/genres into
// plain string lists instead of the raw type's nested shape.
type hardcoverSearchDoc struct {
	// ID comes back as a string even though it's numeric — Typesense (the
	// engine behind Hardcover's search index) always encodes document IDs
	// as strings, unlike the raw `books`/`list_books` GraphQL types this
	// file also queries, which use real Int ids (confirmed live: unmarshal
	// into an int field failed with "cannot unmarshal string into...id of
	// type int"). Converted to int in resolveHardcoverBook once parsed,
	// since every other query in this file (list membership, exclusion
	// filters) needs the real Int id.
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Slug           string   `json:"slug"`
	Description    string   `json:"description"`
	UsersReadCount int      `json:"users_read_count"`
	AuthorNames    []string `json:"author_names"`
	Genres         []string `json:"genres"`
	Image          *struct {
		URL string `json:"url"`
	} `json:"image"`
}

func (d hardcoverSearchDoc) hasAuthor(author string) bool {
	for _, n := range d.AuthorNames {
		if strings.Contains(strings.ToLower(n), strings.ToLower(author)) {
			return true
		}
	}
	return false
}

// resolveHardcoverBook turns a user-supplied title/author into Hardcover's
// canonical entry, same rationale as music.go's resolveTrack: Hardcover has
// near-duplicate/companion entries per title (translations, "Summary and
// Analysis of X" study guides, omnibus editions bundling several books),
// most with near-zero read activity, so picking Typesense's top text match
// alone could land on one of those instead of the real, well-known book.
// Disambiguates by users_read_count among the search hits, same "pick by
// real usage" logic as resolveTrack's listener-count comparison.
func resolveHardcoverBook(ctx *Context, title, author string) (*hardcoverBook, error) {
	const query = `query($q: String!) {
		search(query: $q, query_type: "Title", per_page: 20) {
			results
		}
	}`
	data, err := hardcoverQuery(ctx, query, map[string]interface{}{"q": title})
	if err != nil {
		return nil, fmt.Errorf("resolving book: %w", err)
	}
	var resp struct {
		Search struct {
			Results struct {
				Hits []struct {
					Document hardcoverSearchDoc `json:"document"`
				} `json:"hits"`
			} `json:"results"`
		} `json:"search"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing hardcover search response: %w", err)
	}
	if len(resp.Search.Results.Hits) == 0 {
		return nil, fmt.Errorf("no book found on hardcover for %q", title)
	}

	docs := make([]hardcoverSearchDoc, len(resp.Search.Results.Hits))
	for i, h := range resp.Search.Results.Hits {
		docs[i] = h.Document
	}
	if author != "" {
		var filtered []hardcoverSearchDoc
		for _, d := range docs {
			if d.hasAuthor(author) {
				filtered = append(filtered, d)
			}
		}
		// Falls back to the unfiltered hits rather than failing outright if
		// none match — the caller's spelling of the author might just not
		// match Hardcover's stored name string, same fallback rationale as
		// resolveTrack's.
		if len(filtered) > 0 {
			docs = filtered
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].UsersReadCount > docs[j].UsersReadCount })

	best := docs[0]
	id, err := strconv.Atoi(best.ID)
	if err != nil {
		return nil, fmt.Errorf("parsing hardcover search result id %q: %w", best.ID, err)
	}
	imageURL := ""
	if best.Image != nil {
		imageURL = best.Image.URL
	}
	return &hardcoverBook{
		ID:          id,
		Title:       best.Title,
		Author:      strings.Join(best.AuthorNames, ", "),
		Slug:        best.Slug,
		ImageURL:    imageURL,
		Genres:      best.Genres,
		Description: best.Description,
	}, nil
}

type hardcoverList struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	LikesCount int    `json:"likes_count"`
	BooksCount int    `json:"books_count"`
}

// density is likes per member book — a niche list a reader clearly curated
// with intent ("Lasers Go Pew Pew", 43 books/1 like each) scores higher
// here than a sprawling "best books of the century" list with more raw
// likes spread across 100 generic picks. Raw likes_count alone (tried
// first, against live data) skewed results toward whatever's broadly
// popular in general rather than genuinely similar to the source book —
// two very different, very popular literary novels both turning up on the
// same handful of big "best of" lists reads as false similarity.
func (l hardcoverList) density() float64 {
	if l.BooksCount == 0 {
		return 0
	}
	return float64(l.LikesCount) / float64(l.BooksCount)
}

// fetchHardcoverLists finds the source book's best public curated lists —
// filtered to hardcoverListMinBooks..hardcoverListMaxBooks members, pulled
// from a larger likes-ranked pool and re-ranked by density (see
// hardcoverList.density) — so the co-occurrence fan-out below draws from
// lists a reader actually curated with intent, not a thousand-book
// "everything I own" catalog or a generic "best books ever" list that would
// otherwise swamp the real signal the same way an artist's own back-catalog
// swamps music.go's similar-track results without its same-artist exclusion.
func fetchHardcoverLists(ctx *Context, bookID int) ([]hardcoverList, error) {
	const query = `query($bookID: Int!, $minBooks: Int!, $maxBooks: Int!, $limit: Int!) {
		list_books(
			where: {book_id: {_eq: $bookID}, list: {public: {_eq: true}, books_count: {_gte: $minBooks, _lte: $maxBooks}}}
			order_by: {list: {likes_count: desc}}
			limit: $limit
		) {
			list { id name likes_count books_count }
		}
	}`
	data, err := hardcoverQuery(ctx, query, map[string]interface{}{
		"bookID": bookID, "minBooks": hardcoverListMinBooks, "maxBooks": hardcoverListMaxBooks, "limit": hardcoverListPoolSize,
	})
	if err != nil {
		return nil, fmt.Errorf("fetching lists: %w", err)
	}
	var resp struct {
		ListBooks []struct {
			List hardcoverList `json:"list"`
		} `json:"list_books"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parsing lists response: %w", err)
	}
	lists := make([]hardcoverList, 0, len(resp.ListBooks))
	for _, r := range resp.ListBooks {
		lists = append(lists, r.List)
	}
	sort.Slice(lists, func(i, j int) bool { return lists[i].density() > lists[j].density() })
	if len(lists) > hardcoverListFanout {
		lists = lists[:hardcoverListFanout]
	}
	return lists, nil
}

func fetchListBooks(ctx *Context, listID, excludeBookID int) ([]hardcoverBookRow, error) {
	const query = `query($listID: Int!, $excludeBookID: Int!) {
		list_books(where: {list_id: {_eq: $listID}, book_id: {_neq: $excludeBookID}}, limit: 50) {
			book { id title slug description image { url } cached_contributors cached_tags }
		}
	}`
	data, err := hardcoverQuery(ctx, query, map[string]interface{}{"listID": listID, "excludeBookID": excludeBookID})
	if err != nil {
		return nil, err
	}
	var resp struct {
		ListBooks []struct {
			Book hardcoverBookRow `json:"book"`
		} `json:"list_books"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	rows := make([]hardcoverBookRow, 0, len(resp.ListBooks))
	for _, r := range resp.ListBooks {
		rows = append(rows, r.Book)
	}
	return rows, nil
}

// aggregateListBooks fans out fetchListBooks across lists concurrently and
// aggregates by (title, author): a candidate that turns up on more than one
// of the source book's curated lists gets a higher Count — cross-list
// agreement, the book-domain analog of music.go's aggregateSimilarTracks
// cross-track agreement.
func aggregateListBooks(ctx *Context, sourceBookID int, lists []hardcoverList) map[string]*bookCandidate {
	perList := concurrentMap(hardcoverListConcurrency, lists, func(l hardcoverList) ([]hardcoverBookRow, error) {
		return fetchListBooks(ctx, l.ID, sourceBookID)
	})

	agg := map[string]*bookCandidate{}
	for _, rows := range perList {
		for _, r := range rows {
			author := strings.Join(r.authorNames(), ", ")
			key := strings.ToLower(r.Title) + "|" + strings.ToLower(author)
			entry, ok := agg[key]
			if !ok {
				imageURL := ""
				if r.Image != nil {
					imageURL = r.Image.URL
				}
				entry = &bookCandidate{
					Title:       r.Title,
					Author:      author,
					URL:         hardcoverBookURL(r.Slug),
					CoverURL:    imageURL,
					Source:      candidateSourceList,
					Genres:      r.genres(),
					Description: r.Description,
				}
				agg[key] = entry
			}
			entry.Count++
		}
	}
	return agg
}

// hardcoverGenreTags caps the resolved book's crowdsourced genres (already
// relevance-ordered by Hardcover itself) to hardcoverTagLimit — the closest
// analog here to music.go's Last.fm tags: grounding for "why these fit",
// not part of the candidate ranking itself.
func hardcoverGenreTags(genres []string) []string {
	if len(genres) > hardcoverTagLimit {
		return genres[:hardcoverTagLimit]
	}
	return genres
}

// fetchHardcoverBookGenres re-fetches the resolved book's own genre tags
// via cached_tags rather than trusting hardcoverSearchDoc.Genres (the
// Typesense search index's version, resolveHardcoverBook's data source) —
// candidates from fetchListBooks only ever have cached_tags data
// available, so scoring needs the source book on the exact same
// vocabulary and noise threshold (see hardcoverGenreMinCount) for a fair
// comparison, not two different tag representations that happen to look
// similar but aren't guaranteed to match tag-for-tag.
func fetchHardcoverBookGenres(ctx *Context, bookID int) ([]string, error) {
	const query = `query($bookID: Int!) {
		books(where: {id: {_eq: $bookID}}, limit: 1) {
			cached_tags
		}
	}`
	data, err := hardcoverQuery(ctx, query, map[string]interface{}{"bookID": bookID})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Books []hardcoverBookRow `json:"books"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	if len(resp.Books) == 0 {
		return nil, fmt.Errorf("hardcover book id %d not found", bookID)
	}
	return resp.Books[0].genres(), nil
}

func lookupViaHardcover(ctx *Context, title, author string) (string, error) {
	book, err := resolveHardcoverBook(ctx, title, author)
	if err != nil {
		return "", err
	}

	lists, err := fetchHardcoverLists(ctx, book.ID)
	if err != nil {
		return "", err
	}
	if len(lists) == 0 {
		return "", fmt.Errorf("no quality curated lists found for %q on hardcover", book.Title)
	}

	// Three independent fetches run concurrently rather than sequentially:
	// Hardcover's list co-occurrence, Open Library's subject overlap (see
	// this file's package doc comment for why it's always fetched, not just
	// a fallback), and the source book's own genre tags on the SAME
	// vocabulary candidates use (see fetchHardcoverBookGenres — comparing
	// against hardcoverSearchDoc.Genres instead was the bug that let noisy,
	// low-confidence tag matches on obscure candidate editions outscore
	// genuinely similar books).
	var hcAgg map[string]*bookCandidate
	var olRanked []*bookCandidate
	sourceGenres := book.Genres // fallback if the cached_tags re-fetch below fails
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		hcAgg = aggregateListBooks(ctx, book.ID, lists)
	}()
	go func() {
		defer wg.Done()
		work, err := resolveOpenLibraryWork(ctx, title, author)
		if err != nil || len(work.Subjects) == 0 {
			return
		}
		olRanked = rankBookCandidates(aggregateOpenLibrarySubjects(ctx, work))
	}()
	go func() {
		defer wg.Done()
		if genres, err := fetchHardcoverBookGenres(ctx, book.ID); err == nil && len(genres) > 0 {
			sourceGenres = genres
		}
	}()
	wg.Wait()

	openLibraryTitleAuthors := make(map[string]bool, len(olRanked))
	for _, c := range olRanked {
		openLibraryTitleAuthors[strings.ToLower(c.Title)+"|"+strings.ToLower(c.Author)] = true
	}

	ranked := rankHardcoverCandidates(hcAgg, sourceGenres, openLibraryTitleAuthors)

	supplemented := false
	if len(ranked) < hardcoverMinCandidates && len(olRanked) > 0 {
		if extra := openLibraryExtras(olRanked, ranked); len(extra) > 0 {
			ranked = append(ranked, extra...)
			supplemented = true
		}
	}

	ctx.AddCitation(Citation{
		Title:    fmt.Sprintf("Hardcover: %s", book.Title),
		URL:      hardcoverBookURL(book.Slug),
		ImageURL: book.ImageURL,
	})

	ranked = capBookCandidates(ranked)
	enrichSubjectDescriptions(ctx, ranked)
	addBookCards(ctx, ranked)
	return formatBooksResult(book.Title, book.Author, book.Description, hardcoverGenreTags(sourceGenres), ranked, supplemented), nil
}

// --- Open Library path ---

type openLibraryWork struct {
	Key         string
	Title       string
	Author      string
	Subjects    []string
	CoverID     int
	Description string
}

// filterOpenLibrarySubjects keeps only the subjects likely to produce a
// useful /subjects/{slug}.json overlap: colon-qualified tags like
// "nyt:hardcover-fiction=2021-05-23" are per-edition bestseller-list
// metadata, not genre subjects, and bare "Fiction"/"Nonfiction" are too
// broad to mean anything specific to the source book — their subject pages
// are dominated by whatever's generally popular, not by anything actually
// similar to it.
func filterOpenLibrarySubjects(subjects []string) []string {
	out := make([]string, 0, openLibrarySubjectFanout)
	for _, s := range subjects {
		if len(out) >= openLibrarySubjectFanout {
			break
		}
		if strings.Contains(s, ":") || strings.EqualFold(s, "Fiction") || strings.EqualFold(s, "Nonfiction") {
			continue
		}
		out = append(out, s)
	}
	return out
}

func resolveOpenLibraryWork(ctx *Context, title, author string) (*openLibraryWork, error) {
	params := url.Values{
		"title": {title},
		"limit": {"5"},
		// description is requested here too — search.json flattens it to a
		// plain string for the top-level work doc (confirmed live), unlike
		// /subjects/{slug}.json's listing shape used for candidates below,
		// which carries no description at all (see enrichSubjectDescriptions).
		"fields": {"key,title,author_name,subject,cover_i,description"},
	}
	if author != "" {
		params.Set("author", author)
	}
	req, err := http.NewRequestWithContext(ctx.Ctx, "GET", openLibraryBaseURL+"/search.json?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Polaris/1.0 (personal search assistant)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolving book on open library: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open library status %d", resp.StatusCode)
	}

	var parsed struct {
		Docs []struct {
			Key         string   `json:"key"`
			Title       string   `json:"title"`
			AuthorName  []string `json:"author_name"`
			Subject     []string `json:"subject"`
			CoverI      int      `json:"cover_i"`
			Description string   `json:"description"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parsing open library search response: %w", err)
	}
	if len(parsed.Docs) == 0 {
		return nil, fmt.Errorf("no book found on open library for %q", title)
	}
	d := parsed.Docs[0]
	return &openLibraryWork{
		Key:         d.Key,
		Title:       d.Title,
		Author:      strings.Join(d.AuthorName, ", "),
		Subjects:    filterOpenLibrarySubjects(d.Subject),
		CoverID:     d.CoverI,
		Description: strings.TrimSpace(d.Description),
	}, nil
}

type openLibrarySubjectWork struct {
	Key      string
	Title    string
	Author   string
	CoverURL string
}

func openLibrarySubjectSlug(subject string) string {
	slug := strings.ToLower(strings.TrimSpace(subject))
	slug = strings.ReplaceAll(slug, " ", "_")
	return url.PathEscape(slug)
}

func fetchOpenLibrarySubjectWorks(ctx *Context, subject string) ([]openLibrarySubjectWork, error) {
	reqURL := fmt.Sprintf("%s/subjects/%s.json?limit=%d", openLibraryBaseURL, openLibrarySubjectSlug(subject), openLibrarySubjectWorkLimit)
	req, err := http.NewRequestWithContext(ctx.Ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Polaris/1.0 (personal search assistant)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching subject %q: %w", subject, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("open library subject %q status %d", subject, resp.StatusCode)
	}

	var parsed struct {
		Works []struct {
			Key     string `json:"key"`
			Title   string `json:"title"`
			CoverID int    `json:"cover_id"`
			Authors []struct {
				Name string `json:"name"`
			} `json:"authors"`
		} `json:"works"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parsing open library subject response: %w", err)
	}
	out := make([]openLibrarySubjectWork, 0, len(parsed.Works))
	for _, w := range parsed.Works {
		names := make([]string, 0, len(w.Authors))
		for _, a := range w.Authors {
			names = append(names, a.Name)
		}
		coverURL := ""
		if w.CoverID != 0 {
			coverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-M.jpg", w.CoverID)
		}
		out = append(out, openLibrarySubjectWork{Key: w.Key, Title: w.Title, Author: strings.Join(names, ", "), CoverURL: coverURL})
	}
	return out, nil
}

// aggregateOpenLibrarySubjects fans fetchOpenLibrarySubjectWorks out across
// work.Subjects concurrently and aggregates by (title, author): a candidate
// whose subject page turns up under more than one of the source book's
// subjects gets a higher Count — the same cross-source-agreement shape as
// aggregateListBooks, just over a weaker underlying signal (shared genre
// tags rather than a person's deliberate curation).
func aggregateOpenLibrarySubjects(ctx *Context, work *openLibraryWork) map[string]*bookCandidate {
	perSubject := concurrentMap(openLibrarySubjectConcurrency, work.Subjects, func(subject string) ([]openLibrarySubjectWork, error) {
		return fetchOpenLibrarySubjectWorks(ctx, subject)
	})

	agg := map[string]*bookCandidate{}
	for _, works := range perSubject {
		for _, w := range works {
			if w.Key == work.Key {
				continue
			}
			key := strings.ToLower(w.Title) + "|" + strings.ToLower(w.Author)
			entry, ok := agg[key]
			if !ok {
				entry = &bookCandidate{
					Title:    w.Title,
					Author:   w.Author,
					URL:      "https://openlibrary.org" + w.Key,
					CoverURL: w.CoverURL,
					Source:   candidateSourceSubject,
					Key:      w.Key,
				}
				agg[key] = entry
			}
			entry.Count++
		}
	}
	return agg
}

// openLibraryDescription unmarshals Open Library's /works/{key}.json
// description field, which comes back in two different shapes depending on
// the record (confirmed live): a plain string, or {"type": "/type/text",
// "value": "..."}. search.json's flattened `description` field (used for
// the source book in resolveOpenLibraryWork) is always the plain-string
// form, so only this per-work-detail path needs to handle both.
type openLibraryDescription struct{ Value string }

func (d *openLibraryDescription) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		d.Value = s
		return nil
	}
	var obj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	d.Value = obj.Value
	return nil
}

// fetchOpenLibraryWorkDescription is the one extra call the subject-overlap
// path needs per candidate: /subjects/{slug}.json (fetchOpenLibrarySubjectWorks)
// is a lightweight listing with no description field at all, unlike
// search.json's flattened one or Hardcover's `books` type (both available
// with zero extra calls — see this file's bookCandidate.Description doc
// comment). Only called for the final capped/shown candidate set, never the
// full aggregated pool, via enrichSubjectDescriptions.
func fetchOpenLibraryWorkDescription(ctx *Context, workKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx.Ctx, "GET", openLibraryBaseURL+workKey+".json", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Polaris/1.0 (personal search assistant)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("open library work %q status %d", workKey, resp.StatusCode)
	}

	var parsed struct {
		Description openLibraryDescription `json:"description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parsing open library work response: %w", err)
	}
	return strings.TrimSpace(parsed.Description.Value), nil
}

// enrichSubjectDescriptions fetches candidateSourceSubject candidates'
// descriptions concurrently and in place — called after capBookCandidates in
// every caller, so this only ever fans out over the ≤maxBooksResultsShown
// candidates actually displayed, not the full aggregated pool.
// candidateSourceList candidates already have Description populated (see
// aggregateListBooks) and are skipped. Best-effort: a failed lookup just
// leaves that one candidate's Description empty, same as a missing Deezer
// cover elsewhere in this codebase.
func enrichSubjectDescriptions(ctx *Context, candidates []*bookCandidate) {
	concurrentMap(openLibrarySubjectConcurrency, candidates, func(c *bookCandidate) (struct{}, error) {
		if c.Source != candidateSourceSubject || c.Key == "" {
			return struct{}{}, nil
		}
		if desc, err := fetchOpenLibraryWorkDescription(ctx, c.Key); err == nil {
			c.Description = desc
		}
		return struct{}{}, nil
	})
}

func lookupViaOpenLibrary(ctx *Context, title, author string) (string, error) {
	work, err := resolveOpenLibraryWork(ctx, title, author)
	if err != nil {
		return "", err
	}
	if len(work.Subjects) == 0 {
		return "", fmt.Errorf("%q has no usable subject data on open library", work.Title)
	}
	ranked := rankBookCandidates(aggregateOpenLibrarySubjects(ctx, work))

	coverURL := ""
	if work.CoverID != 0 {
		coverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-M.jpg", work.CoverID)
	}
	ctx.AddCitation(Citation{
		Title:    fmt.Sprintf("Open Library: %s", work.Title),
		URL:      "https://openlibrary.org" + work.Key,
		ImageURL: coverURL,
	})

	ranked = capBookCandidates(ranked)
	enrichSubjectDescriptions(ctx, ranked)
	addBookCards(ctx, ranked)
	return formatBooksResult(work.Title, work.Author, work.Description, work.Subjects, ranked, false), nil
}

// openLibraryExtras is lookupViaHardcover's thin-data path — filters an
// already-fetched, already-ranked Open Library candidate list (olRanked,
// fetched concurrently alongside Hardcover's own list data, not re-fetched
// here) down to the ones not already present in existing (deduped by
// title+author), since the caller appends them after its own
// Hardcover-ranked candidates rather than replacing them.
func openLibraryExtras(olRanked, existing []*bookCandidate) []*bookCandidate {
	seen := make(map[string]bool, len(existing))
	for _, c := range existing {
		seen[strings.ToLower(c.Title)+"|"+strings.ToLower(c.Author)] = true
	}
	extra := make([]*bookCandidate, 0, len(olRanked))
	for _, c := range olRanked {
		if len(existing)+len(extra) >= maxBooksResultsShown {
			break
		}
		key := strings.ToLower(c.Title) + "|" + strings.ToLower(c.Author)
		if seen[key] {
			continue
		}
		seen[key] = true
		extra = append(extra, c)
	}
	return extra
}
