#!/bin/bash
if [ ! -f go.mod ]; then
  echo "→ not a Go project, skipping govulncheck"
  exit 0
fi
echo "→ govulncheck (pre-push)..."
if ! which govulncheck >/dev/null 2>&1; then
  echo "  → Installing..."
  go install golang.org/x/vuln/cmd/govulncheck@latest
fi
PKGS=$(bash scripts/web-packages.sh)
govulncheck $PKGS || { echo "❌ Vulnerability scan failed"; exit 1; }
echo "  ✓"
