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
		// Deprecates the old V4 Flash "deepseek" entry (2026-09-22) —
		// deepseek/deepseek-v4-flash-0731 with its five-deep Baidu/
		// DeepInfra/StreamLake/BaseTen/Novita fallback chain — in favor of
		// V4.1 Flash. Reused the "deepseek" ID (rather than dropping the
		// separate "deepseek-v41-flash" ID) so existing thread selections
		// and config.yaml's default_model/model_overrides keep resolving
		// across the swap, same ID-stability approach as the MiMo v2.6
		// replacement above.
		//
		// Per a live GET /api/v1/models/deepseek/deepseek-v4.1-flash/
		// endpoints survey (originally 2026-09-12, re-confirmed
		// 2026-09-22). Genuinely multimodal per
		// architecture.input_modalities (["text","image"]), unlike the V4
		// Flash entry it replaces.
		//
		// Unlike deepseek-pro above, the official "deepseek" tag is the
		// primary route here, not excluded — re-checked the reasoning
		// rather than copying deepseek-pro's exclusion. Its
		// pricing.overrides only double the rate (to $0.30/$1.20 per M,
		// matching the third-party fp8 tier below) during a narrow weekday
		// window (01:00-04:00 and 06:00-10:00 UTC, ~21% of the week); the
		// other ~79% of the time, incl. all weekend, it's $0.15/$0.60 with
		// $0.003/M cache reads — cheaper than every third-party endpoint
		// in the survey at every hour, not just off-peak. That's the
		// opposite of deepseek-pro's official route, whose list price
		// loses to third-party even off-peak — a genuinely different
		// pricing shape per model, not a fixed platform rule, so don't
		// copy this endpoint's inclusion/exclusion onto other models
		// without re-running the survey.
		// Fireworks is the fallback: flat (no time-of-day pricing),
		// $0.22/$0.66 per M, 98.98% uptime, 943,718-token max completion —
		// beats every fp8 third-party provider in the survey (GMICloud,
		// Novita, etc., all $0.30/$1.20) on price while staying reliable,
		// so it's a better second rung than reusing deepseek-pro's known-
		// good fp8 providers here.
		ID:          "deepseek",
		Name:        "DeepSeek V4.1 Flash",
		Model:       "deepseek/deepseek-v4.1-flash",
		Provider:    []string{"deepseek", "fireworks"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Multimodal: true,
		// Off-peak official rate (~79% of the week, see comment above) —
		// the number that actually applies most of the time.
		Pricing: &config.PricingConfig{PromptPerM: 0.15, CompletionPerM: 0.60},
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
		// models above. Text-only per live OpenRouter endpoint metadata
		// (input_modalities: ["text"] only), single-provider (Inception
		// itself, 99.99% 1-day uptime per GET /api/v1/models/inception/
		// mercury-2.5-20260908/endpoints), full tool_choice support
		// (none/auto/required/function all true).
		ID:          "mercury",
		Name:        "Mercury 2.5",
		Model:       "inception/mercury-2.5",
		Provider:    []string{"inception"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Pricing: &config.PricingConfig{PromptPerM: 0.04, CompletionPerM: 0.15},
	},
	{
		// Replaces nemotron-ultra (2026-09-14): that model's single free
		// provider (Nvidia direct) was failing 2 of 3 concurrent requests
		// with 502 "Service temporarily overloaded" in live testing, and
		// even successful calls took 45-90s for a one-line reply (matches
		// OpenRouter's own endpoint metadata: p50 latency ~47s).
		// thinkingmachines/inkling:free was fast and multimodal too, but
		// its free tier 403s on every request regardless of caller
		// (`"failed_routing_step": "Gate Free Endpoints by Agentic
		// Harness"`) unless the app is on OpenRouter's own allowlist of
		// recognized coding-agent tools — no header combination gets past
		// it, since the gate checks app identity at OpenRouter's routing
		// layer, not request contents. google/gemma-4-26b-a4b-it:free
		// worked but is served via Google AI Studio's BYOK quota
		// (`is_byok: true` on every response) rather than a pooled
		// OpenRouter allowance. This model live-tested clean on a pooled
		// allowance (`is_byok: false`): 5/5 concurrent requests under
		// 1.25s, plain text, tool calls, and reasoning all confirmed
		// working via direct OpenRouter API calls. Genuinely multimodal
		// per live endpoint metadata (input_modalities includes
		// image+video) — the free-tier model a non-paying user can
		// actually attach images to.
		ID:          "ling-flash-vl",
		Name:        "Ling 3.0 Flash VL (Free)",
		Model:       "inclusionai/ling-3.0-flash-vl:free",
		Provider:    []string{"novita/bf16"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		Multimodal: true,
		Pricing:    &config.PricingConfig{PromptPerM: 0, CompletionPerM: 0},
	},
}
