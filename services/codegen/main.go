// codegen subscribes to codegen.requested,
// calls the Anthropic Claude API, and publishes codegen.complete.
// Handles BOTH web (React/Next.js) and mobile (KMP/Compose) platforms.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/forge-ai/forge/shared/events"
	"github.com/forge-ai/forge/shared/mq"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const anthropicURL = "https://api.anthropic.com/v1/messages"

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	_ = godotenv.Load()

	amqpURL := envOr("AMQP_URL", "amqp://forge:forge@rabbitmq:5672/")
	apiKey := mustEnv("ANTHROPIC_API_KEY")
	model := envOr("LLM_MODEL", "claude-opus-4-5")
	workers := 3 // concurrent codegen workers

	broker, err := mq.New(amqpURL)
	if err != nil {
		log.Fatal().Err(err).Msg("mq connect")
	}
	defer broker.Close()

	deliveries, err := broker.Subscribe("svc.codegen", events.CodegenRequested)
	if err != nil {
		log.Fatal().Err(err).Msg("subscribe")
	}

	log.Info().Str("model", model).Int("workers", workers).Msg("codegen service started")

	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigs; cancel() }()

	gen := &generator{apiKey: apiKey, model: model, client: &http.Client{}}

	// Fan-out: multiple workers read from same queue
	for i := 0; i < workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case d, ok := <-deliveries:
					if !ok {
						return
					}
					if err := handle(ctx, d, broker, gen); err != nil {
						log.Error().Err(err).Msg("codegen error")
						d.Nack(false, true)
					} else {
						d.Ack(false)
					}
				}
			}
		}()
	}
	<-ctx.Done()
}

func handle(ctx context.Context, d amqp.Delivery, broker *mq.Broker, gen *generator) error {
	p, err := events.Unwrap[events.CodegenRequestedPayload](d.Body)
	if err != nil {
		return err
	}

	log.Info().
		Str("job", p.JobID).
		Str("platform", p.Platform).
		Str("screen", p.Screen.Name).
		Int("iter", p.Iteration).
		Msg("generating code")

	prompt := buildPrompt(*p)
	code, err := gen.generate(ctx, prompt)
	if err != nil {
		b, _ := events.Wrap(events.CodegenFailed, events.CodegenFailedPayload{
			JobID: p.JobID, ScreenIndex: p.ScreenIndex, Platform: p.Platform, Error: err.Error(),
		})
		return broker.Publish(ctx, events.CodegenFailed, b)
	}

	filename := filenameFor(p.Screen.Name, p.Platform)
	b, _ := events.Wrap(events.CodegenComplete, events.CodegenCompletePayload{
		JobID:       p.JobID,
		ScreenIndex: p.ScreenIndex,
		Platform:    p.Platform,
		Iteration:   p.Iteration,
		Code:        code,
		Filename:    filename,
		Threshold:   p.Threshold,
		Screen:      p.Screen,
	})
	return broker.Publish(ctx, events.CodegenComplete, b)
}

// ── Prompt builder ────────────────────────────────────────────────────────────

func buildPrompt(p events.CodegenRequestedPayload) string {
	tokensJSON, _ := json.MarshalIndent(p.Screen.Colors, "", "  ")
	typJSON, _ := json.MarshalIndent(p.Screen.Typography, "", "  ")
	treeJSON, _ := json.MarshalIndent(p.Screen.ComponentTree, "", "  ")

	var sb strings.Builder

	switch p.Platform {
	case events.PlatformKMP:
		sb.WriteString("You are an expert Kotlin Multiplatform / Jetpack Compose engineer.\n")
		sb.WriteString("Generate a production-ready @Composable function for this screen.\n\n")
		sb.WriteString("Rules:\n")
		sb.WriteString("1. Output ONLY raw Kotlin code — no markdown fences, no explanation\n")
		sb.WriteString("2. Use Compose Multiplatform (commonMain) — no Android-only APIs\n")
		sb.WriteString("3. Use Material3 components\n")
		sb.WriteString("4. Match exact colors from design tokens\n")
		sb.WriteString("5. Match exact spacing/padding values\n")
		sb.WriteString("6. Composable must be a top-level fun named after the screen\n")
		sb.WriteString("7. Include @Preview annotation\n")
	case events.PlatformNextJS:
		sb.WriteString("You are an expert Next.js 14 engineer using the App Router.\n")
		sb.WriteString("Generate a production-ready React Server Component (or 'use client' if needed).\n\n")
		sb.WriteString("Rules:\n")
		sb.WriteString("1. Output ONLY raw TypeScript/TSX code — no markdown, no explanation\n")
		sb.WriteString("2. Use Tailwind CSS for all styling\n")
		sb.WriteString("3. Default export the component\n")
		sb.WriteString("4. Use Next.js Image and Link where appropriate\n")
		sb.WriteString("5. Match exact colors from design tokens\n")
	default: // react
		sb.WriteString("You are an expert React 18 engineer.\n")
		sb.WriteString("Generate a production-ready functional component with TypeScript.\n\n")
		sb.WriteString("Rules:\n")
		sb.WriteString("1. Output ONLY raw TSX code — no markdown fences, no explanation\n")
		sb.WriteString("2. Use Tailwind CSS for all styling\n")
		sb.WriteString("3. Default export the component\n")
		sb.WriteString("4. Match exact colors from design tokens\n")
		sb.WriteString("5. Match exact font sizes, weights, and spacing\n")
	}

	sb.WriteString(fmt.Sprintf("\nSCREEN: %s (%gx%g)\n", p.Screen.Name, p.Screen.Width, p.Screen.Height))
	sb.WriteString(fmt.Sprintf("PLATFORM: %s\n", p.Platform))
	sb.WriteString(fmt.Sprintf("STYLING: %s\n\n", p.Styling))
	sb.WriteString(fmt.Sprintf("COLORS:\n%s\n\n", tokensJSON))
	sb.WriteString(fmt.Sprintf("TYPOGRAPHY:\n%s\n\n", typJSON))
	sb.WriteString(fmt.Sprintf("COMPONENT TREE:\n%s\n", treeJSON))

	if p.RepoContext != "" {
		sb.WriteString(fmt.Sprintf("\nCODE STYLE REFERENCE (follow this architecture):\n%s\n", p.RepoContext))
	}

	if p.PrevDiff != nil {
		sb.WriteString(fmt.Sprintf(`
PREVIOUS ATTEMPT FEEDBACK — similarity was %.1f%% (target: %d%%) — FIX THESE:
- Layout: %.1f%%   Typography: %.1f%%   Spacing: %.1f%%   Color: %.1f%%

SPECIFIC ISSUES:
`, p.PrevDiff.Score, p.Threshold,
			p.PrevDiff.Layout, p.PrevDiff.Typography,
			p.PrevDiff.Spacing, p.PrevDiff.Color))
		for _, r := range p.PrevDiff.Regions {
			sb.WriteString(fmt.Sprintf("• %s: got %q, need %q\n", r.Property, r.Actual, r.Expected))
		}
	}

	sb.WriteString("\nRespond with ONLY the complete component code. Nothing else.")
	return sb.String()
}

// ── Anthropic client ──────────────────────────────────────────────────────────

type generator struct {
	apiKey string
	model  string
	client *http.Client
}

func (g *generator) generate(ctx context.Context, prompt string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      g.model,
		"max_tokens": 8192,
		"system":     "You are an expert UI engineer. Output only raw code, never markdown fences or explanations.",
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})

	req, err := http.NewRequestWithContext(ctx, "POST", anthropicURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", g.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := g.client.Do(req)
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

func stripFences(code string) string {
	lines := strings.Split(strings.TrimSpace(code), "\n")
	if len(lines) > 0 && (strings.HasPrefix(lines[0], "```") || strings.HasPrefix(lines[0], "~~~")) {
		lines = lines[1:]
	}
	if len(lines) > 0 && (strings.HasPrefix(lines[len(lines)-1], "```") || strings.HasPrefix(lines[len(lines)-1], "~~~")) {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func filenameFor(screenName, platform string) string {
	safe := strings.ReplaceAll(strings.Title(strings.ToLower(screenName)), " ", "")
	switch platform {
	case events.PlatformKMP:
		return safe + "Screen.kt"
	default:
		return safe + ".tsx"
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatal().Str("key", k).Msg("required env var missing")
	}
	return v
}
