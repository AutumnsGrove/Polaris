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
		// Pinned to Baidu (fp8, ~68% below OpenRouter's list rate for this
		// model) with DeepInfra (fp8) as fallback, per the same 2026-08-29
		// survey: official pricing here is $0.22/$0.66 per M tokens
		// off-peak (doubling on the same weekday peak hours as Pro above),
		// while Baidu serves the same fp8 precision at $0.045/$0.09 with
		// 99.95% uptime. The fp4 options at this price point (OpenInference,
		// Relace) offer no actual savings over Baidu's fp8 — no reason to
		// take the precision hit.
		ID:          "deepseek",
		Name:        "DeepSeek V4 Flash",
		Model:       "deepseek/deepseek-v4-flash-0731",
		Provider:    []string{"baidu/fp8", "deepinfra/fp8"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
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
