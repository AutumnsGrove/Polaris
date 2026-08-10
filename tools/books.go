// books finds book recommendations grounded in two real signals, instead of
// guesswork or shared-genre matching alone:
//
//   - Hardcover.app's user-curated lists (primary, when hardcover.api_key is
//     configured): books that turn up on the same curated lists as the
//     source book — "Lasers Go Pew Pew", "The Esquire 75 Best Sci-Fi Books
//     of All Time" — ranked by how many distinct lists agree, the book
//     equivalent of music.go's aggregateSimilarTracks cross-track agreement
//     signal. Hardcover also supplies member-crowdsourced genre tags for the
//     resolved book (via its search index, not a separate query), used the
//     same way music.go uses Last.fm's track.gettoptags: grounding for "why
//     these fit", not part of the ranking itself.
//   - Open Library subject-tag overlap (fallback, no key required, always
//     available): a book's declared subjects fanned out concurrently, each
//     subject's own top works aggregated by how many subjects they share
//     with the source book. A distinctly weaker signal than Hardcover's
//     list data — it's "same shelf", not "curated alongside on purpose" —
//     so it's used only when Hardcover isn't configured, its token is
//     invalid/expired (see hardcoverAuthError), the title can't be resolved
//     there at all, or its list data for this specific book is too thin to
//     trust alone (new/obscure titles with few or no curated-list
//     placements — see hardcoverMinCandidates). In the thin-data case the
//     Open Library results are appended after, not merged into, the
//     Hardcover-ranked ones: the primary signal still outranks the
//     fallback, it's just padded out to a fuller list.
//
// Unlike music.go's LastFMAPIKey, HardcoverAPIKey is optional (see
// tools/registry.go's Context.HardcoverAPIKey doc comment). Hardcover
// issues personal-account JWTs, not stable service keys, with roughly a
// one-year expiry — a previously-working deployment can start hitting auth
// failures with no config change on this end, which is exactly why the
// Open Library fallback exists: expiry degrades the tool to a weaker
// signal instead of breaking it outright.
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
	"time"

	"polaris/llm"
)

var booksDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "books",
		Description: "Find real book recommendations grounded in readers' curated lists (Hardcover.app) " +
			"and shared subject/genre data (Open Library), not guesswork or hoping a web search turns up a " +
			"\"books like X\" listicle. Use when the user names a book and wants more like it.",
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
			log.Warn("books: hardcover token missing/invalid/expired, falling back to open library", "err", err)
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
func formatBooksResult(title, author string, tags []string, ranked []*bookCandidate, supplemented bool) string {
	var sb strings.Builder
	if author != "" {
		fmt.Fprintf(&sb, "%s by %s\n", title, author)
	} else {
		fmt.Fprintf(&sb, "%s\n", title)
	}
	if len(tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(tags, ", "))
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
			fmt.Fprintf(&sb, "%d. %s%s\n", i+1, c.Title, label)
		} else {
			fmt.Fprintf(&sb, "%d. %s by %s%s\n", i+1, c.Title, c.Author, label)
		}
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
type hardcoverAuthError struct{ message string }

func (e *hardcoverAuthError) Error() string {
	if e.message == "" {
		return "hardcover: authorization failed (token missing, invalid, or expired)"
	}
	return "hardcover: " + e.message
}

func isHardcoverAuthError(err error) bool {
	var authErr *hardcoverAuthError
	return errors.As(err, &authErr)
}

// hardcoverQuery POSTs a GraphQL query and returns its "data" field.
// Hardcover has two distinct error shapes, both handled here rather than at
// each call site: a top-level `{"error": "..."}` for auth failures (seen
// live: `{"error":"Unable to verify token"}`, HTTP 401), and the standard
// GraphQL `{"errors": [...]}` array for query-level failures (HTTP 200).
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
		Error  string          `json:"error"`
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parsing hardcover response: %w", err)
	}
	if parsed.Error != "" || resp.StatusCode == http.StatusUnauthorized {
		return nil, &hardcoverAuthError{message: parsed.Error}
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
	ID    int    `json:"id"`
	Title string `json:"title"`
	Slug  string `json:"slug"`
	Image *struct {
		URL string `json:"url"`
	} `json:"image"`
	CachedContributors []struct {
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"cached_contributors"`
}

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

func hardcoverBookURL(slug string) string {
	if slug == "" {
		return ""
	}
	return "https://hardcover.app/books/" + slug
}

type hardcoverBook struct {
	ID       int
	Title    string
	Author   string
	Slug     string
	ImageURL string
	Genres   []string
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
		ID:       id,
		Title:    best.Title,
		Author:   strings.Join(best.AuthorNames, ", "),
		Slug:     best.Slug,
		ImageURL: imageURL,
		Genres:   best.Genres,
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
			book { id title slug image { url } cached_contributors }
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
					Title:    r.Title,
					Author:   author,
					URL:      hardcoverBookURL(r.Slug),
					CoverURL: imageURL,
					Source:   candidateSourceList,
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

	ranked := rankBookCandidates(aggregateListBooks(ctx, book.ID, lists))

	supplemented := false
	if len(ranked) < hardcoverMinCandidates {
		if extra, extraErr := openLibrarySupplement(ctx, title, author, ranked); extraErr == nil && len(extra) > 0 {
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
	addBookCards(ctx, ranked)
	return formatBooksResult(book.Title, book.Author, hardcoverGenreTags(book.Genres), ranked, supplemented), nil
}

// --- Open Library path ---

type openLibraryWork struct {
	Key      string
	Title    string
	Author   string
	Subjects []string
	CoverID  int
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
		"title":  {title},
		"limit":  {"5"},
		"fields": {"key,title,author_name,subject,cover_i"},
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
			Key        string   `json:"key"`
			Title      string   `json:"title"`
			AuthorName []string `json:"author_name"`
			Subject    []string `json:"subject"`
			CoverI     int      `json:"cover_i"`
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
		Key:      d.Key,
		Title:    d.Title,
		Author:   strings.Join(d.AuthorName, ", "),
		Subjects: filterOpenLibrarySubjects(d.Subject),
		CoverID:  d.CoverI,
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
				}
				agg[key] = entry
			}
			entry.Count++
		}
	}
	return agg
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
	addBookCards(ctx, ranked)
	return formatBooksResult(work.Title, work.Author, work.Subjects, ranked, false), nil
}

// openLibrarySupplement is lookupViaHardcover's thin-data path — resolves
// and aggregates Open Library the same way lookupViaOpenLibrary does, but
// returns only the candidates not already present in existing (deduped by
// title+author) instead of a formatted result, since the caller appends
// them after its own Hardcover-ranked candidates rather than replacing them.
func openLibrarySupplement(ctx *Context, title, author string, existing []*bookCandidate) ([]*bookCandidate, error) {
	work, err := resolveOpenLibraryWork(ctx, title, author)
	if err != nil {
		return nil, err
	}
	if len(work.Subjects) == 0 {
		return nil, nil
	}
	ranked := rankBookCandidates(aggregateOpenLibrarySubjects(ctx, work))

	seen := make(map[string]bool, len(existing))
	for _, c := range existing {
		seen[strings.ToLower(c.Title)+"|"+strings.ToLower(c.Author)] = true
	}
	extra := make([]*bookCandidate, 0, len(ranked))
	for _, c := range ranked {
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
	return extra, nil
}
