.PHONY: up down logs build dev migrate run status

# ── Docker ────────────────────────────────────────────────────
up:
	docker compose up -d
	@echo ""
	@echo "  ⚡ Forge is running"
	@echo "  UI:        http://localhost:8080"
	@echo "  RabbitMQ:  http://localhost:15672  (forge/forge)"
	@echo ""

down:
	docker compose down

build:
	docker compose build

logs:
	docker compose logs -f

logs-orch:
	docker compose logs -f orchestrator

scale-codegen:
	docker compose up -d --scale codegen=$(N)
	@echo "Codegen scaled to $(N) replicas"

# ── Database ──────────────────────────────────────────────────
migrate:
	supabase db push
	@echo "Migrations applied"

# ── Submit a job ──────────────────────────────────────────────
run:
	@[ -n "$(FIGMA)" ] || (echo "Usage: make run FIGMA=<url> [PLATFORMS=react,kmp]" && exit 1)
	curl -s -X POST http://localhost:8080/api/jobs \
		-H 'Content-Type: application/json' \
		-d "{\"figma_url\":\"$(FIGMA)\",\"platforms\":[$(shell echo '$(PLATFORMS)' | sed 's/,/","/g' | sed 's/^/"/;s/$/"/')],\"threshold\":95}" \
		| jq .

status:
	curl -s http://localhost:8080/api/status | jq .

# ── Dev (run services locally, not in Docker) ─────────────────
dev-gateway:
	cd services/gateway && go run ./main.go

dev-orch:
	cd services/orchestrator && go run ./main.go

dev-codegen:
	cd services/codegen && go run ./main.go

dev-differ:
	cd services/differ && go run ./main.go

# ── Local dev (no Docker) ─────────────────────────────────────
setup:
	./scripts/setup.sh

dev:
	./scripts/dev.sh

# Install RabbitMQ shortcuts
rabbit-mac:
	brew install rabbitmq && brew services start rabbitmq

rabbit-ubuntu:
	sudo apt-get install -y rabbitmq-server && sudo systemctl enable --now rabbitmq-server
