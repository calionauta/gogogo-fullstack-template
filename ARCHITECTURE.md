# Architecture — LLM Entrypoint

> This file is the **canonical entrypoint for LLM agents** navigating the template.
> Each row describes one component: where it lives, what it depends on, and how to remove or replace it.

---

## Principles

- **Unified build.** One `go build ./cmd/web` compiles everything — no build tags. `ncruces/go-sqlite3` is the always-on SQLite driver (it registers `sqlite3`); PocketBase also bundles `modernc.org/sqlite`, but that registers `sqlite` and stays unused, so nothing is gated behind a tag. Opt out at runtime with env vars (`NATS_ENABLED=false`, `DAGNATS_ENABLED=false`).
- **Features in `features/`**, infrastructure in `internal/`. Features depend on infra, never the reverse.
- **Wiring in `router/router.go` → `Init()`**. Every feature is registered by one function call.
- **Startup order in `cmd/web/main.go`** (see diagram below).

---

> **SCOPE annotations in code.** Every non-test, non-generated `.go` file under `internal/` and `features/` carries a leading doc-comment line declaring its SCOPE on **two orthogonal axes**: `// SCOPE:layer=<infra|feature>,removal=<core|plugin|feature> — <description>`. The `layer` axis says where the code lives (cross-cutting infra vs product feature). The `removal` axis says what happens if you delete it (binary breaks, binary loses capability, or pure demo). This two-axis scheme eliminates the prior ambiguity where `SCOPE:core - REMOVE if not using NATS` and `SCOPE:core - DO NOT REMOVE - SSE Hub` shared the same label.

> The `cmd/check-scope` Go program walks every `.go` file in `internal/` and `features/` and asserts the canonical SCOPE line is present in the leading doc-comment group. `make ci-local` runs it via the `check-scope` target. The pre-commit hook runs it conditionally when staged files match `^(internal|features)/.*\.go$`. Migrating an existing file uses `python3 scripts/migrate-scope.py` (idempotent). The `SCOPE` annotations in source files are the authoritative reference; this table is a summary.

## Layer taxonomy (SCOPE)

Every component belongs to one of three layers, matching the `SCOPE:` annotation in each source file:

| SCOPE | Meaning | You would… |
|:------|:--------|:-----------|
| **Core** 🔴 `SCOPE:layer=…,removal=core` | Binary does not work without it. | Customize, never remove. |
| **Plugin** 🟡 `SCOPE:layer=…,removal=plugin` | Binary works but loses capability if removed. Clear removal instructions inline. | Swap for an equivalent, or delete + remove wiring call. |
| **Feature** 🟢 `SCOPE:layer=…,removal=feature` | A demo/add-on. Depends on other packages (listed in comment). | Delete the package + dependent packages + remove wiring call. |

---

## Features (features/)

These are **product-level demos** — what the end user sees. All are Feature layer: you can remove, replace, or keep them.

| Feature | Package | SCOPE | Router wiring | Remove by |
|---------|---------|:-----:|---------------|-----------|
| **Auth** | `features/auth/` | 🟢/🔴 | `auth.RegisterAuth(se)` | Delete package + remove call |
| **Landing page** | `features/landing/` | 🟢 FEATURE | `landing.New(cfg).RegisterRoutes(se)` (registered on `se.Router` directly) | Delete package + remove call. The todo demo is at `/todo`; root becomes 404 or your replacement. |
| **Read-only config view** | `features/config/` | 🟢 FEATURE | `config.New(cfg).RegisterRoutes(se)` (registered on `se.Router` directly) | Delete package + remove call. Page is auth-gated; secrets are masked via `mask.go` before render. |
| **Todo** | `features/todo/` | 🟢 FEATURE | `todoH.RegisterRoutes(se)` | Delete package + remove block |
| **Whiteboard** | `features/whiteboard/` + `internal/collab/` | 🟢 FEATURE | `registerWhiteboard(se, q)` | Delete both + `whiteboard.js` + remove call |
| **Onboarding** | `features/todo/handlers/onboarding.go` + `internal/dagnats/` | 🟢 FEATURE | `registerOnboarding(...)` | Delete both + remove call |
| **EntityStore (persistence)** | `features/store/` (interface) + `features/store/pbstore/` (default impl) + `features/store/crdtstore/` (alternative) | 🟡 PLUGIN | `todoH.SetStore(pbstore.New(app, "todos"))` | Drop the `SetStore` call from `router.Init`; handler's lazy fallback (`h.st()`) rebuilds a PBStore. Switch strategy at runtime via `ENTITY_STORE=crdt` (see `config/config.go`). |

> **⚠️ Auth is a mixed package.** The **login UI** (login page, navbar) is 🟢 FEATURE — replace with OAuth, SSO, etc. The **auth middleware** (`LoadAuthFromCookie`) is 🔴 CORE — the app's security model depends on it. They live in the same package for cohesion; if you replace the UI, keep the middleware functions.

---

## Infrastructure (internal/)

These are the **plumbing layers**. Each is independently replaceable.

| Component | Package | SCOPE | Startup | Swap / Remove by |
|-----------|---------|:-----:|---------|------------------|
| **PocketBase** (DB + Auth + API) | `db/` | 🔴 CORE | `db.Init(cfg)` in `server.Run()` | Replace with your own DB + auth stack |
| **Config** | `config/` | 🔴 CORE | `config.Load()` in `main.go` | Add/remove env vars |
| **Queue + SSE Hub** (goqite) | `internal/queue/` | 🔴 CORE | `queue.New(cfg)` in `server.Run()` | Replace goqite with Redis; SSE Hub is in `ssehub.go` |
| **Event bus: NATS JetStream** | `internal/nats/` | 🟡 PLUGIN | `startNATS(cfg)` in `main.go` | Remove `startNATS` call; falls back to in-memory fan-out via SSE Hub |
| **CRUD proxy (offline sync)** | `internal/nats/crudproxy.go` | 🟡 PLUGIN | `NewCrudPublisher(js)` + `NewCrudConsumer(app, js)` in `router.Init()` | Remove `crudproxy.go`; toggle via `OFFLINE_SYNC_ENABLED=false` (default on) |
| **DagNats** (workflows) | `internal/dagnats/` | 🟡 PLUGIN | `startDagNats(cfg, pb, ...)` in `main.go` | Remove call + delete package |
| **CRDT + Sync** (Loro) | `internal/collab/` | 🟡 PLUGIN | Via `registerWhiteboard` + `registerCollabSync` | Delete with whiteboard |
| **SSE helpers** (Datastar) | `internal/datastar/` | 🟡 PLUGIN | Imported by handlers | Replace with your own SSE rendering |
| **Secrets** (age) | `internal/secrets/` | 🔴 CORE | `secrets.Load(appName)` in `config.Load()` | Remove call; env vars work without it |
| **LLM client** (GoAI) | `internal/llm/` | 🟡 PLUGIN | `llm.New(apiKey)` in `server.Run()` | Remove env var; UI auto-hides the Suggest button. *The package stays if you add your own AI feature — only the demo Suggest route is removable.* |
| **UI skins** (pluggable) | `web/skins/` (`daisyui`, `basecoat`, `morpheus`) | 🟡 PLUGIN | Imported at init via `features/todo/components/skin_imports.go` blank imports; active skin read by `config.Skin` and `?skin=` query | Delete `web/skins/` directory + drop the blank imports in `skin_imports.go` + drop the `SkinSelector` call from the navbar. The handler's lazy fallback returns DaisyUI assets when no skin is registered. See [UI skins](#ui-skins). |

> **🔴 CORE** = keep or replace the whole stack.  
> **🟡 PLUGIN** = you could remove it and still serve pages, but lose cross-instance broadcast, async jobs, etc.  
> **🟢 FEATURE** = pure demo. Delete the package, remove the wiring call, nothing breaks.

## Offline strategy for PocketBase features (todo, CRUD records)

PocketBase is a server-side SQLite database — it **cannot work offline natively.** Features like the todo app that use PocketBase directly (REST + realtime SSE) fail when the client loses connectivity. Here is the recommended strategy for our stack:

### Recommended: Service Worker + Background Sync (Web)

```
Browser                          Server
┌──────────────────┐            ┌──────────────────────┐
│  IndexedDB cache │            │  PocketBase           │
│  (Dexie.js)      │  ◄── REST ─┤  (source of truth)    │
│                  │            │                       │
│  Service Worker  │  ── POST ─►│  Realtime SSE         │
│  (Background     │  (replay   │  (when online)        │
│   Sync API)      │   offline) │                       │
└──────────────────┘            └──────────────────────┘
```

**How it works:**
1. A **Service Worker** intercepts all fetch requests to the PocketBase API
2. Online: requests pass through normally; responses are cached in an **IndexedDB** store
3. Offline: GET requests read from the cache; POST/PUT/DELETE requests are queued in IndexedDB
4. On reconnect, the Service Worker uses the **Background Sync API** to replay queued mutations
5. PocketBase **realtime SSE** auto-reconnects; the SW re-subscribes on the `online` event

**Pros:** ✅ Preserves PocketBase realtime when online | ✅ Zero backend changes | ✅ SW survives tab close
**Cons:** ❌ Requires client-side JS (not available in all Wails webviews) | ❌ Conflict resolution is LWW only

### Alternative: Loro CRDT as sync layer (like the whiteboard)

For features that need **conflict-free offline editing** (not just offline queuing), use the same Loro CRDT architecture the whiteboard uses:

1. Store the feature's data as a Loro document (JSON-like map/list)
2. Persist Loro snapshots to PocketBase (already implemented in `persist_pb.go`)
3. Use SSE Hub (in-process) + NATS (cross-instance) for realtime sync
4. Loro CRDT handles merge conflicts automatically

**Pros:** ✅ True offline-first (edit anywhere, merge automatically) | ✅ CRDT convergence
**Cons:** ❌ Loses PocketBase realtime for record-level events | ❌ Data not directly queryable via PocketBase REST API

### Decision: When to use each

| Scenario | Strategy | Why |
|----------|----------|-----|
| Simple CRUD (todo, forms) | SW + Background Sync | PB realtime works; LWW is fine for single-owner data |
| Collaborative editing (whiteboard, docs) | Loro CRDT | Conflict-free merge is essential for multi-user |
| Read-heavy, write-rare (catalog, settings) | SW cache-only | No offline writes needed; stale-while-revalidate is fine |

The **whiteboard already uses Loro CRDT**. For the **todo feature**, SW + Background Sync is the implemented path — `web/resources/static/sw.js` intercepts `/api/*` mutations, queues them in IndexedDB, and replays them via Background Sync on reconnect; the shared **`OfflineBanner`** (`internal/components/`) surfaces the offline/syncing/online state to the user. This preserves PocketBase realtime while keeping the KISS offline-queue option.

**Replay dedup (`db/idempotency_hook.go` + `db/idempotency_seed.go`, SCOPE:plugin).** PocketBase generates record IDs server-side, so a naive replay of a queued POST creates a duplicate. The fix: `createForm` sends a fresh `idem_key` UUID in the form body, and the `OnRecordCreateRequest` hook (`RegisterIdempotencyHook`) on the `todos` collection looks up an existing record with the same `(idem_key, owner)` and returns it in place of the inbound create. The field + unique index `(idem_key, owner)` are installed by `enableTodosIdempotency(col)` in `db/idempotency_seed.go`. The unique index is the race-condition safety net: two concurrent requests racing the hook see the second one fail at the DB layer with `idem_key: Value must be unique`. UPDATE/DELETE handlers are not covered: toggles are naturally idempotent (two flips cancel out), and delete-on-already-deleted is a benign 404. Onboarding start (DagNats workflow trigger) accepts a small duplicate-cost on replay — the second run creates a second set of example todos, but the durable workflow tracks them as separate runs.

---

### The three async layers — how they compose

```
                ┌──────────────────────────────────┐
                │  goqite queue (SQLite-backed)     │  ← Core: background jobs
                └─────────┬────────────┬───────────┘
                          │            │
                          ▼            ▼
              ┌──────────────────┐  ┌───────────────────┐
              │    SSE Hub       │  │  DagNats workflows│
              │ (in-process fan) │  │  (Plugin 🟡, :8090)  │
              └────────┬─────────┘  └────────┬──────────┘
                       │                     │
                       ▼                     ▼
              ┌──────────────────────────────────┐
              │         Browser SSE               │
              │        (Datastar)                 │
              └──────────────────────────────────┘

              ┌──────────────────────────────────┐
              │   NATS JetStream                 │  ← Plugin 🟡: cross-instance event bus
              │                                   │
              │  ▲ WebSyncWorker publishes ops    │
              │  ▲ DagNats persists workflow      │
              │  ▼ SyncWorker receives ops         │
              └──────────────────────────────────┘
```

- **goqite** (Core 🔴): every feature's async work. Worker pool streams progress via SSE Hub.
- **SSE Hub** (Core 🔴, in `internal/queue/ssehub.go`): in-process fan-out to browser tabs via Go channels. Per-client channels, replay buffer, backpressure. **Does not cross the NATS boundary** — it is purely in-process.
- **NATS JetStream** (Plugin 🟡): cross-instance event bus. Used by:
  - Whiteboard sync — shapes published directly via `WebSyncWorker.nc.Publish()` to subject `app.sync.<docID>` (bypasses SSE Hub entirely)
  - DagNats — workflow engine uses JetStream for durable state
  - Desktop Leaf Node — optional edge sync
  - *(Todos currently use the in-memory SSE Hub broadcaster, not NATS — the `JetStreamBroadcaster` exists in code but the startup order means it's never triggered. This is a pre-existing limitation: `server.Run(cfg, nil)` runs before `startNATS()`, so the router never receives a valid JetStream context. To fix it, either: (a) pass `startNATS()`'s JetStream context through `server.Run(cfg, js)`, or (b) follow the whiteboard's pattern of holding a direct `nc` connection reference.)*
- **DagNats** (Plugin 🟡): durable multi-step workflows as declarative JSON. Uses NATS JetStream for state.

---

## UI skins (pluggable)

The same UI is rendered through one of three pluggable skins, compiled into the same binary. The active skin is selected by `cfg.Skin` (env `UI_SKIN`, default `"daisyui"`) or per-request via the `?skin=` query string. The navbar exposes a `SkinSelector` widget that updates the query param and reloads.

| Skin | Package | What it is | Source CSS |
|------|---------|-----------|------------|
| **DaisyUI** (default) | `web/skins/daisyui/` | DaisyUI v5 components over TailwindCSS; morph-friendly with Datastar | `src/css/input.css` → `app.min.css` |
| **Basecoat** | `web/skins/basecoat/` | BasecoatUI (shadcn-style) with shadcn-inspired OKLCH `@theme inline` tokens; native Basecoat JS runtime (`basecoat.initAll`) debounced via `requestAnimationFrame` for Datastar DOM morphing | `src/css/basecoat-input.css` → `basecoat.min.css` + `basecoat.min.js` |
| **Morpheus** | `web/skins/morpheus/` | Vendorized web-components bundle (SHA-pinned, `VENDOR_SHA`); gives the todo demo a different visual treatment without DaisyUI | `morpheus/bundle.js` + `morpheus.css` + `theme-default.css` |

**Plugin contract** (`web/skins/skin.go`). Every skin is a `Skin{Name, Assets}` value registered at init time via blank imports in `features/todo/components/skin_imports.go`. The dispatcher falls back to DaisyUI when the env value is unknown, logging a warning. Adding a fourth skin is: create `web/skins/<name>/`, register it from the import file, add a `make css-<name>` Makefile target.

**Skin-aware SSE patches.** Morpheus uses web components, not DaisyUI; some fragments need to render with the matching skin's HTML (e.g. morpheus todo cards). The skin dispatcher threads the active skin name into the SSE patch so `MergeSignals` + `RenderAndPatch` produce skin-correct markup. See `web/skins/morpheus/todo_morpheus.templ`.

**Removal.** Delete `web/skins/`, drop the blank imports in `features/todo/components/skin_imports.go`, drop the `SkinSelector` call from the navbar. The handler's lazy fallback returns DaisyUI assets when no skin is registered.

---

## Startup order (cmd/web/main.go)

```
main.go
  ├─ config.Load()              ← 🔴 Core: reads env vars + age secrets
  ├─ server.Run(cfg, nil)       ← 🔴 Core: PocketBase + queue + router.Init
  │   └─ router.Init(app, q, cfg, js, todoH)   # js is nil here (see note below)
  │       ├─ static files         🔴 Core
  │       ├─ auth.RegisterAuth    🟢 FEATURE UI + 🔴 CORE middleware
  │       ├─ todo routes          🟢 FEATURE
  │       │   └─ CrudPublisher (if cfg.OfflineSync.Enabled && js != nil)
  │       ├─ registerOnboarding   🟢 FEATURE
  │       ├─ registerWhiteboard   🟢 FEATURE (creates DocStore, separate SSEHub)
  │       ├─ registerCollabSync   🟢 FEATURE (NATS listener)
  │       └─ registerCrudConsumer 🟡 PLUGIN (if cfg.OfflineSync.Enabled)
  ├─ startDagNats(cfg, pb, ...) ← 🟡 PLUGIN: boots engine (NATS on :4222)
  ├─ startNATS(cfg)             ← 🟡 PLUGIN: connects or starts embedded NATS
  └─ pb.Start()                 ← 🔴 Core: serves HTTP
```

> **OfflineSync toggle.** The entire offline sync stack is gated by `cfg.OfflineSync.Enabled`. When `false`: CrudPublisher is nil (handler's publishCrudOp is a no-op), registerCrudConsumer is skipped, and no NATS CRUD stream is created. Set `OFFLINE_SYNC_ENABLED=false` in production for an always-online deployment with zero overhead.

> **Note on startup order:** `server.Run(cfg, nil)` is called BEFORE `startDagNats` and `startNATS`. This means `js` (the JetStream context) is always `nil` when `router.Init` runs. As a result, `newTodoBroadcaster(nil, hub)` always falls back to `InMemoryBroadcaster`. The whiteboard bypasses this limitation by publishing to NATS directly via `WebSyncWorker.nc.Publish()`. If you need NATS-backed todo broadcasting, pass the JetStream context through `server.Run(cfg, js)` instead of `nil`.

> **CRDT store transport (optional, post-`Init`).** If the configured store is a `*crdtstore.CRDTStore`, `main.go` wires its JetStream transport and SSE Hub publisher *after* `startNATS()` (post-`router.Init`) via `server.WireCRDTStoreTransport` / `server.WireCRDTStorePublisher`. This is the one piece of realtime wiring that intentionally runs after `Init` because it needs the live JetStream context that `startNATS` produces.

---

## Adding a new feature

1. Create `features/<name>/` with your HTTP handlers + Templ components.
2. Create a `RegisterRoutes(se, deps)` function that wires your routes.
3. Call it from `router/router.go` → `Init()`.
4. Add a "Remove by" comment.
5. Add a row to the Features table above.

New infra should go in `internal/<name>/`. Same pattern: create the package, wire it in `main.go` or `router.go`, add a "Swap / Remove by" comment.

---

## File tree

```
cmd/web/main.go            Entry point (PB + goqite + SSE Hub + DagNats + NATS)
config/config.go           Env-based config (incl. UI_SKIN, ENTITY_STORE, BUILD_LABEL/COMMIT)
db/pocketbase.go           PocketBase + seed (incl. idempotency hook)
features/                  Demo features (🟢 FEATURE, except auth middleware 🔴 + store 🟡)
  auth/                      Login/logout/cookie (UI 🟢, middleware 🔴)
  landing/                   Public marketing hero on GET / (🟢 FEATURE)
  config/                    Auth-gated read-only /config view (🟢 FEATURE, with masked secrets)
  store/                     EntityStore interface (🟡 PLUGIN) + pbstore (default) + crdtstore (Loro)
  todo/                      Todo MVC (the reference implementation)
    handlers/                  HTTP routes + SSE stream + onboarding
    components/                Templ components (layout, todo_item, todo_list, skin_imports) — offline indicator is the shared `OfflineBanner` in `internal/components/`
  whiteboard/                Collaborative canvas
internal/                  Infrastructure
  queue/                     goqite + SSE Hub + workers + retry + handler registry
  nats/                      Embedded NATS + JetStream broadcaster + CRUD proxy
  dagnats/                   DagNats client + workflow definitions
  collab/                    Loro CRDT + DocStore + sync workers + presence
  llm/                       GoAI LLM client + simulated client (SSE-streaming suggest)
  components/                Shared UI helpers (Toast + OfflineBanner offline indicator)
  datastar/                  Datastar SSE rendering helpers
  secrets/                   age-decrypted secrets loader
router/router.go            Route wiring (central dependency graph)
web/
  resources/static/          Embedded JS/CSS assets (app.min.css, basecoat.min.css, morpheus/, sw.js, theme.js)
  skins/                     Pluggable UI skin registry (daisyui + basecoat + morpheus) 🟡 PLUGIN
```

### Routing notes

- **`GET /api` and `GET /api/` are served EXACTLY** (registered with both forms on `se.Router`). PocketBase's subtree-matching path handling would otherwise let a trailing slash drift; serving both keeps the URL the PB admin UI advertises (`/_/`'s realtime banner shows `/api`) matching the actual endpoint.
- **Static assets are exact-match only** (`/static/<file>`, no wildcards). PB's catch-all shadows anything more permissive; using exact routes prevents the next maintainer's regex from accidentally serving `web/resources/static/app.min.css.map` from the wrong origin.
- **Page-style handlers register DIRECTLY on `se.Router`** inside the `OnServe` hook (todo, whiteboard, landing, config, SkinSelector). Nested `OnServe().BindFunc` calls **never fire** in PB's middleware chain — register at the top-level `OnServe` or use `se.Router.GET(...)` as every feature in this repo does.
