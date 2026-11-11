package llm

// Settings is a read-only snapshot of how a Client is configured: everything
// needed to rebuild an equivalent request on a different transport. agent's
// Nova path reads it off ctx.LLM (a ChatClient interface, so the concrete
// fields are otherwise unreachable) to build its own provider without every
// call site that constructs a Client having to also pass a second copy of
// the same connection details.
type Settings struct {
	BaseURL       string
	APIKey        string
	Model         string
	Temperature   float64
	MaxTokens     int
	Provider      *ProviderRouting
	Reasoning     *ReasoningParams
	PromptCaching bool
}

// Settings returns the client's configuration. The routing and reasoning
// pointers are shared with the client, not copied: callers treat them as
// read-only.
func (c *Client) Settings() Settings {
	return Settings{
		BaseURL:       c.baseURL,
		APIKey:        c.apiKey,
		Model:         c.model,
		Temperature:   c.temperature,
		MaxTokens:     c.maxTokens,
		Provider:      c.provider,
		Reasoning:     c.reasoning,
		PromptCaching: c.promptCaching,
	}
}

// SettingsProvider is implemented by clients that can describe their own
// configuration. *Client does; test doubles such as llmtest.MockClient do not.
type SettingsProvider interface {
	Settings() Settings
}
