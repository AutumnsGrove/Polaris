package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polaris/search"
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
	atlasSearchCmd.Flags().IntVarP(&atlasMaxResults, "max-results", "n", 20, "real-page fetch size requested from the provider (not the same as the 10-result virtual page size results are grouped into)")
	atlasSearchCmd.Flags().IntVarP(&atlasPage, "page", "p", 1, "virtual page to fetch (1-indexed, 10 results each)")
	atlasSearchCmd.Flags().StringVarP(&atlasCategory, "category", "c", "", `SearXNG category filter, e.g. "news" (currently ignored — /api/search always searches general)`)
	atlasSearchCmd.Flags().BoolVarP(&atlasVerbose, "verbose", "v", false, "show which provider answered, cache hit/miss, and how many real pages were fetched (currently unsupported — /api/search doesn't expose this observability)")
	atlasCmd.AddCommand(atlasSearchCmd)
	rootCmd.AddCommand(atlasCmd)
}

func runAtlasSearch(cmd *cobra.Command, args []string) error {
	return runDockerAtlasSearch(strings.Join(args, " "), atlasMaxResults, atlasPage, atlasCategory)
}

// runDockerAtlasSearch is deliberately not runSearch (cmd/search.go):
// that command routes the query through the LLM agent loop (web_search
// as one tool among several, synthesized into an answer) — this one GETs
// the running container's own /api/search (gateway/search.go), the same
// endpoint Atlas's web UI itself calls, and prints the ranked list as-is,
// the terminal equivalent of what Atlas's web UI shows. That endpoint
// doesn't echo back cache/provider observability or support a category
// filter, so --verbose/--category are currently no-ops — the container's
// own logs (`docker compose logs polaris`) are the closest equivalent.
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes))
		return fmt.Errorf("search failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out search.SearchResponse
	if err := readCappedJSON(resp, &out); err != nil {
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
