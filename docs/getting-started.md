# Getting Started with cali-go-stack

## Prerequisites

- Go 1.26+
- Air (optional, for live reload): `go install github.com/air-verse/air@latest`
- Templ: `go install github.com/a-h/templ/cmd/templ@latest`

## Quick Start

```bash
# Clone the template
git clone https://github.com/calionauta/cali-go-stack.git my-project
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
| Turbine durable workflows | 🔲 Opt-in (`make build-turbine`) |
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
make build-turbine    # Build with Turbine workflow support
make build-all        # Build with both JetStream and Turbine
make dev              # Live reload with Air
make test             # Run tests
make test-turbine     # Run tests with Turbine tag
make lint             # Run linters
```

## Adding JetStream

```bash
# Build with JetStream support
make build-jetstream

# Or run in dev mode
go run -tags jetstream ./cmd/web/
NATS_ENABLED=true ./cali-go-stack
```

## Adding Turbine Workflows

```bash
# Build with Turbine support
make build-turbine

# Or run in dev mode
go run -tags turbine ./cmd/web/
WORKFLOW_ENABLED=true ./cali-go-stack
```

## Secrets Setup

```bash
bin/init-secrets
# Add to ~/.bashrc:
export AGE_SECRET_KEY=$(cat ~/.secrets/key.txt)
```
