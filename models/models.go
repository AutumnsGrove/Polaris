package models

import "polaris/config"

// Registry is the complete catalog of models Polaris knows about.
// Config can override the default and tune per-model settings, but
// adding a new model happens here, not in config.yaml.
var Registry = []config.ModelConfig{
	{
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
		Model:       "deepseek/deepseek-v4-flash",
		Provider:    []string{"deepseek"},
		Temperature: 0.4,
		MaxTokens:   32000,
		Reasoning: &config.ReasoningConfig{
			Enabled: true,
			Effort:  "medium",
		},
	},
}
