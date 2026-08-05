// vision.go adds one capability doRequest's plain-string ChatMessage.Content
// can't express: OpenRouter's vision format needs a content ARRAY (a text
// block plus an image_url block), not a string. Rather than widen
// ChatMessage.Content for every other call site's sake, DescribeImage
// builds its own one-off, non-streaming request — it's a single quick
// "what's in this picture" call, not part of the tool-use loop, so it
// doesn't need doRequest's SSE/tool-call machinery at all.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"polaris/prompts"
)

type visionContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *visionImageURL `json:"image_url,omitempty"`
}

type visionImageURL struct {
	URL string `json:"url"`
}

type visionMessage struct {
	Role    string               `json:"role"`
	Content []visionContentBlock `json:"content"`
}

type visionRequest struct {
	Model       string           `json:"model"`
	Messages    []visionMessage  `json:"messages"`
	Temperature float64          `json:"temperature"`
	MaxTokens   int              `json:"max_tokens"`
	Provider    *ProviderRouting `json:"provider,omitempty"`
	Stream      bool             `json:"stream"`
}

type visionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		Cost float64 `json:"cost"`
	} `json:"usage"`
}

// DescribeImage asks this client's model (expected to be vision-capable —
// see config.ModelConfig.Multimodal) to describe an image, for the
// multimodal-attachment pipeline: a model that isn't itself multimodal
// still needs some way to "see" an attached photo, so a capable model
// describes it first and that description is folded into the main
// model's context as plain text (see gateway's resolveAttachment).
//
// The prompt itself (vision.describe_image in prompts.yaml) asks for a
// thorough, literal description rather than an interpretation — the
// result becomes the ONLY thing the main model (which never sees the
// actual image) has to work with, so vague output there directly limits
// what questions about the image can be answered downstream.
func (c *Client) DescribeImage(ctx context.Context, imageBase64, mimeType string) (description string, costUSD float64, err error) {
	reqBody := visionRequest{
		Model: c.model,
		Messages: []visionMessage{
			{
				Role: "user",
				Content: []visionContentBlock{
					{Type: "text", Text: prompts.Get().Vision.DescribeImage},
					{Type: "image_url", ImageURL: &visionImageURL{URL: fmt.Sprintf("data:%s;base64,%s", mimeType, imageBase64)}},
				},
			},
		},
		Temperature: c.temperature,
		MaxTokens:   c.maxTokens,
		Provider:    c.provider,
		Stream:      false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("marshaling vision request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(jsonData))
	if err != nil {
		return "", 0, fmt.Errorf("creating vision request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("HTTP-Referer", "https://github.com/AutumnsGrove/Polaris")
	req.Header.Set("X-Title", "Polaris")

	httpClient := c.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("calling vision API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("reading vision response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("vision API returned %d: %s", resp.StatusCode, string(body))
	}

	var parsed visionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("parsing vision response: %w", err)
	}
	if len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content == "" {
		return "", 0, fmt.Errorf("vision model returned no description")
	}
	if parsed.Usage != nil {
		costUSD = parsed.Usage.Cost
	}
	return parsed.Choices[0].Message.Content, costUSD, nil
}
