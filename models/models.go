package models

import "polaris/config"

// Registry is the complete catalog of models Polaris knows about.
// Config can override the default and tune per-model settings, but
// adding a new model happens here, not in config.yaml.
var Registry = []config.ModelConfig{
	{
		// NOT multimodal, despite the naming symmetry with "mimo" below —
		// confirmed against OpenRouter's own live endpoint metadata
		// (GET /api/v1/models/xiaomi/mimo-v2.5-pro/endpoints): every
		// endpoint for this model reports input_modalities: ["text"]
		// only. Marking it multimodal here previously broke image
		// uploads entirely, since it's listed first and
		// Config.MultimodalModel picks the first match.
		ID:          "mimo-pro",
		Name:        "MiMo v2.5 Pro",
		Model:       "xiaomi/mimo-v2.5-pro",
		Provider:    []string{"xiaomi/fp8"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
	},
	{
		// Genuinely vision-capable — confirmed against OpenRouter's live
		// endpoint metadata: input_modalities includes "image" (and
		// audio/video) across all of this model's providers, unlike
		// mimo-pro above. Used as the describe-image step for uploads
		// when the thread's own selected model can't see images itself
		// (see gateway's resolveAttachment / Config.MultimodalModel).
		ID:          "mimo",
		Name:        "MiMo v2.5",
		Model:       "xiaomi/mimo-v2.5",
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
		// Five-deep fallback chain: Baidu -> DeepInfra -> StreamLake ->
		// BaseTen -> Novita, all fp8 (no fp4, per this file's existing
		// precision policy). Baidu/DeepInfra were the original pair, but a
		// live incident on 2026-09-06 showed OpenRouter can exhaust both
		// entries of a two-provider Provider list in a single request —
		// Baidu and DeepInfra returned 429 tpm_rate_limit_exceeded
		// simultaneously mid-conversation (a shared/pooled provider tier
		// saturating under other OpenRouter users' traffic, not this
		// account hitting its own cap), surfacing OpenRouter's raw error
		// JSON straight into the chat transcript (see llm.APIError). Three
		// more rungs were added the same day from a live GET /api/v1/
		// models/deepseek/deepseek-v4-flash-0731/endpoints query, in
		// priority order:
		//   - StreamLake: cheapest input_cache_read of any fp8 provider,
		//     $0.0028/M vs. Baidu's $0.028/M and DeepInfra's $0.015/M —
		//     matters because prompt caching, not fresh prompt tokens, is
		//     where this app's actual DeepSeek spend concentrates. Already
		//     a known-good fp8 provider here, as deepseek-pro's own
		//     fallback below.
		//   - BaseTen: the only other fp8 option with full tool_choice
		//     support (none/auto/required/function all true — several
		//     others in the survey only support "auto"), moderate pricing,
		//     99.84% 1-day uptime.
		//   - Novita: best observed reliability in the survey (99.99%
		//     1-day, 100% 5-minute uptime) — added purely as a last-resort
		//     rung ("just in case"), despite pricier prompt/completion
		//     rates, since this deep in the chain availability matters more
		//     than shaving cost further.
		// The official "deepseek" endpoint remains excluded: its list price
		// is now higher than every one of these third-party routes even
		// off-peak.
		ID:          "deepseek",
		Name:        "DeepSeek V4 Flash",
		Model:       "deepseek/deepseek-v4-flash-0731",
		Provider:    []string{"baidu/fp8", "deepinfra/fp8", "streamlake/fp8", "baseten/fp8", "novita/fp8"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
		// Tier 2 Deep Research sub-agent default (see
		// docs/plans/deep-research-two-tier.md) — a roster survey found
		// nothing that clearly beats this model on cost + speed +
		// confirmed tool-calling reliability for the worker role (MiMo
		// Pro is disqualified outright: no tools support on its default
		// route). See config.ModelConfig.ResearchWorker.
		ResearchWorker: true,
	},
	{
		// Additive alongside "deepseek" above, not a replacement — per a
		// live GET /api/v1/models/deepseek/deepseek-v4.1-flash/endpoints
		// survey on 2026-09-12 (released 2026-09-10). Genuinely
		// multimodal per architecture.input_modalities (["text","image"]),
		// unlike the V4 Flash/Pro entries above.
		//
		// Unlike deepseek-pro/deepseek above, the official "deepseek" tag
		// is the primary route here, not excluded — re-checked the
		// reasoning rather than copying the sibling entries' exclusion.
		// Its pricing.overrides only double the rate (to $0.30/$1.20 per M,
		// matching the third-party fp8 tier below) during a narrow weekday
		// window (01:00-04:00 and 06:00-10:00 UTC, ~21% of the week);
		// the other ~79% of the time, incl. all weekend, it's $0.15/$0.60
		// with $0.003/M cache reads — cheaper than every third-party
		// endpoint in the survey at every hour, not just off-peak. That's
		// the opposite of deepseek-pro/deepseek's official route, whose
		// list price loses to third-party even off-peak — a genuinely
		// different pricing shape per model, not a fixed platform rule, so
		// don't copy this endpoint's inclusion/exclusion onto other models
		// without re-running the survey.
		// Fireworks is the fallback: flat (no time-of-day pricing),
		// $0.22/$0.66 per M, 98.98% uptime, 943,718-token max completion —
		// beats every fp8 third-party provider in the survey (GMICloud,
		// Novita, etc., all $0.30/$1.20) on price while staying reliable,
		// so it's a better second rung than reusing deepseek-pro's known-
		// good fp8 providers here.
		ID:          "deepseek-v41-flash",
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
		// Text-only (no image input) — NOT marked multimodal, matching
		// mimo-pro above, so Config.MultimodalModel doesn't accidentally
		// pick this for image-description duty. The ":free" slug routes
		// through OpenRouter's no-cost provider pool; cost shows as $0.
		ID:          "nemotron-ultra",
		Name:        "NemoTron Ultra (Free)",
		Model:       "nvidia/nemotron-3-ultra-550b-a55b:free",
		Provider:    []string{"nvidia"},
		Temperature: 0.4,
		MaxTokens:   32000,
	},
}
