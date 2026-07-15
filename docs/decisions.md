# Architectural Decisions

## Why goqite (Not NATS JetStream) as Default Task Queue

goqite is a SQLite-backed queue that runs in-process. Zero external dependencies.
- ✅ Fire-and-forget jobs with SSE streaming via Hub
- ✅ No network calls, no broker process
- ✅ ~18.5k msgs/s — enough for LLM calls, email, etc.

JetStream is available as an **additional** layer for multi-user real-time, when needed.

## Why DagNats for Durable Workflows

[DagNats](https://github.com/danmestas/dagnats) is a DAG-based durable workflow engine built on NATS JetStream. Workflows are **declarative JSON** (not Go code): the workflow references task *names* (strings), not Go symbols, so renaming a Go handler never orphans an in-flight run. Each step's result is recorded in the event-sourced history; on crash, the workflow resumes from the last completed step.
- ✅ Multi-step transactions that survive restarts
- ✅ Native in-step suspend via `WaitForSignal` (the engine blocks a step until an external signal arrives)
- ✅ Step retries with exponential backoff
- ✅ Scheduling (cron), human-in-the-loop approvals, sub-workflows, agent loops
- ✅ Runs as a library (the `server` package boots an embedded NATS + orchestrator + REST API + console) — no separate service to operate

DagNats is opt-in via the `dagnats` build tag. It boots an embedded NATS on the conventional port `:4222` and exposes its REST API + console on `DAGNATS_HTTP_ADDR` (default `127.0.0.1:8090`), separate from the app port. **Single-NATS convention:** under `-tags "jetstream dagnats"`, the realtime `TodoBroadcaster` does NOT start its own NATS — it connects to the one DagNats already owns on `:4222` (see `cmd/web/nats.go` → `ConnectExisting`). One NATS process, two consumers (DagNats workflows + JetStream realtime). Building `-tags dagnats` alone keeps the in-memory broadcaster (single-instance), since there is no `jetstream` tag to provide the JetStream-backed one.

## Why Three Async Layers (Complementary, Not Alternatives)

| Layer | Problem It Solves | When You Need It |
|-------|-------------------|------------------|
| goqite | "I need to run background tasks and notify the user" | Always (default) |
| dagnats | "I need N steps that survive a crash" | Complex onboarding, pipelines (opt-in build tag) |
| NATS JetStream | "Multiple users need to see the same live state" | Whiteboard, presence, shared UI (opt-in build tag) |

You can have all three in the same binary. They do not conflict.

## Why PocketBase (Not Plain SQLite)

PocketBase embeds as a Go library and provides:
- ✅ Built-in auth (OTP, OAuth2, JWT)
- ✅ Automatic REST API for collections
- ✅ Realtime subscriptions
- ✅ Admin dashboard
- ✅ File storage

Plain SQLite is available as an escape hatch when PocketBase is too opinionated.

## Why age + ~/.secrets/ (Not Doppler/Vault)

For 1-2 developer teams with <20 secrets:
- ✅ Zero external services
- ✅ Single binary (age is static-linked)
- ✅ No cloud dependency
- ✅ Simple mental model

Move to Doppler/Vault when the team grows or secrets exceed 20.

## v0.18.0 — Offline todo add + CI flake + CSS staleness (post-mortem)

Three issues shipped together in v0.18.0 (`4741055`). Each taught a separate lesson; together they form the "testing discipline" section in `AGENTS.md`.

### 1. Offline todo add button stuck in loading

**Symptom (live, gogogo.calionauta.com):** click Add while offline → button enters loading state and never returns. No banner. No feedback.

**Investigation:**
- `diff <(curl https://gogogo.calionauta.com/static/sw.js) <(repo web/resources/static/sw.js)` → byte-identical. Deploy was current (v0.17.0). Not a stale build.
- `grep` on `features/todo/components/todo_list.templ` → `createForm` does `if (!$loading) { $loading = true; @post('/api/todos?clientID=...', {contentType: 'form'}); }`. `$loading` is reset only by the server's HTML fragment response.
- Read `web/resources/static/sw.js` → on offline POST, `networkFirstWithQueue` queues in IndexedDB and returns `new Response(JSON.stringify({queued:true,...}), {status: 202})`. Datastar's `@post` sees a 2xx → resolves the promise → but the body is JSON, not an HTML fragment, so it patches nothing → `$loading` stays `true` forever → button stuck.

**Why no banner?** `OfflineBanner` should have shown via either the SW `sync-error` message or `navigator.onLine === false`. Hard to tell from the report whether the user actually saw it (browsers vary on the offline toggle's effect on `navigator.onLine`), but the primary symptom was the stuck button.

**Fix (`4741055`):**
- `internal/components/offline_banner.templ`: when the SW posts `sync-error`, dispatch `window.dispatchEvent(new CustomEvent('gogogo:queued'))`. Inline JS — unavoidable for SW bridge.
- `features/todo/components/todo_list.templ`: on the signals root, `data-on:gogogo:queued__window="$loading = false"`. Pure Datastar expression, zero JS.

**Design note:** the bridge stays inline (locality of behavior) because Datastar expressions can't subscribe to SW `postMessage`. The consumer is fully declarative — no per-form listener to add. Any future form that flips its own `$loading` gets the reset for free by mounting the OfflineBanner in its layout.

### 2. TestCrudConsumerCreate "flaky" — actually a Bootstrap panic

**Symptom:** `make test` failed ~every run with `--- FAIL: TestCrudConsumerCreate (0.01s)` in `internal/nats`. Isolation passes (`go test -run TestCrudConsumerCreate ./internal/nats/`).

**Investigation:**
- `make test > /tmp/suite.out 2>&1` then `grep -B 2 -A 15 'FAIL: TestCrudConsumerCreate' /tmp/suite.out` → revealed `panic: DBConnect config option must be set when the no_default_driver tag is used!` from `pocketbase/core.DefaultDBConnect`.
- Root cause: `Makefile`'s `go test $(TAGS) ...` lines forced `-tags no_default_driver` on every test binary. That tag (used legitimately by the shipped binary to exclude PB's bundled `modernc/sqlite` for size) requires every `app.Bootstrap()` to supply a `DBConnect`. Our tests use the default modernc driver, so Bootstrap panicked on first PB init.
- 0.01s timing + intermittent appearance in full suite (but not isolation) was the giveaway that this was a **build-config error masquerading as a race** — the consumer `t.Logf("consumer Run exited: connection closed")` from a previous test's goroutine was a red herring.

**Fix:** `Makefile`'s three `go test` recipes (line 44, 115, 153) no longer pass `$(TAGS)`. Build recipes keep it — the shipped binary still excludes modernc via `cmd/web`'s DBConnect (ncruces).

**Bonus hardening:** `internal/nats/embedded.go` `StartEmbedded` now nils `NS/NC/JS` on entry and `Stop` nils on exit. Defensive against any future cross-package leak (the package globals had no zero-reset, so a partially-torn-down state could be inherited).

### 3. CSS bundle silently stale

**Symptom:** `make ci-local` failed at `css-check`: "❌ CSS out of date. Run `make css` and re-commit." Even on a clean stash (no working changes). `git show HEAD:web/resources/static/app.min.css` → 272,022 bytes; `make css` produced 176,689 bytes (95 KB smaller, 3,229 lines deleted).

**Investigation:**
- `npm ci` is deterministic (lockfile pinned), so the build wasn't pulling newer deps between commits.
- Diff of utility classes: the committed bundle had `alert-*`, `badge-accent`, `btn-circle`, `bg-base-100`, etc. that no `.templ` file currently uses. Someone simplified/removed features earlier and never re-ran `make css`.
- `css-check` passed in CI by inertia: it runs `git diff --quiet --exit-code web/resources/static/app.min.css` after `make css`. When nobody touched the file, working tree == HEAD == checked-in stale bundle → no diff → "passes." The check only fires when something actually rebuilds.

**Fix (`c24d3f3`):** rebuild the CSS from current `.templ` sources, commit the smaller bundle. `css-check` now passes legitimately.

**Rule added to AGENTS.md:** any change to a `.templ` file must be followed by `make templ && make css`. The pre-commit hook already runs `make check` (which includes `css-check`), but the hook only fires when staged `.templ` files change; partial rebuilds (only `_templ.go`, not `app.min.css`) slip past it.

### Lessons

1. **Build tags are not test tags by default.** Tags that change lib behavior (drivers, feature gates) for the binary must be explicitly excluded from `go test`, OR every test bootstrap must supply the equivalent setup. Failure mode: panic with a confusing message, easy to misread as a race.
2. **Don't blame flake for a 0.01s panic.** Test ordering can mask real errors (the consumer `Run` `t.Logf` was a red herring; the real failure was earlier, in `Bootstrap`). Always dump the full panic stack on suite failure, not just `FAIL|ok` lines.
3. **The cheapest "is the fix live?" test is a byte diff of an embedded asset.** `diff <(curl /static/<asset>) <(repo <asset>)` — if it matches, the running binary embeds the latest source. Took seconds to confirm v0.17.0 was deployed.
4. **Stale committed artifacts (CSS, generated files) can pass CI by inertia** when no rebuild is triggered. Make the rebuild part of the change that introduces the source drift, or have a guard that fails when generated files are older than their sources.
5. **Locality of behavior for the bridge, declarative for the consumer.** The OfflineBanner script stays inline (SW postMessage isn't reachable from Datastar expressions); the consumer side (`data-on:gogogo:queued__window`) is pure Datastar. No per-form glue code; future features inherit the fix by mounting the banner.

## v0.19.0 — Idempotent offline replay (PocketBase hook + unique index)

Reformulated from v0.18.0's middleware approach (which broke SSE handlers — the bufWriter didn't implement `http.Flusher`, so `sdk.NewSSE`'s `rc.Flush()` panicked). Replaced with PocketBase-native dedup.

### Why

PocketBase generates record IDs server-side. A Service Worker that replays a queued POST after reconnect creates a *new* record instead of returning the original. Documented in `sw.js` since v0.18.0 as "production would need a client-generated UUID."

### The fix

`db/seed.go` adds an `idem_key` field (text, nullable, max 100) to the `todos` collection and a unique index `(idem_key, owner)`. `db/idempotency_hook.go` registers an `OnRecordCreateRequest` hook: if the inbound record carries a non-empty `idem_key` and an `owner`, it looks up an existing record with the same `(idem_key, owner)` and, on hit, replaces `e.Record` with the existing one and returns nil (PB treats a missing `e.Next()` as "handled" — no error, no create). The unique index is the safety net for the race: two concurrent requests racing the hook see the second one fail at the DB layer with `idem_key: Value must be unique`.

The UI generates a fresh UUID per click (`crypto.randomUUID()`) and includes it in the form body via a hidden `<input name="idem_key">`. The SW forwards the form verbatim on replay.

### Why this approach over the middleware

- **Doesn't touch `core.RequestEvent`.** The bufWriter in the previous attempt intercepted the response writer; `sdk.NewSSE` calls `rc.Flush()` to start the stream and the wrap silently broke SSE. PB hooks run in the PB core layer (no ResponseWriter), so SSE is unaffected.
- **DB-level dedup.** Permanent (survives restart), multi-instance safe (DB is source of truth). The in-memory LRU only worked single-instance and lost state on restart.
- **PocketBase-native.** The hook pattern is documented officially and is the standard extension point for the framework. The in-memory LRU approach taught nothing reusable.
- **Covered by Stack Overflow + PB docs.** Multiple threads (#545, #2593, PB docs JS Event Hooks) recommend unique-index + hook for dedup. The pattern is known.

### Why only CREATE

The user originally asked for "all handlers" to be wrapped, but the natural idempotency profile differs per mutation:

| Handler | Replay dedup needed? | Why |
|---|---|---|
| `POST /api/todos` (create) | YES — hook + index | Naive replay creates a visible duplicate. |
| `POST /api/todos/{id}/toggle` | NO — naturally idempotent | Two flips cancel; final state identical. |
| `POST /api/todos/{id}/delete` | NO — naturally idempotent | Second delete is a benign 404. |
| `POST /api/todos/completed/delete` | NO | Same: empty result on second call. |
| `POST /api/todos/suggest` | Minor | Replay enqueues a second LLM call. Cost ~1s, not user-visible. |
| `POST /api/onboarding/start` | Minor | Replay starts a second workflow run → 3 more example todos. Acceptable for a demo. |

Only CREATE has a user-visible bug on replay. The hook + index covers it.

### Files

- `db/seed.go` — adds `idem_key` field + `(idem_key, owner)` unique index to the `todos` collection. Idempotent (only adds if missing).
- `db/idempotency_hook.go` — `RegisterIdempotencyHook(app)` installs the `OnRecordCreateRequest` hook. Called once from `SeedDefaults` (outside `OnServe`, so it survives every serve start without re-binding).
- `db/idempotency_hook_test.go` — E2E test for the (idem_key, owner) unique index. The PB request hook is exercised end-to-end by the live site's manual offline-replay test (direct `app.Save` bypasses HTTP hooks — that's PB's design).
- `features/todo/components/todo_list.templ` — `createForm` includes a hidden `<input name="idem_key" data-bind="idemKey">` and sets `$idemKey = crypto.randomUUID()` before posting. Other forms reverted to plain `@post(...)`.
- `web/resources/static/sw.js` — comment updated; production caveat removed.
- `ARCHITECTURE.md` — offline strategy documents the hook approach + per-handler natural-idempotency analysis.
- `AGENTS.md` — SCOPE table + removal instructions updated.

### Why not also handle UPDATE/DELETE with hooks?

`OnRecordUpdateRequest` and `OnRecordDeleteRequest` exist and could similarly dedup by idem_key. But the value-to-effort ratio is poor: toggle 2× is naturally idempotent, delete 2× is a benign 404. Adding two more hooks + body field plumbing to handle a non-bug is YAGNI. If a future feature mutation is *not* naturally idempotent (e.g. incrementing a counter), the same hook pattern applies.

### Why not NATS JetStream MsgId?

JetStream's `Nats-Msg-Id` header dedup is built-in but operates at the **publish→consume** boundary inside JetStream, not at the HTTP→PocketBase boundary where this bug lives. The CrudPublisher runs *after* the handler creates the record, so JetStream can't intercept the duplicate at insert time. JetStream MsgId would be the right place to dedup if the entire CRUD path went through JetStream (no direct HTTP to PocketBase) — that's a bigger architecture shift, out of scope for this fix.

### Why not a Go library?

Surveyed 5 active 2026 libs (`eben-vranken/idempo`, `polanski13/idemkit`, `velmie/idempo`, `fco-gt/gopotency`, `bright-room/idem`). None integrate with PocketBase's request hook signature; all require Redis or Postgres as a separate store; stars 0-11 (bus-factor risk). The PB hook approach uses the existing SQLite (no extra infra), is the documented extension pattern, and survives multi-instance.

## v0.20.0 — Pluggable persistence (EntityStore strategy pattern)

Adds a swap point for how domain entities (today: Todo) are persisted, so the same HTTP layer can back onto PocketBase records today and onto Loro CRDT + JetStream tomorrow without rewriting handlers.

### Why

Two persistence backends make sense for the template's evolution:

- **PocketBase records** (current): simple, single-user, SQL-queryable, integrates with the admin UI. Replay dedup via the OnRecordCreateRequest hook + (idem_key, owner) unique index (v0.19.0).
- **Loro CRDT + JetStream** (future, "scenario B" from the conversation): multi-user collaborative, conflict-free merge, offline-first by design, dedup via JetStream MsgId.

Forcing a choice between them in `cmd/web/main.go` would mean either committing to one forever or writing conditional code paths in every handler. The Strategy pattern + a generic Go interface lets us pick at startup and add a second backend in a single new file.

### The interface (`features/store/store.go`)

```go
type EntityStore[T any] interface {
    Create(ctx, e T, ownerID, idemKey string) (T, error)
    Get(ctx, ownerID, id string) (T, error)
    List(ctx, ownerID, filter string) ([]T, error)
    Update(ctx, ownerID, id string, patch map[string]any) (T, error)
    Delete(ctx, ownerID, id string) error
    ClearCompleted(ctx, ownerID string) (int, error)
    Count(ctx, ownerID string) (int, error)
}
```

Generic so future entities (Note, Task, ...) reuse the pattern with their own domain type. The compile-time guard `var _ store.EntityStore[todo.Todo] = (*pbstore.PBStore)(nil)` in `features/todo/handlers/todo_repo.go` makes the build fail loudly if PBStore drifts from the interface.

### The default strategy (`features/store/pbstore/pbstore.go`)

Extracted from the inline logic that used to live in `features/todo/handlers/todo_repo.go` + `todo_crud.go`. PBStore wraps PocketBase records: each entity is a record, ownership is a relation field, idempotency is delegated to the existing OnRecordCreateRequest hook + the (idem_key, owner) unique index added in v0.19.0.

### Wiring

`router/router.go` instantiates `pbstore.New(app, "todos")` and calls `todoH.SetStore(s)`. Tests that don't call `SetStore` keep working via the `h.st()` lazy fallback (rebuilds a PBStore on first use). Production wiring is explicit so the choice is grep-able in `router.Init`.

### Future: CRDTStore (`features/store/crdtstore/` — sketch only, not implemented)

The interface is designed for a second strategy that:

- holds one Loro doc per owner (key: `todos:<owner_id>`)
- exposes the doc as a snapshot encoded to PB (`todos_snapshot` collection: `owner`, `snapshot` (Loro bytes), `version` (int))
- ships ops via JetStream with `Nats-Msg-Id = op.ID` (built-in dedup; replaces our `idem_key` field)
- rewrites PB realtime as doc-level "version bumped" events over our existing SSE Hub

Migration path: PBStore stays as the default. `cmd/web/main.go` adds a config switch (`TODO_STORE=crdt`) that constructs the CRDTStore instead. Handlers and templates don't change.

Estimated effort for CRDTStore: ~200 LOC + Loro Go binding wiring + JetStream producer/consumer + doc↔snapshot adapter. Not trivial but contained — the interface does the heavy lifting.

### Trade-offs

- **Generics add a learning curve** for new contributors. Mitigated by a single ~150 LOC interface file with one worked example (PBStore) and a clear ADR. Adding a second strategy that doesn't fit the interface surfaces in code review.
- **Lazy fallback in `h.st()` masks missing wiring in production** if a future feature forgets to call `SetStore`. Mitigated by a defensive `var _ store.EntityStore[todo.Todo] = (*pbstore.PBStore)(nil)` compile-time guard in `todo_repo.go` and a TODO in `router.Init` to make the wiring call obvious.
- **PB Realtime stays PB-Store-specific.** A future CRDTStore would replace the realtime channel with doc-version-bumped events (see sketch above). The HTTP-layer abstraction is independent of the realtime path; both can change.
- **Tests don't cover the abstraction itself** — only each strategy's behaviour. The compile-time guard catches interface drift; behaviour drift is caught by per-strategy tests. Acceptable for now; a small fuzz-style test of the interface contract could be added later.

### Files

- `features/store/store.go` (new, SCOPE:core — the interface itself is contract, not removable)
- `features/store/pbstore/pbstore.go` (new, SCOPE:pluggable)
- `features/todo/handlers/todo.go` (added `store` field + `SetStore` setter)
- `features/todo/handlers/todo_repo.go` (delegates listTodos/saveTodo/countOwnedTodos to `h.st()`)
- `features/todo/handlers/todo_crud.go` (toggle/confirm-delete/delete/clear use `h.st()`)
- `router/router.go` (`todoH.SetStore(pbstore.New(app, "todos"))` after the broadcaster wire)
- `AGENTS.md` (SCOPE tree + removal table)
- `ARCHITECTURE.md` (Feature table)

## ADR-0017: CRDTStore cross-instance transport (Phase 2+3)

**Decision.** Make `crdtstore.CRDTStore` the second concrete
implementation of `store.EntityStore[todo.Todo]`, ship a JetStream
transport for cross-instance op convergence, and add a
signal-driven `Watch(ownerID)` channel for realtime doc-version
events.

**Why.** Phase 1 proved the Strategy pattern works (PBStore +
CRDTStore behind one `EntityStore` interface). Phase 2 closes the
gap to multi-instance production: a todo created on server A lands
on server B via Loro CRDT ops, even if B was offline when A wrote.
Phase 3 surfaces the cross-instance signal back to the UI so
clients re-fetch only when their doc actually changes, not on a
timer.

**How.**

- `transport.go` — JetStream publisher + consumer. Each binary
  generates a per-process `PublisherID`; the consumer drops ops
  whose body fingerprint matches its own PublisherID (in-process
  loop filter). Cross-process messages always carry a different
  PublisherID so the filter only drops the publisher's own
  re-deliveries.
- `crdtstore.ApplyRemoteOp(ctx, ownerID, op)` — applies a peer's
  Loro update to the local doc and bumps the version counter.
  Called from the transport's Subscribe handler.
- `cmd/web/main.go` — calls `server.WireCRDTStoreTransport` after
  `server.Run` for the CRDT strategy. The wire file sets the
  transport and starts one Subscribe per active owner.
- `Watch(ownerID)` — signal-driven buffered channel. Initial
  snapshot (current version) is sent first, then bump events. The
  SSE broadcaster (router side) calls Watch once per authenticated
  user and emits a `crdt-version` event for each value.

**Trade-offs.**

- Polling fallback was removed. If the buffered channel fills the
  consumer misses events (the next bump fills a fresh slot, so
  it always lands eventually). Callers that need every event must
  read fast or handle drop explicitly.
- The PublisherID fingerprint is 8 hex chars (~1/4B collision
  chance per pair). Acceptable for the in-process loop filter, not
  for security; the actual op identity is the `Op.ID` (UUID-style)
  which isMsgId-dedup'd by JetStream.
- Two CRDTStores in one process must use distinct PublisherIDs
  (test pattern); the production wire uses one PublisherID per
  process. Integration test enforces this with two `NewTransport`
  calls instead of shared.

**Test surface.** `crdtstore_test.go` (single-process), 
`transport_test.go` (cross-process transport), 
`integration_test.go` (cross-process CRDTStore),
`Watch` signal test (replay-first + bump events).

**Removability.** Setting `ENTITY_STORE=pb` (default) skips all of
this code at startup. To remove entirely, delete
`features/store/crdtstore/`, `internal/server/crdtstore_wire.go`,
and the `WireCRDTStoreTransport` call in `cmd/web/main.go`.

## ADR-0017 (Phase 3 closure)

The original ADR-0017 stopped at "watcher channel exposes the
counter". Phase 3 closure wires that channel to a real consumer:

1. **`crdtstore.DocPublisher` interface** — pluggable event sink
   invoked synchronously from `bumpVersion` (must not block;
   runs under `versionMu`).
2. **`server.WireCRDTStorePublisher(store, q.Hub())`** — `main.go`
   install of the SSE Hub adapter. The Hub uses
   `BroadcastToUser(payload, ownerID, "")` (no excludeClientID —
   cross-store events have no originating client).
3. **SSE handler `streamDocVersionBumped`** — new dispatch branch.
   Decodes the envelope, merges `$docVersion` + `$docVersionSeen`
   signals via Datastar.
4. **Client `$docVersion` watcher** — `realtime.templ` polls the
   signal and clicks the existing `pb-realtime-resync` button on
   change, reusing the established fragment re-fetch path.

The full chain is exercised by
`TestCRDTStore_FullPipeline_BumpPublisherFires` which uses a
`fakePublisher` instead of the SSE Hub — eliminating the goroutine
race window that a real Hub would introduce in a unit test.

**Trade-off note.** `$docVersion` is polled at 250ms because Datastar
v1 does not expose a signal-change subscribe API. Cost is one number
compare per tab per 250ms (negligible), benefit is a single
implementation that works regardless of which side produced the
event (PB record / CRDT local / CRDT peer).
