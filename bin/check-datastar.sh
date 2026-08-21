#!/bin/bash
# datastar-lint — only if bin exists
if [ -x ./bin/datastar-lint ]; then
  ./bin/datastar-lint -r -e "templ,html,htm" ./features/ ./web/ ./internal/web/
else
  echo "⚠  datastar-lint not found; skipping"
fi
