# syntax=docker/dockerfile:1.7
#
# Multi-stage build (maximo lean):
#   1. builder  — golang:1.26-alpine + nodejs/npm. Runs the Tailwind v4
#                  + DaisyUI v5 CSS build, generates Templ, compiles
#                  the Go binary (CGO_ENABLED=0 + static + stripped).
#   2. runtime  — scratch. The runtime image contains ONLY:
#                    - /app         (the 30 MB static binary)
#                    - /etc/ssl/certs/ca-certificates.crt (for HTTPS to
#                      LLM providers, fetched in the builder)
#                    - /tmp         (writable; Go runtime expects it)
#                  No shell, no libc, no /etc/passwd, no package manager.
#                  Total image size: ~30 MB (binary + ca-bundle).
#
# We do NOT use gcr.io/distroless/static-debian12 because it adds a
# ~2 MB base that scratch eliminates. Scratch is the leanest viable
# base for a static Go binary; the only thing it lacks is /etc/passwd
# and a writable /tmp, both of which we explicitly handle below
# (USER 65532 and mkdir /tmp). ca-certificates is the only library
# asset we need to ship because Go's net/http uses it for TLS.
#
# Build pipeline (Makefile 'css' target) + this Dockerfile are the
# only two compile steps. No JS runtime in the final image.

# ────────────────────────────
# Stage 1: builder
# ────────────────────────────
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates nodejs npm
WORKDIR /src

# Install Go module deps first (cached independently of source).
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    go mod download

# Install CSS build deps (Tailwind v4 CLI + DaisyUI v5).
COPY package.json package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm,sharing=locked \
    npm install --no-audit --no-fund

# Now copy the rest of the source and run all the build steps.
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    --mount=type=cache,target=/root/.npm,sharing=locked \
    go tool templ generate && \
    npm run build --silent && \
    CGO_ENABLED=0 go build -tags "jetstream dagnats" -trimpath -ldflags="-s -w" -o /out/app ./cmd/web/

# Stash the ca-certificates bundle so the runtime stage can copy it
# without needing a shell. The alpine image installs the bundle at
# /etc/ssl/certs/ca-certificates.crt; we copy it directly.
RUN mkdir -p /out/etc/ssl/certs && \
    cp /etc/ssl/certs/ca-certificates.crt /out/etc/ssl/certs/

# ────────────────────────────
# Stage 2: runtime (scratch)
# ────────────────────────────
FROM scratch

# The static Go binary. CGO_ENABLED=0 + -ldflags="-s -w" means no
# libc dependency; the binary runs on the empty scratch image.
COPY --from=builder /out/app /app

# ca-certificates for HTTPS to LLM providers (OpenAI, Groq, etc.).
# The bundle is ~250 KB and ships with most Linux distributions;
# we copy it from the builder stage to keep the runtime image
# self-contained.
COPY --from=builder /out/etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Go's runtime expects /tmp to be writable (e.g. for memory-mapped
# file backings in some stdlib paths). scratch has no /tmp, so
# create it. Because scratch has no shell, we use the Docker
# "in-place directory creation" trick: COPY a tmp directory from
# the builder, then this layer sets the perms. Alternative: use a
# multi-stage copy with --chown. We do not use --chown because the
# base has no user to own with.
COPY --from=builder /tmp /tmp

# PocketBase (used by the binary for its admin UI) and the Go runtime
# expect a writable HOME for some operations. Set it to /tmp.
ENV HOME=/tmp \
    XDG_CONFIG_HOME=/tmp \
    XDG_CACHE_HOME=/tmp

# PocketBase reads the encryption key from an env var; we document
# it here so operators know to set ENCRYPTION_ENV before starting
# the container. See bin/init-secrets for the age-encrypted workflow.
# ENCRYPTION_ENV=

# PocketBase default-data dir: write to /tmp/data so the container
# is stateless. For a real deployment, mount a volume here.
ENV PB_DATA_DIR=/tmp/data

# Run as non-root: scratch has no /etc/passwd, so we use the
# numeric UID directly. 65532 is the standard "nonroot" UID used
# by the distroless nonroot images.
USER 65532:65532

EXPOSE 8080

# By default, a Go program is PID 1 and receives signals directly
# (no shell wrapping). HEALTHCHECK would require a shell which
# scratch doesn't have; orchestrators (k8s livenessProbe, ECS health
# check) should probe GET /health directly. See the project README
# for the /health endpoint.
ENTRYPOINT ["/app"]
