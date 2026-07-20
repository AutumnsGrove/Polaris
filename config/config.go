// Package config loads config.yaml, expanding ${ENV_VAR} references
// before parsing so secrets stay out of the committed file.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port int    `yaml:"port"`
		Host string `yaml:"host"` // bind address, e.g. "0.0.0.0" or "100.81.103.51"
	} `yaml:"server"`

	OpenRouter struct {
		APIKey  string `yaml:"api_key"`
		BaseURL string `yaml:"base_url"`
	} `yaml:"openrouter"`

	SearXNG struct {
		BaseURL string `yaml:"base_url"`
	} `yaml:"searxng"`

	Foursquare struct {
		APIKey string `yaml:"api_key"` // Service API Key; empty disables nearby_search's Foursquare path (falls back to SearXNG)
	} `yaml:"foursquare"`

	// DefaultLocation is geocoded and used when nearby_search omits an
	// explicit location — e.g. "Seattle, WA" or raw "47.6062, -122.3321".
	// Optional; without it, nearby_search requires a location argument.
	DefaultLocation string `yaml:"default_location"`

	Voice struct {
		// STTModel/STTFallbackModel are OpenRouter model slugs for push-to-talk
		// transcription.
		STTModel         string `yaml:"stt_model"`
		STTFallbackModel string `yaml:"stt_fallback_model"`

		// TTSModel/TTSVoice/TTSFormat drive spoken replies in voice mode.
		// Kokoro-82M via OpenRouter's dedicated /audio/speech endpoint.
		TTSModel  string `yaml:"tts_model"`
		TTSVoice  string `yaml:"tts_voice"`
		TTSFormat string `yaml:"tts_format"` // "mp3" or "pcm" — only two OpenRouter documents for this endpoint
	} `yaml:"voice"`

	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`

	Logging struct {
		Dir string `yaml:"dir"` // daily-rotated files (YYYY-MM-DD.log), 90-day retention
	} `yaml:"logging"`

	Service struct {
		Label string `yaml:"label"`
	} `yaml:"service"`

	DefaultModel string        `yaml:"default_model"`
	Models       []ModelConfig `yaml:"models"`

	// ContextWindowTokens is the threshold (prompt + completion tokens,
	// per the LLM's own usage numbers) at which a thread auto-compacts:
	// the model summarizes everything so far, and future turns continue
	// from that summary instead of the full raw history. Also the
	// denominator for the context-usage % shown next to thread cost.
	ContextWindowTokens int `yaml:"context_window_tokens"`

	// MaxAgentTurns bounds one turn's tool-use loop (search/read/nearby_search
	// calls) before the model is forced to wrap up with whatever it's
	// gathered so far. Exists to stop a genuinely stuck model from looping
	// forever, not to rush a thorough one — the more agentic models
	// routinely use 5-8 calls on a real multi-part research question.
	MaxAgentTurns int `yaml:"max_agent_turns"`
}

// ModelConfig describes one entry in the model selector. Provider pins
// the OpenRouter provider order (e.g. ["xiaomi/fp8"]) so prompt caching
// stays consistent — different providers for the same model often have
// wildly different (or no) caching support/pricing.
type ModelConfig struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Model       string   `yaml:"model"`
	Provider    []string `yaml:"provider"`
	Temperature float64  `yaml:"temperature"`
	MaxTokens   int      `yaml:"max_tokens"`

	// Reasoning turns on OpenRouter's unified reasoning-token support for
	// models that do internal "thinking" before answering (DeepSeek's
	// reasoning line, Xiaomi MiMo, etc). Without this, some providers
	// still reason internally but don't surface it in the response at
	// all — nil/omitted means "don't ask for it".
	Reasoning *ReasoningConfig `yaml:"reasoning"`
}

// ReasoningConfig mirrors OpenRouter's `reasoning` request field
// (https://openrouter.ai/docs/use-cases/reasoning-tokens). Effort and
// MaxTokens are mutually exclusive per OpenRouter's API — set at most one.
type ReasoningConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Effort    string `yaml:"effort"`     // "low" | "medium" | "high"
	MaxTokens int    `yaml:"max_tokens"` // token budget for reasoning, if not using Effort
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8899
	}
	if cfg.OpenRouter.BaseURL == "" {
		cfg.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "./polaris.db"
	}
	if cfg.Logging.Dir == "" {
		cfg.Logging.Dir = "./logs"
	}
	if cfg.Service.Label == "" {
		cfg.Service.Label = "polaris"
	}
	if cfg.Voice.STTModel == "" {
		cfg.Voice.STTModel = "mistralai/voxtral-mini-transcribe"
	}
	if cfg.Voice.STTFallbackModel == "" {
		cfg.Voice.STTFallbackModel = "openai/whisper-large-v3"
	}
	if cfg.Voice.TTSModel == "" {
		cfg.Voice.TTSModel = "hexgrad/kokoro-82m"
	}
	if cfg.Voice.TTSVoice == "" {
		cfg.Voice.TTSVoice = "bf_lily"
	}
	if cfg.Voice.TTSFormat == "" {
		cfg.Voice.TTSFormat = "mp3"
	}
	if len(cfg.Models) == 0 {
		return nil, fmt.Errorf("config: at least one entry required under models")
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = cfg.Models[0].ID
	}
	if cfg.ContextWindowTokens <= 0 {
		cfg.ContextWindowTokens = 100_000
	}
	if cfg.MaxAgentTurns <= 0 {
		cfg.MaxAgentTurns = 50
	}

	return &cfg, nil
}

// ModelByID looks up a model config by its selector ID. Falls back to
// the default model if id is empty or unknown.
func (c *Config) ModelByID(id string) ModelConfig {
	if id != "" {
		for _, m := range c.Models {
			if m.ID == id {
				return m
			}
		}
	}
	for _, m := range c.Models {
		if m.ID == c.DefaultModel {
			return m
		}
	}
	return c.Models[0]
}
