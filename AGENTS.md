# gogogo-fullstack-template

> Full-stack Go web app template — back-end + front-end + DB + auth + LLM + deploy in one binary.

## Project Overview

Go template: Datastar + Templ + PocketBase + goqite + DagNats + NATS JetStream.
Module: `github.com/calionauta/gogogo-fullstack-template`

**Naming:** repo, module, binary, deploy dir (`/home/deploy/<APP_NAME>/`), container, and tunnel hostname all share the project name. Replace `gogogo-fullstack-template` everywhere when cloning.

## Stack (exact versions)

Go 1.26 | Templ v0.3.1020 | Datastar v1.2.2 | PocketBase v0.39.5 (ncruces/go-sqlite3) | TailwindCSS v4.1.13 + DaisyUI v5.6.15 | goqite v0.4.0 | retry-go v4 | DagNats v0.0.5 (opt-in) | NATS JetStream (opt-in) | age v1.3.1 | uuid v1.6.0

Skills: `cali-coding-go-standards` (code quality), `cali-code-navigation` (cymbal-first search). Install via `npx skills add .../cali-coding-go-standards`.

## Commands

| Command | Description |
|---------|-------------|
| `make dev` | Air live reload (gofumpt + vet + golangci-lint info) |
| `make build` / `build-jetstream` / `build-dagnats` / `build-all` | Build with optional tags |
| `make test` / `test-jetstream` / `test-dagnats` | Race tests |
| `make templ` | Generate Templ |
| `make datastar-lint` | Lint `.templ` via datastar-lint (`-only-errors` keeps intentional custom attrs) |
| `make check` | **Gate**: fmt + datastar-lint + golangci-lint + vet + sizes + deadcode + race tests |
| `make ci-local` / `make signoff` | Local CI matrix + gh-signoff stamp (see Local CI) |
| `make setup` | Blocking pre-commit + pre-push (pre-push adds `govulncheck`) |

## Don'ts

- NO HTMX/Alpine — use Datastar. NO `fmt.Sprintf` for HTML — use Templ.
- NO raw CSS class when a DaisyUI component exists. NO `log` — use `log/slog`.
- NO `modernc.org/sqlite` (driver is ncruces/go-sqlite3). NO removing goqite when adding JetStream.
- NO manual `id` on PocketBase records (PK Max=15, `^[a-z0-9]+$`).
- NO Datastar `PatchElements` whose top-level element lacks `id` + `WithSelector` (client throws `PatchElementsNoTargetsFound`). Use `internal/datastar.RenderAndPatch` paired with a selector.
- NO real LLM in tests — inject a stub (`internal/llm/fakeserver` only inside `internal/llm/`).

## Architecture (concise)

```
cmd/web/        Entry point (PB + goqite + SSE Hub); builds: jetstream | dagnats (opt-in)
config/         Env config
db/             PocketBase + collection seeds
internal/       {secrets,queue,nats,dagnats,llm,datastar,collab}
features/app/   AppContext (cross-cutting deps bundle)
features/todo/  Todo MVC (Datastar + DaisyUI + PB + SSE Hub)
features/whiteboard/  Loro CRDT + Rough.js canvas + SSEHub broadcast (offline-first outbox)
web/resources/  Embedded static assets
```

**Three complementary async layers:** `goqite` (jobs+SSE) · `dagnats` (durable workflows, opt-in) · `JetStream` (multi-instance realtime, opt-in). See README "Feature matrix" for the Lean→Full mix.

**Routing (read before touching `router.Init`):** PocketBase `RouterGroup` compiles to stdlib `http.ServeMux` (Go 1.22+ subtree matching — `GET /` swallows unregistered subpaths). Register all routes DIRECTLY on `se.Router` inside the OnServe hook (nested `OnServe().BindFunc` never fires). App cookie is `gogogo_auth` (NOT `pb_auth`). Serve static assets via EXACT `/static/<file>` routes (PB catch-all shadows wildcards). Full routing war-stories: `docs/decisions.md`.

## Build tags (opt-in, file-level stubs)

| Tag | Gets | Wired in |
|-----|------|----------|
| _(default)_ | goqite + InMemoryBroadcaster + Todo + auth + LLM + whiteboard | `router/router.go` |
| `-tags jetstream` | + embedded NATS + durable `TODOS` stream (`NATS_ENABLED=false` opts out) | `cmd/web/nats.go` |
| `-tags dagnats` | + DagNats workflows on `:8090` (`DAGNATS_ENABLED=false` opts out); owns NATS `:4222` | `cmd/web/dagnats.go` |
| `-tags "jetstream dagnats"` | both, one shared NATS (recommended prod combo) | — |

New opt-in feature shape: `internal/feature/<name>.go` + `<name>_noop.go` + `cfg.<Name>.Enabled`.

## Realtime transport decision

Task/whiteboard broadcast uses **`SSEHub` + `InMemoryBroadcaster`** (web path) — embedded, user-scoped, `BroadcastExcept` gives correct exclude-origin. JetStream is kept for **DagNats** + optional **desktop-edge whiteboard sync** (Leaf Node) + multi-instance todos behind a LB. Do NOT stand up JetStream just to broadcast todo mutations. Whiteboard clients are **offline-first**: Loro CRDT merges late/replayed ops on reconnect (outbox in `whiteboard.js`). Regression: `TestSSEBroadcast_*`, `TestWhiteboard_*`.

## Local CI (gh-signoff)

CI runs the 4-tag matrix on push to `master` then deploys. Run the **same gate locally** to avoid broken pushes:

```bash
gh extension install basecamp/gh-signoff
make ci-local      # templ + golangci-lint + datastar-lint + css-check + tests (all 4 tags) + builds
make signoff       # ci-local + gh signoff -f
```

Uses golangci-lint (not standalone gofumpt) as the formatter gate — gofumpt can be a newer release than golangci-lint bundles, causing false positives. Signoff is **advisory** (push-to-master flow, not PR merge) — do NOT `gh signoff install`.

## Deploy

Push-to-`master` triggers `.github/workflows/deploy.yml` (Tailscale OIDC + Docker to single server). Server layout/deploy-user/secret tables: see `/skill:cali-ops-deploy-github-tailscale`. Two gotchas: (1) grant container write via `setfacl`/`chmod`, NEVER `chown` (non-root deploy user); (2) never `scp` into the server's repo clone — `git pull --ff-only` aborts. Scratch image healthcheck: `CMD ["/app","health"]` (no `wget`/`curl`/`CMD-SHELL`).

## DaisyUI

ALL HTML UI uses DaisyUI components (read https://daisyui.com/llms.txt). Load `/static/app.min.css` (built by `npm run build`, regenerated in Dockerfile). NEVER `daisyui.min.css` (v4 relic, breaks v5 markup).

## Testing

Temp-dir PocketBase + Bootstrap + real SQLite; `httptest.NewServer` over a real router; assert against DB. LLM fakes via `internal/llm/fakeserver` (transport) or injected stubs (business logic).
