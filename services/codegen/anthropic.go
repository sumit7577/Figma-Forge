package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const anthropicURL = "https://api.anthropic.com/v1/messages"

// AnthropicProvider implements the Provider interface for Anthropic's Claude API.
type AnthropicProvider struct {
	apiKey string
	model  string
	client *http.Client
}

// NewAnthropicProvider creates a new Anthropic provider instance.
func NewAnthropicProvider(apiKey, model string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
	}
}

// Generate calls the Anthropic Claude API and returns generated code.
func (ap *AnthropicProvider) Generate(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      ap.model,
		"max_tokens": 8192,
		"system":     "You are an expert UI engineer. Output only raw code, never markdown fences or explanations.",
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})

	req, err := http.NewRequestWithContext(ctx, "POST", anthropicURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", ap.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := ap.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	var ar struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &ar); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if ar.Error != nil {
		return "", fmt.Errorf("anthropic: %s", ar.Error.Message)
	}
	if len(ar.Content) == 0 {
		return "", fmt.Errorf("empty response")
	}

	return stripFences(ar.Content[0].Text), nil
}
