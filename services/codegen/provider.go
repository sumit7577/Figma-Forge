package main

import "context"

// Provider is an abstraction for different LLM API providers.
// Each implementation handles provider-specific HTTP details, authentication,
// request/response formatting, and error handling.
type Provider interface {
	// Generate calls the LLM API with the given prompt and returns generated code.
	Generate(ctx context.Context, prompt string) (string, error)
}
