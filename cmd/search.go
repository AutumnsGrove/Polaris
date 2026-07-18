package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/places"
	"polaris/search"
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
	searchCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml")
	searchCmd.Flags().StringVarP(&searchModel, "model", "m", "", "model id from config.yaml (defaults to default_model)")
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	modelCfg := cfg.ModelByID(searchModel)
	falseVal := false
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: &falseVal})

	searxng := search.NewSearXNGClient(cfg.SearXNG.BaseURL)
	foursquare := places.NewFoursquareClient(cfg.Foursquare.APIKey)

	fmt.Printf("model: %s\n\n", modelCfg.Name)

	emit := func(eventType string, payload map[string]interface{}) {
		switch eventType {
		case "thinking":
			fmt.Printf("\033[2m(thinking) %v\033[0m\n", payload["content"])
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
		Foursquare:      foursquare,
		DefaultLocation: cfg.DefaultLocation,
		LLM:             client,
		Emit:            emit,
	}

	result, err := agent.Run(agentCtx, nil, query)
	if err != nil {
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
