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
		ID:          "deepseek-pro",
		Name:        "DeepSeek V4 Pro",
		Model:       "deepseek/deepseek-v4-pro",
		Provider:    []string{"deepseek"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
	},
	{
		ID:          "deepseek",
		Name:        "DeepSeek V4 Flash",
		Model:       "deepseek/deepseek-v4-flash-0731",
		Provider:    []string{"deepseek"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
	},
}
