package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polaris/brave"
	"polaris/config"
	"polaris/gateway"
	"polaris/models"
	"polaris/search"
	"polaris/store"
)

var atlasMaxResults int
var atlasPage int
var atlasCategory string
var atlasVerbose bool

// atlasCmd is a parent for Atlas-specific subcommands — just `search` for
// now, but keeping it as its own namespace (rather than a top-level
// `polaris atlas-search`) leaves room for `polaris atlas rank <domain>` or
// similar later without a breaking rename.
var atlasCmd = &cobra.Command{
	Use:   "atlas",
	Short: "Atlas: the local search frontend (raw ranked results, not the LLM assistant)",
}

var atlasSearchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Run an Atlas search from the terminal — ranked results, no LLM synthesis",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAtlasSearch,
}

func init() {
	atlasSearchCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml (bare-metal only — a Docker install queries the running container instead)")
	atlasSearchCmd.Flags().IntVarP(&atlasMaxResults, "max-results", "n", 20, "real-page fetch size requested from the provider (not the same as the 10-result virtual page size results are grouped into)")
	atlasSearchCmd.Flags().IntVarP(&atlasPage, "page", "p", 1, "virtual page to fetch (1-indexed, 10 results each)")
	atlasSearchCmd.Flags().StringVarP(&atlasCategory, "category", "c", "", `SearXNG category filter, e.g. "news" (bare-metal only — ignored under Docker, which always searches general)`)
	atlasSearchCmd.Flags().BoolVarP(&atlasVerbose, "verbose", "v", false, "show which provider answered, cache hit/miss, and how many real pages were fetched (bare-metal only)")
	atlasCmd.AddCommand(atlasSearchCmd)
	rootCmd.AddCommand(atlasCmd)
}

// runAtlasSearch is deliberately not runSearch (cmd/search.go): that
// command routes the query through the LLM agent loop (web_search as one
// tool among several, synthesized into an answer) — this one calls
// gateway.ResolveAtlasSearch directly and prints the ranked list as-is,
// the terminal equivalent of what Atlas's web UI shows. Bare-metal shares
// the exact same resolver (SearXNG → cache → Brave fallback, virtual
// pagination) the HTTP endpoint uses — see gateway/atlas_search.go — so
// this command is a real way to exercise and observe that logic from the
// terminal, not a separate reimplementation that could drift from what
// the browser actually sees.
func runAtlasSearch(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	// Docker mode: same reasoning as runSearch/runStats — ./config.yaml
	// doesn't correctly describe a Docker install. Route through the
	// running container's own /api/search instead of building local
	// clients against a config/blocklist/domain-rankings file this
	// process can't correctly see under Docker. That endpoint doesn't
	// echo back cache/provider observability (see handleSearch), so
	// --verbose has no effect here — the container's own logs
	// (`docker compose logs polaris`) are the equivalent view under Docker.
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		return runDockerAtlasSearch(query, atlasMaxResults, atlasPage, atlasCategory)
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		log.Warn("loading config failed", "path", configPath, "err", err)
		return err
	}

	blocklist, err := search.LoadBlocklist(cfg.BlockedSourcesFile)
	if err != nil {
		log.Warn("loading source blocklist failed, continuing with no blocked sources", "path", cfg.BlockedSourcesFile, "err", err)
		blocklist = nil
	}

	searxngClient := search.NewSearXNGClient(cfg.SearXNG.BaseURL, blocklist).WithDomainRankings(cfg.DomainRankingsFile)
	braveClient := brave.NewClient(cfg.Brave.APIKey)

	// Opened for the same reason cmd/search.go opens one: the cache and
	// Brave's monthly-usage cap both live in the DB, and this one-shot
	// CLI command needs to share the *same* running state the web UI
	// does (same cache entries, same usage count against the same cap),
	// not a separate view that could let this path silently overspend.
	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		log.Warn("opening database failed, search caching and Brave's usage cap can't be enforced — disabling the Brave fallback for this run", "path", cfg.Database.Path, "err", err)
		braveClient = nil
	} else {
		defer db.Close()
	}
	if db == nil {
		return fmt.Errorf("search caching requires a database — opening %s failed", cfg.Database.Path)
	}

	resp, meta, err := gateway.ResolveAtlasSearch(context.Background(), db, searxngClient, braveClient, query, atlasCategory, atlasPage, atlasMaxResults)
	if err != nil {
		log.Warn("atlas search failed", "query", query, "err", err)
		return fmt.Errorf("search failed: %w", err)
	}

	if atlasVerbose {
		printAtlasMeta(resp, meta)
	}
	printAtlasResults(resp.Results)
	return nil
}

// printAtlasMeta shows the observability gateway.ResolveAtlasSearch
// returns but the JSON API itself doesn't expose — provider/cache/
// real-page mechanics are internal resolution detail, not part of the
// SearXNG-compatible search.SearchResponse shape a browser or another
// programmatic caller actually needs.
func printAtlasMeta(resp *search.SearchResponse, meta *gateway.AtlasSearchMeta) {
	cacheState := "MISS (live fetch)"
	if meta.CacheHit {
		cacheState = "HIT"
	}
	fmt.Printf("[provider=%s cache=%s real_pages=%d live_fetches=%d page=%d has_more=%v]\n",
		meta.Provider, cacheState, meta.RealPagesFetched, meta.LiveFetches, resp.Page, resp.HasMore)
	if resp.Degraded {
		fmt.Printf("[degraded — unresponsive engines: %s]\n", strings.Join(resp.UnresponsiveEngines, ", "))
	}
	fmt.Println()
}

// runDockerAtlasSearch is runAtlasSearch's Docker-mode implementation —
// GET the running container's own /api/search (gateway/search.go), the
// same endpoint Atlas's web UI itself calls, instead of duplicating the
// ranking pipeline here.
func runDockerAtlasSearch(query string, maxResults, page int, category string) error {
	u := fmt.Sprintf("%s/api/search?q=%s&max_results=%d&page=%d", dockerLocalBaseURL(), url.QueryEscape(query), maxResults, page)
	if category != "" {
		u += "&category=" + url.QueryEscape(category)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", u, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("search failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out search.SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", u, err)
	}

	printAtlasResults(out.Results)
	return nil
}

func printAtlasResults(results []search.SearchResult) {
	if len(results) == 0 {
		fmt.Println("no results")
		return
	}
	for i, r := range results {
		fmt.Printf("%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Content != "" {
			fmt.Printf("   %s\n", r.Content)
		}
		var meta []string
		if r.Engine != "" {
			meta = append(meta, "via "+r.Engine)
		}
		if r.RankState != "" && r.RankState != "default" {
			meta = append(meta, r.RankState)
		}
		if len(meta) > 0 {
			fmt.Printf("   [%s]\n", strings.Join(meta, ", "))
		}
		fmt.Println()
	}
}
