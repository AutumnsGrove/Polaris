package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polaris/agent"
	"polaris/benchmark"
	"polaris/brave"
	"polaris/config"
	"polaris/embed"
	"polaris/llm"
	"polaris/models"
	"polaris/store"
	"polaris/tools"
)

var (
	benchmarkDataset  string
	benchmarkN        int
	benchmarkSeed     int64
	benchmarkModel    string
	benchmarkDB       string
	benchmarkOut      string
	benchmarkTracking string
)

var benchmarkCmd = &cobra.Command{
	Use:   "benchmark",
	Short: "Run the Polaris harness against a local BrowseComp sample, graded by an LLM judge",
	Long: "Runs a sample of OpenAI's BrowseComp dataset (openai/simple-evals) through the Polaris\n" +
		"harness and grades each answer with an LLM judge, the same way the published BrowseComp\n" +
		"scores are produced. This is entirely separate from a normal Polaris run: it opens its own\n" +
		"SQLite database (--db) instead of config.yaml's, so it never touches production stats or\n" +
		"the real Brave/Parallel monthly usage caps, and it pins every search to Brave (no SearXNG/\n" +
		"Parallel/Tavily fallback chain) so results come from the same provider on every question and\n" +
		"across repeated runs.\n\n" +
		"You need the dataset file yourself first — download browse_comp_test_set.csv from\n" +
		"https://www.kaggle.com/datasets/openai/browsecomp-a-benchmark-for-browsing-agents (requires\n" +
		"a free Kaggle account; this command doesn't script that download) and point --dataset at it.",
	RunE: runBenchmark,
}

func init() {
	benchmarkCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml (used only for API keys/URLs)")
	benchmarkCmd.Flags().StringVar(&benchmarkDataset, "dataset", "", "path to the (encrypted) browse_comp_test_set.csv (required)")
	benchmarkCmd.Flags().IntVar(&benchmarkN, "n", 50, "number of questions to sample")
	benchmarkCmd.Flags().Int64Var(&benchmarkSeed, "seed", 0, "sampling seed — omit for a fresh random subset every run (default), pass an explicit value to reproduce a specific run's subset")
	benchmarkCmd.Flags().StringVarP(&benchmarkModel, "model", "m", "", "model id, used for BOTH the harness under test and the LLM grader (defaults to default_model)")
	benchmarkCmd.Flags().StringVar(&benchmarkDB, "db", "polaris-benchmark.db", "path to the isolated benchmark database (never the production one)")
	benchmarkCmd.Flags().StringVar(&benchmarkOut, "out", "benchmark-results.jsonl", "path to write per-question JSONL results")
	benchmarkCmd.Flags().StringVar(&benchmarkTracking, "tracking-db", "benchmark-tracking.db", "persistent SQLite DB of per-question outcomes across every run (accumulates — never wiped, unlike --db)")
	if err := benchmarkCmd.MarkFlagRequired("dataset"); err != nil {
		panic(err)
	}
	rootCmd.AddCommand(benchmarkCmd)
}

// benchmarkResult is one line of the --out JSONL file. Kept alongside
// (not instead of) --tracking-db's SQLite record: this file carries the
// full text (question/answer/grader reasoning) for reading a single run
// by eye, while the tracking DB deliberately omits that text and
// accumulates across every run for querying trends. DatasetRow ties a
// line here back to its tracking-DB row.
type benchmarkResult struct {
	DatasetRow      int     `json:"dataset_row"`
	Question        string  `json:"question"`
	ReferenceAnswer string  `json:"reference_answer"`
	HarnessAnswer   string  `json:"harness_answer"`
	Correct         bool    `json:"correct"`
	Verdict         string  `json:"verdict"`
	GraderReasoning string  `json:"grader_reasoning,omitempty"`
	SubjectCostUSD  float64 `json:"subject_cost_usd"`
	GraderCostUSD   float64 `json:"grader_cost_usd"`
	AssistantTurns  int     `json:"assistant_turns"`
	BraveCalls      int     `json:"brave_calls"`
	Error           string  `json:"error,omitempty"`
}

func runBenchmark(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	modelCfg := cfg.ModelByID(benchmarkModel)

	falseVal := false
	client := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: &falseVal})
	if rc := modelCfg.Reasoning; rc != nil && rc.Enabled {
		trueVal := true
		client = client.WithReasoning(&llm.ReasoningParams{Enabled: &trueVal, Effort: rc.Effort, MaxTokens: rc.MaxTokens})
	}

	if cfg.Brave.APIKey == "" {
		return fmt.Errorf("brave.api_key is not set in %s — the benchmark pins search to Brave, so it's a hard requirement", configPath)
	}
	braveClient := brave.NewClient(cfg.Brave.APIKey)
	// nil (feature disabled) unless ollama.base_url is set in config.yaml —
	// this is exactly the harness meant to validate/tune
	// querySimilarityThreshold/querySimilarityStreakThreshold (see
	// agent/query_similarity.go) against real BrowseComp runs before
	// trusting them as tightly as the citation-based signals.
	embedClient := embed.NewClient(cfg.Ollama.BaseURL, cfg.Ollama.EmbedModel)

	// Deliberately NOT cfg.Database.Path — see the command's Long
	// description. A separate DB file means gateway/stats.go's
	// aggregation (which only ever reads whatever file store.Open was
	// given) can't see a single row this run writes, and Brave usage
	// recorded below never touches the production monthly cap.
	db, err := store.Open(benchmarkDB)
	if err != nil {
		return fmt.Errorf("opening benchmark database %s: %w", benchmarkDB, err)
	}
	defer db.Close()

	trackingDB, err := benchmark.OpenTracking(benchmarkTracking)
	if err != nil {
		return fmt.Errorf("opening tracking database %s: %w", benchmarkTracking, err)
	}
	defer trackingDB.Close()

	rows, err := benchmark.LoadDataset(benchmarkDataset)
	if err != nil {
		return fmt.Errorf("loading dataset: %w", err)
	}
	seed := benchmarkSeed
	if !cmd.Flags().Changed("seed") {
		// Default to a fresh random sample every run — --seed is only for
		// the rare case of wanting to reproduce one specific run's subset
		// later. Printed below either way, so a run you want to repeat is
		// always recoverable by passing this back in as --seed.
		seed = time.Now().UnixNano()
	}
	sample := benchmark.Sample(rows, benchmarkN, seed)
	fmt.Printf("model: %s\ndataset: %d questions, sampled %d (seed %d)\nbenchmark db: %s\n\n",
		modelCfg.Name, len(rows), len(sample), seed, benchmarkDB)

	outFile, err := os.Create(benchmarkOut)
	if err != nil {
		return fmt.Errorf("creating output file %s: %w", benchmarkOut, err)
	}
	defer outFile.Close()
	enc := json.NewEncoder(outFile)

	var correct, graded int
	var totalCost float64

	for i, row := range sample {
		fmt.Printf("[%d/%d] (row %d) ", i+1, len(sample), row.Index)

		// Brave calls are only ever incremented (never per-question reset)
		// against this run's single isolated db — a before/after delta on
		// the running total is the only way to attribute calls to THIS
		// question specifically, rather than the run as a whole.
		braveBefore, _ := db.GetAPIUsage("brave")
		record := benchmark.RunRecord{
			RunAt:      time.Now().UTC().Format(time.RFC3339),
			Model:      modelCfg.ID,
			DatasetRow: row.Index,
		}

		agentCtx := &tools.Context{
			Ctx:            context.Background(),
			Brave:          braveClient,
			PinnedProvider: "brave",
			Embed:          embedClient,
			LLM:            client,
			MaxTurns:       cfg.MaxAgentTurns,
			Emit: func(eventType string, payload map[string]interface{}) {
				// agent_nudge is the only event this harness surfaces —
				// check-in/stale-streak/query-similarity/max-turns-wrapup
				// firing (or NOT firing) is exactly what a benchmark run
				// is for validating; everything else (tokens, individual
				// tool calls) is already visible via the search-provider
				// log lines tools/web_search.go emits on its own.
				if eventType != "agent_nudge" {
					return
				}
				args, _ := payload["args"].(map[string]interface{})
				fmt.Printf("  [nudge] %v (call/streak=%v, citations=%v)\n", args["kind"], args["call_count"], args["citation_count"])
			},
			BraveUsageThisMonth: func() (int, error) { return db.GetAPIUsage("brave") },
			IncrementBraveUsage: func() error { _, err := db.IncrementAPIUsage("brave"); return err },
		}

		// recordAndLog fills in whatever RunRecord fields are known so far
		// (brave_calls via the before/after delta, always computable) and
		// writes the tracking-DB row — called from every exit point below,
		// success or failure, so the tracking DB's history is complete
		// even for questions that never made it to grading.
		recordAndLog := func(verdict, errMsg string) {
			braveAfter, _ := db.GetAPIUsage("brave")
			record.BraveCalls = braveAfter - braveBefore
			record.Verdict = verdict
			record.Error = errMsg
			if err := trackingDB.Record(record); err != nil {
				fmt.Printf("(tracking-db write failed: %v) ", err)
			}
		}

		result, err := agent.Run(context.Background(), agentCtx, nil, benchmark.BuildQuery(row.Problem))
		if err != nil {
			fmt.Printf("agent run failed: %v\n", err)
			recordAndLog("error", err.Error())
			_ = enc.Encode(benchmarkResult{DatasetRow: row.Index, Question: row.Problem, ReferenceAnswer: row.Answer, Error: err.Error()})
			continue
		}
		totalCost += result.CostUSD
		record.SubjectCostUSD = result.CostUSD
		record.AssistantTurns = result.TurnCount

		// agent.Run can legitimately return a nil error with an empty
		// Answer — the model exhausted every retry inside a turn and its
		// forced wrap-up call also came back empty (see driver.go's
		// doc comment on why the wrap-up path doesn't fabricate
		// placeholder text). gateway/turn.go has its own check for exactly
		// this; the benchmark needs the same one, since grading "" against
		// a real reference answer would otherwise silently record it as a
		// normal (if wrong) graded answer instead of the harness failure
		// it actually is.
		if strings.TrimSpace(result.Answer) == "" {
			fmt.Printf("empty answer (model exhausted its turn budget without producing one)\n")
			recordAndLog("empty", "empty answer")
			_ = enc.Encode(benchmarkResult{DatasetRow: row.Index, Question: row.Problem, ReferenceAnswer: row.Answer, SubjectCostUSD: result.CostUSD, AssistantTurns: result.TurnCount, Error: "empty answer"})
			continue
		}

		graderPrompt := benchmark.BuildGraderPrompt(row.Problem, result.Answer, row.Answer)
		graderResp, err := client.ChatCompletionStreaming(context.Background(),
			[]llm.ChatMessage{{Role: "user", Content: graderPrompt}}, nil, nil)
		if err != nil {
			fmt.Printf("grading failed: %v\n", err)
			recordAndLog("error", err.Error())
			_ = enc.Encode(benchmarkResult{DatasetRow: row.Index, Question: row.Problem, ReferenceAnswer: row.Answer, HarnessAnswer: result.Answer, SubjectCostUSD: result.CostUSD, AssistantTurns: result.TurnCount, Error: err.Error()})
			continue
		}
		totalCost += graderResp.CostUSD
		record.GraderCostUSD = graderResp.CostUSD

		isCorrect, verdict := benchmark.Grade(graderResp.Content)
		graded++
		if isCorrect {
			correct++
		}
		fmt.Printf("correct: %s (running: %d/%d)\n", verdict, correct, graded)
		record.Correct = &isCorrect
		recordAndLog(verdict, "")

		_ = enc.Encode(benchmarkResult{
			DatasetRow:      row.Index,
			Question:        row.Problem,
			ReferenceAnswer: row.Answer,
			HarnessAnswer:   result.Answer,
			Correct:         isCorrect,
			Verdict:         verdict,
			GraderReasoning: graderResp.Content,
			SubjectCostUSD:  result.CostUSD,
			GraderCostUSD:   graderResp.CostUSD,
			AssistantTurns:  result.TurnCount,
			BraveCalls:      record.BraveCalls,
		})
	}

	braveUsed, _ := db.GetAPIUsage("brave")
	fmt.Printf("\naccuracy: %d/%d (%.1f%%)\ntotal cost: $%.4f\nbrave requests this run: %d\nresults written to: %s\ntracking db: %s\n",
		correct, graded, safePct(correct, graded), totalCost, braveUsed, benchmarkOut, benchmarkTracking)
	return nil
}

func safePct(correct, graded int) float64 {
	if graded == 0 {
		return 0
	}
	return 100 * float64(correct) / float64(graded)
}
