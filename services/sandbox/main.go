// sandbox subscribes to sandbox.build.requested,
// scaffolds an isolated Docker container, builds and serves the UI,
// then publishes sandbox.ready with the URL.
package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/forge-ai/forge/shared/events"
	"github.com/forge-ai/forge/shared/mq"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	_ = godotenv.Load()

	amqpURL := envOr("AMQP_URL", "amqp://forge:forge@rabbitmq:5672/")
	network := envOr("DOCKER_NETWORK", "forge-net")
	timeout := 120 // seconds

	broker, err := mq.New(amqpURL)
	if err != nil {
		log.Fatal().Err(err).Msg("mq connect")
	}
	defer broker.Close()

	deliveries, err := broker.Subscribe("svc.sandbox", events.SandboxBuildRequested)
	if err != nil {
		log.Fatal().Err(err).Msg("subscribe")
	}

	log.Info().Str("network", network).Msg("sandbox service started")

	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigs; cancel() }()

	sb := &sandboxRunner{network: network, timeout: time.Duration(timeout) * time.Second}

	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-deliveries:
			if !ok {
				return
			}
			if err := handle(ctx, d, broker, sb); err != nil {
				log.Error().Err(err).Msg("sandbox error")
				d.Nack(false, false)
			} else {
				d.Ack(false)
			}
		}
	}
}

func handle(ctx context.Context, d amqp.Delivery, broker *mq.Broker, sb *sandboxRunner) error {
	p, err := events.Unwrap[events.SandboxBuildRequestedPayload](d.Body)
	if err != nil {
		return err
	}

	log.Info().
		Str("job", p.JobID).
		Str("platform", p.Platform).
		Int("iter", p.Iteration).
		Msg("building sandbox")

	buildCtx, cancel := context.WithTimeout(ctx, sb.timeout)
	defer cancel()

	containerID, port, err := sb.spin(buildCtx, p.Code, p.Filename, p.Platform)
	if err != nil {
		b, _ := events.Wrap(events.SandboxFailed, events.SandboxFailedPayload{
			JobID:       p.JobID,
			ScreenIndex: p.ScreenIndex,
			Platform:    p.Platform,
			Error:       err.Error(),
		})
		return broker.Publish(ctx, events.SandboxFailed, b)
	}

	host := envOr("SANDBOX_HOST", "localhost")
	url := fmt.Sprintf("http://%s:%d", host, port)

	b, _ := events.Wrap(events.SandboxReady, events.SandboxReadyPayload{
		JobID:       p.JobID,
		ScreenIndex: p.ScreenIndex,
		Platform:    p.Platform,
		Iteration:   p.Iteration,
		ContainerID: containerID,
		Port:        port,
		URL:         url,
		Threshold:   p.Threshold,
		Screen:      p.Screen,
	})
	return broker.Publish(ctx, events.SandboxReady, b)
}

// ── Sandbox runner ────────────────────────────────────────────────────────────

type sandboxRunner struct {
	network string
	timeout time.Duration
}

func (s *sandboxRunner) spin(ctx context.Context, code, filename, platform string) (string, int, error) {
	dir, err := os.MkdirTemp("", "forge-sb-*")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(dir)

	port := 30000 + rand.Intn(10000)
	tag := fmt.Sprintf("forge-sandbox:%d", port)

	if err := scaffold(dir, code, filename, platform, port); err != nil {
		return "", 0, fmt.Errorf("scaffold: %w", err)
	}

	// Build
	build := exec.CommandContext(ctx, "docker", "build", "-t", tag, dir)
	if out, err := build.CombinedOutput(); err != nil {
		return "", 0, fmt.Errorf("docker build: %s", strings.TrimSpace(string(out)))
	}

	// Run
	containerName := fmt.Sprintf("forge-%d", port)
	run := exec.CommandContext(ctx,
		"docker", "run", "--rm", "--detach",
		"--network", s.network,
		"--name", containerName,
		"-p", fmt.Sprintf("%d:%d", port, port),
		"-e", fmt.Sprintf("PORT=%d", port),
		"--memory", "512m",
		"--cpus", "1",
		tag,
	)
	out, err := run.Output()
	if err != nil {
		return "", 0, fmt.Errorf("docker run: %w", err)
	}

	containerID := strings.TrimSpace(string(out))
	log.Debug().Str("container", containerID[:12]).Int("port", port).Msg("sandbox up")
	return containerID, port, nil
}

func (s *sandboxRunner) kill(containerID string) {
	if containerID == "" {
		return
	}
	exec.Command("docker", "rm", "-f", containerID).Run()
}

// ── Scaffolding ───────────────────────────────────────────────────────────────

func scaffold(dir, code, filename, platform string, port int) error {
	switch platform {
	case events.PlatformKMP:
		return scaffoldKMP(dir, code, filename, port)
	default:
		return scaffoldReact(dir, code, filename, port)
	}
}

func scaffoldReact(dir, code, filename string, port int) error {
	// Wrap the generated component into an app
	appCode := fmt.Sprintf(`import React from 'react'
import ReactDOM from 'react-dom/client'
import Component from './%s'
import './index.css'
ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><Component /></React.StrictMode>)`,
		strings.TrimSuffix(filename, ".tsx"))

	files := map[string]string{
		"package.json": fmt.Sprintf(`{
  "name": "forge-sandbox",
  "private": true,
  "scripts": { "dev": "vite --port %d --host 0.0.0.0" },
  "dependencies": { "react": "^18.3.0", "react-dom": "^18.3.0" },
  "devDependencies": {
    "vite": "^5.2.0",
    "@vitejs/plugin-react": "^4.2.1",
    "tailwindcss": "^3.4.3",
    "typescript": "^5.4.5",
    "@types/react": "^18.3.0",
    "@types/react-dom": "^18.3.0"
  }
}`, port),
		"vite.config.ts":     `import { defineConfig } from 'vite'; import react from '@vitejs/plugin-react'; export default defineConfig({ plugins: [react()] })`,
		"tsconfig.json":      `{"compilerOptions":{"target":"ES2020","useDefineForClassFields":true,"lib":["ES2020","DOM","DOM.Iterable"],"module":"ESNext","moduleResolution":"bundler","jsx":"react-jsx","strict":true}}`,
		"index.html":         fmt.Sprintf(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Forge</title></head><body><div id="root"></div><script type="module" src="/src/main.tsx"></script></body></html>`),
		"src/main.tsx":       appCode,
		"src/index.css":      `@tailwind base; @tailwind components; @tailwind utilities;`,
		"tailwind.config.js": `module.exports={content:['./index.html','./src/**/*.{ts,tsx}'],theme:{extend:{}},plugins:[]}`,
		"postcss.config.js":  `module.exports={plugins:{tailwindcss:{},autoprefixer:{}}}`,
		fmt.Sprintf("src/%s", filename): code,
		"Dockerfile": fmt.Sprintf(`FROM node:20-alpine
WORKDIR /app
COPY package.json .
RUN npm install
COPY . .
EXPOSE %d
CMD ["npm","run","dev"]`, port),
	}

	for path, content := range files {
		full := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(full), 0755)
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func scaffoldKMP(dir, code, filename string, port int) error {
	// For KMP we use a Compose Web preview (JS target) in a Docker container.
	// This allows browser screenshot capture without a physical Android device.
	files := map[string]string{
		"build.gradle.kts": `
plugins {
    kotlin("multiplatform") version "1.9.23"
    id("org.jetbrains.compose") version "1.6.2"
}
kotlin {
    js(IR) { browser {} }
    sourceSets {
        val commonMain by getting { dependencies {
            implementation(compose.runtime)
            implementation(compose.foundation)
            implementation(compose.material3)
            implementation(compose.ui)
        }}
    }
}`,
		"settings.gradle.kts":       `rootProject.name = "forge-preview"`,
		fmt.Sprintf("src/commonMain/kotlin/%s", filename): code,
		"Dockerfile": fmt.Sprintf(`FROM gradle:8-jdk17
WORKDIR /app
COPY . .
RUN gradle jsBrowserDevelopmentRun --no-daemon -x test &
EXPOSE %d
CMD ["gradle", "jsBrowserDevelopmentRun", "--no-daemon", "--continuous"]`, port),
	}

	for path, content := range files {
		full := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(full), 0755)
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
