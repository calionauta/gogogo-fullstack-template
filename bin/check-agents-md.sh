#!/bin/bash
# AGENTS.md staleness — language-agnostic, gated on go.mod
if [ ! -f go.mod ]; then exit 0; fi
if ! command -v sem >/dev/null 2>&1; then exit 0; fi
[ -f AGENTS.md ] || exit 0
git diff --cached --quiet && exit 0
echo "→ AGENTS.md staleness check..."
SEM_OUTPUT=$(sem diff --staged --format json 2>/dev/null || echo '{"summary":{"total":0}}')
TOTAL=$(echo "$SEM_OUTPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('summary',{}).get('total',0))" 2>/dev/null || echo 0)
TOTAL="${TOTAL:-0}"
if [ "$TOTAL" -gt 0 ]; then
  echo "⚠️  AGENTS.md staleness: $TOTAL entities changed — review"
  echo "$SEM_OUTPUT" | python3 -c "import sys,json;d=json.load(sys.stdin);c=d.get('changes',[]);[print('  %s  %s (%s) in %s'%(x.get('changeType','?'),x.get('entityName','?'),x.get('entityType','?'),x.get('filePath','?'))) for x in c[:15]]" 2>/dev/null || true
  VALIDATOR_DIR="${HOME}/.agents/skills/cali-agents-md-validator"
  if [ -f "${VALIDATOR_DIR}/references/validate-agents-md.sh" ] && git diff --cached --name-only | grep -q '^AGENTS\.md$'; then
    bash "${VALIDATOR_DIR}/references/validate-agents-md.sh" ./AGENTS.md 2>&1 || true
  fi
fi
