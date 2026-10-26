package oraclebench

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"polaris/gateway"
	"polaris/decisions"
)

// Asker is the one decision-model call the runner makes. *decisions.Client satisfies
// it; tests pass stubs. It is the same shape gateway.RunOracleRaw accepts,
// which is the point: the bench drives the real Oracle code path.
type Asker interface {
	AskChoice(ctx context.Context, state interface{}, questions map[string]decisions.ChoiceQuestion) (*decisions.Response, error)
}

// Model is one candidate: its catalog id and a client already pointed at it.
type Model struct {
	ID    string
	Asker Asker
}

// ModelRuns is everything one model did across the bank.
type ModelRuns struct {
	Model string
	// NA is non-empty when the model can't take part at all (it rejects Choice
	// questions). Such a model is reported as N/A, not scored 0.
	NA   string
	Runs []CaseRun
}

// CallEvent is one finished call, delivered as it completes so a long run
// leaves a usable record even if it is interrupted.
type CallEvent struct {
	Model  string
	CaseID string
	Track  string
	Repeat int
	Sample Sample
}

// CallRecord is one call as written to --out (JSONL): enough to re-score it or
// investigate a surprising result without re-spending the call.
type CallRecord struct {
	Model         string                 `json:"model"`
	ResolvedModel string                 `json:"resolved_model,omitempty"`
	CaseID        string                 `json:"case"`
	Track         string                 `json:"track"`
	Repeat        int                    `json:"repeat"`
	LatencyMS     int64                  `json:"latency_ms"`
	Err           ErrKind                `json:"err,omitempty"`
	ErrText       string                 `json:"err_text,omitempty"`
	CostUSD       float64                `json:"cost_usd,omitempty"`
	Checks        []gateway.CheckOutcome `json:"checks,omitempty"`
	ChipOutcomes  []gateway.CheckOutcome `json:"chip_outcomes,omitempty"`
	FocusMode     string                 `json:"focus_mode,omitempty"`
	FocusCleared  bool                   `json:"focus_cleared,omitempty"`
}

// Record flattens the event. OracleResult's bench-only fields are json:"-" so
// they never reach the persisted production payload; the bench copies them out
// here, where they belong.
func (ev CallEvent) Record() CallRecord {
	r := ev.Sample.Result
	return CallRecord{
		Model: ev.Model, ResolvedModel: r.ResolvedModel, CaseID: ev.CaseID, Track: ev.Track, Repeat: ev.Repeat,
		LatencyMS: ev.Sample.Latency.Milliseconds(), Err: ev.Sample.Err, ErrText: ev.Sample.ErrText, CostUSD: r.CostUSD,
		Checks: r.Checks, ChipOutcomes: r.ChipOutcomes, FocusMode: r.FocusMode, FocusCleared: r.FocusCleared,
	}
}

// RunConfig configures a run. Concurrency is recorded with the results: a
// latency figure measured at one load is not comparable to one at another
// (spike round 2: one provider went from 1.4s sequential to 4.1s at 5 workers).
type RunConfig struct {
	Cases       []Case
	Models      []Model
	Repeats     int
	Timeout     time.Duration
	Concurrency int
	// FreeInterval is the minimum gap between call starts for a ":free" model.
	// OpenRouter caps free models per minute across the whole account, and a 429
	// is recorded as that model's result and never retried, so an unpaced free
	// model would be disqualified for our account's limit rather than its own
	// quality (Elephant 1 lost 5 of 12 calls this way). The wait happens in the
	// feeder, outside callOnce's timer, so it never inflates measured latency.
	// Zero disables pacing.
	FreeInterval time.Duration
	// OnCall, if set, is called once per finished call, serialised.
	OnCall func(CallEvent)
	// OnProgress, if set, is called after each call with (done, total).
	OnProgress func(done, total int)
}

// fixtureFieldIDs gives every FixtureFields name an id. RunOracle drops a field
// chip whose winner has no id ("moving a thread needs the id"), so without
// these every chip_field case would silently never offer.
func fixtureFieldIDs() map[string]string {
	ids := make(map[string]string, len(FixtureFields))
	for name := range FixtureFields {
		ids[name] = "fixture-" + strings.ReplaceAll(strings.ToLower(name), " ", "-")
	}
	return ids
}

// BuildInput turns a case into the OracleInput production would have built for
// that turn. The environment is fixed (and documented in the plan) so results
// are comparable: Prism dial normal, a web thread, no ghost, no manual focus,
// the fixture Fields, a turn that could be promoted to Deep Research (without
// this the deep_research check is never asked, and a false "yes" there spends
// real sub-agent money, so it has to be exercised), and production's own
// thresholds (Rules left zero means config.DefaultOracle).
func BuildInput(c Case) gateway.OracleInput {
	source := c.State.ThreadSource
	if source == "" {
		source = "web"
	}
	return gateway.OracleInput{
		CurrentMessage:       c.State.Latest,
		PrevUserMessage:      c.State.Previous,
		IsFirstMessage:       c.State.IsFirst(),
		PriorOracleFocusMode: c.State.PriorOracleFocus,
		FieldOptions:         FixtureFields,
		FieldIDs:             fixtureFieldIDs(),
		ThreadSource:         source,
		Visuals:              "normal",
		OffersDeepResearch:   true,
	}
}

// notChoiceCapable recognises the answer a model that only takes free-text
// questions gives to a Choice question (the respan/span-01 family): a 400, not
// a refusal, and not worth counting 280 times.
func notChoiceCapable(text string) bool { return strings.Contains(text, "only accepts noul") }

// callOnce makes one real call and classifies the outcome.
func callOnce(ctx context.Context, m Model, c Case, timeout time.Duration) Sample {
	start := time.Now()
	res, err := gateway.RunOracleRaw(ctx, m.Asker, BuildInput(c), timeout)
	s := Sample{Result: res, Latency: time.Since(start), Err: ClassifyError(err)}
	if err != nil {
		s.ErrText = err.Error()
	}
	return s
}

type job struct{ model, cas, repeat int }

// IsFreeModel reports whether an OpenRouter model ID is a rate-limited free
// variant.
func IsFreeModel(id string) bool { return strings.HasSuffix(id, ":free") }

// Run executes every (model, case, repeat) call and returns the results in
// input order. Failures are recorded as samples, never retried: a refusal or a
// timeout is that model's result.
func Run(ctx context.Context, cfg RunConfig) ([]ModelRuns, error) {
	if len(cfg.Cases) == 0 || len(cfg.Models) == 0 {
		return nil, fmt.Errorf("oraclebench: need at least one case and one model (got %d cases, %d models)", len(cfg.Cases), len(cfg.Models))
	}
	repeats := max(1, cfg.Repeats)
	workers := max(1, cfg.Concurrency)

	out := make([]ModelRuns, len(cfg.Models))
	var jobs []job
	for mi, m := range cfg.Models {
		out[mi] = ModelRuns{Model: m.ID, Runs: make([]CaseRun, len(cfg.Cases))}
		for ci, c := range cfg.Cases {
			out[mi].Runs[ci] = CaseRun{Case: c, Samples: make([]Sample, repeats)}
		}
		// One probe before committing hundreds of calls to a model that can't
		// take Choice questions at all.
		if probe := callOnce(ctx, m, cfg.Cases[0], cfg.Timeout); probe.Err == ErrOther && notChoiceCapable(probe.ErrText) {
			out[mi].NA = "rejects Choice questions"
			continue
		}
		for ci := range cfg.Cases {
			for r := 0; r < repeats; r++ {
				jobs = append(jobs, job{mi, ci, r})
			}
		}
	}

	var (
		mu   sync.Mutex
		done int
		wg   sync.WaitGroup
	)
	queue := make(chan job)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				m, c := cfg.Models[j.model], cfg.Cases[j.cas]
				s := callOnce(ctx, m, c, cfg.Timeout)
				mu.Lock()
				out[j.model].Runs[j.cas].Samples[j.repeat] = s
				done++
				if cfg.OnCall != nil {
					cfg.OnCall(CallEvent{Model: m.ID, CaseID: c.ID, Track: c.Track, Repeat: j.repeat, Sample: s})
				}
				if cfg.OnProgress != nil {
					cfg.OnProgress(done, len(jobs))
				}
				mu.Unlock()
			}
		}()
	}
	var lastStart time.Time
feed:
	for _, j := range jobs {
		// Jobs are grouped by model, so one lastStart is enough: it only matters
		// within a run of consecutive jobs for the same free model.
		if cfg.FreeInterval > 0 && IsFreeModel(cfg.Models[j.model].ID) {
			if wait := cfg.FreeInterval - time.Since(lastStart); wait > 0 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					break feed
				}
			}
			lastStart = time.Now()
		}
		select {
		case queue <- j:
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return out, fmt.Errorf("run interrupted: %w", err)
	}
	return out, nil
}

// ResolvedModels lists the distinct builds that answered a model's calls, so a
// result recorded for an alias stays interpretable after the alias moves.
func (mr ModelRuns) ResolvedModels() []string {
	seen := map[string]bool{}
	for _, cr := range mr.Runs {
		for _, s := range cr.Samples {
			if s.Result.ResolvedModel != "" {
				seen[s.Result.ResolvedModel] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
