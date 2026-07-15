# Architecture — LLM Entrypoint

> This file is the **canonical entrypoint for LLM agents** navigating the template.
> Each row describes one component: where it lives, what it depends on, and how to remove or replace it.

---

## Principles

- **Unified build.** One `go build ./cmd/web` compiles everything. No build tags. Opt out at runtime with env vars (`NATS_ENABLED=false`, `DAGNATS_ENABLED=false`).
- **Features in `features/`**, infrastructure in `internal/`. Features depend on infra, never the reverse.
- **Wiring in `router/router.go` → `Init()`**. Every feature is registered by one function call.
- **Startup order in `cmd/web/main.go`** (see diagram below).

---

> **SCOPE annotations in code.** Every source file carries a `SCOPE` comment at the top. This table uses the same taxonomy so the two are always in sync. The `SCOPE` annotations in source files are the authoritative reference; this table is a summary.

## Layer taxonomy (SCOPE)

Every component belongs to one of three layers, matching the `SCOPE:` annotation in each source file:

| SCOPE | Meaning | You would… |
|:------|:--------|:-----------|
| **Core** 🔴 `SCOPE:core` | Binary does not work without it. | Customize, never remove. |
| **Pluggable** 🟡 `SCOPE:pluggable` | Binary works but loses capability if removed. Clear removal instructions inline. | Swap for an equivalent, or delete + remove wiring call. |
| **Feature** 🟢 `SCOPE:feature` | A demo/add-on. Depends on other packages (listed in comment). | Delete the package + dependent packages + remove wiring call. |

---

## Features (features/)

These are **product-level demos** — what the end user sees. All are Feature layer: you can remove, replace, or keep them.

| Feature | Package | SCOPE | Router wiring | Remove by |
|---------|---------|:-----:|---------------|-----------|
| **Auth** | `features/auth/` | 🟢/🔴 | `auth.RegisterAuth(se)` | Delete package + remove call |
| **Todo** | `features/todo/` | 🟢 FEATURE | `todoH.RegisterRoutes(se)` | Delete package + remove block |
| **Whiteboard** | `features/whiteboard/` + `internal/collab/` | 🟢 FEATURE | `registerWhiteboard(se, q)` | Delete both + `whiteboard.js` + remove call |
| **Onboarding** | `features/todo/handlers/onboarding.go` + `internal/dagnats/` | 🟢 FEATURE | `registerOnboarding(...)` | Delete both + remove call |
| **EntityStore (persistence)** | `features/store/` (interface) + `features/store/pbstore/` (default impl) | 🟡 PLUGGABLE | `todoH.SetStore(pbstore.New(app, "todos"))` | Drop the `SetStore` call from `router.Init`; handler's lazy fallback (`h.st()`) rebuilds a PBStore. Add a new impl (e.g. `features/store/crdtstore/`) and switch the wire call — zero changes in the handlers. |

> **⚠️ Auth is a mixed package.** The **login UI** (login page, navbar) is 🟢 FEATURE — replace with OAuth, SSO, etc. The **auth middleware** (`LoadAuthFromCookie`) is 🔴 CORE — the app's security model depends on it. They live in the same package for cohesion; if you replace the UI, keep the middleware functions.

---

## Infrastructure (internal/)

These are the **plumbing layers**. Each is independently replaceable.

| Component | Package | SCOPE | Startup | Swap / Remove by |
|-----------|---------|:-----:|---------|------------------|
| **PocketBase** (DB + Auth + API) | `db/` | 🔴 CORE | `db.Init(cfg)` in `server.Run()` | Replace with your own DB + auth stack |
| **Config** | `config/` | 🔴 CORE | `config.Load()` in `main.go` | Add/remove env vars |
| **Queue + SSE Hub** (goqite) | `internal/queue/` | 🔴 CORE | `queue.New(cfg)` in `server.Run()` | Replace goqite with Redis; SSE Hub is in `ssehub.go` |
| **Event bus: NATS JetStream** | `internal/nats/` | 🟡 PLUGGABLE | `startNATS(cfg)` in `main.go` | Remove `startNATS` call; falls back to in-memory fan-out via SSE Hub |
| **CRUD proxy (offline sync)** | `internal/nats/crudproxy.go` | 🟡 PLUGGABLE | `NewCrudPublisher(js)` + `NewCrudConsumer(app, js)` in `router.Init()` | Remove `crudproxy.go`; toggle via `OFFLINE_SYNC_ENABLED=false` (default on) |
| **DagNats** (workflows) | `internal/dagnats/` | 🟡 PLUGGABLE | `startDagNats(cfg, pb, ...)` in `main.go` | Remove call + delete package |
| **CRDT + Sync** (Loro) | `internal/collab/` | 🟡 PLUGGABLE | Via `registerWhiteboard` + `registerCollabSync` | Delete with whiteboard |
| **SSE helpers** (Datastar) | `internal/datastar/` | 🟡 PLUGGABLE | Imported by handlers | Replace with your own SSE rendering |
| **Secrets** (age) | `internal/secrets/` | 🔴 CORE | `secrets.Load(appName)` in `config.Load()` | Remove call; env vars work without it |
| **LLM client** (GoAI) | `internal/llm/` | 🟡 PLUGGABLE | `llm.New(apiKey)` in `server.Run()` | Remove env var; UI auto-hides the Suggest button. *The package stays if you add your own AI feature — only the demo Suggest route is removable.* |

> **🔴 CORE** = keep or replace the whole stack.  
> **🟡 PLUGGABLE** = you could remove it and still serve pages, but lose cross-instance broadcast, async jobs, etc.  
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

**Replay dedup (`db/idempotency_hook.go` + `db/idempotency_seed.go`, SCOPE:pluggable).** PocketBase generates record IDs server-side, so a naive replay of a queued POST creates a duplicate. The fix: `createForm` sends a fresh `idem_key` UUID in the form body, and the `OnRecordCreateRequest` hook (`RegisterIdempotencyHook`) on the `todos` collection looks up an existing record with the same `(idem_key, owner)` and returns it in place of the inbound create. The field + unique index `(idem_key, owner)` are installed by `enableTodosIdempotency(col)` in `db/idempotency_seed.go`. The unique index is the race-condition safety net: two concurrent requests racing the hook see the second one fail at the DB layer with `idem_key: Value must be unique`. UPDATE/DELETE handlers are not covered: toggles are naturally idempotent (two flips cancel out), and delete-on-already-deleted is a benign 404. Onboarding start (DagNats workflow trigger) accepts a small duplicate-cost on replay — the second run creates a second set of example todos, but the durable workflow tracks them as separate runs.

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
              │ (in-process fan) │  │  (Plugin, :8090)  │
              └────────┬─────────┘  └────────┬──────────┘
                       │                     │
                       ▼                     ▼
              ┌──────────────────────────────────┐
              │         Browser SSE               │
              │        (Datastar)                 │
              └──────────────────────────────────┘

              ┌──────────────────────────────────┐
              │   NATS JetStream                 │  ← Infra: cross-instance event bus
              │                                   │
              │  ▲ WebSyncWorker publishes ops    │
              │  ▲ DagNats persists workflow      │
              │  ▼ SyncWorker receives ops         │
              └──────────────────────────────────┘
```

- **goqite** (Core 🔴): every feature's async work. Worker pool streams progress via SSE Hub.
- **SSE Hub** (Core 🔴, in `internal/queue/ssehub.go`): in-process fan-out to browser tabs via Go channels. Per-client channels, replay buffer, backpressure. **Does not cross the NATS boundary** — it is purely in-process.
- **NATS JetStream** (Infra 🟡): cross-instance event bus. Used by:
  - Whiteboard sync — shapes published directly via `WebSyncWorker.nc.Publish()` to subject `app.sync.<docID>` (bypasses SSE Hub entirely)
  - DagNats — workflow engine uses JetStream for durable state
  - Desktop Leaf Node — optional edge sync
  - *(Todos currently use the in-memory SSE Hub broadcaster, not NATS — the `JetStreamBroadcaster` exists in code but the startup order means it's never triggered. This is a pre-existing limitation: `server.Run(cfg, nil)` runs before `startNATS()`, so the router never receives a valid JetStream context. To fix it, either: (a) pass `startNATS()`'s JetStream context through `server.Run(cfg, js)`, or (b) follow the whiteboard's pattern of holding a direct `nc` connection reference.)*
- **DagNats** (Plugin 🟢): durable multi-step workflows as declarative JSON. Uses NATS JetStream for state.

---

## Startup order (cmd/web/main.go)

```
main.go
  ├─ config.Load()              ← 🔴 Core: reads env vars + age secrets
  ├─ server.Run(cfg, js)       ← 🔴 Core: PocketBase + queue + router.Init
  │   └─ router.Init(app, q, cfg, js, todoH)
  │       ├─ static files         🔴 Core
  │       ├─ auth.RegisterAuth    🟢/🔴 Plugin UI + Core middleware
  │       ├─ todo routes          🟢 Plugin
  │       │   └─ CrudPublisher (if cfg.OfflineSync.Enabled && js != nil)
  │       ├─ registerOnboarding   🟢 Plugin
  │       ├─ registerWhiteboard   🟢 Plugin (creates DocStore, separate SSEHub)
  │       ├─ registerCollabSync   🟢 Plugin (NATS listener)
  │       └─ registerCrudConsumer 🟢 Plugin (if cfg.OfflineSync.Enabled)
  ├─ startDagNats(cfg, pb, ...) ← 🟢 Plugin: boots engine (NATS on :4222)
  ├─ startNATS(cfg)             ← 🟡 Infra: connects or starts embedded NATS
  └─ pb.Start()                 ← 🔴 Core: serves HTTP
```

> **OfflineSync toggle.** The entire offline sync stack is gated by `cfg.OfflineSync.Enabled`. When `false`: CrudPublisher is nil (handler's publishCrudOp is a no-op), registerCrudConsumer is skipped, and no NATS CRUD stream is created. Set `OFFLINE_SYNC_ENABLED=false` in production for an always-online deployment with zero overhead.

> **Note on startup order:** `server.Run(cfg, nil)` is called BEFORE `startDagNats` and `startNATS`. This means `js` (the JetStream context) is always `nil` when `router.Init` runs. As a result, `newTodoBroadcaster(nil, hub)` always falls back to `InMemoryBroadcaster`. The whiteboard bypasses this limitation by publishing to NATS directly via `WebSyncWorker.nc.Publish()`. If you need NATS-backed todo broadcasting, pass the JetStream context through `server.Run(cfg, js)` instead of `nil`.

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
cmd/web/main.go            Entry point
config/config.go           Env-based config
db/pocketbase.go           PocketBase + seed
features/                  Demo features (all 🟢 Plugin)
  auth/                      Login/logout/cookie (UI 🟢, middleware 🔴)
  todo/                      Todo MVC (the reference implementation)
    handlers/                  HTTP routes + SSE stream + onboarding
    components/                Templ components (layout, todo_item, toast) — offline indicator is the shared `OfflineBanner` in `internal/components/`
  whiteboard/                Collaborative canvas
internal/                  Infrastructure
  queue/                     goqite + SSE Hub + workers + retry + handler registry
  nats/                      Embedded NATS + JetStream broadcaster
  dagnats/                   DagNats client + workflow definitions
  collab/                    Loro CRDT + DocStore + sync workers + presence
  llm/                       GoAI LLM client + simulated client
  components/                Shared UI helpers (Toast + OfflineBanner offline indicator)
  datastar/                  Datastar SSE rendering helpers
  secrets/                   age-decrypted secrets loader
router/router.go            Route wiring (central dependency graph)
web/resources/static/       Embedded JS/CSS assets
```
