package models

import "polaris/config"

// Registry is the complete catalog of models Polaris knows about.
// Config can override the default and tune per-model settings, but
// adding a new model happens here, not in config.yaml.
var Registry = []config.ModelConfig{
	{
		// Replaces the v2.5 "mimo" entry (2026-09-22) — same ID kept
		// stable across the version bump so existing thread selections
		// and any model_overrides.mimo config keep resolving, same
		// pattern as deepseek-pro/deepseek's own dated-snapshot bumps
		// below. Confirmed live via GET /api/v1/models/xiaomi/
		// mimo-v2.6-flash/endpoints: single Xiaomi/fp8 provider,
		// genuinely multimodal (input_modalities: text+image+video+
		// audio) — unlike the old v2.5 Pro tier, this one isn't
		// text-only.
		ID:          "mimo",
		Name:        "MiMo v2.6 Flash",
		Model:       "xiaomi/mimo-v2.6-flash",
		Provider:    []string{"xiaomi/fp8"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Multimodal: true,
		// Tier 2 Deep Research sub-agent default (see
		// docs/plans/deep-research-two-tier.md), moved here from the
		// "deepseek" entry below (2026-09-22). Same live endpoint survey
		// cited above confirms tool support (supported_parameters
		// includes "tools"; supports_tool_choice auto/required both
		// true) and a 1,048,576-token context window, well beyond
		// deepseek's 384K-token providers -- also meaningfully cheaper
		// per token ($0.14/$0.28 per M vs. deepseek's $0.15/$0.60 to
		// $0.22/$0.66) at comparable uptime (99.99%/99.97%/99.99% at
		// 30m/5m/1d). See config.ModelConfig.ResearchWorker.
		ResearchWorker: true,
		Pricing:        &config.PricingConfig{PromptPerM: 0.14, CompletionPerM: 0.28},
	},
	{
		// Replaces the v2.5 "mimo-pro" entry (2026-09-22), same ID-
		// stability reasoning as "mimo" above. Same live-endpoint survey:
		// single Xiaomi/fp8 provider, genuinely multimodal — unlike the
		// v2.5 Pro tier it replaces, which was text-only.
		ID:          "mimo-pro",
		Name:        "MiMo v2.6 Pro",
		Model:       "xiaomi/mimo-v2.6-pro",
		Provider:    []string{"xiaomi/fp8"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Multimodal: true,
		Pricing:    &config.PricingConfig{PromptPerM: 0.435, CompletionPerM: 0.87},
	},
	// "deepseek-pro" (DeepSeek V4 Pro, deepseek/deepseek-v4-pro-0813) was
	// retired 2026-09-22: DeepSeek's own V4.1 Flash release notes state
	// Flash supersedes Pro for essentially every use case, and at $1.12/
	// $3.36 per M it cost ~7-8x the "deepseek" entry below ($0.15/$0.60)
	// for a model its own maker says isn't worth reaching for anymore.
	// Was also Pulsar Daily's architect_model default — see
	// store/store.go's schema comment and migration for the retirement
	// of that reference too.
	{
		// DeepSeek V4.1 Flash, moved onto the dated snapshot
		// deepseek/deepseek-v4.1-flash-1017 (2026-10-19). The "deepseek"
		// ID is kept across the swap — it's the shipped default_model, so
		// existing thread selections and config.yaml's default_model/
		// model_overrides must keep resolving — same ID-stability approach
		// as the MiMo v2.6 and Mercury 3 bumps.
		//
		// Per a live GET /api/v1/models/deepseek/deepseek-v4.1-flash-1017/
		// endpoints survey: genuinely multimodal per
		// architecture.input_modalities (["text","image"]), 1M-token
		// context on every route below, pinged with an identical
		// ~3.2k-token prefix twice per provider, each pinned with
		// allow_fallbacks:false. All three answered, all three hit their
		// prompt cache on the second call (~99% of tokens cached), all
		// three billed exactly their listed rates, and all three were at
		// 100% 1-day uptime when surveyed:
		//
		//   DeepInfra fp8   $0.098 / $0.294 per M, $0.00294/M cache reads
		//   Cloudflare      $0.10  / $0.60  per M, $0.003/M   cache reads
		//   DeepSeek        $0.15  / $0.60  per M, $0.003/M   cache reads
		//
		// Order is DeepInfra, then Cloudflare, then DeepSeek's own route
		// (trimmed from the previous five-deep chain). Cache reads dominate
		// an agent thread's cost, and DeepInfra and Cloudflare sit within
		// 2% of each other on them, so input/completion price decides the
		// top two — DeepInfra wins both. Cloudflare is the mid-rung on
		// rate rather than on reputation: flat $0.10/$0.60 with no
		// time-of-day surcharge, so it stays under the official route's
		// cost even at DeepSeek's off-peak price.
		//
		// The official route is last, not excluded: it doubles to
		// $0.30/$1.20/$0.006 on weekdays 01:00-04:00 and 06:00-10:00 UTC
		// (~21% of the week) via pricing.overrides, and its
		// supports_tool_choice only covers none/auto — required and
		// function are false there, so a forced-tool call (Comet's trends
		// extraction is the one caller) would fail on it. It's still the
		// wide-context / highest-throughput route, which is why it's kept
		// rather than dropped. DeepInfra also lists deepinfra/fp8/flex
		// ($0.0784/$0.2352) and deepinfra/fp8/priority ($0.147/$0.441);
		// this entry takes the plain fp8 tier, same as before. DeepInfra's
		// endpoints carry a `discount` (51%); if that's promotional,
		// re-survey when it lapses. Don't copy this order onto other
		// models without re-running the survey — pricing shape differs per
		// model.
		ID:          "deepseek",
		Name:        "DeepSeek V4.1 Flash",
		Model:       "deepseek/deepseek-v4.1-flash-1017",
		Provider:    []string{"deepinfra/fp8", "cloudflare", "deepseek"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Multimodal: true,
		// Primary route's (DeepInfra fp8) list rate; see the comment above.
		Pricing: &config.PricingConfig{PromptPerM: 0.098, CompletionPerM: 0.294},
	},
	{
		// Claude Haiku 5.5 (2026-10-08). Per GET /api/v1/models/anthropic/
		// claude-haiku-5.5/endpoints: 1M-token context, text+image+file in,
		// tools + reasoning_effort supported, tool_choice auto/none only
		// (required/function are false), 100% 1-day uptime on every route.
		//
		// The pricing shape is the reason this entry carries
		// CompactionTokens. Base is $0.10/$0.50 per M ($0.01 cache reads),
		// but every endpoint has a pricing.overrides tier at
		// min_prompt_tokens=100000 that reprices the *whole request* (not
		// just the excess) to $0.50/$2.50 ($0.05 cache reads) — 5x. The
		// global 200K compaction threshold would leave a thread paying that
		// on every turn from 100K to 200K, and the compaction call (full
		// history in) would be billed at it too. Compacting at 80K keeps
		// requests under the cliff with ~20K of headroom for the next
		// turn's user message and agent-loop tool results; a single very
		// tool-heavy turn can still cross it mid-loop, which is accepted —
		// the ledger uses usage.cost, so it's billed correctly, just dearer.
		//
		// Anthropic models don't cache implicitly; llm.NewClient sends a
		// top-level cache_control for any anthropic/* slug (verified live
		// 2026-10-08: 10x cheaper cache reads), so nothing to set here.
		//
		// Provider order: the three 1.0x "global" routes. The regional
		// routes (google-vertex/us|europe, azure/us, amazon-bedrock/us-east-1
		// |eu-west-1) list a flat 1.1x on every rate, so don't add them.
		// Re-run the survey if the override tier or those rates change.
		ID:          "haiku",
		Name:        "Claude Haiku 5.5",
		Model:       "anthropic/claude-haiku-5.5",
		Provider:    []string{"anthropic", "google-vertex/global", "amazon-bedrock"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Multimodal:       true,
		CompactionTokens: 80_000,
		Pricing: &config.PricingConfig{
			PromptPerM: 0.10, CompletionPerM: 0.50,
			LongPromptTokens: 100_000, LongPromptPerM: 0.50, LongCompletionPerM: 2.50,
		},
	},
	{
		// GPT-6 Luna, released 2026-09-22 alongside GPT-6 Sol — the
		// low-cost, high-volume member of that same-day family.
		// Supersedes GPT-5.6 Luna at a lower official API rate
		// ($0.10/$0.50 per M in/out vs. 5.6's $1.00/$6.00) while
		// matching its tool-calling and reasoning-effort support.
		ID:          "luna",
		Name:        "ChatGPT Luna",
		Model:       "openai/gpt-6-luna",
		Provider:    []string{"openai"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			// OpenRouter's live metadata for this model lists six tiers
			// (none/low/medium/high/xhigh/max), not just the three
			// ReasoningConfig.Effort's own doc comment mentions — bumped
			// two steps above the model's own "medium" default.
			Effort: "xhigh",
		},
		// input_modalities includes "image" per live OpenRouter endpoint
		// metadata (GET /api/v1/models/openai/gpt-6-luna/endpoints).
		Multimodal: true,
		Pricing:    &config.PricingConfig{PromptPerM: 0.10, CompletionPerM: 0.50},
	},
	{
		// Diffusion-based (dLLM), not autoregressive — generates/refines
		// tokens in parallel instead of one at a time, which is the whole
		// reason it's here: a speed comparison against the sequential
		// models above. Bumped from Mercury 2.5 (2026-10-19) with the same
		// ID kept stable so existing thread selections and
		// model_overrides.mercury keep resolving, same pattern as the MiMo
		// and DeepSeek bumps above. Per a live GET /api/v1/models/
		// inception/mercury-3/endpoints survey: text-only
		// (input_modalities: ["text"] only), single-provider (Inception
		// itself, ~97.8% 1-day uptime), 1M-token context (up from 2.5's
		// 260K), full tool_choice support (none/auto/required/function all
		// true), implicit caching. It costs ~2.5x Mercury 2.5 ($0.10/$0.35
		// per M vs. $0.04/$0.15) — still among the cheapest here.
		ID:          "mercury",
		Name:        "Mercury 3",
		Model:       "inception/mercury-3",
		Provider:    []string{"inception"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Pricing: &config.PricingConfig{PromptPerM: 0.10, CompletionPerM: 0.35},
	},
	{
		// Replaces ling-flash-vl (2026-10-05): inclusionai/ling-3.0-flash-
		// vl:free stopped being free — OpenRouter now lists it at a paid
		// rate — and every other :free model we tried (nemotron-ultra,
		// inkling:free, gemma-4-26b-a4b-it:free) had unusable rate limits,
		// gated routing, or poor quality. So the "cheapest model we offer"
		// slot is now pinned to the cheapest *good* paid model instead of
		// chasing a free one: $0.10/$0.20 per M in/out, $0.002/M cache
		// reads, per GET /api/v1/models/meta/muse-spark-1.3-contributor/
		// endpoints (single Meta provider, ~99.95% 1-day uptime).
		// Multimodal (text+image+video+file in), 1M-token context,
		// tool_choice none/auto/required/function all supported.
		//
		// Privacy tradeoff, accepted deliberately: the Contributor tier is
		// NOT zero-data-retention. Meta retains prompts/completions and
		// trains on them. That is the price of the lowest rate on the
		// list; don't route anything sensitive here, and don't assume it
		// shares the privacy posture of the other entries. New ID rather
		// than reusing "ling-flash-vl" since this is a different model;
		// threads still pointing at the old ID fall back to the default
		// via config.ModelByID.
		ID:          "muse-spark",
		Name:        "Muse Spark 1.3 Contributor",
		Model:       "meta/muse-spark-1.3-contributor",
		Provider:    []string{"meta"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Multimodal: true,
		Pricing:    &config.PricingConfig{PromptPerM: 0.10, CompletionPerM: 0.20},
	},
}
