#!/bin/bash
# ── Forge one-time setup ──────────────────────────────────────
# Run once after cloning to install all Go and Node deps

set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "⚡ Forge setup"
echo ""

# ── Go deps ───────────────────────────────────────────────────
echo "▶ Installing Go dependencies..."
(cd shared           && go mod tidy)
(cd services/gateway      && go mod tidy)
(cd services/orchestrator && go mod tidy)
(cd services/figma-parser && go mod tidy)
(cd services/codegen      && go mod tidy)
(cd services/sandbox      && go mod tidy)
(cd services/differ       && go mod tidy)
(cd services/notifier     && go mod tidy)
echo "✓ Go deps ready"

# ── Node deps ─────────────────────────────────────────────────
echo "▶ Installing Node dependencies..."
(cd web && npm install)
echo "✓ Node deps ready"

# ── Playwright ────────────────────────────────────────────────
echo "▶ Installing Playwright Chromium..."
npx playwright install chromium
echo "✓ Playwright ready"

# ── .env ──────────────────────────────────────────────────────
if [ ! -f .env ]; then
  cp .env.example .env
  echo ""
  echo "⚠️  Created .env from .env.example"
  echo "   Edit .env and add your keys before running:"
  echo "   FIGMA_TOKEN, ANTHROPIC_API_KEY, SUPABASE_URL, SUPABASE_SERVICE_KEY"
else
  echo "✓ .env exists"
fi

echo ""
echo "✅ Setup complete. Run:  ./scripts/dev.sh"
