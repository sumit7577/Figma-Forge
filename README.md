# ⚡ Forge v0.2 — Autonomous Figma → Web + Mobile Engine

Forge is a distributed, event-driven AI system that converts Figma designs into production-ready web and mobile applications using a continuous self-healing validation loop.

It does not just generate UI code.  
It builds, runs, screenshots, compares, refactors, and repeats — until the rendered output matches the original Figma design.

Built with Go microservices, RabbitMQ, Supabase, Docker sandboxes, and Playwright.

---

## 🌱 Origin

Forge was created to solve a fundamental problem in modern frontend development:

Most design-to-code tools generate UI once — but they do not verify if it actually renders correctly.

Real-world UI engineering requires:

- Pixel accuracy  
- Iterative refinement  
- Runtime validation  
- Automated testing  
- Scalable orchestration  

Forge introduces a closed-loop autonomous UI engineer:

Design → Generate → Build → Screenshot → Diff → Refactor → Validate → Deliver

Instead of static code generation, Forge continuously validates and improves the output until it meets a visual similarity threshold.

---

## 🏗 Architecture Overview

Forge v0.2 runs as **7 independent Go microservices** communicating exclusively through RabbitMQ.

There is **no direct service-to-service HTTP communication**.

All state is persisted in Supabase.

```
┌─────────────────────────────────────────────────────────────────────┐
│                     FORGE ARCHITECTURE                               │
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
