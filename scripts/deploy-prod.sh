#!/usr/bin/env bash
# deploy-prod.sh — runs ON the server, after the GitHub Action has
# scp'd the new binary + compose file into place. Idempotent: it can
# run any number of times and converges to the deployed state.
#
# Layout (managed by the deploy workflow):
#   /home/deploy/gogogo-template/
#     bin/gogogo-template        (chmod 755, replaced on every deploy)
#     compose/docker-compose.prod.yml   (replaced on every deploy)
#     env/.env                  (committed to repo, no secrets)
#     secrets/gogogo-template.env  (mode 600, regenerated every deploy
#                                   from GH Secrets; never committed)
#     data/pb_data/             (gitignored, persistent volume)
#
# We use /home/deploy/ (not /opt/) because the deploy user does not
# have passwordless sudo; /opt is root-owned. /home/deploy is writable
# by the deploy user and Docker still reads the compose file + binds
# the volume from there.
#
# This script is the second half of the deploy: the GH Action does
# the build + scp; this script restarts the container. We split it
# so the operator can also run it manually (e.g. for fast rollback
# to the previous binary which is kept as `gogogo-template.previous`).

set -euo pipefail

PROJECT="gogogo-template"
APP_DIR="/home/deploy/${PROJECT}"
BIN_DIR="${APP_DIR}/bin"
COMPOSE_DIR="${APP_DIR}/compose"
SECRETS_DIR="${APP_DIR}/secrets"
SECRETS_FILE="${SECRETS_DIR}/${PROJECT}.env"
DATA_DIR="${APP_DIR}/data"
COMPOSE_FILE="${COMPOSE_DIR}/docker-compose.prod.yml"

cd "${APP_DIR}"

# ── 1. Atomic binary swap ──
# We keep the previous binary as `gogogo-template.previous` so an
# operator can roll back with one `ln -sf` if the new binary
# crashes on startup.
if [ -f "${BIN_DIR}/${PROJECT}" ] && [ -f "${BIN_DIR}/${PROJECT}.new" ]; then
    mv "${BIN_DIR}/${PROJECT}" "${BIN_DIR}/${PROJECT}.previous"
    mv "${BIN_DIR}/${PROJECT}.new" "${BIN_DIR}/${PROJECT}"
    chmod 0755 "${BIN_DIR}/${PROJECT}"
    echo "→ Binary swapped (previous kept as ${PROJECT}.previous)"
elif [ -f "${BIN_DIR}/${PROJECT}.new" ]; then
    # First deploy — no previous to preserve.
    mv "${BIN_DIR}/${PROJECT}.new" "${BIN_DIR}/${PROJECT}"
    chmod 0755 "${BIN_DIR}/${PROJECT}"
    echo "→ Binary installed (first deploy)"
fi

# ── 2. Compose file in place ──
# The GH Action scp'd docker-compose.prod.yml directly into COMPOSE_DIR.
# Sanity-check it.
if [ ! -f "${COMPOSE_FILE}" ]; then
    echo "❌ ${COMPOSE_FILE} missing. Aborting." >&2
    exit 1
fi

# ── 3. Secrets file mode + ownership ──
# GH Action wrote a .env file with real secrets (GOAI_API_KEY etc).
# Lock it down to 600, owned by deploy user, before any container
# can read it.
if [ -f "${SECRETS_FILE}" ]; then
    chmod 0600 "${SECRETS_FILE}"
    echo "→ Secrets file mode set to 0600"
fi

# ── 4. Data dir ownership (PocketBase writes here) ──
mkdir -p "${DATA_DIR}/pb_data"
chmod 0750 "${DATA_DIR}"

# ── 5. Roll the container ──
# Build context is the repo checkout (cloned/updated by the GH
# Action into ${APP_DIR}/repo). The compose file lives at
# deploy/docker-compose.prod.yml relative to the repo root.
REPO_DIR="${APP_DIR}/repo"
cd "${REPO_DIR}"
echo "→ docker compose build + up -d (context: ${REPO_DIR})"
docker compose -f deploy/docker-compose.prod.yml build "${PROJECT}"
docker compose -f deploy/docker-compose.prod.yml up -d "${PROJECT}"

# ── 6. Wait for healthy + report ──
echo "→ Waiting for /health (max 30s)..."
for i in $(seq 1 30); do
    if curl -fsS --max-time 2 "http://127.0.0.1:8080/health" >/dev/null 2>&1; then
        echo "✓ Service is healthy after ${i}s"
        break
    fi
    sleep 1
done

echo "→ Service status:"
docker compose -f "${COMPOSE_FILE}" ps "${PROJECT}" || true
echo "→ Recent logs:"
docker compose -f "${COMPOSE_FILE}" logs --tail 20 "${PROJECT}" || true
