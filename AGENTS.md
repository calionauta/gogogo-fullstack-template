# gogogo-fullstack-template

> Full-stack Go web app template — back-end + front-end + DB + auth + LLM + deploy in one binary.

## Project Overview

Go template: Datastar + Templ + PocketBase + goqite + DagNats + NATS JetStream.
Module: `github.com/calionauta/gogogo-fullstack-template`

**Naming:** repo, module, binary, deploy dir (`/home/deploy/<APP_NAME>/`), container, and tunnel hostname all share the project name. Replace `gogogo-fullstack-template` everywhere when cloning.

**Unified build.** `go build ./cmd/web` or `make build` compiles **everything** — no build tags. Every feature (queue, workflows, realtime, whiteboard, onboarding) is always included. Opt out at runtime via env vars like `NATS_ENABLED=false`, `DAGNATS_ENABLED=false`.

## Stack (exact versions)

Go 1.26 | Templ v0.3.1020 | Datastar v1.2.2 | PocketBase v0.39.5 (ncruces/go-sqlite3) | TailwindCSS v4.1.13 + DaisyUI v5.6.15 | goqite v0.4.0 | retry-go v4 | DagNats v0.0.5 | NATS JetStream | age v1.3.1 | uuid v1.6.0

Skills: `cali-coding-go-standards` (code quality), `cali-code-navigation` (cymbal-first search). Install via `npx skills add .../cali-coding-go-standards`.

## Commands

| Command | Description |
|---------|-------------|
| `make dev` | Air live reload (gofumpt + vet + golangci-lint info) |
| `make build` | Unified build (everything included) |
| `make test` | Race tests (`-p 1` for DagNats engine stability) |
| `make templ` | Generate Templ |
| `make datastar-lint` | Lint `.templ` via datastar-lint (`-only-errors` keeps intentional custom attrs) |
| `make check` | **Gate**: fmt + datastar-lint + golangci-lint + vet + sizes + deadcode + race tests |
| `make ci-local` / `make signoff` | Local CI gate + gh-signoff stamp (see Local CI) |
| `make setup` | Blocking pre-commit + pre-push (pre-push adds `govulncheck`) |

## Don'ts

- NO HTMX/Alpine — use Datastar. NO `fmt.Sprintf` for HTML — use Templ.
- NO raw CSS class when a DaisyUI component exists. NO `log` — use `log/slog`.
- NO `modernc.org/sqlite` (driver is ncruces/go-sqlite3). NO removing goqite when adding JetStream; they solve different problems.
- NO manual `id` on PocketBase records (PK Max=15, `^[a-z0-9]+$`).
- NO Datastar `PatchElements` whose top-level element lacks `id` + `WithSelector` (client throws `PatchElementsNoTargetsFound`). Use `internal/datastar.RenderAndPatch` paired with a selector.
- NO real LLM in tests — inject a stub (`internal/llm/fakeserver` only inside `internal/llm/`).

## SCOPE annotations (read before editing)

Every source file carries a `SCOPE` comment at the top showing its removal risk.

| Annotation | Meaning | You would… |
|------------|---------|------------|
| `SCOPE:core` 🔴 | Binary does not work without it. | Customize, never remove. |
| `SCOPE:pluggable` 🟡 | Binary works but loses capability. Has removal instructions. | Swap or delete with wiring call. |
| `SCOPE:feature` 🟢 | A demo/add-on. Has removal instructions and dependency notes. | Delete package + wiring call. |

**Agent rule:** When the user asks to trim the project, never delete a `SCOPE:core` file — always ask first. Delete `SCOPE:feature` and `SCOPE:pluggable` files freely, following their "Remove by" comments.

## Architecture (concise)

```
cmd/web/                 🔴 CORE  Entry point (PB + goqite + SSE Hub + DagNats + NATS)
config/                  🔴 CORE  Env config
db/                      🔴 CORE  PocketBase + collection seeds
internal/
  secrets/               🔴 CORE  age-decrypted env loader
  queue/                 🔴 CORE  goqite + SSE Hub + workers + retry + handler registry
  datastar/              🟡 PLUGGABLE  Datastar rendering helpers
  nats/                  🟡 PLUGGABLE  NATS JetStream + embedded server
  dagnats/               🟡 PLUGGABLE  DagNats durable workflow client
  llm/                   🟡 PLUGGABLE  GoAI LLM client
  collab/                🟡 PLUGGABLE  Loro CRDT + DocStore + sync workers
  components/             🟡 PLUGGABLE  Shared UI helpers (Toast + OfflineBanner)
features/
  auth/                  🔴/🟢 CORE (middleware) / FEATURE (UI)
  app/                   🔴 CORE  AppContext (cross-cutting deps bundle)
  todo/                  🟢 FEATURE  Todo MVC example (keep as reference, remove when done)
  whiteboard/            🟢 FEATURE  Collaborative canvas (remove if not needed)
web/resources/           🔴 CORE  Embedded static assets
router/                  🔴 CORE  Route wiring
```

**Three complementary async layers:** `goqite` (jobs+SSE) · `dagnats` (durable workflows) · `JetStream` (cross-instance realtime). They coexist in the same binary; all three are always compiled.

**Routing (read before touching `router.Init`):** PocketBase `RouterGroup` compiles to stdlib `http.ServeMux` (Go 1.22+ subtree matching — `GET /` swallows unregistered subpaths). Register all routes DIRECTLY on `se.Router` inside the OnServe hook (nested `OnServe().BindFunc` never fires). App cookie is `gogogo_auth` (NOT `pb_auth`) — the two cookies are intentional: PocketBase keeps admin (`_superusers`) and regular users as SEPARATE auth namespaces, so sharing `pb_auth` clobbers the admin session in the same browser (known PB gotcha, issues #5050/#1780). Run the admin UI on a separate origin/port (`:8090/_/`) so even `pb_auth` never collides. Serve static assets via EXACT `/static/<file>` routes (PB catch-all shadows wildcards). Full routing war-stories: `docs/decisions.md`.

## Realtime transport decision

**Todo records** (create/toggle/delete) flow through **PocketBase realtime** — the realtime SSE lives at `/api/realtime`, is authenticated by the app's `LoadAuthFromCookie` middleware (reads `gogogo_auth`), and the collection's `ListRule`/`ViewRule` (`@request.auth.id != '' && owner = @request.auth.id`) make delivery **per-user scoped**. Each subscribed client re-fetches `/api/todos/fragment` and morphs `#todo-list` on a `todos` event. This is the mechanism for DB actions — do NOT add a parallel SSE-hub re-render for todo mutations.

**The SSE hub (`/api/todos/stream`)** is reserved for **ephemeral signals only**: the live clients count, LLM suggest feedback, and DagNats workflow progress. It also carries the **originating client's** synchronous patch on its own mutation POST. It does NOT broadcast record mutations to other clients.

**Whiteboard** uses **SSEHub + NATS** — shapes are dual-broadcast: in-process via the SSE Hub (same-process tabs) and over NATS via the SyncWorker (cross-instance convergence). Presence cursors use the same SSE Hub with exclude-origin fan-out. Clients are **offline-first**: Loro CRDT merges late/replayed ops on reconnect (outbox in `whiteboard.js`).

**NATS JetStream** is used by DagNats (workflow engine state), the whiteboard SyncWorker (cross-instance doc sync), and the optional desktop-edge Leaf Node. The todo broadcaster uses an in-memory fan-out by default (can be wired to JetStream for multi-instance deployments).

## Local CI (gh-signoff)

CI runs on push to `master` then deploys. Run the **same gate locally** to avoid broken pushes:

```bash
gh extension install basecamp/gh-signoff
make ci-local      # templ + golangci-lint + datastar-lint + css-check + race tests + build
make signoff       # ci-local + gh signoff -f
```

Uses golangci-lint (not standalone gofumpt) as the formatter gate — gofumpt can be a newer release than golangci-lint bundles, causing false positives. Signoff is **advisory** (push-to-master flow, not PR merge) — do NOT `gh signoff install`.

### Pre-push workflow: gate locally, then ask before pushing

Remote CI + deploy is slow and runs on every push to `master`. To save time, **always run the local gate first** and only push when it is green:

1. `make ci-local` (full local gate).
2. If it passes, **ask the user** (via `ask_user_question`) whether they want to push now or keep working.

Rationale: a developer often wants only a local green signal before continuing with more changes; pushing prematurely kicks off a slow remote run they may not need yet.

## Deploy

Push-to-`master` triggers `.github/workflows/deploy.yml` (Tailscale OIDC + Docker to single server). Server layout/deploy-user/secret tables: see `/skill:cali-ops-deploy-github-tailscale`. Two gotchas: (1) grant container write via `setfacl`/`chmod`, NEVER `chown` (non-root deploy user); (2) never `scp` into the server's repo clone — `git pull --ff-only` aborts. Scratch image healthcheck: `CMD [\"/app\",\"health\"]` (no `wget`/`curl`/`CMD-SHELL`).

## DaisyUI

ALL HTML UI uses DaisyUI components (read https://daisyui.com/llms.txt). Load `/static/app.min.css` (built by `npm run build`, regenerated in Dockerfile). NEVER `daisyui.min.css` (v4 relic, breaks v5 markup).

## Key config constants (single source of truth)

| Constant | File | Default | Purpose |
|----------|------|---------|---------|
| `DefaultReplayBufferSize` | `config/config.go` | 64 | Per-client replay ring-buffer length |
| `DefaultClientQueueSize` | `config/config.go` | 64 | Per-client SSE channel buffer |
| `DefaultSSEHeartbeatInterval` | `config/config.go` | 15s | SSE heartbeat to detect disconnection |
| `OfflineSync.Enabled` | `config/config.go` | `true` (opt-out: `OFFLINE_SYNC_ENABLED=false`) | Toggle hybrid offline sync |

**One place for all configs:** `config/config.go`. Runtime constants that are package-specific (e.g. `DefaultBaseURL` in `internal/llm/goai.go`) stay cohesionated — but all env vars are documented in config.go's comment block.

## Removing features & tests by SCOPE

When you remove a feature or pluggable component, tests come along naturally:

| If you remove… | Delete these packages | These test files go with them automatically |
|----------------|----------------------|----------------------------------------------|
| **Todo** (feature) | `features/todo/` | `features/todo/*_test.go` ✅ |
| **Whiteboard** (feature) | `features/whiteboard/` (including its `static/` subdir), `internal/collab/` | `features/whiteboard/*_test.go`, `internal/collab/*_test.go` ✅ |
| **DagNats** (pluggable) | `internal/dagnats/`, `router/onboarding_dagnats.go` | `internal/dagnats/*_test.go`, `features/todo/onboarding_e2e_test.go` ⚠️ check cross-package deps |
| **NATS** (pluggable) | `internal/nats/` | `internal/nats/*_test.go`, `internal/collab/*_test.go` ⚠️ collab may depend on NATS |
| **OfflineSync** (opt-out) | `config/config.go` (+ `sw.js`) | `internal/nats/crudproxy_test.go` ✅ (covers create/toggle/delete/clear_completed e2e with JetStream). Remove `sw.js` + SW registration from templ files + delete crudproxy.go |
| **LLM** (pluggable) | `internal/llm/` | `internal/llm/*_test.go`, `features/todo/suggest_test.go` ⚠️ |

**Rule of thumb:** `go test ./...` after deleting a package. If a compilation error mentions the deleted package in a test file, delete that test file too. Cross-package tests (like `features/todo/onboarding_e2e_test.go` depending on `internal/dagnats`) will fail to compile — that's your checklist.

## Desktop builds

```bash
# One-time: install Wails v3 CLI
# go install github.com/wailsapp/wails/v3/cmd/wails@latest

# Build for current platform
./scripts/desktop-build.sh

# Build Android APK (requires SDK + NDK + JDK 21)
./scripts/desktop-build.sh android

# Build macOS .app bundle
./scripts/desktop-build.sh package
```

The desktop binary shares 100% of the backend. With `NATS_LEAFNODE_URL` set, it becomes a NATS Leaf Node syncing JetStream with the server (offline edits replay on reconnect). See `scripts/desktop-build.sh` for full docs.

## Testing

Temp-dir PocketBase + Bootstrap + real SQLite; `httptest.NewServer` over a real router; assert against DB. LLM fakes via `internal/llm/fakeserver` (transport) or injected stubs (business logic). `go test -race -p 1 ./...` (serialized packages for DagNats engine stability).
