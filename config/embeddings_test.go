package config

import (
	"testing"

	"polaris/embed"
)

// TestNewEmbedClient_ProviderSelection pins the resolution order that lets an
// existing config.yaml keep working untouched while a new one opts into the
// hosted provider. The stakes: getting the unset case wrong silently disables
// embeddings for every deployment that predates this block.
func TestNewEmbedClient_ProviderSelection(t *testing.T) {
	t.Run("unset provider keeps the pre-subsystem behavior", func(t *testing.T) {
		var c Config
		c.Ollama.BaseURL = "http://localhost:11434"
		c.Ollama.EmbedModel = "nomic-embed-text"
		client := c.NewEmbedClient()
		if client == nil {
			t.Fatal("nil client, want the local Ollama fallback")
		}
		if got := client.Model(); got != "nomic-embed-text" {
			t.Errorf("model = %q, want the ollama.embed_model fallback", got)
		}
		if !client.Local() {
			t.Error("client is not Local; an unset provider must resolve to Ollama, not OpenRouter")
		}
	})

	t.Run("unset provider with no ollama is nil", func(t *testing.T) {
		var c Config
		if got := c.NewEmbedClient(); got != nil {
			t.Errorf("client = %v, want nil (embeddings disabled, not broken)", got)
		}
	})

	t.Run("openrouter provider reuses the openrouter key", func(t *testing.T) {
		var c Config
		c.OpenRouter.APIKey = "sk-or-key"
		c.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
		c.Ollama.BaseURL = "http://localhost:11434" // must be ignored
		c.Embeddings.Provider = "openrouter"
		client := c.NewEmbedClient()
		if client == nil {
			t.Fatal("nil client with openrouter configured")
		}
		if client.Local() {
			t.Error("Local() = true for the openrouter provider")
		}
		if got := client.Model(); got != embed.DefaultOpenRouterModel {
			t.Errorf("model = %q, want the OpenRouter default %q", got, embed.DefaultOpenRouterModel)
		}
	})

	t.Run("openrouter with no key is nil, not a broken client", func(t *testing.T) {
		var c Config
		c.Embeddings.Provider = "openrouter"
		if got := c.NewEmbedClient(); got != nil {
			t.Errorf("client = %v, want nil when openrouter.api_key is missing", got)
		}
	})

	t.Run("explicit model wins over both providers' defaults", func(t *testing.T) {
		var c Config
		c.OpenRouter.APIKey = "k"
		c.Embeddings.Provider = "openrouter"
		c.Embeddings.Model = "openai/text-embedding-3-small"
		if got := c.NewEmbedClient().Model(); got != "openai/text-embedding-3-small" {
			t.Errorf("model = %q, want the explicit embeddings.model", got)
		}

		var o Config
		o.Ollama.BaseURL = "http://localhost:11434"
		o.Ollama.EmbedModel = "nomic-embed-text"
		o.Embeddings.Provider = "ollama"
		o.Embeddings.Model = "snowflake-arctic-embed"
		if got := o.NewEmbedClient().Model(); got != "snowflake-arctic-embed" {
			t.Errorf("model = %q, want the explicit embeddings.model", got)
		}
	})

	t.Run("ollama provider falls back to ollama.base_url", func(t *testing.T) {
		var c Config
		c.Ollama.BaseURL = "http://host.docker.internal:11434"
		c.Embeddings.Provider = "ollama"
		client := c.NewEmbedClient()
		if client == nil || !client.Local() {
			t.Fatalf("client = %v, want a local client", client)
		}
		if got := client.Model(); got != embed.DefaultOllamaModel {
			t.Errorf("model = %q, want the Ollama default %q", got, embed.DefaultOllamaModel)
		}
	})

	t.Run("embeddings.base_url overrides both", func(t *testing.T) {
		var c Config
		c.OpenRouter.APIKey = "k"
		c.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
		c.Embeddings.Provider = "openrouter"
		c.Embeddings.BaseURL = "https://proxy.internal/v1"
		if c.NewEmbedClient() == nil {
			t.Fatal("nil client with an explicit base_url")
		}
	})
}
