#!/bin/bash
# Dead-code scan — runs at pre-push, not pre-commit: it compiles every
# package and takes ~80s. internal/llm and internal/datastar are
# intentionally-provided library APIs (wire them when a feature needs
# them); deadcode would flag their not-yet-wired exports as noise.
set -uo pipefail

if ! which deadcode >/dev/null 2>&1; then
    echo "⚡ deadcode not installed (go install golang.org/x/tools/cmd/deadcode@latest) — skipping"
    exit 0
fi

echo "→ Running deadcode scan..."
output=$(deadcode -test ./cmd/... ./features/... ./router/... ./internal/nats/... ./internal/queue/... 2>&1) || true
if [ -n "$output" ]; then
    echo "  ⚠️  Dead code found:"
    echo "$output" | head -20
fi
echo "  ✓ done (advisory only)"
exit 0
