package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polaris/agent"
	"polaris/config"
	"polaris/gateway"
	"polaris/llm"
	"polaris/models"
	"polaris/parallel"
	"polaris/places"
	"polaris/search"
	"polaris/store"
	"polaris/tavily"
	"polaris/tools"
)

var searchModel string

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Run a search-augmented query straight from the terminal, no web UI needed",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearch,
}

func init() {
	searchCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml (bare-metal only — a Docker install queries the running container instead)")
	searchCmd.Flags().StringVarP(&searchModel, "model", "m", "", "model id (defaults to default_model)")
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	// Docker mode: same reasoning as runStats — ./config.yaml doesn't
	// correctly describe a Docker install (missing, or worse, stale).
	// Route the query through the running container's own /api/ask
	// instead, the exact same synchronous endpoint any other
	// programmatic caller uses.
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		return runDockerSearch(query, searchModel)
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		log.Warn("loading config failed", "path", configPath, "err", err)
		return err
	}

	modelCfg := cfg.ModelByID(searchModel)
	falseVal := false
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: &falseVal})
	if rc := modelCfg.Reasoning; rc != nil && rc.Enabled {
		trueVal := true
		client = client.WithReasoning(&llm.ReasoningParams{Enabled: &trueVal, Effort: rc.Effort, MaxTokens: rc.MaxTokens})
	}

	blocklist, err := search.LoadBlocklist(cfg.BlockedSourcesFile)
	if err != nil {
		log.Warn("loading source blocklist failed, continuing with no blocked sources", "path", cfg.BlockedSourcesFile, "err", err)
		blocklist = nil
	}

	searxng := search.NewSearXNGClient(cfg.SearXNG.BaseURL, blocklist).WithDomainRankings(cfg.DomainRankingsFile)
	foursquare := places.NewFoursquareClient(cfg.Foursquare.APIKey)
	tavilyClient := tavily.NewClient(cfg.Tavily.APIKey)
	parallelClient := parallel.NewClient(cfg.Parallel.APIKey)

	// Opened even for this one-shot CLI command specifically so
	// web_search's Parallel monthly-usage cap (see tools/web_search.go)
	// is checked against the *same* running total the web UI/assistant
	// use, not a separate count that would let this path silently spend
	// past the real cap — cmd/stats.go opens the same DB the same way
	// for the same "one-shot CLI command still needs real state" reason.
	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		log.Warn("opening database failed, Parallel's usage cap can't be enforced — disabling that fallback for this run", "path", cfg.Database.Path, "err", err)
		parallelClient = nil
	} else {
		defer db.Close()
	}

	fmt.Printf("model: %s\n\n", modelCfg.Name)

	emit := func(eventType string, payload map[string]interface{}) {
		switch eventType {
		case "thinking":
			fmt.Printf("\033[2m(thinking) %v\033[0m\n", payload["content"])
		case "reasoning":
			fmt.Printf("\033[2m%v\033[0m", payload["content"])
		case "tool_call":
			tool, _ := payload["tool"].(string)
			toolArgs, _ := payload["args"].(map[string]interface{})
			switch tool {
			case "web_search":
				fmt.Printf("searching: %v\n", toolArgs["query"])
			case "web_read":
				fmt.Printf("reading: %v\n", toolArgs["url"])
			case "nearby_search":
				fmt.Printf("finding nearby: %v near %v\n", toolArgs["query"], toolArgs["location"])
			}
		case "token":
			fmt.Print(payload["content"])
		}
	}

	agentCtx := &tools.Context{
		SearXNG:         searxng,
		Blocklist:       blocklist,
		Foursquare:      foursquare,
		Tavily:          tavilyClient,
		Parallel:        parallelClient,
		GitHubToken:     cfg.GitHub.Token,
		LastFMAPIKey:    cfg.LastFM.APIKey,
		HardcoverAPIKey: cfg.Hardcover.APIKey,
		TMDBAPIKey:      cfg.TMDB.APIKey,
		DefaultLocation: cfg.DefaultLocation,
		LLM:             client,
		Emit:            emit,
		MaxTurns:        cfg.MaxAgentTurns,
	}
	// Only set once db is known-good (see the store.Open error handling
	// above, which also nils out parallelClient on failure) — handleWebSearch
	// requires both Context.Parallel and these closures non-nil before
	// ever calling Parallel, so leaving them nil here is enough to keep
	// this path safe without a nil-db guard inside the closures themselves.
	if db != nil {
		agentCtx.ParallelUsageThisMonth = func() (int, error) { return db.GetAPIUsage("parallel") }
		agentCtx.IncrementParallelUsage = func() error { _, err := db.IncrementAPIUsage("parallel"); return err }
	}

	result, err := agent.Run(context.Background(), agentCtx, nil, query)
	if err != nil {
		log.Warn("agent run failed", "query", query, "err", err)
		return fmt.Errorf("agent run failed: %w", err)
	}

	fmt.Println()
	if len(result.Citations) > 0 {
		fmt.Println("\nSources:")
		for _, c := range result.Citations {
			fmt.Printf("  - %s (%s)\n", c.Title, c.URL)
		}
	}
	fmt.Printf("\ncost: $%.5f\n", result.CostUSD)
	return nil
}

// runDockerSearch is runSearch's Docker-mode implementation: POST the
// query to the running container's own /api/ask (the same synchronous,
// non-streaming endpoint any programmatic caller uses — see
// gateway/ask.go's doc comment) instead of building a local agent.Run
// call against a config/SearXNG/store this process can't correctly see
// under Docker. No live "thinking"/tool-call progress lines here,
// unlike the bare-metal path above — /api/ask blocks until the whole
// turn finishes, so there's genuinely nothing to print until then.
func runDockerSearch(query, model string) error {
	reqBody, err := json.Marshal(gateway.AskRequest{Content: query, Model: model, Source: "cli"})
	if err != nil {
		return err
	}

	url := dockerLocalBaseURL() + "/api/ask"
	// A real research turn (several search/read rounds) can legitimately
	// take a couple of minutes — same reasoning as runDockerModeCall's
	// generous timeout.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error != "" {
			return fmt.Errorf("%s", errBody.Error)
		}
		return fmt.Errorf("query failed (status %d)", resp.StatusCode)
	}

	var ask gateway.AskResponse
	if err := json.NewDecoder(resp.Body).Decode(&ask); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}

	fmt.Println(ask.Answer)
	if len(ask.Citations) > 0 {
		fmt.Println("\nSources:")
		for _, c := range ask.Citations {
			fmt.Printf("  - %s (%s)\n", c.Title, c.URL)
		}
	}
	fmt.Printf("\ncost: $%.5f\n", ask.CostUSD)
	return nil
}
