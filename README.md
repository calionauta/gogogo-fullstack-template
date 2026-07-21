# gogogo-fullstack-template

<p align="center">
  <img src="web/resources/static/logo.png" alt="gogogo-fullstack-template" width="512">
</p>

> **Built to be useful.** Every decision favors practical outcomes over abstract ideals. The stack intentionally optimizes for simplicity, consistency, and shipping software with minimal friction.

> **I still don't understand why GitHub keeps crediting Claude as a contributor to my repos.** My daily drivers are pi.dev with Minimax M3 and DeepSeek V4 Flash. Claude, if you're freelancing on my repositories while I'm asleep, at least start fixing the bugs too. If anyone knows how GitHub actually computes contributors, I'd genuinely love to know.

## Contents

- [Who this template is for](#who-this-template-is-for)
- [What's in the package](#whats-in-the-package)
- [Stack in layers, not silos](#stack-in-layers-not-silos)
- [Feature overview](#feature-overview)
- [Architecture taxonomy: Core / Plugin / Feature](#architecture-taxonomy-core--plugin--feature)
- [Code quality for LLM agents](#code-quality-for-llm-agents)
- [The example: Todo App with realtime](#the-example-todo-app-with-realtime)
- [Adding your own feature](#adding-your-own-feature)
- [UI skins (pluggable: DaisyUI / Basecoat / Morpheus)](#ui-skins-pluggable-daisyui--basecoat--morpheus)
- [Admin & Dashboard](#admin--dashboard)
- [Configuring the LLM (GoAI)](#configuring-the-llm-goai)
- [Getting started](#getting-started)
- [Local CI (gh-signoff)](#local-ci-gh-signoff)
- [Desktop & Mobile](#desktop--mobile-wails-v3--loro-crdt--nats-leaf-node)
- [Deploy to your own box](#deploy-to-your-own-box)
- [Structure (annotated by SCOPE)](#structure-annotated-by-scope)
- [Acknowledgements & inside jokes](#acknowledgements--inside-jokes)
- [License, feedback](#license-feedback)

---

Every web project we start begins with the same conversation: pick a database, auth, router, reactive frontend, task queue… and the project stalls at the decisions, installations, and configurations — not the code.

## Who this template is for

- **You who get tired of configuring the same stack over and over**
- **You who want everything in one binary, with no external dependencies, no Docker required.** One self-contained file. Environment-independent.
- **You who need offline-first resilience.** A Service Worker + Background Sync queue (web) and a NATS Leaf Node (desktop) let clients keep working without a connection and replay mutations on reconnect, with idempotency so replays never duplicate. See [Hybrid offline sync](#feature-overview).
- **You who prefer a single source of truth on the backend, one language for the whole stack, and reactive frontend without heavy frontend frameworks** — server-rendered HTML via SSE, lightweight and fast, no bloated SPAs, no JS build step.
- **You who want a language that is predictable for both humans and LLMs.** Go's syntax is minimal and consistent. Same formatting everywhere (`gofumpt`). No surprises. Static typing catches whole classes of bugs at compile time. Native concurrency (goroutines + channels) that is easy to reason about — no async/await chains, no callback pyramids. This makes the codebase equally readable by you, your team, and AI coding agents.
- **You who care about supply chain security.** Go has no mass npm-style dependency trees. Every module is verified by content hash (`go.sum`). Built-in vulnerability auditing (`govulncheck`) scans your dependency graph for known CVEs. No transitive dependency hell.
- **You who want an LLM client wired in without pulling in a whole orchestration framework** — `internal/llm` wraps GoAI (any OpenAI-compatible provider) behind an injectable interface, callable from handlers. It calls a *remote* provider API; it is **not** a local-model runtime.

## What's in the package

Everything you need to build a modern web app, in a single binary:

| Layer | Choice | Why |
|-------|--------|-----|
| **Language** | Go 1.26 | Fast compilation, easy deploy, lean runtime |
| **Database + Auth + API** | [PocketBase](https://pocketbase.io) (embedded, on `ncruces/go-sqlite3`) | Zero-config auth, REST, [admin UI at `/_/`](https://<your-domain>/_/), file storage — all in SQLite |
| **Templating** | [Templ](https://templ.guide) | Type-safe Go components, generated at build time |
| **Reactive UI** | [Datastar](https://data-star.dev) (SSE) | Server-rendered over SSE, single ~12 KiB client. CSS built once via Tailwind v4 CLI; no JS framework build step. |
| **CSS / UI skin** | [DaisyUI v5](https://daisyui.com) (default) + TailwindCSS; pluggable skins: [BasecoatUI](https://basecoatui.com), Morpheus (web components). Switch at runtime via `UI_SKIN` or `?skin=` query | DaisyUI ~34 kB; Basecoat shadcn-style OKLCH tokens; Morpheus vendorized bundle. See [UI skins](#ui-skins-pluggable-daisyui--basecoat--morpheus). |
| **Task queue** | [goqite](https://github.com/maragudk/goqite) + SSE Hub | Background jobs streamed to the browser, no Redis |
| **Retries** | [avast/retry-go v4](https://github.com/avast/retry-go) | Exponential backoff with jitter, no boilerplate |
| **Durable Workflows** | [DagNats](https://github.com/danmestas/dagnats) | Multi-step durable workflows as declarative JSON over NATS JetStream |
| **LLM SDK** | [GoAI](https://github.com/zendev-sh/goai) | Any provider: OpenAI, Anthropic, Groq, Ollama… |
| **Real-time** | [NATS JetStream](https://nats.io) | Multi-user real-time, cross-instance broadcast |
| **Secrets** | [age](https://age-encryption.org) + `~/.secrets/` | Local encryption, no vault, no cloud |
| **IDs** | [google/uuid](https://github.com/google/uuid) | Stable request/job IDs |
| **Live reload** | [Air](https://github.com/air-verse/air) | `make dev` regenerates templ and restarts the binary |
| **CRDT (collaborative docs)** | [loro-go](https://github.com/aholstenson/loro-go) | Conflict-free merging of whiteboard/notes state; converges offline edits with no LWW data loss |
| **Hand-drawn canvas** | [Rough.js](https://roughjs.com) (embedded) | Minimalist sketchy whiteboard rendering, embedded in the binary for self-contained removal with the whiteboard feature |
| **Linting** | [golangci-lint](https://golangci-lint.run) + [datastar-lint](https://github.com/calionauta/datastar-lint) | 27 linters: `govet`, `staticcheck`, `gosec`, `revive`, `gocritic`, `errcheck`, `ineffassign`, `unused`, `errorlint`, `nilerr`, `bodyclose`, `contextcheck`, `containedctx`, `sloglint`, `thelper`, `testifylint`, `gocyclo`, `gocognit`, `funlen`, `noctx`, `goconst`, `dupl`, `lll`, `mnd`, `tagliatelle`, `modernize`, `nolintlint` (see `.golangci.yml`); `datastar-lint` catches Datastar attribute/signal/expression mistakes (run via `make datastar-lint`) |
| **CI/CD** | GitHub Actions | `ci.yml` (lint + test + build, unified build) + `deploy.yml` (multi-arch Docker to ghcr.io, runs on `master`) |

> **Why `ncruces/go-sqlite3`?** It's the pure-Go (no cgo) SQLite engine this template standardizes on and the always-on driver — `db/pocketbase.go` registers it as the `sqlite3` database/sql driver that every query uses. PocketBase also bundles `modernc.org/sqlite`, but that registers itself as `sqlite` and stays unused, so **no build tag is required** and a plain `go build` just works. Being cgo-free means clean cross-compilation for the multi-arch Docker image (linux/amd64 + arm64) and the Wails desktop/mobile builds.

## Stack in layers, not silos

Most templates force you to pick one async strategy — usually a queue, sometimes a workflow runtime, rarely both. Real apps need **a queue for background jobs**, **a workflow runtime for durable multi-step processes**, **a collaboration layer for conflict-free state merging**, and **a real-time layer for cross-client state** — each solving a different problem. We ship all five in one unified build.

We solve this with **six complementary layers**:

```
goqite       → background jobs + SSE hub (always on)
dagnats      → durable multi-step workflows as JSON (runtime opt-out: DAGNATS_ENABLED=false)
Loro CRDT    → collaborative docs with offline merges (opt-out by removing internal/collab/)
PB realtime  → record-change push via PB's native /api/realtime (always on, per-user scoped)
SSE Hub      → ephemeral signals via Datastar protocol (always on, part of queue)
JetStream    → multi-instance broadcast + cross-instance state (runtime opt-out: NATS_ENABLED=false)
```

**Two realtime mechanisms for different jobs.** PocketBase's native `/api/realtime` pushes record mutations (create/toggle/delete) to subscribers, scoped per-user by the collection's access rules. The **SSE Hub** (`internal/queue/ssehub.go`) is reserved for ephemeral signals (client count, LLM suggest feedback, workflow progress, self-patches) and delivers them via Datastar's SSE protocol (`internal/datastar.RenderAndPatch` / `MergeSignals`). The todo feature uses both: PB realtime for CRUD propagation, SSE Hub for toasts and live hints. The whiteboard uses the SSE Hub directly for shape + presence broadcast.

**Cross-instance sync adds two more paths.** When JetStream is enabled (default: on), the **NATS APP_CRUD stream** converges record operations across server instances: CrudPublisher publishes each mutation, CrudConsumer on the receiving instance writes to its local PocketBase, which then broadcasts via PB realtime to its local clients. The **NATS TODOS stream** carries ephemeral signals across instances and re-emits them through the local SSE Hub. Both are safe to enable even on a single instance — the streams simply carry no cross-instance traffic.

**Offline sync uses yet another path.** On the web, the **Service Worker** (`web/resources/static/sw.js`) intercepts POST/PUT/DELETE mutations when the browser is offline, queues them in IndexedDB, and replays them via Background Sync when connectivity returns. On the desktop (NATS Leaf Node), the local JetStream persists mutations to disk and replays them when the Leaf Node reconnects to the server — no Service Worker needed.

**Replay is dedup'd at the server.** The todo create form attaches a fresh `idem_key` UUID to every submit; `db/idempotency_hook.go` intercepts `OnRecordCreateRequest` and returns the existing record on a `(idem_key, owner)` match, so a Service Worker replay doesn't create a duplicate todo. The whiteboard avoids this entirely because Loro CRDT ops already carry unique IDs and converge idempotently on their own.

**The opt-out rules are simple:**
- **Infrastructure components** (NATS, DagNats) have runtime env vars in `config/config.go`. Set `NATS_ENABLED=false` or `DAGNATS_ENABLED=false` and the engine won't boot; downstream consumers handle nil gracefully.
- **Product features** (Todo, Whiteboard) have no runtime flag. To remove them, delete the package directory and remove the wiring call from `router/router.go` — that's the SCOPE removal pattern. See [Architecture taxonomy](#architecture-taxonomy-core--plugin--feature).

They coexist in the same binary. They don't compete.

**One `make build` compiles everything.** No build tags, no stub files, no matrix. DagNats and NATS share a single embedded JetStream on `:4222`.

**Offline-first is baked in, not bolted on.** Web clients intercept mutations in a Service Worker and replay them via Background Sync; the desktop build becomes a NATS Leaf Node that keeps its JetStream replica in sync while offline. Replays are deduplicated server-side (the todo create form attaches an `idem_key` that the idempotency hook collapses). See [Hybrid offline sync](#feature-overview).

## Feature overview

Every capability is always compiled. What you get:

| Capability | Runtime opt-out | What it does |
|-----------|----------------|--------------|
| **Todo app + PocketBase realtime** | — | DB actions (create/toggle/delete) stream through PocketBase realtime, per-user scoped via `owner` rule. SSE Hub for ephemeral signals (toasts, clients count, AI suggest) |
| **Queue + retry** | — | `goqite` background jobs + `retry-go` (the "Queue + Retry" demo). Stepper UI streamed via SSE; uses signal-set `techStep`/`techPhase` |
| **AI Suggest** | `GOAI_API_KEY` unset | GoAI/Groq call from the todo UI; button hidden when no key. Stepper UI streamed via SSE; uses signal-set `aiStep`/`aiPhase` (kept independent from Queue + Retry's stepper signals) |
| **Collaborative whiteboard** | — | Loro CRDT + Rough.js canvas, SSE + NATS broadcast, offline-first outbox replay, PocketBase-persisted snapshots |
| **UI skins (pluggable)** | `UI_SKIN` | DaisyUI v5 (default), BasecoatUI (shadcn-style OKLCH tokens), or Morpheus (vendorized web components). Switch at runtime via `UI_SKIN` env var or `?skin=` query. See [UI skins](#ui-skins-pluggable-daisyui--basecoat--morpheus) |
| **Landing page** | — | Public marketing page on `GET /` (the project tagline + a single CTA). Does NOT require auth, does NOT read the database. The todo demo moved from `/` to `/todo` |
| **Read-only config view** | — | Auth-gated `GET /config` shows what the binary has decided to do (env-decrypted values, masked secrets, runtime constants). Never mutates state |
| **Pluggable persistence** | `ENTITY_STORE` | `pb` (default, PocketBase records + admin UI works) or `crdt` (Loro per-owner doc + JetStream cross-instance transport). Same `EntityStore[T]` interface, swap via one env var |
| **Multi-instance real-time** | `NATS_ENABLED=false` | NATS JetStream fan-out for todo + whiteboard sync across >1 instance behind a LB |
| **Durable workflows** | `DAGNATS_ENABLED=false` | DagNats JSON workflows — HTTP API on `:8090`, durable state on JetStream `:4222` (e.g. `WelcomeOnboarding`) |
| **Desktop-edge sync** | `NATS_LEAFNODE_URL` unset | Leaf-Node JetStream replication of Loro updates for desktop/edge clients |
| **Hybrid offline sync** | `OFFLINE_SYNC_ENABLED=false` | Disables NATS CRUD proxy + Service Worker offline queue (default on). When enabled: desktop edges publish CRUD ops via NATS JetStream, the server's CrudConsumer writes to PocketBase. Web clients use Service Worker + Background Sync for offline queuing and replay. Toggle with a single env var — set to `false` for always-online deployments, and zero code paths are traversed. |

> **Adding a new feature?** Create `features/<name>/` with your handlers + templates. Wire it in `router/router.go` → `Init()` with a single function call. See `ARCHITECTURE.md` for the full pattern.

## Architecture taxonomy: Core / Plugin / Feature

Every file in the codebase carries a `SCOPE` annotation at the top to tell agents and developers what can be safely removed:

| Annotation | Meaning | Examples | You would… |
|------------|---------|----------|------------|
| `SCOPE:core` 🔴 | Binary does not work without it. Some have runtime opt-out via env vars. | `config/`, `db/`, `internal/queue/`, `internal/secrets/`, `features/auth/` (middleware), `router/`, `web/resources/` | Customize, never remove. |
| `SCOPE:plugin` 🟡 | Binary works but loses a capability. Swap or delete with its wiring call. | `internal/datastar/`, `internal/nats/`, `internal/dagnats/`, `internal/llm/`, `internal/collab/`, `internal/components/`, `features/store/`, `web/skins/` | Swap for another implementation, or delete the package + wiring call (e.g. `router.Init`, `cmd/web/main.go`). |
| `SCOPE:feature` 🟢 | A demo/add-on. Delete the package + remove the wiring call. | `features/todo/`, `features/whiteboard/`, `features/landing/`, `features/config/`, `router/onboarding_dagnats.go`, `router/realtime_jet.go` | Keep as reference while building your own, then remove. |

**Rule of thumb for agents:** If you see a `SCOPE` annotation on a file, respect it. Never delete a `SCOPE:core` file without asking. Never keep a `SCOPE:feature` file in production if the domain doesn't need it.

### How to remove a plugin or feature component

1. Delete the package directory (e.g. `features/todo/`).
2. Delete dependent packages listed in the `Depends on:` comment.
3. Remove the wiring call from `router/router.go` → `Init()`.
4. If it was plugin, also remove the `start*` call in `cmd/web/main.go`.

See `ARCHITECTURE.md` for the full dependency graph.

## Code quality for LLM agents

This template ships a strict `golangci-lint` configuration (27 linters) designed to catch the kinds of mistakes LLMs make most often: unchecked errors, insecure patterns, broken context propagation, resource leaks, and inconsistent error wrapping. The goal is not to block development but to redirect agents toward correct Go idioms automatically.

**What the linters enforce:**

| Category | Linters | What they catch |
|----------|---------|-----------------|
| Correctness | `govet`, `staticcheck`, `errcheck`, `ineffassign`, `unused` | Shadowed variables, dead code, unchecked returns |
| Error handling | `errorlint`, `nilerr`, `gosec` | Wrong `%w` formatting, returning nil inside an error path, hardcoded credentials |
| Resource safety | `bodyclose`, `noctx` | HTTP bodies and contexts not closed or propagated |
| Test quality | `thelper`, `testifylint`, `sloglint`, `containedctx` | Missing `t.Helper()`, `assert` vs `require` misuse, context embedded in structs |
| Complexity | `gocyclo`, `gocognit`, `funlen`, `nestif` (implicit via funlen+gocyclo) | Functions too long or too nested to hold in working memory |
| Style | `revive`, `gocritic`, `tagliatelle`, `goconst`, `dupl`, `lll`, `modernize` | Non-idiomatic patterns, magic numbers, duplicated code, long lines |
| Formatting | `gofumpt` + `goimports` (formatters, not linters) | Compulsory consistent layout and import ordering |

**For LLM agents reading this:** the cheapest reliable path is
**`make signoff`** (= `make ci-local` + `gh signoff` stamp on
HEAD). Run it before pushing anything. The 1–3 min local gate
catches ~95%% of regressions that would otherwise only surface
on remote CI: race detector races (e.g. `TestXxx` tripping
`-race` on a TOCTOU field), lint warnings, format drift, CSS
staleness, `sync.Once` wrong placement, Dockerfile `ARG`
inline placement that breaks `docker buildx`, etc. CI then
becomes a parallel validator + auto-deploy step, not the
primary gatekeeper.

Workflow: `edit → make ci-local (if iterating) → git commit -F /tmp/msg → make signoff → git push origin master`. The `golangci-lint` configuration lives in `.golangci.yml` at the project root — read it if you need to understand what each linter expects. If a lint forces you to restructure code, that is usually a sign the original approach had a deeper issue.

**For human developers:** `make ci-local` runs the full gate (templ + datastar-lint + css-check + golangci-lint + race tests + build). `make lint` runs just `go vet` + `golangci-lint`. We deliberately keep `gofumpt` and `goimports` as formatters (not linters) so `golangci-lint run` never auto-formats your files — formatting is a separate explicit step.

**How to run each lint layer:**

| Command | What it checks |
|---------|---------------|
| `make lint` | `go vet` + golangci-lint (27 linters) |
| `make datastar-lint` | Datastar-specific anti-patterns in `.templ` files |
| `make fmt` | `gofumpt` + `goimports` formatting only |
| `make ci-local` | Full local gate, identical to CI: templ → datastar-lint → css-check → golangci-lint → race tests → build |

The pre-commit hook (`make setup`) runs `gofumpt`, `goimports`,
`datastar-lint`, a CSS staleness check, `go mod tidy`, and
`golangci-lint` on every commit — so formatting and lint
violations never reach the remote. Run **`make signoff`** for
the full gate (adds `ci-local` + `gh signoff` stamp) before
pushing. Same checks the remote CI runs.

## The example: Todo App with realtime

We ship a working Todo App:

- Full CRUD via PocketBase
- Reactive frontend with Datastar + the active skin (DaisyUI by default; BasecoatUI / Morpheus switchable via `UI_SKIN` or `?skin=` — see [UI skins](#ui-skins-pluggable-daisyui--basecoat--morpheus))
- **Database actions stream through PocketBase realtime.** Todo `create`/`toggle`/`delete` fire PocketBase record events; each subscribed client re-fetches the fragment and morphs `#todo-list`. Delivery is per-user scoped by the collection's `owner` rule (`@request.auth.id != '' && owner = @request.auth.id`), so a client only receives events for its own records. The SSE Hub is reserved for ephemeral signals (success/retry toasts, live clients count, AI suggest) and the originating client's own synchronous patch.
- Stacked toast notifications (auto-dismiss, manual close, progress bar)
- Async jobs: `handleCreate` enqueues a `todo_created` job; a worker picks it up and streams a success toast to the right browser tab via the SSE Hub (`clientID` routing)
- Retries with exponential backoff and jitter (`internal/queue/retry.go`, retry-go v4) — SSE-aware: a retry emits a `lastRetry` signal so the UI can show "retrying…"
- `WelcomeOnboarding` DagNats workflow (always compiled) that creates 3 example todos via durable steps — kill the server mid-run, restart, watch it resume at the last incomplete step. The workflow is declarative JSON (`internal/dagnats/workflow.go`), so renaming Go handlers never orphans an in-flight run.
- **Admin unlock** via `age` + `~/.secrets/`. The Todo example wires a master-password path: when `ADMIN_UNLOCK_TOKEN` is set (in the age-encrypted secrets file), the UI shows a "Clear all" form; the handler compares constant-time and clears all todos on match. Demonstrates the age flow end-to-end.
- **AI suggest** via GoAI. When `GOAI_API_KEY` is set, the input gets a "Suggest" button that enqueues an async suggest job (see queue below) and streams the 3 completions back via SSE. It talks to whatever OpenAI-compatible provider `GOAI_BASE_URL`/`GOAI_MODEL` point at — see [Configuring the LLM](#configuring-the-llm-goai). Retries with exponential backoff use the same `internal/queue/retry.go` as the SSE toast path. The stepper UI (`aiStep`/`aiPending`/`aiPhase` signals) is **kept independent** from the Queue + Retry demo's stepper signals (`techStep`/`techPhase`), so running one never lights the other. For a **keyless** demo of the exact same queue + retry path, `SIMULATE_LLM` is on by default (opt out with `SIMULATE_LLM=false`): a "Suggest (simulated)" button enqueues a job that hits an in-process fake LLM scripting 500 → 200 + delay, so you can watch the retry feedback toasts (enqueued → attempt failed → slow → result).
- Tests run with `-race`

> **This is the contract you should imitate when adding a new feature:**
> 1. **Pure HTTP + Datastar** for the user-facing surface.
> 2. **goqite job** for any work that takes more than ~50ms (LLM, email, exports).
> 3. **SSE toast** for async feedback to the originating client via `clientID` routing.
> 4. **age-encrypted secret** if the feature needs a credential.
> Every existing feature (toast on create, AI suggest, admin unlock, DagNats onboarding) follows this exact shape.

Enough to understand the pattern.

## Adding your own feature

Every feature in this template follows the same pattern. Use it as a blueprint when building yours:

1. Create `features/<name>/` with your HTTP handlers + Templ components.
2. Wire it in `router/router.go` → `Init()` with a single function call.
3. Use **goqite** for async work, **SSE Hub** for user-facing feedback (toasts, progress), and **Datastar** for reactive frontend.
4. Add `SCOPE:feature` or `SCOPE:core` annotations so agents know what they can remove.
5. Add a `RegisterRoutes(se, deps)` function and call it from `router.Init`.

See the Todo feature for the full reference implementation.


## UI skins (pluggable: DaisyUI / Basecoat / Morpheus)

Every page in this template ships with a runtime-switchable UI skin. Three skins are compiled into the same binary; the active one is chosen per process via env, per request via query string, or interactively via the `SkinSelector` widget in the navbar.

| Skin | What it is | CSS / JS |
|------|-----------|----------|
| **DaisyUI** (default) | The reference UI. Server-rendered DaisyUI v5 components over TailwindCSS, morph-friendly with Datastar | `app.min.css` |
| **Basecoat** | [BasecoatUI](https://basecoatui.com) (a shadcn-style component lib) with shadcn-inspired OKLCH `@theme inline` color tokens, native Basecoat JS runtime (`basecoat.initAll`) debounced via `requestAnimationFrame` for Datastar DOM morphing | `basecoat.min.css` + `basecoat.min.js` |
| **Morpheus** | Vendorized web-components bundle (SHA-pinned, `web/skins/morpheus/VENDOR_SHA`) that gives the todo demo a different visual treatment without DaisyUI | `morpheus/bundle.js` + theme CSS |

> **⚠️ Basecoat and Morpheus are community-supported.** DaisyUI (the default) is the polished, battle-tested skin that gets the most development attention. Basecoat and Morpheus integrate correctly but may have rough edges in their current state — CSS alignment nuances, missing component states (disabled, focus, error), and less extensive Datastar morph testing. **Contributions are welcome:** if you'd like to fix a skin-specific issue, improve a component template for a non-DaisyUI skin, or add a missing state, open a PR or issue. Every skin is a self-contained directory under `web/skins/<name>/` — changes are scoped and safe.

**Three ways to switch:**

1. **Env var (process-wide).** `UI_SKIN=basecoat ./gogogo-fullstack-template` switches the active skin for the lifetime of the binary.
2. **Query string (per request).** Append `?skin=morpheus` to any route; the skin dispatcher reads it and renders that skin's assets without restart.
3. **Interactive selector.** The navbar exposes a `SkinSelector` that updates a query param + reloads.

**Plugin contract** (`web/skins/skin.go`). Every skin is a `Skin{Name, Assets}` value registered at init time via blank imports in `features/todo/components/skin_imports.go`. The dispatcher falls back to DaisyUI when the env value is unknown, logging a warning. Adding a fourth skin is: create `web/skins/<name>/`, register it from the import file, add a `make css-<name>` target. See `web/skins/daisyui/skin.go` for the minimal reference implementation (assets only, no Templ templates — those stay in the feature).

**Removal.** Delete `web/skins/`, drop the blank imports in `features/todo/components/skin_imports.go`, drop the `SkinSelector` call from the navbar. The handler's lazy fallback returns DaisyUI assets when no skin is registered.


## Admin & Dashboard

Three built-in surfaces, available as soon as the binary boots:

| Surface | URL | What it gives you |
|---------|-----|-------------------|
| **Landing page** | `/` | Public marketing hero (project tagline + CTA). Guests and signed-in users see the same page; no DB read, no auth gate |
| **Read-only config view** | `/config` | Auth-gated view of the running binary: env-decrypted values, masked secrets, runtime constants. Never mutates state. Source: `features/config/` |
| **PocketBase admin** | `/_/` | Data browser, REST playground, superuser management, backups, logs |
| **DagNats console** | `:8090` | Workflow runs, step inspection, JSON API for durable workflows |

> The admin UI is the **upstream PocketBase UI**, embedded in the same binary on the same port. No extra service to deploy. Point a Cloudflare Tunnel at `/_/` and lock it down with PocketBase's own superuser auth.

### PocketBase admin UI (`/_/`)

- **Visual data browser** for every collection (todos, users, etc.) with sort/filter/CSV export
- **REST + JS SDK playground** for the API endpoints PocketBase generated from your schema
- **Superuser management** (create the first one via the install link printed in the server logs)
- **File storage** (S3-compatible uploads, images, attachments)
- **Backups** (SQLite snapshot, download + restore)
- **Logs** (requests, errors, slow queries)

This is **not a custom admin panel** — it's the upstream PocketBase UI, embedded in the same binary, on the same port. No extra service to deploy, no extra auth to wire. For production, point a Cloudflare Tunnel / Caddy ingress at the same `/_/` path and lock it down (IP allowlist, oauth2-proxy in front, or just PocketBase's own superuser auth).

### App session cookie vs `pb_auth` (why two)

The app **never** reuses PocketBase's own `pb_auth` cookie. PocketBase keeps the superuser (`_superusers`) and regular users as **separate auth namespaces with different endpoints**, and a single client holds only **one** auth state (one cookie). Sharing `pb_auth` for the app session clobbers the admin session in the same browser (and vice-versa) — a well-known PocketBase gotcha ([#5050](https://github.com/pocketbase/pocketbase/issues/5050), [#1780](https://github.com/pocketbase/pocketbase/issues/1780)).

So login issues **two** cookies:

- `gogogo_auth` — the app's own session cookie, read by `LoadAuthFromCookie`.
- `pb_auth` — the same token under PocketBase's native name, so PB-native surfaces (notably the `/api/realtime` SSE channel for record-change subscriptions) authenticate as the same user. Without it, realtime record events are silently dropped by PB's per-subscriber access check.

The split is **intentional, not tech debt** — keep the two cookies separate. **Best practice:** run the admin UI on a separate origin/port (e.g. `:8090/_/`) so even `pb_auth` never collides between admin and app.

### DagNats console (`:8090`)

The DagNats workflow engine exposes its own HTTP API + console at `DAGNATS_HTTP_ADDR` (default `127.0.0.1:8090`). Inspect runs, steps, or trigger workflows via the API. The `WelcomeOnboarding` workflow runs here — declarative JSON over NATS JetStream, kickstarted automatically on first login.

## Try it live

A running deployment of this exact template is live. You can touch every feature from the README without cloning:

| What | URL | What you can do |
|------|-----|-----------------|
| **Todo demo app** | `https://gogogo.calionauta.com/` | Log in with the seeded demo account (`demo@demo.app` / `demo`). |
| **Live PocketBase admin dashboard** | `https://gogogo.calionauta.com/_/` | Open the embedded PocketBase UI to browse the `todos` + `users` collections, run the REST/JS SDK playground, and inspect logs. The demo's `users` collection is **locked** — visitors can log in as the demo user but cannot create or delete accounts through the API or this dashboard (only the superuser can). |
| **Durable workflow engine (DagNats)** | `https://gogogo.calionauta.com:8090` | The DagNats HTTP API where the `WelcomeOnboarding` workflow runs (declarative JSON over NATS JetStream). Inspect runs/steps or trigger them via the API; the Todo demo drives it automatically on first login. |

> The demo runs the unified build (everything compiled in). DagNats + NATS share a **single embedded JetStream** on `:4222` — DagNats boots it and the whiteboard SyncWorker attaches to it, so there is only one NATS process in the binary. To stand up your own, see [Deploy](#deploy).

## Configuring the LLM (GoAI)

The AI Suggest + Queue/Retry demo is wired through [GoAI](https://github.com/zendev-sh/goai) and reads its configuration from the environment (or your age-encrypted secrets file). Two paths:

**1. A real OpenAI-compatible provider (recommended for production).** Set `GOAI_API_KEY`, point `GOAI_BASE_URL` at the provider's `/v1` endpoint, and pick a `GOAI_MODEL`. Any OpenAI-compatible endpoint works — we do not hardcode a provider, you choose. With a key present, the Todo UI shows the **Suggest** button.

```bash
GOAI_API_KEY=sk-...
GOAI_BASE_URL=https://api.groq.com/openai/v1
GOAI_MODEL=llama-3.3-70b-versatile
```

**2. Keyless simulated LLM (on by default — best for trying the queue + retry path in dev).** `SIMULATE_LLM` is enabled automatically (no API key needed); set `SIMULATE_LLM=false` to disable it. It spins up an in-process fake GoAI client that scripts a realistic failure (500 → retry → slow → 200) so you can watch the retry feedback toasts end-to-end. The UI shows a **Suggest (simulated)** button that reuses the exact same `goqite` + `retry-go` path as the real provider.

If neither `GOAI_API_KEY` is set nor `SIMULATE_LLM` is enabled (i.e. `SIMULATE_LLM=false` and no key), the AI suggest route is **not registered** and the UI button is hidden. The Todo example keeps working — AI is opt-in, not required.

## Getting started

Use this template (green **Use this template** button above) or clone it:

```bash
git clone https://github.com/calionauta/gogogo-fullstack-template.git my-project
cd my-project
make dev
```

Open `http://localhost:8080` for the landing page, then `http://localhost:8080/todo` for the demo (sign in with the seeded `demo@demo.app` / `demo`). The root URL is public; `/todo` is auth-gated; `/config` shows the running binary's configuration (auth-gated).

> The default port is `8080` (override with `PORT`). The default branch is `master`.

### Other commands

```bash
make build         # Build binary (unified: everything included)
make dev           # Live reload with Air (also re-runs templ + vet)
make templ         # Regenerate .templ Go files after a .templ edit
make css           # Rebuild app.min.css from src/css/input.css
make lint          # go vet + golangci-lint (27 linters), full repo
make datastar-lint # Datastar attribute / signal anti-patterns in .templ
make fmt           # gofumpt + goimports check (CI gate; apply via gofumpt -w)
make test          # Race tests (`-p 1` for DagNats engine stability). Discouraged locally — remote CI runs them.
make ci-local      # Full local gate (= CI): templ + datastar-lint + css-check + golangci-lint + race tests + build
make signoff       # `make ci-local` + `gh signoff -f` stamp. Default pre-push gate — catches ~95%% of regressions in <3min locally.
make setup         # Install pre-commit + pre-push hooks
make docker-image  # Build and push multi-arch image to ghcr.io

`make check` was removed (redundant subset of `make ci-local`).
```

### Build pipeline

The compile steps **outside** `go build` are the CSS bundles (one per
skin) and the Go ldflags that bake the version badge into the binary.
All assets are embedded into the Go binary via `//go:embed` — there is
no runtime CSS build step, no JS runtime, and no CDN.

```
src/css/input.css         →  tailwindcss v4 CLI  →  web/resources/static/app.min.css         (DaisyUI bundle)
src/css/basecoat-input.css →  tailwindcss v4 CLI  →  web/resources/static/basecoat.min.css   (Basecoat bundle)
                                                                            │
                                              web/skins/morpheus/static/bundle.js            (Morpheus web components, vendorized, SHA-pinned)
                                                                            │
                                                                            └─ //go:embed in the Go binary
```

The Makefile wires each skin's CSS build behind its own target:
`make css` (DaisyUI, default), `make css-basecoat` (Basecoat),
`make css-all` (all skins). The pre-commit hook regenerates
`app.min.css` automatically whenever `.templ` or `.go` files change,
and `make ci-local` includes a `css-check` step that fails the gate
if the working CSS file is out of date.

The pre-commit hook regenerates `app.min.css` automatically whenever
`.templ` or `.go` files change, and `make ci-local` includes a `css-check`
step that fails the gate if the working CSS file is out of date.

### Version badge build metadata (ldflags + docker buildx)

The navbar version badge (`features/todo/components/layout.templ`) shows
`BuildLabel` (the git tag, e.g. `v0.24.8`) and `BuildCommit` (the short
SHA). Both are baked into the binary at build time via Go `ldflags` and
flow through the Docker image via `ARG`:

```bash
# Local build — the Makefile sets these automatically:
make build
#   VERSION  = git describe --tags --abbrev=0 | sed 's/^v//'   (e.g. 0.24.8; "dev" if no tags)
#   COMMIT   = git rev-parse --short HEAD                       (e.g. d9c8010; "unknown" if no git)
#   BUILDTIME = date -u +"%Y-%m-%dT%H:%M:%SZ"
# LDFLAGS = -ldflags="-w -X main.Version=$VERSION -X main.CommitHash=$COMMIT -X main.BuildTime=$BUILDTIME"

# Docker build (CI / deploy):
docker buildx build --platform=linux/amd64,linux/arm64 \
  --build-arg VERSION=$VERSION \
  --build-arg COMMIT=$COMMIT \
  --build-arg BUILDTIME=$BUILDTIME \
  -t ghcr.io/calionauta/gogogo-fullstack-template:latest \
  -t ghcr.io/calionauta/gogogo-fullstack-template:$VERSION \
  --push .
```

The Dockerfile declares `ARG VERSION COMMIT BUILDTIME` at the stage top
(not inline inside a `RUN` chain — that breaks Buildkit parse), then
bakes them into the binary in the same `go build` step that produces
the scratch image. The result: opening the running app, the navbar
version badge matches the deploy commit, and you can byte-diff
`app.min.css` or the binary itself against the tag to confirm.

**Confirming a deploy.** The cheapest proof a fix is live:
`diff <(curl https://<host>/static/app.min.css) <(git show HEAD:web/resources/static/app.min.css)`.
For the version badge specifically: `diff <(curl https://<host>/api/version) <(echo $VERSION)`.

## Local CI (gh-signoff) — T5 of the feedback loop

Pushing to `master` triggers GitHub Actions: `ci.yml` runs the full
gate, then `deploy.yml` ships to production. Run that **exact gate
on your own machine** before pushing, so you don't wait on remote
runners and don't push a broken commit.

We use [gh-signoff](https://github.com/basecamp/gh-signoff) — a GitHub CLI
extension that stamps a green commit status after your local gate passes.

```bash
# one-time: install the extension
gh extension install basecamp/gh-signoff

# before pushing: run ci-local, then stamp the commit green
make signoff
```

`make signoff` runs `make ci-local` (templ generate → golangci-lint →
datastar-lint → CSS check → `go test -race -p 1` → `go build`) and then
stamps the current commit green with `gh signoff`. `make ci-local` uses
`golangci-lint` as the authoritative formatter/lint gate (the same linter
CI runs) rather than the standalone `gofumpt` binary, which can be a newer
release than the one golangci-lint bundles and would otherwise produce
false-positive listings. **The dependency runs one way: `signoff` calls `ci-local`; `ci-local` never calls `signoff`** — that keeps the local gate clean to run on its own, and reserves the git-stamp for the explicit pre-push moment.

### Feedback tiers at a glance

| Tier | Command | Cost | Catches |
|---|---|---|---|
| T1 format+build | `gofumpt -l -d <files>` + `go build ./...` | ~10s | Format drift + compile errors |
| T2 lint scoped | `go vet` + `golangci-lint run <changed-glob>` + `make templ/datalint` (when templ) | ~15–20s | Shadow, mnd, nolintlint, revive, staticcheck, datastar attrs |
| T3 tests scoped | `go test -race -count=1 <changed-pkg>` | ~5–30s | Race detector on tests, business logic |
| T4 full local gate | `make ci-local` | ~60–180s | Full pre-push check (= CI) |
| **T5 signoff local** | `make signoff` (= T4 + `gh signoff -f`) | ~60–180s | Same as T4 + commits the verification to git |

T5 is the recommended pre-push gate. The remote CI then runs the same
T4 checks as a parallel validator + drives the auto-deploy step;
signoff does **not** skip CI. Cycle when a regression appears on CI:

1. CI red: read the failing log step (test, lint, css-check, build).
2. Reproduce locally (`make ci-local`) — usually the same failure.
3. Fix + commit.
4. `make signoff` again — green means it would pass CI on a re-run.

> **Advisory status, by design.** This repo deploys on **push to `master`**
> (not PR merge), so the signoff status is a *signal*, not a hard gate.
> We do not run `gh signoff install` (which would gate PR merges) — it would be meaningless for a push-to-deploy flow, so we leave it off by design.

### What signoff catches that CI doesn't

Nothing — CI runs the same checks. What signoff does is run them *locally
first*, so a regression shows up in your terminal in 1–3 min instead of
after a CI queue. The biggest emitters the local loop has caught:

- Race detector on `TestXxx` (e.g. `sync.Once` instead of TOCTOU
  init).
- Dockerfile `ARG` inline (`ARG X=foo` inside `RUN ... && ...` chain
  fails Buildkit parse).
- Stale `-tags jetstream dagnats` after the unified-build era (silent
  in Go compile, silent in lint, but visible on `docker buildx`).
- CSS bundle silently stale (Template or GO file changed but
  `app.min.css` not regenerated).
- Format drift accumulating through several small commits.

CI does the same checks. The local run is the same `make ci-local`
target CI runs. The difference is **who waits** for the run.


## Desktop & Mobile (Wails v3 + Loro CRDT + NATS Leaf Node)

The same Go backend (PocketBase + queue + router + handlers) also runs as a
**native desktop app** via Wails v3. The desktop build reuses 100% of the
business logic — it boots `internal/server.Run`, serves PocketBase in a
goroutine, and points the webview at it through a reverse proxy.

Build commands (Wails v3 CLI):

```bash
# Current platform
wails3 build
# Cross-platform
wails3 build GOOS=windows
wails3 build GOOS=linux
wails3 build GOOS=darwin GOARCH=arm64
# macOS .app bundle
wails3 package GOOS=darwin
# Android APK (needs Android SDK/NDK + JDK 21 — see Mobile below)
wails3 android:package
```

All builds compile with everything included (unified build).
If you prefer a plain binary without the wails CLI, `make desktop` runs
`go build ./cmd/desktop`.

**Edge sync.** If `NATS_LEAFNODE_URL` is set, the desktop boots as a **NATS Leaf Node** that
syncs its JetStream streams with your central server — offline edits replay
on reconnect. Without it, it runs a standalone embedded NATS for local
realtime. On top of that transport, **Loro CRDT** collaboration
(`internal/collab`) publishes whiteboard updates on `app.sync.<docID>` and
ephemeral multi-user **cursors** on `app.presence.<docID>`; the central
server persists resolved Loro snapshots to PocketBase (`whiteboards`
collection) and streams presence to browser clients via SSE
(`GET /api/collab/presence/{docID}`).

**Desktop builds are local-only — not in CI.** Generate them with the
commands above: `wails3 build` for a binary, `wails3 package GOOS=darwin`
for a macOS `.app` (wrap in a `.dmg` with `hdiutil` if you want a
redistributable installer). Keeping the desktop out of CI keeps the pipeline
lean; the full e2e gate (incl. `TestCollab_LeafNodeE2E` and
`TestPresence_SSEBridgeE2E`) still runs in `ci.yml` under the unified build.

> **Mobile (Android) is opt-in, not in CI.** Wails v3 targets Android from
the same `main.go` (Go → `libwails.so`, WebView frontend) — no separate
mobile project. Generate an APK locally with `wails3 android:package`
(or `android:package:fat`). This requires the **Android SDK (API 35) +
NDK (26.3.x) + JDK 21**; `wails3 doctor` reports what's missing. Because
that toolchain is heavy, APK builds are left to the developer and are not
part of the CI matrix. iOS is analogous but requires Xcode.

## Deploy to your own box

The default workflow is to **clone + `make dev`** for local work. For a permanent
demo, the project ships a production deploy workflow that publishes to a
server of your choosing (recommended: a small Linux box + Tailscale + a
Cloudflare-tunneled domain). No registry, no cold starts, full control.

### Server layout (multi-project standard)

Pick a directory on your server (e.g. `/opt`) and follow this layout for
**every** project that adopts the pattern — siblings share the same shape:

```
/opt/
└── gogogo-fullstack-template/                  ← this project
    ├── bin/
    │   ├── gogogo-fullstack-template             ← current binary (chmod 755)
    │   └── gogogo-fullstack-template.previous    ← prior binary, kept for fast rollback
    ├── compose/
    │   └── docker-compose.prod.yml
    ├── env/
    │   └── .env                       ← non-secret env (DATABASE_URL, APP_URL, ...)
    ├── secrets/
    │   └── gogogo-fullstack-template.env         ← mode 600, regenerated every deploy from GH Secrets
    ├── data/
    │   └── pb_data/                    ← persistent volume, survives restarts
    ├── repo/                           ← git clone of this repo (for re-syncing on each deploy)
    ├── scripts/
    │   └── deploy-prod.sh             ← the on-server deploy runner
    └── README.md                       ← operator's guide (link to this section)

/opt/<other-project>/                  ← siblings follow the same shape
    ├── bin/
    ├── compose/
    ├── env/
    ├── secrets/
    └── data/
```

### First-time setup on the server

1. Install Docker + create a `deploy` user with SSH key access.
2. Add the box to your Tailscale tailnet.
3. Configure a Cloudflare Tunnel that routes your domain (e.g. `fullstack.example.com`)
   to the Tailscale hostname on port 8080.
4. Clone the repo at `/opt/gogogo-fullstack-template/repo/`. A `setup-server.sh` helper is planned as a follow-up; for now run the manual steps: `mkdir -p bin compose env secrets data/pb_data scripts`.
5. Add the GitHub Actions secrets (see `.github/workflows/deploy.yml` for the full list).

### After setup, every push to `master` deploys

The workflow at `.github/workflows/deploy.yml` runs on every push to `master` and:

1. Builds the project (lint + race tests + CSS build).
2. Builds the production Docker image (linux/amd64 scratch) in the GH Action runner.
3. SCPs the new binary to the server as `gogogo-fullstack-template.new` (atomic swap).
4. Writes the secrets file (`/opt/gogogo-fullstack-template/secrets/gogogo-fullstack-template.env`) with mode 600.
5. SSHes in and runs `scripts/deploy-prod.sh` which:
   - Atomically renames `gogogo-fullstack-template.new` → `gogogo-fullstack-template` and keeps the old binary as `.previous`.
   - Restarts the container via `docker compose -f docker-compose.prod.yml up -d`.
   - Waits up to 30s for `/health` to return 200.
6. Prints the new container status + last 20 log lines for confirmation.

Secrets are **never stored long-term on the server**: every deploy
re-renders `/opt/gogogo-fullstack-template/secrets/gogogo-fullstack-template.env` from GitHub
Actions secrets. The file is `chmod 600`, owned by the `deploy` user,
and overwritten on every run — there is no history of secrets on disk.

## Structure (annotated by SCOPE)

```
cmd/web/                          🔴 CORE  Entry point (PB + goqite + SSE Hub + DagNats + NATS)
cmd/desktop/                      🔴 CORE  Wails v3 desktop/edge shell (NATS Leaf Node when NATS_LEAFNODE_URL set)
config/                           🔴 CORE  Per-environment config
  config.go                       🔴 CORE  Env vars + age secrets
  config_dev.go / config_prod.go  🔴 CORE  Build-tag defaults
db/                               🔴 CORE  PocketBase setup + seed
internal/
  secrets/                        🔴 CORE  age-decrypted secrets loader
  queue/                          🔴 CORE  goqite + SSE Hub + workers + retry + handler registry
    goqite.go                     🔴 CORE  goqite setup, schema, graceful shutdown
    ssehub.go                     🔴 CORE  register-before-enqueue, replay buffer, backpressure
    workers.go                    🔴 CORE  worker pool with context cancellation
    retry.go                      🔴 CORE  exponential backoff + jitter (retry-go v4)
    handlers.go                   🔴 CORE  HandlerRegistry: job-type to handler dispatch
  datastar/                       🟡 PLUGIN  Datastar SSE rendering helpers
  nats/                           🟡 PLUGIN  NATS JetStream + embedded server
  dagnats/                        🟡 PLUGIN  DagNats durable workflow client
  llm/                            🟡 PLUGIN  GoAI LLM SDK helpers
  collab/                         🟡 PLUGIN  Loro CRDT + DocStore + sync workers + presence
features/
  app/                            🔴 CORE  AppContext (cross-cutting deps bundle)
  auth/                           🔴 CORE  Login/logout/cookie (UI) + 🔴 middleware
  store/                          🟡 PLUGIN  EntityStore interface (PB + CRDT strategies). `ENTITY_STORE` selects impl.
  landing/                        🟢 FEATURE  Public marketing hero on `GET /` (no auth, no DB read)
  config/                         🟢 FEATURE  Auth-gated read-only `/config` view (masked secrets, runtime constants)
  todo/                           🟢 FEATURE  Todo MVC example (keep as reference)
    handlers/                       HTTP + SSE handlers, onboarding
    components/                     Templ components
  whiteboard/                     🟢 FEATURE  Collaborative canvas (remove if not needed)
web/
  resources/                      🔴 CORE  Static assets (embedded JS)
  skins/                          🟡 PLUGIN  Pluggable UI skin registry (`UI_SKIN` / `?skin=`). Ships daisyui / basecoat / morpheus.
router/                           🔴 CORE  Route wiring (central dependency graph)
```

### Key configuration constants

| Constant | Location | Default | Purpose |
|----------|----------|---------|---------|
| `DefaultReplayBufferSize` | `config/config.go` | 64 | Per-client SSE replay ring-buffer size (was `internal/queue/ssehub.go`) |
| `DefaultClientQueueSize` | `config/config.go` | 64 | Per-client SSE channel buffer (was `internal/queue/ssehub.go`) |
| `DefaultSSEHeartbeatInterval` | `config/config.go` | 15s | SSE heartbeat interval (was `internal/queue/ssehub.go`) |
| `OfflineSync.Enabled` | `config/config.go` | `true` | Toggle hybrid offline-sync-online. Set `OFFLINE_SYNC_ENABLED=false` to opt out. |
| `EntityStore` | `config/config.go` | `"pb"` | Pluggable persistence strategy: `pb` (PocketBase records, default) or `crdt` (Loro per-owner doc + JetStream). Set via `ENTITY_STORE` |
| `Skin` | `config/config.go` | `"daisyui"` | Active UI skin: `daisyui` (default), `basecoat`, `morpheus`. Override per request with `?skin=` |
| `BuildLabel` | `config/config.go` | `"dev"` | Git tag (e.g. `v0.24.8`) baked into the binary via `-ldflags="-X main.Version=..."`; surfaced on the navbar version badge |
| `BuildCommit` | `config/config.go` | `"unknown"` | Short git SHA baked into the binary via `-ldflags="-X main.CommitHash=..."`; surfaced alongside `BuildLabel` |
| `DefaultBaseURL` (GoAI) | `internal/llm/goai.go` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `DefaultModel` (GoAI) | `internal/llm/goai.go` | `gpt-4o-mini` | Default LLM model |

All are configurable in one place (env var in `config/config.go`, runtime constant in `config/config.go` or the owning package). Change it once, every feature picks up the new value.

> **Why some constants are NOT in config.go?** Runtime constants that are implementation details of a single package (like `DefaultBaseURL` in `internal/llm/goai.go`) stay in that package to keep cohesion. `config/config.go` documents every env var and the most commonly tuned runtime constants.

## Acknowledgements & inside jokes

This template was inspired by [northstar](https://github.com/zangster300/northstar) by Nicholas Zanghi — a Go + NATS + Datastar + Templ + DaisyUI application starter.

## License, feedback

Licensed under the [MIT License](./LICENSE). This project is open to feedback, PRs, and adaptations. If something doesn't make sense, if the stack doesn't fit your problem, or if you have a better idea — open an issue.

---

Made with intent to be useful, not to be right. — feedback, PRs, and adaptations welcome.
