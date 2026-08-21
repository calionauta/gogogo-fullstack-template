#!/bin/bash
# CSS staleness check — ported from .githooks/pre-commit.old.
# If a staged templ/go change affects generated classes, rebuilds the
# Tailwind v4 + DaisyUI v5 bundle and blocks if the result is unstaged.
set -e

grep -q "tailwindcss" package.json 2>/dev/null || exit 0

if [ ! -d node_modules ]; then
  echo "⚠ node_modules not present; run \`make css-install\` first — skipping css-check"
  exit 0
fi

echo "→ Rebuilding CSS (Tailwind v4 + DaisyUI v5)..."
npm run build --silent

if ! git diff --quiet --exit-code web/resources/static/app.min.css 2>/dev/null; then
  echo "❌ CSS is out of date. Run \`make css\` and stage web/resources/static/app.min.css"
  exit 2
fi
echo "✅ CSS up to date"
