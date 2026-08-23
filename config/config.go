// Package config loads config.yaml, expanding ${ENV_VAR} references
// before parsing so secrets stay out of the committed file.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"polaris/backup"
	"polaris/logger"
	"polaris/r2"
)

// log writes to stderr only until logger.Init runs (see logger.go's doc
// comment on dynamicWriter) — Load always runs before that, since cmd/run.go
// needs cfg.Logging.Dir to call Init in the first place. Still worth using
// here rather than fmt.Println: it's the same timestamped/leveled format as
// every other package, and it means a future caller that does call Init
// earlier gets these lines in the log file too, for free.
var log = logger.WithPrefix("config")

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

	// BlockedSourcesFile lists domains (one per line, "#" comments allowed)
	// that web_search and web_read must never surface or fetch — for
	// sources that are unreliable by construction (AI-generated wikis,
	// known misinformation mills), not just low-ranked. Edit the file
	// directly; no code changes or config reload needed beyond a restart
	// (the blocklist, like the SearXNG client itself, is loaded once at
	// startup — see gateway.New/cmd/search.go).
	BlockedSourcesFile string `yaml:"blocked_sources_file"`

	// DomainRankingsFile holds per-domain search ranking preferences
	// (block/lower/default/raise/pin — see search.DomainRankings), edited
	// either by hand or by Atlas's ranking popover UI. Unlike
	// BlockedSourcesFile this *is* hot-reloaded — re-read on its mtime
	// changing, no restart needed, since the popover writes to it live
	// while the server keeps running.
	DomainRankingsFile string `yaml:"domain_rankings_file"`

	Foursquare struct {
		APIKey string `yaml:"api_key"` // Service API Key; empty disables nearby_search's Foursquare path (falls back to SearXNG)
	} `yaml:"foursquare"`

	Tavily struct {
		// APIKey; empty disables web_read's Tavily fallback entirely (it's
		// only ever a fallback, never the default fetch path — see
		// tools/web_read.go). https://tavily.com
		APIKey string `yaml:"api_key"`
	} `yaml:"tavily"`

	Parallel struct {
		// APIKey; empty disables web_search's Parallel fallback entirely
		// (see tools/web_search.go) — only ever tried once SearXNG itself
		// reports it's degraded, ahead of Tavily's own fallback in the
		// same spot. The account has a card on file, so going over the
		// free tier bills real money — see store.Store's api_usage table,
		// which enforces the monthly cap this key alone doesn't.
		// https://parallel.ai
		APIKey string `yaml:"api_key"`
	} `yaml:"parallel"`

	Brave struct {
		// APIKey; empty disables the Brave fallback entirely — tried
		// first among the paid fallbacks once SearXNG reports degraded
		// (SearXNG -> Brave -> Parallel -> Tavily, see tools/web_search.go
		// and gateway/search.go), since it's the only one returning real,
		// multi-result listings rather than an AI-pre-summarized answer —
		// what Atlas's browsing UI actually needs. No ongoing free tier
		// (just a one-time $5/mo signup credit), so every query bills the
		// account's card on file — see store.Store's api_usage table,
		// which enforces the monthly cap this key alone doesn't.
		// https://brave.com/search/api/
		APIKey string `yaml:"api_key"`
	} `yaml:"brave"`

	GitHub struct {
		// Token is an optional personal access token attached to
		// github_repo's API calls. Unlike Foursquare/Tavily's API keys,
		// this doesn't gate the tool on/off — GitHub's REST API works fine
		// unauthenticated, just capped at 60 requests/hour instead of
		// 5000. A no-scope token is enough (only public read endpoints are
		// used); needs at least public repo read to see private repos it
		// was granted access to. https://github.com/settings/tokens
		Token string `yaml:"token"`
	} `yaml:"github"`

	LastFM struct {
		// APIKey; empty disables the music tool entirely — unlike
		// GitHub's token, Last.fm has no unauthenticated fallback path, so
		// every mode fails with a clear error rather than degrading.
		// Free self-service signup, no approval wait:
		// https://www.last.fm/api/account/create
		APIKey string `yaml:"api_key"`
	} `yaml:"lastfm"`

	Hardcover struct {
		// APIKey is optional, unlike LastFM's — the books tool's Open
		// Library fallback (subject-tag overlap) works with no key at all,
		// just as a weaker signal than Hardcover's curated-list
		// co-occurrence data. Empty, invalid, or expired all degrade to
		// Open Library-only rather than failing the tool outright (see
		// tools/books.go). Hardcover tokens are personal-account JWTs with
		// a ~1 year expiry, not a stable service key — regenerate at
		// https://hardcover.app account settings when books.go's logs
		// start reporting auth failures.
		APIKey string `yaml:"api_key"`
	} `yaml:"hardcover"`

	TMDB struct {
		// APIKey; empty disables the movies tool entirely — like LastFM's,
		// TMDB has no unauthenticated fallback path, so every call fails
		// with a clear error rather than degrading. Free self-service
		// signup, no approval wait: https://www.themoviedb.org/settings/api
		APIKey string `yaml:"api_key"`
	} `yaml:"tmdb"`

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

		// TTSProvider pins Kokoro's OpenRouter backing provider (currently
		// "DeepInfra" or "Together" — see the model's /endpoints listing).
		// DeepInfra is Kokoro's cheaper default but has shown wildly
		// inconsistent latency (15-40s+, sometimes hanging past our own
		// client timeout entirely); Together has tested consistently ~12-13s.
		// Empty means no provider field is sent — OpenRouter picks/fails
		// over between them itself.
		TTSProvider string `yaml:"tts_provider"`
	} `yaml:"voice"`

	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`

	Logging struct {
		Dir string `yaml:"dir"` // daily-rotated files (YYYY-MM-DD.log), 90-day retention
	} `yaml:"logging"`

	Backup struct {
		// Dir defaults to a "backups" subdirectory next to the database
		// file (see backup.Dir) — set this only to point backups
		// somewhere other than right beside polaris.db, e.g. a second
		// disk.
		Dir string `yaml:"dir"`
		// RetentionDays is how long a daily backup is kept before the
		// scheduler (backup.RunScheduler, started by cmd/run.go) prunes
		// it. 30 gives a full month of rollback room without the
		// backups directory growing unbounded on the potato's limited
		// disk.
		RetentionDays int `yaml:"retention_days"`
	} `yaml:"backup"`

	R2 struct {
		// AccountID/AccessKeyID/SecretAccessKey/Bucket together enable
		// mirroring every backup off-device to Cloudflare R2, in addition
		// to (not instead of) the local backups/ folder — protects
		// against the device itself failing, not just against needing to
		// roll back a bad database state. All four empty (the default)
		// disables mirroring entirely; local backups keep working exactly
		// as before with no other change in behavior. Use a dedicated
		// bucket and a scoped API token limited to Object Read & Write on
		// that bucket only, not an account-wide key. See backup.Mirror/
		// PruneRemote/Fetch, and r2.Client, which signs requests with
		// AWS SigV4 by hand rather than pulling in aws-sdk-go-v2.
		// https://developers.cloudflare.com/r2/api/tokens/
		AccountID       string `yaml:"account_id"`
		AccessKeyID     string `yaml:"access_key_id"`
		SecretAccessKey string `yaml:"secret_access_key"`
		Bucket          string `yaml:"bucket"`
	} `yaml:"r2"`

	Attachments struct {
		// Dir stores uploaded files (PDFs, images) on disk next to the
		// database — see gateway's handleUpload. Referenced by generated
		// filename from messages.attachment_path, not by anything the
		// client supplies directly.
		Dir string `yaml:"dir"`
	} `yaml:"attachments"`

	Service struct {
		Label string `yaml:"label"`
	} `yaml:"service"`

	DefaultModel string `yaml:"default_model"`

	// ModelOverrides allows config.yaml to tune per-model settings
	// (temperature, max_tokens, reasoning effort) without declaring the
	// full model catalog. The registry is the source of truth for what
	// models exist; config is for deployment-specific tuning.
	ModelOverrides map[string]ModelOverride `yaml:"model_overrides"`

	// Models is the final merged model list (registry + overrides),
	// populated by Load. Not part of the YAML schema.
	Models []ModelConfig `yaml:"-"`

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

	// Multimodal marks a model as vision-capable — used to pick which
	// model describes an uploaded image ahead of the main turn when the
	// thread's own selected model isn't multimodal itself (DeepSeek, as
	// of this registry). See gateway's resolveAttachment and
	// Config.MultimodalModel.
	Multimodal bool `yaml:"multimodal"`
}

// ReasoningConfig mirrors OpenRouter's `reasoning` request field
// (https://openrouter.ai/docs/use-cases/reasoning-tokens). Effort and
// MaxTokens are mutually exclusive per OpenRouter's API — set at most one.
type ReasoningConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Effort    string `yaml:"effort"`     // "low" | "medium" | "high"
	MaxTokens int    `yaml:"max_tokens"` // token budget for reasoning, if not using Effort
}

// ModelOverride specifies per-model tuning in config.yaml. All fields
// optional; unset fields inherit from the registry default.
type ModelOverride struct {
	Temperature *float64         `yaml:"temperature"`
	MaxTokens   *int             `yaml:"max_tokens"`
	Reasoning   *ReasoningConfig `yaml:"reasoning"`
}

// Load reads config.yaml and applies defaults. The registry parameter
// provides the base model catalog; config.yaml can tune per-model
// settings via model_overrides but doesn't declare new models.
func Load(path string, registry []ModelConfig) (*Config, error) {
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
	if cfg.OpenRouter.APIKey == "" {
		// Virtually every feature (chat, search, voice, titles) depends on
		// this — without it, the first real symptom would be an opaque 401
		// from OpenRouter deep inside a request, far from the actual
		// misconfiguration. Failing fast here means the operator sees the
		// real problem before the server even starts.
		return nil, fmt.Errorf("config: openrouter.api_key is required")
	}
	if cfg.OpenRouter.BaseURL == "" {
		cfg.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	}
	if cfg.SearXNG.BaseURL == "" {
		// Not fatal — web_search/nearby_search degrade gracefully without
		// it (see tools/web_search.go's nil-ctx.SearXNG guard and
		// nearby_search's Foursquare-first fallback) — but worth a loud
		// warning rather than the alternative: search silently returning
		// "unsupported protocol scheme" on first use with nothing pointing
		// back to this being unset.
		log.Warn("searxng.base_url is not set — web_search and nearby_search's web-search fallback will be unavailable")
	}
	// Catches a real, previously-silent production gap: a deploy can have
	// BRAVE_API_KEY/PARALLEL_API_KEY/TAVILY_API_KEY set in its actual
	// environment (e.g. Docker's .env, passed through docker-compose.yml)
	// while config.yaml — gitignored, hand-maintained, never synced by
	// `git pull` — still lacks the brave:/parallel:/tavily: block that
	// would reference it via ${...}. os.ExpandEnv above only substitutes
	// placeholders that literally appear in the file, so the env var being
	// set gives false confidence the fallback tier is live: web_search's
	// SearXNG -> Brave -> Parallel -> Tavily chain (tools/web_search.go)
	// silently skips any tier whose APIKey ends up empty, with nothing in
	// the logs pointing back to this being the reason.
	warnIfEnvSetButUnconfigured := func(envVar, configuredKey, yamlPath string) {
		if configuredKey == "" && os.Getenv(envVar) != "" {
			log.Warn(envVar+" is set in the environment but config.yaml has no "+yamlPath+" referencing it — this fallback tier will be silently disabled", "env_var", envVar, "yaml_path", yamlPath)
		}
	}
	warnIfEnvSetButUnconfigured("BRAVE_API_KEY", cfg.Brave.APIKey, "brave.api_key")
	warnIfEnvSetButUnconfigured("PARALLEL_API_KEY", cfg.Parallel.APIKey, "parallel.api_key")
	warnIfEnvSetButUnconfigured("TAVILY_API_KEY", cfg.Tavily.APIKey, "tavily.api_key")
	if cfg.BlockedSourcesFile == "" {
		cfg.BlockedSourcesFile = "./blocked_sources.txt"
	}
	if cfg.DomainRankingsFile == "" {
		cfg.DomainRankingsFile = "./domain_rankings.yaml"
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "./polaris.db"
	}
	if cfg.Logging.Dir == "" {
		cfg.Logging.Dir = "./logs"
	}
	if cfg.Backup.Dir == "" {
		// Depends on Database.Path already being defaulted above.
		cfg.Backup.Dir = backup.Dir(cfg.Database.Path)
	}
	if cfg.Backup.RetentionDays <= 0 {
		cfg.Backup.RetentionDays = backup.DefaultRetentionDays
	}
	if cfg.Attachments.Dir == "" {
		cfg.Attachments.Dir = "./attachments"
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
	} else if cfg.Voice.TTSFormat != "mp3" && cfg.Voice.TTSFormat != "pcm" {
		// OpenRouter's Kokoro endpoint only documents these two — anything
		// else would only surface as an opaque upstream error the first
		// time /audio/speech is actually called, far from this typo.
		log.Warn("voice.tts_format is neither \"mp3\" nor \"pcm\", falling back to \"mp3\"", "configured", cfg.Voice.TTSFormat)
		cfg.Voice.TTSFormat = "mp3"
	}
	if cfg.Voice.TTSProvider == "" {
		cfg.Voice.TTSProvider = "Together"
	}
	if cfg.ContextWindowTokens <= 0 {
		cfg.ContextWindowTokens = 100_000
	}
	if cfg.MaxAgentTurns <= 0 {
		cfg.MaxAgentTurns = 50
	}

	// Apply registry as base, then merge config overrides
	if len(registry) == 0 {
		return nil, fmt.Errorf("config: model registry is empty")
	}
	cfg.Models = applyOverrides(registry, cfg.ModelOverrides)

	if cfg.DefaultModel == "" {
		cfg.DefaultModel = cfg.Models[0].ID
	} else {
		found := false
		for _, m := range cfg.Models {
			if m.ID == cfg.DefaultModel {
				found = true
				break
			}
		}
		if !found {
			// ModelByID/DefaultModelOrFirst already fall back to the first
			// model whenever DefaultModel doesn't match — silently, with no
			// trace of why the configured default was ignored. Fixing that
			// here, at the one place that actually knows the original
			// (invalid) value, rather than in every fallback call site.
			log.Warn("default_model doesn't match any configured model, falling back to the first model",
				"configured", cfg.DefaultModel, "fallback", cfg.Models[0].ID)
			cfg.DefaultModel = cfg.Models[0].ID
		}
	}

	return &cfg, nil
}

func applyOverrides(registry []ModelConfig, overrides map[string]ModelOverride) []ModelConfig {
	result := make([]ModelConfig, len(registry))
	for i, base := range registry {
		result[i] = base
		if override, ok := overrides[base.ID]; ok {
			if override.Temperature != nil {
				result[i].Temperature = *override.Temperature
			}
			if override.MaxTokens != nil {
				result[i].MaxTokens = *override.MaxTokens
			}
			if override.Reasoning != nil {
				result[i].Reasoning = override.Reasoning
			}
		}
	}
	return result
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

// MultimodalModel returns the first vision-capable model in the registry
// (see ModelConfig.Multimodal), for describing an uploaded image ahead of
// a turn whose own selected model can't see images itself. ok is false if
// none is configured — the caller should tell the user rather than
// silently ignore the attachment.
func (c *Config) MultimodalModel() (model ModelConfig, ok bool) {
	for _, m := range c.Models {
		if m.Multimodal {
			return m, true
		}
	}
	return ModelConfig{}, false
}

// R2Client builds an r2.Client from the R2 config section, or nil if R2
// mirroring isn't configured (see r2.NewClient) — the single place every
// caller (the backup scheduler, `polaris backup create`/`restore-remote`,
// the Docker-mode REST handlers) gets a client from, so they all agree on
// what "configured" means without duplicating the four-empty-fields check.
func (c *Config) R2Client() *r2.Client {
	return r2.NewClient(c.R2.AccountID, c.R2.AccessKeyID, c.R2.SecretAccessKey, c.R2.Bucket)
}

// DefaultModelOrFirst returns the configured default model ID, falling
// back to the first model if default_model is unset or invalid.
func (c *Config) DefaultModelOrFirst() string {
	if c.DefaultModel != "" {
		for _, m := range c.Models {
			if m.ID == c.DefaultModel {
				return c.DefaultModel
			}
		}
	}
	if len(c.Models) > 0 {
		return c.Models[0].ID
	}
	return ""
}
