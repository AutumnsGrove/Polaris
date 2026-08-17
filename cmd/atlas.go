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

	"polaris/config"
	"polaris/models"
	"polaris/search"
)

var atlasMaxResults int

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
	atlasSearchCmd.Flags().IntVarP(&atlasMaxResults, "max-results", "n", 8, "maximum number of results")
	atlasCmd.AddCommand(atlasSearchCmd)
	rootCmd.AddCommand(atlasCmd)
}

// runAtlasSearch is deliberately not runSearch (cmd/search.go): that
// command routes the query through the LLM agent loop (web_search as one
// tool among several, synthesized into an answer) — this one calls
// search.SearXNGClient.Search directly and prints the ranked list as-is,
// the terminal equivalent of what Atlas's web UI shows.
func runAtlasSearch(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	// Docker mode: same reasoning as runSearch/runStats — ./config.yaml
	// doesn't correctly describe a Docker install. Route through the
	// running container's own /api/search instead of building a local
	// SearXNGClient against a config/blocklist/domain-rankings file this
	// process can't correctly see under Docker.
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		return runDockerAtlasSearch(query, atlasMaxResults)
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

	searxng := search.NewSearXNGClient(cfg.SearXNG.BaseURL, blocklist).WithDomainRankings(cfg.DomainRankingsFile)

	resp, err := searxng.Search(context.Background(), query, atlasMaxResults, "")
	if err != nil {
		log.Warn("atlas search failed", "query", query, "err", err)
		return fmt.Errorf("search failed: %w", err)
	}

	printAtlasResults(resp.Results)
	return nil
}

// runDockerAtlasSearch is runAtlasSearch's Docker-mode implementation —
// GET the running container's own /api/search (gateway/search.go), the
// same endpoint Atlas's web UI itself calls, instead of duplicating the
// ranking pipeline here.
func runDockerAtlasSearch(query string, maxResults int) error {
	u := fmt.Sprintf("%s/api/search?q=%s&max_results=%d", dockerLocalBaseURL(), url.QueryEscape(query), maxResults)

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
