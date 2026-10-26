package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"polaris/config"
	"polaris/decisions"
	"polaris/llm"
	"polaris/models"
	"polaris/oraclebench"
	"polaris/prompts"
)

var (
	obCases        string
	obModels       string
	obAllDecision  bool
	obN            int
	obSeed         int64
	obRepeats      int
	obTimeout      time.Duration
	obConcurrency  int
	obFreeInterval time.Duration
	obJudgeModel   string
	obJudgeAudit   float64
	obNoJudge      bool
	obWeights      string
	obOut          string
	obTracking     string
	obSaveDir      string
	obLabel        string
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
	f.DurationVar(&obFreeInterval, "free-interval", 3500*time.Millisecond, "minimum gap between calls to a :free model (OpenRouter allows about 20/min across all free models; 0 disables pacing)")
	f.StringVar(&obJudgeModel, "judge-model", "deepseek", "registry id of the non-decision chat model that judges disputed cases and audits labels")
	f.Float64Var(&obJudgeAudit, "judge-audit", 0.10, "share of ordinary labeled checks the judge spot-checks to measure label quality")
	f.BoolVar(&obNoJudge, "no-judge", false, "skip the judge entirely (deterministic scores only)")
	f.StringVar(&obWeights, "weights", "", "weights file (default: the embedded oraclebench/weights.yaml)")
	f.StringVar(&obOut, "out", "", "per-call JSONL, written as calls complete (default: "+oraclebench.DefaultResultsDir+"/<run>-calls.jsonl, gitignored)")
	f.StringVar(&obTracking, "tracking-db", "", "SQLite file accumulating one table per run (default: "+oraclebench.DefaultResultsDir+"/oracle-bench.db, gitignored)")
	f.StringVar(&obSaveDir, "save-dir", oraclebench.DefaultSaveDir, "where the finished leaderboard (JSON + printed text) is saved, timestamped and never overwritten; meant to be committed")
	f.StringVar(&obLabel, "label", "", "free-text note stored with the run and used in the saved filenames")
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
		catalog, err := decisions.ListDecisionModels(ctx, cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey)
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
		client := decisions.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey).WithDecisionsAPI().WithModel(id).WithMaxQuestions(oraclebench.MaxQuestionsFor(id)).WithTimeout(obTimeout + 5*time.Second)
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
	fmt.Printf("about %d calls, roughly $%.3f for the decision calls (judge extra); weights %s, latency budget %s\n", calls, float64(calls)*0.0002, weights.Hash, weights.LatencyBudget)
	// Pacing fixes the per-minute limit but not the daily one (1000 free requests
	// for a funded key), so say up front when this run can't fit under it.
	freeCalls := 0
	for _, m := range roster {
		if oraclebench.IsFreeModel(m.ID) {
			freeCalls += len(cases) * obRepeats
		}
	}
	if freeCalls > 0 {
		fmt.Printf("%d calls go to :free models, paced %s apart (about %s). OpenRouter's daily free-model cap is 1000 requests per key, shared by every :free model.\n", freeCalls, obFreeInterval, (time.Duration(freeCalls) * obFreeInterval).Round(time.Minute))
	}
	fmt.Println()

	// Everything this run saves is named from its start time, fixed once here, so
	// the raw calls, scores, leaderboard and tracking table all line up.
	runAt := time.Now().UTC().Format(time.RFC3339)
	base := oraclebench.BaseName(runAt, obLabel)
	if err := os.MkdirAll(oraclebench.DefaultResultsDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", oraclebench.DefaultResultsDir, err)
	}
	if obOut == "" {
		obOut = filepath.Join(oraclebench.DefaultResultsDir, base+"-calls.jsonl")
	}
	if obTracking == "" {
		obTracking = filepath.Join(oraclebench.DefaultResultsDir, "oracle-bench.db")
	}

	out, err := os.OpenFile(obOut, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("creating %s (an existing result is never overwritten): %w", obOut, err)
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
		Cases: cases, Models: roster, Repeats: obRepeats, Timeout: obTimeout, Concurrency: obConcurrency, FreeInterval: obFreeInterval,
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

	// Score everything first, then save in order of importance: the committed
	// leaderboard, then the per-case scores, then the tracking DB. A failure in a
	// later, bulkier step must never cost the result that matters most.
	board := oraclebench.Leaderboard{RunAt: runAt, Cases: len(cases), Repeats: obRepeats, Concurrency: obConcurrency, Budget: weights.LatencyBudget, WeightsHash: weights.Hash}
	p := prompts.Get()
	type caseRow struct {
		Model string `json:"model"`
		oraclebench.CaseScore
	}
	var rows []caseRow
	for _, mr := range results {
		if mr.NA != "" {
			board.Entries = append(board.Entries, oraclebench.Entry{Model: mr.Model, NA: mr.NA})
			continue
		}
		for _, cr := range mr.Runs {
			rows = append(rows, caseRow{mr.Model, oraclebench.ScoreCase(weights, p, cr, 0)})
		}
		board.Entries = append(board.Entries, oraclebench.Entry{Model: mr.Model, Resolved: mr.ResolvedModels(), Score: oraclebench.Summarize(weights, p, mr.Runs)})
	}
	board = oraclebench.BuildLeaderboard(board)

	saved, saveErr := oraclebench.SaveResults(obSaveDir, runAt, obLabel, board)
	board.Render(os.Stdout)
	reportReview(board)
	if saveErr != nil {
		// Still print and continue to the other saves, but fail the command: a run
		// whose headline result wasn't saved is not a finished run.
		fmt.Fprintf(os.Stderr, "\nERROR: the leaderboard was NOT saved: %v\n", saveErr)
	}

	scoresPath := filepath.Join(oraclebench.DefaultResultsDir, base+"-scores.jsonl")
	if f, err := os.OpenFile(scoresPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: per-case scores not saved: %v\n", err)
	} else {
		senc := json.NewEncoder(f)
		for _, r := range rows {
			_ = senc.Encode(r)
		}
		f.Close()
	}

	tableName := "(not recorded)"
	run, trackErr := tracking.StartRun(oraclebench.RunMeta{
		RunAt: runAt, Label: obLabel, WeightsHash: weights.Hash, Cases: len(cases), Repeats: obRepeats,
		Concurrency: obConcurrency, Budget: weights.LatencyBudget, Seed: obSeed, JudgeModel: judgeLabel(judge), Models: modelIDs,
	})
	if trackErr == nil {
		tableName = run.Table
		for _, mr := range results {
			if mr.NA != "" {
				continue
			}
			for _, cr := range mr.Runs {
				if err := run.RecordCase(mr.Model, mr.ResolvedModels(), cr, oraclebench.ScoreCase(weights, p, cr, 0)); err != nil {
					trackErr = fmt.Errorf("recording %s/%s: %w", mr.Model, cr.Case.ID, err)
					break
				}
			}
		}
		if js, err := board.JSON(); err == nil && trackErr == nil {
			trackErr = run.SaveLeaderboard(js)
		}
	}
	if trackErr != nil {
		fmt.Fprintf(os.Stderr, "warning: tracking DB not fully written: %v\n", trackErr)
	}

	fmt.Printf("\nSaved:\n  leaderboard (commit this): %s\n  printed leaderboard:       %s\n  raw calls (local):         %s\n  per-case scores (local):   %s\n  tracking db (local):       %s (table %s)\n",
		saved.JSON, saved.Text, obOut, scoresPath, obTracking, tableName)
	return saveErr
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
