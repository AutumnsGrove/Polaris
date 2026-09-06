// pulsar_daily.go implements Pulsar Daily's generation pipeline — see
// docs/plans/pulsar-daily.md. Unlike a routine's single firePulse call,
// this assembles N independent mini-generations (Stage A), elects a Top
// Story (Stage B), elaborates it (Stage C), and persists one edition row
// (Stage D). Reuses firePulseRecovered's panic-recovery-per-goroutine
// shape throughout, so one block's failure degrades to "silently
// dropped" rather than sinking the whole run.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"polaris/agent"
	"polaris/config"
	"polaris/llm"
	"polaris/store"
	"polaris/tools"
)

// dailyBlockKind controls how a block's content gets produced.
type dailyBlockKind int

const (
	// dailyBlockDirect calls one existing tool directly (tools.Dispatch),
	// no LLM involved — Weather.
	dailyBlockDirect dailyBlockKind = iota
	// dailyBlockPick is a single no-tools LLM call drawing on the model's
	// own knowledge — Word of the Day, On This Day, Quote.
	dailyBlockPick
	// dailyBlockResearch is a narrow-toolset agent.Run, same "small
	// restricted-toolset agent run" shape pulsar_wizard.go's interview
	// loop uses — Headlines, Trending, Tech & Science, Local, Sports.
	dailyBlockResearch
)

type dailyBlockSpec struct {
	Key   string
	Title string
	Kind  dailyBlockKind
	// Watch: true means this block reports on an evolving situation and
	// goes through Stage A's diff-judge, which can legitimately drop it
	// as "unchanged" — see the plan doc's "Empty-day floor". false means
	// it's a fresh pick every day by construction: it always renders and
	// skips the diff-judge call entirely (paying for a comparison whose
	// answer is always "new" has zero decision value).
	Watch bool
}

// dailyBlockRegistry is the v1 block set — see the plan doc's "Default
// block set". Top Story isn't listed here: it's Stage B's elevation of
// whichever Watch block wins the ranking pass, not independently
// generated content a user can toggle on/off (see
// store.PulsarDailyConfig.EnabledBlocks' doc comment).
var dailyBlockRegistry = []dailyBlockSpec{
	{Key: "word_of_day", Title: "Word of the Day", Kind: dailyBlockPick, Watch: false},
	{Key: "weather", Title: "Weather", Kind: dailyBlockDirect, Watch: false},
	{Key: "on_this_day", Title: "On This Day", Kind: dailyBlockPick, Watch: false},
	{Key: "quote", Title: "Quote of the Day", Kind: dailyBlockPick, Watch: false},
	{Key: "picture_of_day", Title: "Picture of the Day", Kind: dailyBlockDirect, Watch: false},
	{Key: "headlines", Title: "Top Headlines", Kind: dailyBlockResearch, Watch: true},
	{Key: "trending", Title: "Trending Now", Kind: dailyBlockResearch, Watch: true},
	{Key: "tech_science", Title: "Tech & Science", Kind: dailyBlockResearch, Watch: true},
	{Key: "local", Title: "Local", Kind: dailyBlockResearch, Watch: true},
	// Sports sits between Watch and fresh-pick — see the plan doc's
	// "Empty-day floor": real daily data, but "no games today" is a
	// legitimate empty state for this one block specifically, handled by
	// its own generation prompt (dailySportsNoGamesMarker below) rather
	// than the diff-judge.
	{Key: "sports", Title: "Sports", Kind: dailyBlockResearch, Watch: false},
}

func dailyBlockSpecByKey(key string) (dailyBlockSpec, bool) {
	for _, b := range dailyBlockRegistry {
		if b.Key == key {
			return b, true
		}
	}
	return dailyBlockSpec{}, false
}

// dailyMinBlockCount is the floor described in the plan doc's "Empty-day
// floor": below this many rendered blocks, something's actually broken
// (infra outage, config error), not just a quiet news day — the worst
// realistic quiet day still leaves the 5 fresh-pick blocks standing.
const dailyMinBlockCount = 4

// dailySportsNoGamesMarker is the literal token the Sports generation
// prompt is instructed to return verbatim when there's nothing to
// report — checked for exactly, not inferred from short/empty content,
// so a genuinely short but real answer ("Warriors won 110-98") never
// gets mistaken for "nothing happened".
const dailySportsNoGamesMarker = "NO_GAMES_TODAY"

// dailyResearchDisabledTools locks a research block's agent.Run down to
// web_search/web_read/visualize/image_search/think — a different cut
// than pulsar_wizard.go's NoResearch (which excludes research entirely):
// a Daily research block needs web_search itself, just not the rest of
// the full chat catalog (dictionary, weather, recommendations, memory,
// ...) that has nothing to do with writing one short digest card.
var dailyResearchDisabledTools = map[string]bool{
	"calculator":         true,
	"nearby_search":      true,
	"youtube_transcript": true,
	"weather":            true,
	"reference_lookup":   true,
	"github_repo":        true,
	"github_activity":    true,
	"dictionary":         true,
	"music":              true,
	"books":              true,
	"movies":             true,
}

var dailyPickTasks = map[string]string{
	"word_of_day": "Pick one interesting, not-too-common English word and write a short Word of the Day " +
		"entry: the word, its definition, and a one-sentence note on its etymology or a memorable example " +
		"of use. Keep it to 2-3 short sentences total — this is one card in a larger digest, not a full essay.",
	"on_this_day": "Pick one genuinely interesting historical event that happened on today's calendar date " +
		"(any past year) and write 2-3 short sentences about it. Prefer something notable over the most " +
		"overused textbook example if you can.",
	"quote": "Share one genuinely interesting quote (with correct attribution) and, in one short sentence, " +
		"why it's worth reading today. Avoid the most overused quotes if a fresher one fits.",
}

var dailyResearchTasks = map[string]string{
	"headlines": "Give me a short rundown of today's biggest general news headlines — 2-4 sentences covering " +
		"the most significant stories, written for someone who wants the gist, not a full briefing.",
	"trending": "What's genuinely trending or being talked about today (news, culture, internet, or " +
		"otherwise)? 2-4 sentences, skimmable.",
	"tech_science": "Give me a short digest of today's most interesting tech and science news — 2-4 " +
		"sentences. If there's genuinely chart-worthy quantitative data (a multi-step timeline, a " +
		"comparison of numbers), consider using visualize — but only if the data earns it, not by default.",
	"local": "Give me a short local news/events digest for {{location}} — 2-4 sentences on anything notable " +
		"happening there today.",
	"sports": "Give me a short update on today's notable sports scores/results for these teams/leagues: " +
		"{{teams}}. If there are genuinely no games or results to report today for any of them, reply with " +
		"EXACTLY the single line \"" + dailySportsNoGamesMarker + "\" and nothing else — don't pad with " +
		"unrelated commentary.",
}

// dailyClient builds the LLM client for one block/stage — writer model for
// prose (Stage A content, Stage C elaboration), architect model for
// judgment calls (Stage A diff-verdicts, Stage B ranking). Same
// construction shape as runWizardTurn's client, minus reasoning: neither
// role here needs extended internal reasoning turned on explicitly (see
// generateSuggestions' doc comment for the opposite case).
func dailyClient(cfg *config.Config, modelID string) llm.ChatClient {
	modelCfg := cfg.ModelByID(modelID)
	return llm.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey, modelCfg.Model, modelCfg.Temperature, modelCfg.MaxTokens).
		WithProvider(&llm.ProviderRouting{Order: modelCfg.Provider, AllowFallbacks: boolPtr(false)})
}

// appendCustomInstruction folds a user-supplied steering instruction
// (store.PulsarDailyConfig.CustomInstructions) into a block's base task
// text — added after real usage showed the original v1 assumption
// ("sane defaults work, no per-block setting earns its keep besides
// Sports") was wrong: a generic "give me the news" task with no way to
// say what you actually care about isn't useful even with good defaults.
// A blank instruction is a no-op, so every existing block keeps behaving
// exactly as before until someone actually fills one in.
func appendCustomInstruction(task, custom string) string {
	custom = strings.TrimSpace(custom)
	if custom == "" {
		return task
	}
	return task + " The reader specifically wants: " + custom + "."
}

// generateDailyPickBlock is a plain one-shot LLM call for a fresh-pick
// block that leans on the model's own knowledge — no tools, no research,
// same "cheap and shallow every day" framing the plan doc's expand-to-
// chat section uses to justify the opposite (deep) behavior on tap.
func generateDailyPickBlock(reqCtx context.Context, client llm.ChatClient, key, customInstruction string) (string, float64, error) {
	task, ok := dailyPickTasks[key]
	if !ok {
		return "", 0, fmt.Errorf("no pick task defined for block %q", key)
	}
	task = appendCustomInstruction(task, customInstruction)
	resp, err := client.ChatCompletionStreaming(reqCtx, []llm.ChatMessage{
		{Role: "system", Content: "You are writing one short card for a personal daily digest page. Be " +
			"concise, concrete, and skimmable — 2-4 sentences, no headers, no restating the task."},
		{Role: "user", Content: task},
	}, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(resp.Content), resp.CostUSD, nil
}

// generateDailyResearchBlock runs one Watch/Sports block through a
// narrow-toolset agent.Run — no thread, no streaming, no history, mirror
// of runWizardTurn's shape. location/sportsTeams fill the task template's
// placeholders; unused by every key except local/sports respectively.
func (s *Server) generateDailyResearchBlock(reqCtx context.Context, cfg *config.Config, writerClient llm.ChatClient, key, location, sportsTeams, customInstruction string) (string, float64, error) {
	task, ok := dailyResearchTasks[key]
	if !ok {
		return "", 0, fmt.Errorf("no research task defined for block %q", key)
	}
	task = strings.ReplaceAll(task, "{{location}}", location)
	task = strings.ReplaceAll(task, "{{teams}}", sportsTeams)
	task = appendCustomInstruction(task, customInstruction)

	agentCtx := s.newDailyToolContext(reqCtx, writerClient, cfg, location)
	result, err := agent.Run(reqCtx, agentCtx, nil, task)
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(result.Answer), result.CostUSD, nil
}

// generateDailyElaboration is Stage C's deeper pass on the elected Top
// Story block — more research budget, more paragraphs, told explicitly
// what the quick version already said so it adds to it instead of
// repeating it.
func (s *Server) generateDailyElaboration(reqCtx context.Context, cfg *config.Config, writerClient llm.ChatClient, title, quickContent, location string) (string, float64, error) {
	task := fmt.Sprintf("This is today's lead story for a personal daily digest, titled %q. Here's the "+
		"quick version already written: %s\n\nWrite a deeper, more thorough version — more paragraphs, "+
		"additional context or background research, a pulled quote if one fits. Use visualize if the "+
		"story has genuinely chart-worthy quantitative data, or image_search if a relevant image would "+
		"help. Don't just restate the quick version — add to it.", title, quickContent)

	agentCtx := s.newDailyToolContext(reqCtx, writerClient, cfg, location)
	result, err := agent.Run(reqCtx, agentCtx, nil, task)
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(result.Answer), result.CostUSD, nil
}

// newDailyToolContext builds the tools.Context one research/elaboration
// block needs — a fresh instance per call (never shared across the
// concurrent goroutines Stage A fires), since tools.Context's Cards/
// Citations accumulate per-instance and each block is its own
// independent mini-generation, not a shared turn. Carries the full
// SearXNG/Brave/Parallel/Tavily + usage-cap wiring web_search needs (see
// CLAUDE.md's "Web search fallback chain" — a new call site that skips
// any of these degrades silently instead of erroring).
func (s *Server) newDailyToolContext(reqCtx context.Context, client llm.ChatClient, cfg *config.Config, location string) *tools.Context {
	return &tools.Context{
		// Ctx is normally set by agent.Run itself (see its doc comment on
		// Context.Ctx) — but Weather and Picture of the Day's image_search
		// call tools.Dispatch directly, bypassing agent.Run entirely, so
		// nothing else ever sets this. Without it, their outbound HTTP
		// calls got a nil context.Context and failed with "net/http: nil
		// Context" — a real bug only caught by an actual live generation
		// run, not by any unit test (every existing test mocks the LLM
		// client, never reaches the real HTTP call this broke).
		Ctx:                    reqCtx,
		SearXNG:                s.searxng,
		Blocklist:              s.blocklist,
		Tavily:                 s.tavily,
		Brave:                  s.brave,
		BraveUsageThisMonth:    func() (int, error) { return s.db.GetAPIUsage("brave") },
		IncrementBraveUsage:    func() error { _, err := s.db.IncrementAPIUsage("brave"); return err },
		Parallel:               s.parallel,
		ParallelUsageThisMonth: func() (int, error) { return s.db.GetAPIUsage("parallel") },
		IncrementParallelUsage: func() error { _, err := s.db.IncrementAPIUsage("parallel"); return err },
		Embed:                  s.embed,
		DefaultLocation:        location,
		// No live browser session to ask for a GPS fix from — see the
		// plan doc's "Local" note: a scheduled generation always falls
		// straight through ResolveLocation to DefaultLocation.
		RequestLocation: func() (string, bool) { return "", false },
		DisabledTools:   dailyResearchDisabledTools,
		LLM:             client,
		Emit:            func(string, map[string]interface{}) {},
		MaxTurns:        cfg.MaxAgentTurns,
	}
}

// generateDailyPictureBlock picks a short image-search query via the
// writer model, then dispatches image_search directly (tools.Dispatch,
// same direct-call path Weather uses) and reads back the first result
// card for its URL — image_search's own return string is a summary
// sentence, not the image data itself (see tools/image_search.go's
// finishImageSearch), so the actual URL only exists on the Card it adds.
func generateDailyPictureBlock(reqCtx context.Context, writerClient llm.ChatClient, ctx *tools.Context, customInstruction string) (content, imageURL string, cost float64, err error) {
	task := "Give me a search query for an interesting, visually striking photo to feature as today's " +
		"\"Picture of the Day\" — nature, space, art, architecture, wildlife, or similar. Vary it day to " +
		"day rather than defaulting to the same subject."
	task = appendCustomInstruction(task, customInstruction)
	resp, err := writerClient.ChatCompletionStreaming(reqCtx, []llm.ChatMessage{
		{Role: "system", Content: "Reply with ONLY a short (3-6 word) image search query, nothing else."},
		{Role: "user", Content: task},
	}, func(string) {}, nil)
	if err != nil {
		return "", "", 0, err
	}
	query := strings.Trim(strings.TrimSpace(resp.Content), "\"")
	if query == "" {
		return "", "", resp.CostUSD, fmt.Errorf("picture_of_day: model returned an empty search query")
	}

	argsJSON, _ := json.Marshal(map[string]string{"query": query})
	result := tools.Dispatch("image_search", string(argsJSON), ctx)
	if strings.HasPrefix(result, "error:") || strings.HasPrefix(result, "image search is degraded") {
		return "", "", resp.CostUSD, fmt.Errorf("picture_of_day: %s", result)
	}

	for _, c := range ctx.CardsSnapshot() {
		if c.Kind == "image" {
			title := c.Title
			if title == "" {
				title = query
			}
			return title, c.ImageURL, resp.CostUSD, nil
		}
	}
	return "", "", resp.CostUSD, fmt.Errorf("picture_of_day: image_search returned no image card")
}

// dailyVerdictToolDef forces Stage A's diff-judge to answer via a
// structured tool call instead of parseable prose — same pattern as
// tools/finalize_pulsar_prompt.go, applied to a judgment call instead of
// a user-facing turn-ending action.
var dailyVerdictToolDef = llm.ToolDef{
	Type: "function",
	Function: llm.ToolFunctionDef{
		Name: "record_verdict",
		Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"verdict": map[string]interface{}{
					"type": "string",
					"enum": []string{"unchanged", "notable", "normal"},
					"description": "unchanged: today's content says nothing meaningfully new vs yesterday's " +
						"— drop this block from today's edition. notable: today's content is a genuinely " +
						"significant development, a candidate for today's Top Story. normal: today's " +
						"content is real and worth showing, but not standout enough to compete for Top Story.",
				},
				"gist": map[string]interface{}{
					"type": "string",
					"description": "One short sentence summarizing today's content — used only to compare " +
						"this block against others when electing a Top Story, never shown verbatim.",
				},
			},
			"required": []string{"verdict", "gist"},
		},
	},
}

type dailyVerdict struct {
	Verdict string
	Gist    string
}

// dailyDiffJudge hands the architect model yesterday's and today's full
// block content (not summarized — see the plan doc's Stage A reasoning
// on why a lossy summary would weaken a pairwise wording comparison) and
// returns a structured verdict.
func dailyDiffJudge(reqCtx context.Context, client llm.ChatClient, title, yesterday, today string) (dailyVerdict, float64, error) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: fmt.Sprintf("You are comparing yesterday's and today's content for one "+
			"block of a personal daily digest page, titled %q. Decide whether today's content represents a "+
			"meaningfully new development, or says nothing yesterday's didn't already say. Always respond "+
			"by calling record_verdict — never plain text.", title)},
		{Role: "user", Content: "Yesterday:\n" + yesterday + "\n\nToday:\n" + today},
	}
	resp, err := client.ChatCompletionWithTools(reqCtx, messages, []llm.ToolDef{dailyVerdictToolDef}, func(string) {}, nil)
	if err != nil {
		return dailyVerdict{}, 0, err
	}
	verdict, err := parseDailyVerdict(resp)
	return verdict, resp.CostUSD, err
}

// parseDailyVerdict falls back to "normal" (never drops a block outright)
// when the model answers in plain prose instead of calling the mandated
// tool — same "helpful degradation over hard failure" instinct as
// wizardResponse.Answer's fallback for the wizard's own model
// occasionally skipping its required tool call. An unrecognized verdict
// string gets the same treatment.
func parseDailyVerdict(resp *llm.ChatResponse) (dailyVerdict, error) {
	if len(resp.ToolCalls) == 0 {
		return dailyVerdict{Verdict: "normal", Gist: strings.TrimSpace(resp.Content)}, nil
	}
	var args struct {
		Verdict string `json:"verdict"`
		Gist    string `json:"gist"`
	}
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Function.Arguments), &args); err != nil {
		return dailyVerdict{}, err
	}
	if args.Verdict != "unchanged" && args.Verdict != "notable" && args.Verdict != "normal" {
		args.Verdict = "normal"
	}
	return dailyVerdict{Verdict: args.Verdict, Gist: strings.TrimSpace(args.Gist)}, nil
}

type dailyRankCandidate struct {
	Key   string
	Title string
	Gist  string
}

// dailyElectTopStory is Stage B's ranking pass — given only a one-line
// gist per candidate (not full text, see the plan doc's reasoning on why
// a cross-block comparison wants a cleaner substrate than a pairwise
// diff-judge call does), returns which candidate's key is today's lead.
// Only ever called with at least one candidate (see runDailyPipeline);
// falls back to the first candidate if the model names a key outside the
// given list, rather than electing nothing.
func dailyElectTopStory(reqCtx context.Context, client llm.ChatClient, candidates []dailyRankCandidate) (string, float64, error) {
	var b strings.Builder
	for _, c := range candidates {
		fmt.Fprintf(&b, "- %s (%s): %s\n", c.Key, c.Title, c.Gist)
	}
	toolDef := llm.ToolDef{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name: "elect_top_story",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"winner_key": map[string]interface{}{
						"type": "string",
						"description": "The key of whichever candidate is today's single biggest/most " +
							"significant story — must be exactly one of the keys listed.",
					},
				},
				"required": []string{"winner_key"},
			},
		},
	}
	messages := []llm.ChatMessage{
		{Role: "system", Content: "You are electing today's lead story for a personal daily digest page, " +
			"from a short list of candidates each independently flagged as a notable development today. " +
			"Pick whichever is genuinely the biggest/most significant — not by list order. Always respond " +
			"by calling elect_top_story — never plain text."},
		{Role: "user", Content: b.String()},
	}
	resp, err := client.ChatCompletionWithTools(reqCtx, messages, []llm.ToolDef{toolDef}, func(string) {}, nil)
	if err != nil {
		return "", 0, err
	}
	if len(resp.ToolCalls) == 0 {
		return candidates[0].Key, resp.CostUSD, nil
	}
	var args struct {
		WinnerKey string `json:"winner_key"`
	}
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Function.Arguments), &args); err != nil {
		return candidates[0].Key, resp.CostUSD, nil
	}
	for _, c := range candidates {
		if c.Key == args.WinnerKey {
			return args.WinnerKey, resp.CostUSD, nil
		}
	}
	return candidates[0].Key, resp.CostUSD, nil
}

// isDailyDue mirrors isRoutineDue but for the Daily singleton, which only
// ever has one schedule shape (daily, at time_of_day) — see the plan
// doc's "Storage is deliberately not routine-shaped". CreatedAt is the
// same "never generated yet" baseline pulsar_routines.CreatedAt is for a
// routine that's never fired — see that field's doc comment for the bug
// this avoids (an immediate fire on save if today's time-of-day already
// passed).
func isDailyDue(c *store.PulsarDailyConfig, now time.Time) bool {
	hour, minute, ok := parseTimeOfDay(c.TimeOfDay)
	if !ok {
		return false
	}
	scheduled := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if scheduled.After(now) {
		scheduled = scheduled.AddDate(0, 0, -1)
	}
	baseline := c.CreatedAt
	if c.LastGeneratedAt != nil {
		baseline = *c.LastGeneratedAt
	}
	return baseline.Before(scheduled)
}

// runDailyPipelineRecovered wraps runDailyPipeline with the same panic
// recovery and shutdown-drain registration firePulseRecovered/firePulse
// use — this runs in its own goroutine outside any call stack net/http
// recovers, and a pipeline running during a restart's drain window is
// otherwise invisible to WaitForActiveTurns.
func (s *Server) runDailyPipelineRecovered() {
	if !s.TryStartTurn() {
		log.Warn("skipping pulsar daily run — server is restarting")
		return
	}
	defer s.FinishTurn()
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("panic running pulsar daily pipeline", "panic", rec)
		}
	}()
	s.runDailyPipeline(context.Background())
}

// runDailyPipeline runs Stage A-D once — see the plan doc's "Generation
// pipeline" for the full staged design. Meant to be called from the
// scheduler when isDailyDue reports true; also safe to call for a manual
// re-trigger (UpsertDailyEdition overwrites rather than duplicates).
func (s *Server) runDailyPipeline(reqCtx context.Context) {
	cfg := s.liveConfig()
	cfgRow, err := s.db.GetDailyConfig()
	if err != nil {
		log.Warn("pulsar daily: loading config failed, skipping run", "err", err)
		return
	}

	// Recorded before generating, not after — same reasoning as
	// firePulse's identical ordering (see SetPulsarRoutineLastRun's doc
	// comment): a real, observed race without this. Stage A-D can take
	// several minutes (a handful of web_search-backed agent.Run calls),
	// comfortably longer than the scheduler's once-a-minute tick — every
	// tick that lands before Stage D finishes re-evaluates isDailyDue
	// against a still-nil last_generated_at and fires a second concurrent
	// pipeline for the same day. Setting this first closes that window,
	// at the cost of the same accepted tradeoff pulses already make: a
	// crash mid-generation loses that day's run rather than retrying it.
	if err := s.db.SetDailyLastGenerated(time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		log.Warn("pulsar daily: recording last_generated_at failed, skipping this run", "err", err)
		return
	}

	today := time.Now().Format("2006-01-02")
	location := cfg.DefaultLocation

	writerClient := dailyClient(cfg, cfgRow.WriterModel)
	architectClient := dailyClient(cfg, cfgRow.ArchitectModel)

	enabled := map[string]bool{}
	for _, k := range cfgRow.EnabledBlocks {
		enabled[k] = true
	}

	// First-ever day (or a gap since the last real edition — see
	// LatestDailyEdition's doc comment) has no "yesterday" to diff
	// against: every Watch block's verdict defaults to "notable" rather
	// than the diff-judge call being skipped or erroring, per the plan
	// doc's "First-ever day" note.
	yesterday, err := s.db.LatestDailyEdition(today)
	hasYesterday := err == nil
	if err != nil && err != store.ErrDailyEditionNotFound {
		log.Warn("pulsar daily: loading yesterday's edition failed, treating as first-ever day", "err", err)
	}
	yesterdayByKey := map[string]store.PulsarDailyBlock{}
	if hasYesterday {
		for _, b := range yesterday.Blocks {
			yesterdayByKey[b.Key] = b
		}
	}

	// Stage A — every enabled block fires as its own goroutine, so one
	// slow research block (a real web_search call, 10-30s) doesn't delay
	// every other block's generation. Barrier is the WaitGroup below —
	// the one point parallelism has to stop, since Stage B's ranking
	// needs every surviving candidate at once.
	type stageAResult struct {
		spec     dailyBlockSpec
		content  string
		gist     string
		verdict  string // "" for a non-Watch block: always renders, no verdict.
		imageURL string
		costUSD  float64
		chart    *tools.ChartSpec
	}
	results := make([]*stageAResult, len(dailyBlockRegistry))
	var wg sync.WaitGroup
	for i, spec := range dailyBlockRegistry {
		if !enabled[spec.Key] {
			continue
		}
		wg.Add(1)
		go func(i int, spec dailyBlockSpec) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic generating pulsar daily block", "block", spec.Key, "panic", rec)
				}
			}()
			r := s.generateOneDailyBlock(reqCtx, cfg, writerClient, architectClient, spec, location, cfgRow.SportsTeams, cfgRow.CustomInstructions, yesterdayByKey, hasYesterday)
			if r != nil {
				results[i] = &stageAResult{spec: spec, content: r.content, gist: r.gist, verdict: r.verdict, imageURL: r.imageURL, costUSD: r.costUSD, chart: r.chart}
			}
		}(i, spec)
	}
	wg.Wait()

	// totalCost accumulates every LLM call across every stage — Stage A's
	// block generation and diff-judge calls (already summed into each
	// stageAResult.costUSD), Stage B's ranking call, and Stage C's
	// elaboration — into what the frontend shows as this edition's real
	// cost. See store.PulsarDailyEdition.CostUSD.
	var totalCost float64
	for _, r := range results {
		if r != nil {
			totalCost += r.costUSD
		}
	}

	// Stage B — elect a Top Story from whichever Watch blocks came back
	// "notable". No fallback if none did (see the plan doc's "Resolved:
	// no Top Story fallback") — the page just leads with whichever
	// fresh-pick block falls first in the masonry flow.
	var candidates []dailyRankCandidate
	for _, r := range results {
		if r != nil && r.verdict == "notable" {
			candidates = append(candidates, dailyRankCandidate{Key: r.spec.Key, Title: r.spec.Title, Gist: r.gist})
		}
	}
	topStoryKey := ""
	if len(candidates) > 0 {
		key, rankCost, err := dailyElectTopStory(reqCtx, architectClient, candidates)
		totalCost += rankCost
		if err != nil {
			log.Warn("pulsar daily: stage B ranking failed, no top story today", "err", err)
		} else {
			topStoryKey = key
		}
	}

	// Stage C — the elected block alone gets a deeper elaboration pass.
	var topStory *store.PulsarDailyBlock
	var blocks []store.PulsarDailyBlock
	for _, r := range results {
		if r == nil || r.verdict == "unchanged" {
			continue // dropped — either a genuinely quiet Watch block, or a hard generation failure (see generateOneDailyBlock).
		}
		if r.spec.Key == topStoryKey {
			elaborated, elabCost, err := s.generateDailyElaboration(reqCtx, cfg, writerClient, r.spec.Title, r.content, location)
			totalCost += elabCost
			content := r.content
			if err != nil {
				log.Warn("pulsar daily: stage C elaboration failed, using the quick version", "block", r.spec.Key, "err", err)
			} else {
				content = elaborated
			}
			topStory = &store.PulsarDailyBlock{Key: r.spec.Key, Title: r.spec.Title, Content: content, Gist: r.gist, IsTopStory: true}
			continue
		}
		var chartJSON json.RawMessage
		if r.chart != nil {
			if b, err := json.Marshal(r.chart); err != nil {
				log.Warn("pulsar daily: encoding block chart failed, dropping it", "block", r.spec.Key, "err", err)
			} else {
				chartJSON = b
			}
		}
		blocks = append(blocks, store.PulsarDailyBlock{Key: r.spec.Key, Title: r.spec.Title, Content: r.content, Gist: r.gist, ImageURL: r.imageURL, Chart: chartJSON})
	}

	// Stage D — assemble (Top Story first, if any) and persist.
	var edition []store.PulsarDailyBlock
	if topStory != nil {
		edition = append(edition, *topStory)
	}
	edition = append(edition, blocks...)

	if len(edition) < dailyMinBlockCount {
		log.Warn("pulsar daily: only got blocks below the minimum floor, showing a degraded notice instead", "got", len(edition), "min", dailyMinBlockCount)
		edition = []store.PulsarDailyBlock{{
			Key:     "notice",
			Title:   "Today's edition is running light",
			Content: "Something went wrong generating most of today's blocks — check the server logs. This isn't a quiet news day, it's an actual failure.",
		}}
	}

	if err := s.db.UpsertDailyEdition(today, edition, totalCost); err != nil {
		log.Warn("pulsar daily: persisting edition failed", "err", err)
		return
	}
	log.Info("pulsar daily: edition generated", "date", today, "blocks", len(edition), "top_story", topStoryKey != "", "cost_usd", totalCost)
}

type dailyGeneratedBlock struct {
	content  string
	gist     string
	verdict  string
	imageURL string
	// chart carries weather's structured forecast (setWeatherChart) through
	// to the frontend so the Daily card can render the same rich strip a
	// normal chat turn's weather tool gets, instead of the plain-prose
	// fallback direct-dispatch blocks otherwise get stuck with.
	chart *tools.ChartSpec
	// costUSD accumulates every LLM call this block's generation made —
	// its own content-generation call plus (for a Watch block) the
	// diff-judge call — so runDailyPipeline can sum a real total for the
	// whole edition. Direct tool dispatches (Weather) and image_search
	// itself cost nothing here; only the LLM calls do.
	costUSD float64
}

// generateOneDailyBlock produces one block's content and, for a Watch
// block, its diff-judge verdict — the single unit of work each Stage A
// goroutine runs. Returns nil on a hard generation failure, which
// runDailyPipeline's Stage D treats identically to an "unchanged"
// verdict (see the plan doc: "unchanged verdicts and hard generation
// failures both drop out here — same bucket, since both mean 'nothing
// to show'").
func (s *Server) generateOneDailyBlock(reqCtx context.Context, cfg *config.Config, writerClient, architectClient llm.ChatClient, spec dailyBlockSpec, location, sportsTeams string, customInstructions map[string]string, yesterdayByKey map[string]store.PulsarDailyBlock, hasYesterday bool) *dailyGeneratedBlock {
	var content string
	var imageURL string
	var cost float64
	var err error
	var chart *tools.ChartSpec
	custom := customInstructions[spec.Key]

	switch spec.Kind {
	case dailyBlockDirect:
		if spec.Key == "picture_of_day" {
			ctx := s.newDailyToolContext(reqCtx, writerClient, cfg, location)
			content, imageURL, cost, err = generateDailyPictureBlock(reqCtx, writerClient, ctx, custom)
		} else {
			ctx := s.newDailyToolContext(reqCtx, writerClient, cfg, location)
			content = tools.Dispatch(spec.Key, "{}", ctx)
			if strings.HasPrefix(content, "error:") {
				err = fmt.Errorf("%s", content)
			}
			chart = ctx.ChartSnapshot()
			if spec.Key == "weather" && chart != nil {
				content = tools.TrimWeatherForecastSection(content)
			}
		}
	case dailyBlockPick:
		content, cost, err = generateDailyPickBlock(reqCtx, writerClient, spec.Key, custom)
	case dailyBlockResearch:
		content, cost, err = s.generateDailyResearchBlock(reqCtx, cfg, writerClient, spec.Key, location, sportsTeams, custom)
	}

	if err != nil {
		log.Warn("pulsar daily: block generation failed", "block", spec.Key, "err", err)
		return nil
	}
	if spec.Key == "sports" && strings.TrimSpace(content) == dailySportsNoGamesMarker {
		return nil // legitimate empty state, not a failure — see spec's Watch doc comment.
	}

	if !spec.Watch {
		return &dailyGeneratedBlock{content: content, imageURL: imageURL, costUSD: cost, chart: chart}
	}

	yesterdayBlock, ok := yesterdayByKey[spec.Key]
	if !hasYesterday || !ok {
		// No prior content to diff against — treat as notable rather
		// than skipping the diff-judge call silently, per the plan
		// doc's "First-ever day" note.
		return &dailyGeneratedBlock{content: content, gist: content, verdict: "notable", costUSD: cost}
	}

	verdict, verdictCost, err := dailyDiffJudge(reqCtx, architectClient, spec.Title, yesterdayBlock.Content, content)
	if err != nil {
		log.Warn("pulsar daily: diff-judge failed, treating block as normal", "block", spec.Key, "err", err)
		return &dailyGeneratedBlock{content: content, gist: content, verdict: "normal", costUSD: cost}
	}
	return &dailyGeneratedBlock{content: content, gist: verdict.Gist, verdict: verdict.Verdict, costUSD: cost + verdictCost}
}
