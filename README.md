# ⚡ Forge v0.2 — Go Microservices  |  Figma → Web + Mobile

![GitHub stars](https://img.shields.io/github/stars/sumit7577/Figma-Forge?style=flat-square)
![GitHub forks](https://img.shields.io/github/forks/sumit7577/Figma-Forge?style=flat-square)
![Go Version](https://img.shields.io/badge/Go-1.22-blue?style=flat-square)
![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)
![Docker](https://img.shields.io/badge/Docker-Compose-blue?style=flat-square)

**Convert Figma designs to production-ready web and mobile code using AI. Automated end-to-end pipeline with self-healing loop.**

## ✨ What is Forge?

Forge is an **open-source AI-powered code generation system** that transforms Figma designs into fully functional web and mobile applications. It combines:

- 🎨 **Design Parsing** - Extract components, layouts, and styling from Figma
- 🤖 **AI Code Generation** - Claude API generates production-grade code (React, Next.js, Kotlin Multiplatform)
- 📦 **Automated Building** - Docker-based sandboxes compile and run generated code
- 🔍 **Pixel-Perfect Verification** - Playwright-based visual diff ensures design fidelity
- 🔄 **Self-Healing Loop** - Automatically iterates up to 10x to reach 95% similarity match
- 📊 **Real-time Monitoring** - Live dashboard shows each service's progress and errors

Perfect for:
- Rapid prototyping from design mockups
- Reducing design-to-code handoff friction
- Testing UI implementations against original designs
- Design system automation

## 🏗️ Architecture

```
│                                                                      │
│  React UI  ◀──WS──▶  gateway  ──REST──▶  ┐                         │
│                          │                │                          │
│                          ▼                │                          │
│                     RabbitMQ              │  Supabase                │
│                   (forge.events)          │  ├─ jobs                 │
│                          │                │  ├─ screens              │
│          ┌───────────────┼───────────┐    │  ├─ iterations           │
│          ▼               ▼           ▼    │  └─ events               │
│   orchestrator    figma-parser    notifier│                          │
│          │                               │                          │
│   ┌──────┼──────┐                        │                          │
│   ▼       ▼      ▼                       │                          │
│ codegen sandbox differ                   │                          │
│ ×3 rplcas              (Playwright)      │                          │
└─────────────────────────────────────────────────────────────────────┘
```

## 7 Go Microservices

| Service | Subscribes to | Publishes |
|---------|--------------|-----------|
| **gateway** | `log.#`, `screen.done`, `job.done` | `job.submitted` |
| **orchestrator** | everything | routes to all services |
| **figma-parser** | `figma.parse.requested` | `figma.parsed` |
| **codegen** | `codegen.requested` | `codegen.complete` |
| **sandbox** | `sandbox.build.requested` | `sandbox.ready` |
| **differ** | `diff.requested` | `diff.complete` |
| **notifier** | `notify.requested` | — (sends Telegram) |

## Self-Healing Loop (per screen × platform)

```
                   ┌─────────────────────────────────────┐
                   │                                     │
    figma.parsed ──▶ codegen.requested (iter N)          │
                        │                                │
                   codegen.complete                      │
                        │                                │
                   sandbox.build.requested               │
                        │                                │
                   sandbox.ready                         │
                        │                                │
                   diff.requested ──▶ Playwright snap    │
                        │                                │
                   diff.complete                         │
                        │                                │
               score ≥ 95%? ──YES──▶ screen.done         │
                        │                                │
                       NO ──────────────────────────────┘
                  (max 10 iter)
```

## Platforms

| Platform | Generator | Sandbox | Output |
|----------|-----------|---------|--------|
| `react` | TSX + Tailwind | Vite + Node 20 | `.tsx` |
| `nextjs` | RSC + Tailwind | Next.js dev server | `.tsx` |
| `kmp` | Compose Multiplatform | Compose Web (JS) | `.kt` |
| `flutter` | — planned — | — | — |

## Quick Start

```bash
git clone https://github.com/forge-ai/forge
cd forge-v2
cp .env.example .env
# Fill in FIGMA_TOKEN, ANTHROPIC_API_KEY, SUPABASE_URL, SUPABASE_SERVICE_KEY

# Apply DB schema
supabase db push
# or paste supabase/migrations/001_init.sql into Supabase SQL editor

# Start everything
docker compose up -d

# UI:         http://localhost:3000  (or :8080)
# RabbitMQ:   http://localhost:15672  (forge/forge)
# API:        http://localhost:8080/api/status
```

## Submit a Job

```bash
curl -X POST http://localhost:8080/api/jobs \
  -H 'Content-Type: application/json' \
  -d '{
    "figma_url":  "https://www.figma.com/file/XXXX/MyApp",
    "platforms":  ["react", "kmp"],
    "styling":    "tailwind",
    "threshold":  95
  }'
```

## Scale Codegen Workers

```bash
docker compose up -d --scale codegen=5
```
All 5 instances compete for messages on the `svc.codegen` queue.
RabbitMQ distributes work automatically.

## Architecture Notes

- **No direct service-to-service HTTP** — everything flows through RabbitMQ
- **Orchestrator is stateless** per restart — job state in Supabase
- **Codegen is horizontally scalable** — just `--scale codegen=N`
- **Differ uses Playwright** in its own container (Chromium bundled)
- **Sandbox mounts Docker socket** to spawn sibling containers

## Project Structure

```
forge-v2/
├── shared/
│   ├── events/events.go    ← Message contract (ALL payload types)
│   └── mq/broker.go        ← RabbitMQ client (used by all services)
├── services/
│   ├── gateway/main.go     ← REST + WebSocket API
│   ├── orchestrator/       ← Pipeline state machine
│   ├── figma-parser/main.go
│   ├── codegen/main.go     ← Claude API
│   ├── sandbox/main.go     ← Docker sandbox runner
│   ├── differ/main.go      ← Playwright + pixel diff
│   └── notifier/main.go    ← Telegram
├── web/src/
│   ├── App.tsx             ← React dashboard
│   └── app.css
├── supabase/migrations/
├── infra/docker/
├── docker-compose.yml
└── .env.example
```
