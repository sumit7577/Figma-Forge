package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const openrouterURL = "https://openrouter.ai/api/v1/chat/completions"

// OpenRouterProvider implements the Provider interface for OpenRouter's API.
// OpenRouter provides a unified interface to multiple LLM providers including Anthropic.
type OpenRouterProvider struct {
	apiKey string
	model  string
	client *http.Client
}

// NewOpenRouterProvider creates a new OpenRouter provider instance.
func NewOpenRouterProvider(apiKey, model string) *OpenRouterProvider {
	return &OpenRouterProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
	}
}

// Generate calls the OpenRouter API and returns generated code.
// OpenRouter uses OpenAI-compatible API format.
func (or *OpenRouterProvider) Generate(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": or.model,
		"messages": []map[string]string{
			{"role": "system", "content": "You are an expert UI engineer. Output only raw code, never markdown fences or explanations."},
			{"role": "user", "content": prompt},
		},
		"max_tokens": 8192,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", openrouterURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+or.apiKey)

	resp, err := or.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openrouter request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if response.Error != nil {
		return "", fmt.Errorf("openrouter: %s", response.Error.Message)
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("empty response")
	}

	return stripFences(response.Choices[0].Message.Content), nil
}
