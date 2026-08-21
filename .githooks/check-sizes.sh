#!/bin/bash
set -euo pipefail

MAX_FILE_LINES=500
FAIL=0

echo "→ Checking file sizes..."
for f in $(find . -name '*.go' -not -path '*/vendor/*' -not -path '*/node_modules/*' -not -path './tmp/*' -not -name '*_templ.go'); do
    lines=$(wc -l < "$f")
    if [ "$lines" -gt "$MAX_FILE_LINES" ]; then
        echo "  ❌ $f: $lines lines (max $MAX_FILE_LINES)"
        FAIL=1
    fi
done

if [ "$FAIL" -eq 0 ]; then
    echo "  ✅ All files within size limits"
else
    echo "  (deadcode scan moved to pre-push — see bin/check-deadcode.sh)"
fi

exit $FAIL
