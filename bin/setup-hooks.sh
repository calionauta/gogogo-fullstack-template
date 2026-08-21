#!/bin/bash
# Activates git hooks for this repo (lefthook-based).
# Usage: make setup
# Idempotent — safe to run multiple times.
set -e

HOOKS_DIR=$(cd "$(dirname "$0")/.." && pwd)
HOOKS_TARGET=".githooks"

if [ ! -f "$HOOKS_DIR/.lefthook.yml" ]; then
    echo "❌ .lefthook.yml not found. Run from repo root." >&2
    exit 1
fi

git config core.hooksPath "$HOOKS_TARGET"
echo "✅ Git hooksPath set to $HOOKS_TARGET"

# Regenerate wrapper scripts from .lefthook.yml when lefthook is available.
# The wrappers are also committed to the repo as a fallback, so this is
# only strictly needed after editing .lefthook.yml.
if command -v lefthook >/dev/null 2>&1; then
    (cd "$HOOKS_DIR" && lefthook install)
    echo "✅ lefthook wrappers regenerated"
elif [ -x "$HOME/go/bin/lefthook" ]; then
    (cd "$HOOKS_DIR" && "$HOME/go/bin/lefthook" install)
    echo "✅ lefthook wrappers regenerated (~/go/bin)"
else
    echo "⚠ lefthook not found in PATH — using committed wrappers."
    echo "  Install: go install github.com/evilmartians/lefthook@latest"
fi

echo ""
echo "Active hooks:"
for h in "$HOOKS_DIR/$HOOKS_TARGET"/pre-* "$HOOKS_DIR/$HOOKS_TARGET"/post-*; do
    [ -f "$h" ] && echo "  → $(basename "$h")"
done
