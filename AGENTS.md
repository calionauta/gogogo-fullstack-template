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
| `make build` | Unified build — `go build` ONLY (no lint, no tests) |
| `make test` | Race tests (`-p 1` for DagNats engine stability); discouraged locally — the remote CI runs them |
| `make templ` | Generate Templ |
| `make datastar-lint` | Lint `.templ` via datastar-lint (`-only-errors` keeps intentional custom attrs) |
| `make css` / `make css-check` | Rebuild Tailwind/DaisyUI bundle; `css-check` compares against HEAD (used in `ci-local`) |
| `make ci-local` | **Single gate** (= CI): templ + datastar-lint + css-check + golangci-lint + race tests + build |
| `make signoff` | `ci-local` + `gh signoff` stamp (advisory on push-to-master) |
| `make setup` | Blocking pre-commit + pre-push hooks (pre-push adds `govulncheck`) |

> **`make check` was removed.** It was a subset of `make ci-local` (no
> build verification, smaller lint set). The single source of truth
> for "is the gate green" is `make ci-local`.

## Don'ts

- NO HTMX/Alpine — use Datastar. NO `fmt.Sprintf` for HTML — use Templ.
- NO raw CSS class when a DaisyUI component exists. NO `log` — use `log/slog`.
- NO `modernc.org/sqlite` (driver is ncruces/go-sqlite3). NO removing goqite when adding JetStream; they solve different problems.
- NO manual `id` on PocketBase records (PK Max=15, `^[a-z0-9]+$`).
- NO Datastar `PatchElements` whose top-level element lacks `id` + `WithSelector` (client throws `PatchElementsNoTargetsFound`). Use `internal/datastar.RenderAndPatch` paired with a selector.
- NO real LLM in tests — inject a stub (`internal/llm/fakeserver` only inside `internal/llm/`).
- **Prefer Datastar attributes** (`data-on:*`, signals, expressions, `__window`/`__document` modifiers) over vanilla JS for client-side logic. Inline JS only when unavoidable, kept adjacent to the markup (locality of behavior).

## SCOPE annotations (read before editing)

Every source file carries a `SCOPE` comment at the top showing its removal risk.

| Annotation | Meaning | You would… |
|------------|---------|------------|
| `SCOPE:core` 🔴 | Binary does not work without it. | Customize, never remove. |
| `SCOPE:pluggable` 🟡 | Binary works but loses capability. Has removal instructions. | Swap or delete with wiring call. |
| `SCOPE:feature` 🟢 | A demo/add-on. Has removal instructions and dependency notes. | Delete package + wiring call. |

**Agent rule:** When the user asks to trim the project, never delete a `SCOPE:core` file — always ask first. Delete `SCOPE:feature` and `SCOPE:pluggable` files freely, following their "Remove by" comments.

## Testing discipline (learned the hard way)

Lessons from v0.18.0 (offline-add + CI flake). Full post-mortem: `docs/decisions.md`.

- **Build tags ≠ test tags.** `Makefile`'s `$(TAGS)` (e.g. `no_default_driver`) is for the shipped binary. Tests that bootstrap PocketBase with `app.Bootstrap()` need a `DBConnect` when that tag is set; our tests rely on the default modernc driver, so `go test` recipes must NOT inherit `$(TAGS)`. Result of forgetting this: `Bootstrap` panics with `DBConnect config option must be set when the no_default_driver tag is used!` — easy to misread as a flake.
- **Avoid package-level mutable globals.** `var NS/NC/JS *Foo` set by `StartX()` and torn down by `Stop()` leak across `-p 1` packages when `Stop` doesn't nil them. Either nil on entry/exit or, better, return a struct. See `internal/nats/embedded.go` for the belt-and-suspenders nil-out.
- **Pre-commit MUST rebuild `.templ` AND CSS.** Editing a `.templ` without `make templ && make css` leaves `web/resources/static/app.min.css` stale. `css-check` passes by inertia when nobody rebuilt, masking the staleness until a real diff appears.
- **Three-tier feedback loop (replaces the single-tier "make check" rule).** See `## Feedback loop` below for the full breakdown. Short version: `make build` during dev, `make ci-local` pre-commit, **never run `make test` locally** — the remote CI is the test runner.
- **Confirm live deployment by byte-diffing an embedded asset.** `diff <(curl https://<host>/static/<asset>) <(repo <asset>)` is the cheapest proof the running binary matches the latest commit. Use for any "is the fix actually live?" question.
- **`git stash drop` is destructive** — it removes the ref without applying. Use `git stash pop` (apply + remove) or, before any stash drop, snapshot working changes to a `wip-*` branch.
- **Heredoc commit/tag messages**: prefer `git commit -F - <<'EOF' ... EOF` (quoted EOF = literal body) or `git tag -F /tmp/msg`. Avoid `git commit -m "$(cat <<'EOF' ... EOF)"` — bash quoting through the outer `"` + `$()` can fail parse on apostrophes/backticks in the body.

## Feedback loop (4 tiers, cheapest first)

Cascade up the tiers as confidence grows. Each tier catches a
different failure class; lower tiers are ~10x cheaper, so promote
only when (a) about to push/merge, or (b) the change touches an
area the next tier checks.

### T1 — format + build (~10s)

```bash
gofumpt -l -d <changed-files>   # format drift (<1s)
go build ./...                  # compile errors (~5-10s)
```

Catches formatting drift and compilation errors. Run every few
minutes during active editing. Replaces the old habit of running
`make build` repeatedly — `make build` only runs `go build`, not
formatting or lint, so it misses drift that T1 catches.

### T2 — lint + datastar + format + sizes + deadcode (~15-20s)

```bash
go vet ./...                                              # stdlib vet
golangci-lint run <changed-glob>                          # 27 linters, scoped
make templ && make datastar-lint                          # only when .templ changed
make fmt                                                  # gofumpt + goimports, full repo
make check-sizes && make deadcode                         # binary size + deadcode scan
```

Catches shadow, mnd, nolintlint, revive, staticcheck — same
config as CI but scoped to the packages you touched. ~15-20s for
scoped lint. Datastar-lint only applies to `.templ` changes;
sizes + deadcode + full-repo fmt catch the slow-moving drift.

**T2 must be green before T3.** Lint and format errors fail the
build downstream; running tests on a known-linted codebase saves
re-runs.

### T3 — scoped tests (~5-30s)

```bash
go test -race -count=1 -short <changed-glob>
```

Package-level only. `-race` catches data races. `-short` skips
long-running tests when present. Use after T2 is green on a
specific fix. The `-p 1` from `make test` is unnecessary at this
tier because DagNats FD starvation only matters when running ACROSS
packages.

### T4 — full pre-push gate (`make ci-local`, ~60-180s)

```bash
make ci-local
# templ + datastar-lint + css-check + golangci-lint +
# go test -race -p 1 ./... -count=1 + go build
```

The single source of truth for "is the gate green". Run right
before `git add` + `git push`. If this is green, push — the
remote CI runs the same gate plus deploy. Same checks as the
remote CI's lint job.

The remote CI is also the source of truth for **test execution**
in general — re-running the full test suite locally wastes time
that the parallel CI run is doing for you. Only re-run locally if
remote CI is broken or you're reproducing a CI-specific failure.

### Make check removed

`make check` was removed from the Makefile — it was a redundant
gate (subset of `make ci-local` without build verification). Use
`make ci-local` instead.

### Make target audit (what still earns its place)

| Target             | Status   | Why                                                                                  |
|--------------------|----------|-------------------------------------------------------------------------------------|
| `make build`       | keep     | `go build` of the unified binary. The fast "is it compileable?" check.               |
| `make templ`       | keep     | Regenerates `_templ.go` from `.templ` source.                                       |
| `make css`         | keep     | Rebuilds `app.min.css` from `src/css/input.css`.                                     |
| `make css-check`   | keep     | Diff vs HEAD; fails ci-local if stale.                                               |
| `make datastar-lint` | keep   | Catches Datastar anti-patterns in `.templ`.                                           |
| `make fmt`         | keep     | Full-repo gofumpt + goimports (CI gate).                                             |
| `make lint`        | keep     | Full-repo vet + golangci-lint (used by `make check`-equivalent flows; slow).         |
| `make test`        | keep     | Race tests; the remote CI uses this directly.                                         |
| `make check-sizes` | keep     | Binary size budget check.                                                              |
| `make deadcode`    | keep     | Deadcode scan.                                                                        |
| `make ci-local`    | **canonical gate** | The pre-push gate. Replaces `make check`.                              |
| `make check`       | **removed** | Redundant subset of `ci-local`. Promote `ci-local`.                               |
| `make signoff`     | keep     | `ci-local` + `gh signoff` advisory stamp.                                            |
| `make setup`       | keep     | Git hooks install.                                                                     |
| `make desktop`     | keep     | Wails v3 desktop shell.                                                              |
| `make dev`         | keep     | Air live reload.                                                                       |

### Anti-patterns (do not do this)

- **`make check`** — removed; use `make ci-local`.
- **`make test` between every commit** — same wall clock as
  `ci-local` minus the other checks. Use `ci-local` for the
  full pass; use T3 scoped `go test -race` for fast package
  feedback.
- **Skipping T1** — format drift piles up silently.
- **`golangci-lint run ./...`** (whole repo) for small changes —
  always scope to the changed packages (full-repo is ~10x
  slower).
- **Running the full test suite locally** when remote CI is
  healthy — same wall clock, but local fix-ups don't propagate.
- **Touching a class that isn't real CSS** (e.g. writing `p-1`
  in test data, JSON examples, or markdown prose) — Tailwind's
  content scanner reads `.templ`, `.go`, and via the
  `@import "tailwindcss" source(...)` directive, anything that
  matches the source globs. A `class="...p-1..."` accidentally
  emitted as a Tailwind utility creates spurious CSS in
  `app.min.css` and fails `css-check`. Run `make css` during T2
  for any change that might introduce class-shaped tokens.

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
  store/                 🟡 PLUGGABLE  EntityStore interface (PB + CRDT strategies)
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
| **EntityStore** (pluggable) | `features/store/pbstore/` | `features/store/pbstore/*_test.go` (future). Drop `todoH.SetStore(pbstore.New(app, "todos"))` from `router.Init`; the handler's lazy fallback (`h.st()` in `todo_repo.go`) will rebuild a PBStore on first use. Remove `features/store/pbstore/` to use a different strategy (e.g. the future CRDTStore). |
| **Idempotency** (pluggable) | `db/idempotency_hook.go` + `db/idempotency_seed.go` | `db/idempotency_hook_test.go` ✅. Remove both files, drop `RegisterIdempotencyHook(app)` and `enableTodosIdempotency(col)` from `db/seed.go`, and remove the hidden `name="idem_key"` input from `createForm`. |

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
