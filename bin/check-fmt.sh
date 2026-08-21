#!/bin/bash
set -e
out=$(gofumpt -l . 2>/dev/null)
if [ -n "$out" ]; then
  echo "❌ gofumpt issues:"
  echo "$out"
  echo "   Run: gofumpt -w ."
  exit 2
fi
out=$(goimports -l -local github.com/calionauta/gogogo-fullstack-template $(find . -name '*.go' ! -name '*_templ.go') 2>/dev/null)
if [ -n "$out" ]; then
  echo "❌ goimports issues:"
  echo "$out"
  echo "   Run: goimports -w -local github.com/calionauta/gogogo-fullstack-template ."
  exit 2
fi
echo "✅ formatting clean"
