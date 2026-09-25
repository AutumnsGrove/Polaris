// Package main is a one-off dev tool that pulls the raw signals
// docs/plans/hill-climbing.md's Tier 1 evals need out of a real polaris.db
// and into dev/fixtures/ (gitignored — this is the operator's own chat
// history, never committed). It's the "prerequisite for most items" that
// plan calls for: everything else (web_read extraction scoring,
// paywall/empty-page heuristics, search ranking) works from these
// snapshots rather than querying the live database directly.
//
// Exports three things:
//   - web_read.jsonl: every web_read'd URL, whether it was later cited in
//     that turn's answer (an implicit "this one was useful" signal), and
//     the raw HTML re-fetched fresh — the DB only ever kept the extracted
//     text, not the original page, so scoring extraction quality needs a
//     real re-fetch, not a replay of what's already stored.
//   - search_ranking.jsonl: every cached search result list, each result
//     flagged with whether it was later web_read or cited — the implicit
//     click-through label docs/plans/hill-climbing.md's #3 needs for
//     MRR/NDCG scoring.
//   - pulsar_daily_trace.jsonl: the full per-block Stage A/B/D trace,
//     already labelled with keep/drop verdicts and top-story picks.
//
// Usage:
//
//	go run ./dev/fixtures_export -db ./polaris.db -out dev/fixtures
//
// Point -db at a copy of the potato's polaris.db (see CLAUDE.md's
// "Production access" for how to pull one via the named Docker volume) to
// export from real production history instead of local dev data.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type toolEvent struct {
	ThreadID  string
	TurnID    string
	CallID    string
	CreatedAt string
	Args      json.RawMessage
	Result    string
	Error     string
}

type webReadRecord struct {
	URL        string `json:"url"`
	ThreadID   string `json:"thread_id"`
	TurnID     string `json:"turn_id"`
	CreatedAt  string `json:"created_at"`
	Cited      bool   `json:"cited"`
	ResultText string `json:"result_text,omitempty"`
	ToolError  string `json:"tool_error,omitempty"`
	PageFile   string `json:"page_file,omitempty"`
	FetchError string `json:"fetch_error,omitempty"`
}

type searchResultRecord struct {
	Query      string   `json:"query"`
	Provider   string   `json:"provider"`
	Position   int      `json:"position"`
	Title      string   `json:"title"`
	URL        string   `json:"url"`
	RankState  string   `json:"rank_state"`
	Engines    []string `json:"engines"`
	WasClicked bool     `json:"was_clicked"` // web_read'd afterward
	WasCited   bool     `json:"was_cited"`
}

type dailyTraceRecord struct {
	EditionDate       string  `json:"edition_date"`
	BlockKey          string  `json:"block_key"`
	Title             string  `json:"title"`
	StageAContent     string  `json:"stage_a_content"`
	Verdict           string  `json:"verdict"`
	Gist              string  `json:"gist"`
	DiffReasoning     string  `json:"diff_reasoning"`
	Included          bool    `json:"included"`
	IsTopStory        bool    `json:"is_top_story"`
	TopStoryReasoning string  `json:"top_story_reasoning"`
	StageCContent     string  `json:"stage_c_content"`
	Error             string  `json:"error"`
	CostUSD           float64 `json:"cost_usd"`
}

func main() {
	dbPath := flag.String("db", "./polaris.db", "path to a polaris.db (read-only)")
	outDir := flag.String("out", "dev/fixtures", "output directory (gitignored)")
	fetchPages := flag.Bool("fetch-pages", true, "re-fetch raw HTML for web_read URLs")
	fetchTimeout := flag.Duration("fetch-timeout", 15*time.Second, "per-page fetch timeout")
	maxPages := flag.Int("max-pages", 50, "cap on how many unique URLs to re-fetch (0 = no cap, fetch all) — a real live HTTP request per URL, so start small rather than hitting hundreds of sites on every run")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath+"?mode=ro&_foreign_keys=on")
	if err != nil {
		log.Fatalf("opening %s: %v", *dbPath, err)
	}
	defer db.Close()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("creating %s: %v", *outDir, err)
	}

	citedByTurn, err := citedURLsByTurn(db)
	if err != nil {
		log.Fatalf("loading citations: %v", err)
	}

	webReadCount, err := exportWebRead(db, *outDir, citedByTurn, *fetchPages, *fetchTimeout, *maxPages)
	if err != nil {
		log.Fatalf("exporting web_read fixtures: %v", err)
	}
	fmt.Printf("web_read.jsonl: %d records\n", webReadCount)

	searchCount, err := exportSearchRanking(db, *outDir, citedByTurn)
	if err != nil {
		log.Fatalf("exporting search ranking fixtures: %v", err)
	}
	fmt.Printf("search_ranking.jsonl: %d records\n", searchCount)

	traceCount, err := exportPulsarDailyTrace(db, *outDir)
	if err != nil {
		log.Fatalf("exporting pulsar_daily_trace: %v", err)
	}
	fmt.Printf("pulsar_daily_trace.jsonl: %d records\n", traceCount)
}

// citedURLsByTurn maps turn_id -> the set of URLs that turn's assistant
// message actually cited, the implicit "this was useful" label #1 and #3
// in hill-climbing.md both lean on.
func citedURLsByTurn(db *sql.DB) (map[string]map[string]bool, error) {
	rows, err := db.Query(`SELECT turn_id, citations FROM messages WHERE role = 'assistant' AND turn_id != '' AND citations != '[]'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]map[string]bool{}
	for rows.Next() {
		var turnID, citationsJSON string
		if err := rows.Scan(&turnID, &citationsJSON); err != nil {
			return nil, err
		}
		var citations []struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(citationsJSON), &citations); err != nil {
			continue // malformed citations on some old row — skip, not fatal
		}
		set := result[turnID]
		if set == nil {
			set = map[string]bool{}
			result[turnID] = set
		}
		for _, c := range citations {
			set[c.URL] = true
		}
	}
	return result, rows.Err()
}

// matchedToolEvents pairs each source's "tool call started"/"tool call
// finished" events by (thread_id, turn_id, call_id) — the same
// correlation key gateway/turn.go's LogEvent calls use, and the exact join
// project_tool_call_id_correlation_fix.md's bug was about getting right.
func matchedToolEvents(db *sql.DB, source string) ([]toolEvent, error) {
	started, err := loadToolEventData(db, source, "tool call started")
	if err != nil {
		return nil, err
	}
	finished, err := loadToolEventData(db, source, "tool call finished")
	if err != nil {
		return nil, err
	}

	type key struct{ thread, turn, call string }
	finishedByKey := map[key]toolEvent{}
	for _, e := range finished {
		finishedByKey[key{e.ThreadID, e.TurnID, e.CallID}] = e
	}

	var out []toolEvent
	for _, s := range started {
		k := key{s.ThreadID, s.TurnID, s.CallID}
		if f, ok := finishedByKey[k]; ok {
			s.Result = f.Result
			s.Error = f.Error
		}
		out = append(out, s)
	}
	return out, nil
}

func loadToolEventData(db *sql.DB, source, message string) ([]toolEvent, error) {
	rows, err := db.Query(
		`SELECT thread_id, turn_id, created_at, data FROM events WHERE source = ? AND message = ?`,
		source, message,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []toolEvent
	for rows.Next() {
		var threadID sql.NullString
		var turnID, createdAt, dataJSON string
		if err := rows.Scan(&threadID, &turnID, &createdAt, &dataJSON); err != nil {
			return nil, err
		}
		var payload struct {
			Args   json.RawMessage `json:"args"`
			CallID string          `json:"call_id"`
			Result string          `json:"result"`
			Error  string          `json:"error"`
		}
		if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
			continue
		}
		out = append(out, toolEvent{
			ThreadID:  threadID.String,
			TurnID:    turnID,
			CallID:    payload.CallID,
			CreatedAt: createdAt,
			Args:      payload.Args,
			Result:    payload.Result,
			Error:     payload.Error,
		})
	}
	return out, rows.Err()
}

// selectURLsToFetch picks which unique URLs actually get re-fetched when
// maxPages caps the run — a real live HTTP request per URL, so a full
// history's worth (hundreds+) isn't something to default to. Cited URLs go
// first: those are the ones that actually carried an answer-bearing
// sentence, the exact thing extraction-quality scoring needs, so they're
// worth more per fetch than an uncited one.
func selectURLsToFetch(events []toolEvent, citedByTurn map[string]map[string]bool, maxPages int) map[string]bool {
	seen := map[string]bool{}
	var cited, uncited []string
	for _, e := range events {
		var args struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(e.Args, &args); err != nil || args.URL == "" || seen[args.URL] {
			continue
		}
		seen[args.URL] = true
		if citedByTurn[e.TurnID][args.URL] {
			cited = append(cited, args.URL)
		} else {
			uncited = append(uncited, args.URL)
		}
	}

	ordered := append(cited, uncited...)
	if maxPages > 0 && len(ordered) > maxPages {
		ordered = ordered[:maxPages]
	}
	result := make(map[string]bool, len(ordered))
	for _, u := range ordered {
		result[u] = true
	}
	return result
}

func exportWebRead(db *sql.DB, outDir string, citedByTurn map[string]map[string]bool, fetchPages bool, fetchTimeout time.Duration, maxPages int) (int, error) {
	events, err := matchedToolEvents(db, "tool.web_read")
	if err != nil {
		return 0, err
	}

	pagesDir := filepath.Join(outDir, "pages")
	if fetchPages {
		if err := os.MkdirAll(pagesDir, 0o755); err != nil {
			return 0, err
		}
	}

	f, err := os.Create(filepath.Join(outDir, "web_read.jsonl"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	client := &http.Client{Timeout: fetchTimeout}
	fetched := map[string]string{} // url -> page filename, dedup across turns
	toFetch := selectURLsToFetch(events, citedByTurn, maxPages)

	count := 0
	for _, e := range events {
		var args struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(e.Args, &args); err != nil || args.URL == "" {
			continue
		}
		rec := webReadRecord{
			URL:        args.URL,
			ThreadID:   e.ThreadID,
			TurnID:     e.TurnID,
			CreatedAt:  e.CreatedAt,
			Cited:      citedByTurn[e.TurnID][args.URL],
			ResultText: e.Result,
			ToolError:  e.Error,
		}

		if fetchPages && toFetch[args.URL] {
			if filename, ok := fetched[args.URL]; ok {
				rec.PageFile = filename
			} else {
				// fetchPage writes whatever body it got (even a 403's HTML)
				// before checking status, so filename is valid whenever it's
				// non-empty — only a request/network/write failure leaves it
				// blank. Record both: a non-2xx page is still worth keeping
				// (labellable as "blocked", useful for the paywall/empty
				// heuristics in hill-climbing.md's #2), just flagged.
				filename, fetchErr := fetchPage(client, pagesDir, args.URL)
				if filename != "" {
					rec.PageFile = filename
					fetched[args.URL] = filename
				}
				if fetchErr != nil {
					rec.FetchError = fetchErr.Error()
				}
			}
		}

		if err := enc.Encode(rec); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// fetchPage re-fetches url and saves it under pagesDir, named by a
// filesystem-safe hash of the URL rather than the URL itself (query
// strings, slashes, and length limits all make raw URLs unsafe filenames).
func fetchPage(client *http.Client, pagesDir, url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// A generic browser UA — some sites 403 a bare Go http.Client UA
	// outright, which would make every fetch fail identically rather than
	// reflecting the page's actual paywall/JS-only status.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; PolarisFixtureExport/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB cap
	if err != nil {
		return "", err
	}

	filename := fmt.Sprintf("%s.html", urlHash(url))
	if err := os.WriteFile(filepath.Join(pagesDir, filename), body, 0o644); err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return filename, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return filename, nil
}

func urlHash(url string) string {
	// Not cryptographic — just a short, stable, filesystem-safe name.
	var h uint64 = 14695981039346656037
	for i := 0; i < len(url); i++ {
		h ^= uint64(url[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}

func exportSearchRanking(db *sql.DB, outDir string, citedByTurn map[string]map[string]bool) (int, error) {
	// Every URL this DB ever saw web_read'd, regardless of which turn —
	// search ranking's implicit "was this result used" label doesn't need
	// the turn-scoped precision citations do, since a result being clicked
	// at all is already the click-through signal #3 wants.
	events, err := matchedToolEvents(db, "tool.web_read")
	if err != nil {
		return 0, err
	}
	clickedURLs := map[string]bool{}
	citedURLs := map[string]bool{}
	for _, e := range events {
		var args struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(e.Args, &args) == nil && args.URL != "" {
			clickedURLs[args.URL] = true
		}
	}
	for _, set := range citedByTurn {
		for url := range set {
			citedURLs[url] = true
		}
	}

	rows, err := db.Query(`
		SELECT sc.query, sc.provider, r.position, r.title, r.url, r.rank_state, r.engines
		FROM search_cache_results r
		JOIN search_cache sc ON sc.id = r.cache_id
		ORDER BY sc.id, r.position
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	f, err := os.Create(filepath.Join(outDir, "search_ranking.jsonl"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	count := 0
	for rows.Next() {
		var rec searchResultRecord
		var enginesJSON string
		if err := rows.Scan(&rec.Query, &rec.Provider, &rec.Position, &rec.Title, &rec.URL, &rec.RankState, &enginesJSON); err != nil {
			return count, err
		}
		_ = json.Unmarshal([]byte(enginesJSON), &rec.Engines)
		rec.WasClicked = clickedURLs[rec.URL]
		rec.WasCited = citedURLs[rec.URL]
		if err := enc.Encode(rec); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func exportPulsarDailyTrace(db *sql.DB, outDir string) (int, error) {
	rows, err := db.Query(`
		SELECT edition_date, block_key, title, stage_a_content, verdict, gist,
		       diff_reasoning, included, is_top_story, top_story_reasoning,
		       stage_c_content, error, cost_usd
		FROM pulsar_daily_trace
		ORDER BY edition_date, block_key
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	f, err := os.Create(filepath.Join(outDir, "pulsar_daily_trace.jsonl"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	count := 0
	for rows.Next() {
		var rec dailyTraceRecord
		var included, isTopStory int
		if err := rows.Scan(
			&rec.EditionDate, &rec.BlockKey, &rec.Title, &rec.StageAContent,
			&rec.Verdict, &rec.Gist, &rec.DiffReasoning, &included, &isTopStory,
			&rec.TopStoryReasoning, &rec.StageCContent, &rec.Error, &rec.CostUSD,
		); err != nil {
			return count, err
		}
		rec.Included = included != 0
		rec.IsTopStory = isTopStory != 0
		if err := enc.Encode(rec); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}
