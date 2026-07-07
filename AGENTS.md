# cali-go-stack

## Project Overview

Go web application template with Datastar + Templ + PocketBase + goqite + Turbine + NATS JetStream.
Module: `github.com/calionauta/cali-go-stack`

## Stack

Go 1.26 | Templ v0.3.1020 | Datastar v1.2.2 (datastar-go) | PocketBase v0.39.5 (embedded, ncruces/go-sqlite3) | DaisyUI v5.6.13 + TailwindCSS | goqite v0.4.0 | retry-go v4 | Turbine v0.3.0 (opt-in, build tag) | NATS JetStream (opt-in, build tag) | age v1.3.1 | uuid v1.6.0

## Skills

Install via `npx skills add`:

```bash
npx skills add https://github.com/calionauta/agent-sync-public/tree/main/skills/cali-coding-go-standards --yes
npx skills add https://github.com/calionauta/agent-sync-public/tree/main/skills/cali-code-navigation --yes
```

| Skill | When |
|-------|------|
| `cali-coding-go-standards` | Code quality: KISS, DRY, file size, functions, `slog`, error handling, tests, lints |
| `cali-code-navigation` | Code search & impact analysis: cymbal-first, fff fallback, sem diff for refactors. Especially useful on this template because the codebase mixes Go (server), Templ (HTML), and Datastar expressions — cymbal is Go-aware, fff handles the rest |

For stack-specific patterns (Datastar, Templ, DaisyUI, goqite, Turbine, JetStream), see the project's `references/` directory and `docs/` directory.

## DaisyUI Components

Before building any UI, read the full DaisyUI component catalog:
- https://daisyui.com/llms.txt

Use DaisyUI components for ALL HTML UI. Prefer: `table`, `input`, `btn`, `badge`, `card`, `modal`, `toast`, `loading`, `collapse`, `join`, `kbd`, `select`, `toggle`, `range`, `progress`, `chat`, `diff`, `stat`, `timeline`.

## Commands

| Command | Description |
|---------|-------------|
| `make dev` | Live reload with Air |
| `make build` | Build binary |
| `make build-jetstream` | Build with NATS JetStream |
| `make build-turbine` | Build with Turbine workflows |
| `make build-all` | Build with JetStream + Turbine |
| `make test` | Run tests with race detector |
| `make test-turbine` | Run tests with Turbine tag |
| `make templ` | Generate Templ components |
| `golangci-lint run ./...` | Run linter |
| `deadcode -test ./...` | Find dead code |

## Don'ts

- Do NOT use HTMX/Alpine.js — use Datastar
- Do NOT use fmt.Sprintf for HTML — use Templ
- Do NOT remove goqite when adding JetStream (they are complementary)
- Do NOT use modernc.org/sqlite — driver is ncruces/go-sqlite3
- Do NOT use raw CSS class names when a DaisyUI component exists — check `daisyui.com/llms.txt`
- Do NOT use `log` package — use `slog` (`log/slog`, stdlib since Go 1.21)
- Do NOT set the `id` field manually on a PocketBase record — the collection's PK has Max=15 and Pattern=^[a-z0-9]+$, let PocketBase auto-generate

## Architecture

```
cmd/web/main.go          → Entry point (PB embedded + goqite + SSE Hub)
  -tags jetstream        → Also starts embedded NATS server + JetStream
  -tags turbine          → Also starts Turbine durable workflow runtime
config/                  → Env-based config (dev/prod build tags)
db/                      → PocketBase setup + ensureTodosCollection seed
internal/
  secrets/               → age-decrypt loader
  queue/                 → goqite + SSE Hub + workers + retry (schema in goqite_schema.sql)
  nats/                  → NATS JetStream (build-tag gated)
  workflow/              → Turbine durable workflows (build-tag gated)
  llm/                   → GoAI client
  datastar/              → Datastar render helpers
features/
  app/                   → Application core
  todo/                  → Example: Todo MVC (Datastar + DaisyUI + PocketBase + SSE Hub)
web/resources/           → Static assets (embedded)
router/                  → Route registration on PocketBase

Three async layers (complementary, not alternatives):
  goqite    → background jobs (fire-and-forget, SSE streaming)
  turbine   → durable workflows (multi-step, crash recovery)
  JetStream → multi-user real-time (KV, streams, presence)
```

## Testing

Integration tests live next to the code they exercise. Pattern (from cali-coding-go-standards):
- temp-dir PocketBase + Bootstrap + real SQLite (no mocks)
- httptest.NewServer over a real router
- assert against the database, not the rendered HTML

See `features/todo/integration_test.go` for the canonical example.