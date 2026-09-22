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
	},
	{
		// Pinned to GMICloud (fp8 — DeepSeek's own native training/serving
		// precision, not a downgrade) with StreamLake as a same-tier
		// fallback, per a live OpenRouter /endpoints price+quantization
		// survey on 2026-08-29: the official "deepseek" endpoint's price
		// here doubles on weekday UTC 01:00-04:00 and 06:00-10:00 (see its
		// `pricing.overrides`), landing at parity with the generic
		// $1.32/$3.96-per-M-token third-party tier for those hours.
		// GMICloud/StreamLake sit at ~$1.12/$3.36 per M tokens flat,
		// all day — cheaper than official even off-peak, with no
		// fp4-quantized provider actually cheaper than these fp8/native
		// ones. DeepInfra fp8 prices lower still but caps completions at
		// 16K tokens (vs 384K+ here) and ran ~90% uptime in the survey —
		// not viable for this model's reasoning output.
		ID:          "deepseek-pro",
		Name:        "DeepSeek V4 Pro",
		Model:       "deepseek/deepseek-v4-pro-0813",
		Provider:    []string{"gmicloud/fp8", "streamlake"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
	},
	{
		// Deprecates the old V4 Flash "deepseek" entry (2026-09-22) —
		// deepseek/deepseek-v4-flash-0731 with its five-deep Baidu/
		// DeepInfra/StreamLake/BaseTen/Novita fallback chain — in favor of
		// V4.1 Flash. Reused the "deepseek" ID (rather than dropping the
		// separate "deepseek-v41-flash" ID) so existing thread selections,
		// config.yaml's default_model/model_overrides, and the
		// ResearchWorker designation below all keep resolving across the
		// swap, same ID-stability approach as the MiMo v2.6 replacement
		// above.
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
		// Tier 2 Deep Research sub-agent default (see
		// docs/plans/deep-research-two-tier.md), carried over from the old
		// V4 Flash entry this replaces rather than assumed: the same live
		// endpoint survey above confirms both providers report "tools" in
		// supported_parameters, with tool_choice "auto" support on both
		// and full none/auto/required/function support on the Fireworks
		// fallback. See config.ModelConfig.ResearchWorker.
		ResearchWorker: true,
	},
	{
		ID:          "luna",
		Name:        "ChatGPT Luna",
		Model:       "openai/gpt-5.6-luna",
		Provider:    []string{"openai"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
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
	},
}
