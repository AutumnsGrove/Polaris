package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/spf13/cobra"

	"polaris/config"
	"polaris/eval"
	"polaris/jev"
	"polaris/llm"
	"polaris/models"
)

var (
	evalCasesDir string
	evalCategory string
	evalN        int
	evalSeed     int64
	evalModel    string
	evalOut      string
)

var evalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Run the cheap prompt/Jev eval harness (docs/plans/hill-climbing-objectives.md) against committed fixture cases",
	Long: "Runs a sample of eval/cases/*.json through their own single-prompt or single-Jev-call\n" +
		"pipeline (see eval/run.go) and reports pass/fail — the cheap, repeatable inner loop\n" +
		"docs/plans/hill-climbing-objectives.md calls for, sitting alongside `polaris benchmark`\n" +
		"rather than replacing it: benchmark runs a full paid agent turn per question; eval runs one\n" +
		"prompt or one Jev call per case, for the checks that don't need a whole turn to score.\n\n" +
		"v1 covers two categories: format (title/suggestions generation, checked by plain code) and\n" +
		"citation_support (Jev graded, the exact Choice shape gateway/verification.go's verifySource\n" +
		"uses live). --category filters to just one; omit it to run everything.",
	RunE: runEval,
}

func init() {
	evalCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml (used only for API keys/URLs)")
	evalCmd.Flags().StringVar(&evalCasesDir, "cases", "eval/cases", "directory of *.json case files")
	evalCmd.Flags().StringVar(&evalCategory, "category", "", "run only this category (format, citation_support); omit to run every case")
	evalCmd.Flags().IntVar(&evalN, "n", 0, "number of cases to sample (0 = run every matching case)")
	evalCmd.Flags().Int64Var(&evalSeed, "seed", 0, "sampling seed — omit for a fresh random subset every run, pass an explicit value to reproduce one")
	evalCmd.Flags().StringVarP(&evalModel, "model", "m", "", "model id for title/suggestions cases (defaults to default_model) — citation_support cases always use Jev regardless of this flag")
	evalCmd.Flags().StringVar(&evalOut, "out", "eval-results.jsonl", "path to write per-case JSONL results")
	rootCmd.AddCommand(evalCmd)
}

func runEval(cmd *cobra.Command, args []string) error {
	allCases, err := eval.LoadCases(evalCasesDir)
	if err != nil {
		return fmt.Errorf("loading cases: %w", err)
	}

	var cases []eval.Case
	for _, c := range allCases {
		if evalCategory == "" || string(c.Category) == evalCategory {
			cases = append(cases, c)
		}
	}
	if len(cases) == 0 {
		return fmt.Errorf("no cases matched --category %q (loaded %d cases total from %s)", evalCategory, len(allCases), evalCasesDir)
	}

	seed := evalSeed
	if !cmd.Flags().Changed("seed") {
		// Default to a fresh random sample every run, same as
		// cmd/benchmark.go's own --seed handling — printed below either
		// way, so a run worth repeating is always recoverable via --seed.
		seed = time.Now().UnixNano()
	}
	if evalN > 0 && evalN < len(cases) {
		r := rand.New(rand.NewSource(seed))
		shuffled := make([]eval.Case, len(cases))
		copy(shuffled, cases)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		cases = shuffled[:evalN]
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	modelCfg := cfg.ModelByID(evalModel)

	falseVal := false
	llmClient := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: &falseVal})

	jevClient := jev.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey)

	outFile, err := os.Create(evalOut)
	if err != nil {
		return fmt.Errorf("creating output file %s: %w", evalOut, err)
	}
	defer outFile.Close()
	enc := json.NewEncoder(outFile)

	fmt.Printf("cases: %d (category=%q, seed=%d)\nmodel: %s\n\n", len(cases), evalCategory, seed, modelCfg.Name)

	var pass, total int
	var totalCost float64
	for i, c := range cases {
		fmt.Printf("[%d/%d] %s (%s/%s) ", i+1, len(cases), c.ID, c.Category, c.Kind)
		result := eval.RunCase(context.Background(), c, llmClient, jevClient)
		total++
		totalCost += result.CostUSD
		if result.ErrMessage != "" {
			fmt.Printf("ERROR: %s\n", result.ErrMessage)
		} else if result.Pass {
			pass++
			fmt.Printf("PASS\n")
		} else {
			fmt.Printf("FAIL: %s\n", result.Reason)
		}
		if err := enc.Encode(result); err != nil {
			return fmt.Errorf("writing result for %s: %w", c.ID, err)
		}
	}

	fmt.Printf("\npass: %d/%d (%.1f%%)\ntotal cost: $%.4f\nresults written to: %s\n",
		pass, total, safePct(pass, total), totalCost, evalOut)
	return nil
}
