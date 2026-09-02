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
	benchmarkSuite        string
	benchmarkDataset      string
	benchmarkN            int
	benchmarkSeed         int64
	benchmarkRow          int
	benchmarkModel        string
	benchmarkDB           string
	benchmarkOut          string
	benchmarkTracking     string
	benchmarkDeepResearch bool
)

var benchmarkCmd = &cobra.Command{
	Use:   "benchmark",
	Short: "Run the Polaris harness against a local BrowseComp/SimpleQA/LiveNewsBench sample, graded by an LLM judge",
	Long: "Runs a sample of a published benchmark dataset (--suite: browsecomp, simpleqa, or\n" +
		"livenewsbench) through the Polaris harness and grades each answer with an LLM judge, the same\n" +
		"way the published scores are produced. This is entirely separate from a normal Polaris run:\n" +
		"it opens its own SQLite database (--db) instead of config.yaml's, so it never touches\n" +
		"production stats or the real Brave/Parallel monthly usage caps, and it pins every search to\n" +
		"Brave (no SearXNG/Parallel/Tavily fallback chain) so results come from the same provider on\n" +
		"every question and across repeated runs.\n\n" +
		"You need the dataset file yourself first. browsecomp: download browse_comp_test_set.csv from\n" +
		"https://www.kaggle.com/datasets/openai/browsecomp-a-benchmark-for-browsing-agents (requires\n" +
		"a free Kaggle account). simpleqa: download simple_qa_test_set.csv from\n" +
		"https://openaipublic.blob.core.windows.net/simple-evals/simple_qa_test_set.csv (no account\n" +
		"needed). livenewsbench: download human_verified_test.jsonl from\n" +
		"https://github.com/YunfanZhang42/LiveNewsBench's datasets/<release>/ directory (no account\n" +
		"needed). This command doesn't script any of these downloads — point --dataset at whichever\n" +
		"file you fetched, matching --suite.",
	RunE: runBenchmark,
}

func init() {
	benchmarkCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml (used only for API keys/URLs)")
	benchmarkCmd.Flags().StringVar(&benchmarkSuite, "suite", "browsecomp", fmt.Sprintf("benchmark suite to run: %v", benchmark.SuiteNames()))
	benchmarkCmd.Flags().StringVar(&benchmarkDataset, "dataset", "", "path to the suite's dataset CSV (required) — browsecomp: the encrypted browse_comp_test_set.csv; simpleqa: simple_qa_test_set.csv")
	benchmarkCmd.Flags().IntVar(&benchmarkN, "n", 50, "number of questions to sample")
	benchmarkCmd.Flags().Int64Var(&benchmarkSeed, "seed", 0, "sampling seed — omit for a fresh random subset every run (default), pass an explicit value to reproduce a specific run's subset")
	benchmarkCmd.Flags().IntVar(&benchmarkRow, "row", -1, "run only this one dataset row (0-based index, as printed in results/tracking output) instead of sampling --n questions — for spot-checking or reproducing a single question")
	benchmarkCmd.Flags().BoolVar(&benchmarkDeepResearch, "deep-research", false, "run every question with Deep Research on (the spawn_researchers multi-agent fan-out) instead of the normal single-agent harness — goes straight to spawning sub-agents with no plan-confirmation step, since a benchmark run has no interactive user to confirm with")
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
	suite, err := benchmark.LookupSuite(benchmarkSuite)
	if err != nil {
		return err
	}

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

	// workerClient is only built when --deep-research is on — a dedicated
	// client from config.ResearchWorkerModel, same reasoning as
	// gateway/turn.go's identical wiring: sub-agents get their own
	// (cheaper) model rather than reusing the harness's own `client`.
	var workerClient llm.ChatClient
	if benchmarkDeepResearch {
		workerModelCfg, ok := cfg.ResearchWorkerModel()
		if !ok {
			return fmt.Errorf("--deep-research requires at least one model in the registry (config.ResearchWorkerModel found none)")
		}
		wc := llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, workerModelCfg.Model, workerModelCfg.Temperature, workerModelCfg.MaxTokens).
			WithProvider(&llm.ProviderRouting{Order: workerModelCfg.Provider, AllowFallbacks: &falseVal})
		if rc := workerModelCfg.Reasoning; rc != nil && rc.Enabled {
			trueVal := true
			wc = wc.WithReasoning(&llm.ReasoningParams{Enabled: &trueVal, Effort: rc.Effort, MaxTokens: rc.MaxTokens})
		}
		workerClient = wc
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

	rows, err := suite.LoadDataset(benchmarkDataset)
	if err != nil {
		return fmt.Errorf("loading dataset: %w", err)
	}

	// --row picks exactly one question and skips sampling entirely — for
	// spot-checking a specific question (e.g. against --deep-research)
	// rather than running a whole sample. Mutually exclusive with --n/
	// --seed in effect: the seed is never even computed, since there's
	// nothing to sample.
	var sample []benchmark.Row
	var seed int64
	if cmd.Flags().Changed("row") {
		if benchmarkRow < 0 || benchmarkRow >= len(rows) {
			return fmt.Errorf("--row %d out of range (dataset has %d rows, valid 0-%d)", benchmarkRow, len(rows), len(rows)-1)
		}
		sample = []benchmark.Row{rows[benchmarkRow]}
	} else {
		seed = benchmarkSeed
		if !cmd.Flags().Changed("seed") {
			// Default to a fresh random sample every run — --seed is only for
			// the rare case of wanting to reproduce one specific run's subset
			// later. Printed below either way, so a run you want to repeat is
			// always recoverable by passing this back in as --seed.
			seed = time.Now().UnixNano()
		}
		sample = benchmark.Sample(rows, benchmarkN, seed)
	}

	// One table per invocation (see benchmark.TrackingDB's doc comment) —
	// runAt is fixed once here and reused for every row's implicit
	// membership in this run, rather than recomputed per-question, so a
	// long-running sample can't straddle a table-name-changing second
	// boundary partway through.
	runAt := time.Now().UTC().Format(time.RFC3339)
	run, err := trackingDB.StartRun(runAt, suite.Name(), modelCfg.ID)
	if err != nil {
		return fmt.Errorf("starting tracking run: %w", err)
	}

	if cmd.Flags().Changed("row") {
		fmt.Printf("suite: %s\nmodel: %s\ndataset: %d questions, running row %d only\ndeep research: %v\nbenchmark db: %s\ntracking table: %s\n\n",
			suite.Name(), modelCfg.Name, len(rows), benchmarkRow, benchmarkDeepResearch, benchmarkDB, run.Table)
	} else {
		fmt.Printf("suite: %s\nmodel: %s\ndataset: %d questions, sampled %d (seed %d)\ndeep research: %v\nbenchmark db: %s\ntracking table: %s\n\n",
			suite.Name(), modelCfg.Name, len(rows), len(sample), seed, benchmarkDeepResearch, benchmarkDB, run.Table)
	}

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
			DeepResearch:        benchmarkDeepResearch,
		}
		if benchmarkDeepResearch {
			// No plan-confirmation step here (the doc's "Confirm" flow needs
			// a live user to reply to ask_user_question) — a benchmark run
			// has none, so the orchestrator just calls spawn_researchers
			// directly whenever it decides to, same as it would proceed
			// automatically once a real user approved a plan.
			agentCtx.SpawnResearchers = func(subCtx *tools.Context, tasks []tools.SubAgentTask) []tools.SubAgentReport {
				return agent.SpawnResearchers(subCtx.Ctx, subCtx, workerClient, tasks)
			}
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
			if err := run.Record(record); err != nil {
				fmt.Printf("(tracking-db write failed: %v) ", err)
			}
		}

		result, err := agent.Run(context.Background(), agentCtx, nil, suite.BuildQuery(row.Problem))
		if err != nil {
			fmt.Printf("agent run failed: %v\n", err)
			recordAndLog("error", err.Error())
			_ = enc.Encode(benchmarkResult{DatasetRow: row.Index, Question: row.Problem, ReferenceAnswer: row.Answer, Error: err.Error()})
			continue
		}
		totalCost += result.CostUSD
		record.SubjectCostUSD = result.CostUSD
		record.AssistantTurns = result.TurnCount
		record.ResearchCalls = result.ResearchCalls

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

		graderPrompt := suite.BuildGraderPrompt(row.Problem, result.Answer, row.Answer)
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

		isCorrect, verdict := suite.Grade(graderResp.Content)
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
	fmt.Printf("\naccuracy: %d/%d (%.1f%%)\ntotal cost: $%.4f\nbrave requests this run: %d\nresults written to: %s\ntracking db: %s (table: %s)\n",
		correct, graded, safePct(correct, graded), totalCost, braveUsed, benchmarkOut, benchmarkTracking, run.Table)
	return nil
}

func safePct(correct, graded int) float64 {
	if graded == 0 {
		return 0
	}
	return 100 * float64(correct) / float64(graded)
}
