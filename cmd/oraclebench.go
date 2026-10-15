package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"polaris/config"
	"polaris/jev"
	"polaris/llm"
	"polaris/models"
	"polaris/oraclebench"
	"polaris/prompts"
)

var (
	obCases       string
	obModels      string
	obAllDecision bool
	obN           int
	obSeed        int64
	obRepeats     int
	obTimeout     time.Duration
	obConcurrency int
	obJudgeModel  string
	obJudgeAudit  float64
	obNoJudge     bool
	obWeights     string
	obOut         string
	obTracking    string
	obLeaderboard string
	obLabel       string
)

var oracleBenchCmd = &cobra.Command{
	Use:   "oracle-bench",
	Short: "Compare decision models on the Oracle-mode task (docs/plans/oracle-benchmark.md)",
	Long: "Runs the committed case bank (oraclebench/cases) through the real gateway.RunOracle code path\n" +
		"once per decision model, scores each model against hand-labeled answers and production's own\n" +
		"firing rules, and prints a leaderboard plus a latency ranking. Use it to pick the model Oracle\n" +
		"runs on instead of the jev-latest alias.\n\n" +
		"Every model sees the same sampled cases. Failures (refusals, timeouts) are recorded as that\n" +
		"model's result, never retried. Scoring weights live in oraclebench/weights.yaml (override with\n" +
		"--weights); the latency budget there (5s) only affects the within-budget score, not what is run.\n\n" +
		"Cost: roughly $0.0002 per decision call, so the whole 20-model roster at 3 repeats is a few\n" +
		"dollars; the judge adds well under one more. A model list or --n keeps a trial run to cents.",
	RunE: runOracleBench,
}

func init() {
	f := oracleBenchCmd.Flags()
	f.StringVar(&configPath, "config", "config.yaml", "path to config.yaml (used only for the OpenRouter key/URL and the judge model)")
	f.StringVar(&obCases, "cases", "oraclebench/cases", "case bank directory")
	f.StringVar(&obModels, "models", "jev-1.13,jev-1.14", "comma-separated decision models (catalog ids, or the short names jev-1.13, jev-1.14, jev-latest)")
	f.BoolVar(&obAllDecision, "all-decisions", false, "run every model in OpenRouter's live decision-model catalog (overrides --models)")
	f.IntVar(&obN, "n", 0, "number of cases to sample (0 = the whole bank)")
	f.Int64Var(&obSeed, "seed", 1, "sampling seed; every model in one run sees the same subset")
	f.IntVar(&obRepeats, "repeats", 3, "identical calls per case; probabilities are averaged (they move +-0.03-0.07 run to run)")
	f.DurationVar(&obTimeout, "timeout", 15*time.Second, "per-call timeout; generous on purpose, latency is scored separately against the weights file's budget")
	f.IntVar(&obConcurrency, "concurrency", 2, "parallel calls; keep it low and fixed, latency measured at different loads isn't comparable")
	f.StringVar(&obJudgeModel, "judge-model", "deepseek", "registry id of the non-decision chat model that judges disputed cases and audits labels")
	f.Float64Var(&obJudgeAudit, "judge-audit", 0.10, "share of ordinary labeled checks the judge spot-checks to measure label quality")
	f.BoolVar(&obNoJudge, "no-judge", false, "skip the judge entirely (deterministic scores only)")
	f.StringVar(&obWeights, "weights", "", "weights file (default: the embedded oraclebench/weights.yaml)")
	f.StringVar(&obOut, "out", "oracle-bench-results.jsonl", "per-call JSONL, written as calls complete")
	f.StringVar(&obTracking, "tracking-db", "oracle-bench.db", "SQLite file accumulating one table per run")
	f.StringVar(&obLeaderboard, "leaderboard", "oracle-bench-leaderboard.json", "where to write the machine-readable leaderboard")
	f.StringVar(&obLabel, "label", "", "free-text note stored with the run")
	rootCmd.AddCommand(oracleBenchCmd)
}

func runOracleBench(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	weights, err := oraclebench.LoadWeights(obWeights)
	if err != nil {
		return err
	}
	bank, err := oraclebench.LoadCases(obCases)
	if err != nil {
		return fmt.Errorf("loading case bank: %w", err)
	}
	cases := oraclebench.SampleCases(bank, obN, obSeed)

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if cfg.OpenRouter.APIKey == "" {
		return fmt.Errorf("no OpenRouter API key configured in %s", configPath)
	}

	modelIDs := oraclebench.ParseModelList(obModels)
	if obAllDecision {
		catalog, err := jev.ListDecisionModels(ctx, cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey)
		if err != nil {
			return fmt.Errorf("reading the live decision-model catalog: %w", err)
		}
		modelIDs = oraclebench.EligibleDecisionModels(catalog)
	}
	if len(modelIDs) == 0 {
		return fmt.Errorf("no models to run: pass --models or --all-decisions")
	}
	var roster []oraclebench.Model
	for _, id := range modelIDs {
		// The client's own timeout sits above the per-call one so the bench's
		// deadline, not the HTTP client's, is what ends a slow call.
		client := jev.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey).WithDecisionsAPI().WithModel(id).WithTimeout(obTimeout + 5*time.Second)
		roster = append(roster, oraclebench.Model{ID: id, Asker: client})
	}

	var judge oraclebench.Judge
	if !obNoJudge {
		jm := cfg.ModelByID(obJudgeModel)
		if jm.ID != obJudgeModel {
			return fmt.Errorf("--judge-model %q is not in the model registry (it resolved to %q); pass --no-judge to skip judging", obJudgeModel, jm.ID)
		}
		falseVal := false
		judge = oraclebench.LLMJudge{
			P: prompts.Get(),
			Client: llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, jm.Model, jm.Temperature, jm.MaxTokens).
				WithProvider(&llm.ProviderRouting{Order: jm.Provider, AllowFallbacks: &falseVal}),
		}
	}

	calls := len(roster) * len(cases) * obRepeats
	fmt.Printf("cases: %d of %d (seed %d), models: %d, repeats: %d, concurrency: %d\n", len(cases), len(bank), obSeed, len(roster), obRepeats, obConcurrency)
	fmt.Printf("about %d calls, roughly $%.3f for the decision calls (judge extra); weights %s, latency budget %s\n\n", calls, float64(calls)*0.0002, weights.Hash, weights.LatencyBudget)

	out, err := os.Create(obOut)
	if err != nil {
		return fmt.Errorf("creating %s: %w", obOut, err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)

	tracking, err := oraclebench.OpenTracking(obTracking)
	if err != nil {
		return err
	}
	defer tracking.Close()

	started := time.Now()
	results, err := oraclebench.Run(ctx, oraclebench.RunConfig{
		Cases: cases, Models: roster, Repeats: obRepeats, Timeout: obTimeout, Concurrency: obConcurrency,
		OnCall: func(ev oraclebench.CallEvent) { _ = enc.Encode(ev.Record()) },
		OnProgress: func(done, total int) {
			if done%25 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "\r%d/%d calls (%s)   ", done, total, time.Since(started).Round(time.Second))
			}
		},
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		// Half a run isn't scored: missing samples would read as Oracle-off.
		return fmt.Errorf("%w (raw calls so far are in %s)", err, obOut)
	}

	sel := oraclebench.DefaultJudgeSelection()
	sel.AuditRate, sel.Seed = obJudgeAudit, uint64(obSeed)
	if judge != nil {
		fmt.Println("judging disputed cases, near-misses, and the audit slice...")
		judgeAll(ctx, judge, sel, weights, results)
	}

	runAt := time.Now().UTC().Format(time.RFC3339)
	run, err := tracking.StartRun(oraclebench.RunMeta{
		RunAt: runAt, Label: obLabel, WeightsHash: weights.Hash, Cases: len(cases), Repeats: obRepeats,
		Concurrency: obConcurrency, Budget: weights.LatencyBudget, Seed: obSeed, JudgeModel: judgeLabel(judge), Models: modelIDs,
	})
	if err != nil {
		return err
	}

	board := oraclebench.Leaderboard{RunAt: runAt, Cases: len(cases), Repeats: obRepeats, Concurrency: obConcurrency, Budget: weights.LatencyBudget, WeightsHash: weights.Hash}
	p := prompts.Get()
	for _, mr := range results {
		if mr.NA != "" {
			board.Entries = append(board.Entries, oraclebench.Entry{Model: mr.Model, NA: mr.NA})
			continue
		}
		for _, cr := range mr.Runs {
			if err := run.RecordCase(mr.Model, mr.ResolvedModels(), cr, oraclebench.ScoreCase(weights, p, cr, 0)); err != nil {
				return fmt.Errorf("recording %s/%s: %w", mr.Model, cr.Case.ID, err)
			}
		}
		board.Entries = append(board.Entries, oraclebench.Entry{Model: mr.Model, Resolved: mr.ResolvedModels(), Score: oraclebench.Summarize(weights, p, mr.Runs)})
	}
	board = oraclebench.BuildLeaderboard(board)
	board.Render(os.Stdout)

	if js, err := board.JSON(); err == nil {
		if err := os.WriteFile(obLeaderboard, js, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", obLeaderboard, err)
		}
		if err := run.SaveLeaderboard(js); err != nil {
			return err
		}
	}
	reportReview(board)
	fmt.Printf("\nraw calls: %s\nleaderboard: %s\ntracking: %s (table %s)\n", obOut, obLeaderboard, obTracking, run.Table)
	return nil
}

func judgeLabel(j oraclebench.Judge) string {
	if j == nil {
		return ""
	}
	return obJudgeModel
}

// judgeAll runs the judge over every model's selected checks with a small
// fixed fan-out. A failed judgment is logged and dropped: that check simply
// keeps its deterministic score.
func judgeAll(ctx context.Context, judge oraclebench.Judge, sel oraclebench.JudgeSelection, w oraclebench.Weights, results []oraclebench.ModelRuns) {
	p := prompts.Get()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	var mu sync.Mutex
	failures := 0
	for mi := range results {
		if results[mi].NA != "" {
			continue
		}
		for ci := range results[mi].Runs {
			wg.Add(1)
			slots <- struct{}{}
			go func(mi, ci int) {
				defer wg.Done()
				defer func() { <-slots }()
				cr := results[mi].Runs[ci]
				got, errs := oraclebench.JudgeCase(ctx, judge, sel, w, p, results[mi].Model, cr)
				mu.Lock()
				defer mu.Unlock()
				results[mi].Runs[ci].Judgments = got
				failures += len(errs)
			}(mi, ci)
		}
	}
	wg.Wait()
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d judge calls failed and were skipped\n", failures)
	}
}

// reportReview tells the operator which cases the judge flagged, since those
// are bank problems to fix, not model results.
func reportReview(board oraclebench.Leaderboard) {
	seen := map[string]bool{}
	var ids []string
	audited, disagreed := 0, 0
	for _, e := range board.Entries {
		audited += e.Score.JudgeAudited
		disagreed += e.Score.JudgeDisagreed
		for _, id := range e.Score.ReviewCases {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if audited > 0 {
		fmt.Printf("\nlabel audit: the judge disagreed with the label on %d of %d spot-checked answers (%.0f%%)\n", disagreed, audited, 100*float64(disagreed)/float64(audited))
	}
	if len(ids) > 0 {
		fmt.Printf("cases worth a human look (judge disagreement or model-better-than-label): %v\n", ids)
	}
}
