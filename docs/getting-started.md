# Getting Started with gogogo-fullstack-template

## Prerequisites

- Go 1.26+
- Air (optional, for live reload): `go install github.com/air-verse/air@latest`
- Templ: `go install github.com/a-h/templ/cmd/templ@latest`

## Quick Start

```bash
# Clone the template
git clone https://github.com/calionauta/gogogo-fullstack-template.git my-project
cd my-project

# Generate templ components and run
make dev
```

## What's Included

| Feature | Status |
|---------|--------|
| PocketBase (database, auth, REST, realtime) | ✅ Default |
| goqite task queue + SSE Hub | ✅ Default |
| Datastar reactive UI + Templ | ✅ Default |
| DaisyUI + TailwindCSS | ✅ Default |
| age secrets management | ✅ Default |
| GoAI LLM SDK | ✅ Default |
| DagNats durable workflows | 🔲 Opt-in (`make build-dagnats`) |
| NATS JetStream (multi-user real-time) | 🔲 Opt-in (`make build-jetstream`) |

## Project Structure

```
cmd/web/main.go           # Entry point
config/                   # Configuration (dev/prod)
db/                       # PocketBase setup + repositories
internal/
  secrets/                # age-decrypt loader
  queue/                  # goqite + SSE Hub + workers
  nats/                   # NATS JetStream (build-tag gated)
  dagnats/                # DagNats durable workflow client (build-tag gated)
  llm/                    # GoAI client
  datastar/               # Datastar render helpers
features/app/             # Application feature modules
web/resources/            # Static assets (JS, CSS)
router/                   # Route registration
references/               # Reference documentation
```

## Commands

```bash
make templ            # Generate Templ components
make build            # Build the binary
make build-jetstream  # Build with JetStream support
make build-dagnats    # Build with DagNats durable workflow support
make build-all        # Build with JetStream + DagNats
make dev              # Live reload with Air
make test             # Run tests
make test-dagnats     # Run tests with DagNats tag
make lint             # Run linters
```

## Adding JetStream

```bash
# Build with JetStream support
make build-jetstream

# Or run in dev mode
go run -tags jetstream ./cmd/web/
NATS_ENABLED=true ./gogogo-fullstack-template
```

## Adding DagNats Workflows

DagNats is a DAG-based durable workflow engine built on NATS JetStream.
Workflows are **declarative JSON** (not Go), so renaming Go handlers never
breaks an in-flight run. The engine runs in the same binary on its own
port (`DAGNATS_HTTP_ADDR`, default `127.0.0.1:8090`) so its API/console
never collides with the app on `:8080`. It boots its own embedded NATS —
the `jetstream` and `dagnats` build tags are mutually exclusive.

```bash
# Build with DagNats support
make build-dagnats

# Or run in dev mode
go run -tags dagnats ./cmd/web/
DAGNATS_ENABLED=true ./gogogo-fullstack-template
```

The onboarding demo workflow lives in
`internal/dagnats/workflow.go` and is registered idempotently on startup.
Worker handlers (which write example todos to PocketBase) are registered
in `cmd/web/dagnats.go` via the `server.EmbeddedWorker` shim.

## Secrets Setup

```bash
bin/init-secrets
# Add to ~/.bashrc:
export AGE_SECRET_KEY=$(cat ~/.secrets/key.txt)
```
