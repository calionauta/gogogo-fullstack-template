#!/bin/bash
# go mod tidy check — race-safe: never mutates the working tree.
# `go mod tidy -diff` (Go 1.23+) reports drift and exits non-zero
# without writing, so it is safe under lefthook parallel jobs.
set -e
if ! out=$(go mod tidy -diff 2>&1); then
  echo "❌ go.mod/go.sum not tidy:"
  echo "${out:-<diff>}"
  echo "   Run: go mod tidy && git add go.mod go.sum"
  exit 2
fi
echo "✅ mod tidy clean"
