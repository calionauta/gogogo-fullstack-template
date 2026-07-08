# cali-go-stack

## Project Overview

Go web application template with Datastar + Templ + PocketBase + goqite + Turbine + NATS JetStream.
Module: `github.com/calionauta/cali-go-stack`

## Stack

Go 1.26 | Templ v0.3.1020 | Datastar v1.2.2 (datastar-go) | PocketBase v0.39.5 (embedded, ncruces/go-sqlite3) | DaisyUI v5.6.13 + TailwindCSS | goqite v0.4.0 | retry-go v4 | Turbine v0.3.0 (opt-in, build tag) | NATS JetStream (opt-in, build tag) | age v1.3.1 | uuid v1.6.0

## Skills

```bash
npx skills add https://github.com/calionauta/agent-sync-public/tree/main/skills/cali-coding-go-standards --yes
npx skills add https://github.com/calionauta/agent-sync-public/tree/main/skills/cali-code-navigation --yes
```

| Skill | When |
|-------|------|
| `cali-coding-go-standards` | Code quality: KISS, DRY, file size, functions, `slog`, error handling, tests, lints |
| `cali-code-navigation` | Code search & impact: cymbal-first, fff fallback, sem diff for refactors |

For stack-specific patterns, see `docs/` and `references/` (Skill assets).

## DaisyUI Components

For ALL HTML UI, use DaisyUI components. Read https://daisyui.com/llms.txt before building any UI.

## Commands

| Command | Description |
|---------|-------------|
| `make dev` | Live reload with Air (post-build: gofumpt + go vet + golangci-lint info) |
| `make build` / `build-jetstream` / `build-turbine` / `build-all` | Build binary with optional tags |
| `make test` / `test-jetstream` / `test-turbine` | Run tests with race detector |
| `make templ` | Generate Templ components |
| `make fmt` | Check formatting (gofumpt + goimports) |
| `make datastar-lint` | Lint `.templ` / `.html` via [datastar-lint](https://github.com/calionauta/datastar-lint) |
| `make check` | **Full gate**: fmt + datastar-lint + golangci-lint (no `--fast`) + vet + sizes + deadcode + race tests |
| `make setup` | Install blocking pre-commit + pre-push hooks |
| `bin/datastar-lint` | Wrapper that installs and runs datastar-lint (also wired into `make check`) |

## Don'ts

- Do NOT use HTMX/Alpine.js — use Datastar
- Do NOT use `fmt.Sprintf` for HTML — use Templ
- Do NOT remove goqite when adding JetStream (they are complementary)
- Do NOT use modernc.org/sqlite — driver is ncruces/go-sqlite3
- Do NOT use raw CSS class names when a DaisyUI component exists
- Do NOT use `log` package — use `log/slog` (stdlib since Go 1.21)
- Do NOT manually set the `id` field on a PocketBase record (PK Max=15, Pattern=^[a-z0-9]+$)
- Do NOT call the real LLM in tests — inject an `LLMClient` interface and use a fake

## Architecture

```
cmd/web/                  Entry point (PB + goqite + SSE Hub). Builds: jetstream | turbine (opt-in).
config/                   Env-based config
db/                       PocketBase setup + ensureTodosCollection seed
internal/{secrets,queue,nats,workflow,llm,datastar}/
features/app/             Application core
features/todo/            Example: Todo MVC (Datastar + DaisyUI + PocketBase + SSE Hub)
web/resources/            Static assets (embedded)
router/                   Route registration on PocketBase
docs/                     Decision logs and guides
```

Three async layers (complementary, not alternatives):
`goqite` → background jobs + SSE; `turbine` → durable workflows; `JetStream` → multi-user real-time.

## Quality Gate

Run `make check` after each significant edit. The pre-commit hook (`make setup`) is blocking on the same gate. Pre-push adds `govulncheck`. See `docs/decisions.md` for the why.

## Realtime (todo sharing across clients)

Cross-client todo mutations go through `nats.TodoBroadcaster` (wired in `router.Init`):
- default build → `InMemoryBroadcaster` fans out via `SSEHub.Broadcast` (single-instance)
- `-tags jetstream` → `JetStreamBroadcaster` publishes to a durable `TODOS` stream (multi-instance)

## LLM Integration (GoAI)

`internal/llm` wraps GoAI behind an injectable interface. Tests must NOT call the real provider — inject a fake (or VCR replay). Streaming modeled as an iterator so backpressure/cancel are testable.

## Testing

Pattern: temp-dir PocketBase + Bootstrap + real SQLite (no mocks), `httptest.NewServer` over a real router, assert against the database.
