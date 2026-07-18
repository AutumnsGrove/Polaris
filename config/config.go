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
		// transcription. Whisper Turbo is fast but occasionally times out under
		// load; the fallback (plain whisper-large-v3) is slower but more reliable.
		STTModel         string `yaml:"stt_model"`
		STTFallbackModel string `yaml:"stt_fallback_model"`
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
		cfg.Voice.STTModel = "openai/whisper-large-v3-turbo"
	}
	if cfg.Voice.STTFallbackModel == "" {
		cfg.Voice.STTFallbackModel = "openai/whisper-large-v3"
	}
	if len(cfg.Models) == 0 {
		return nil, fmt.Errorf("config: at least one entry required under models")
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = cfg.Models[0].ID
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
