# PLAN-whiteboard.md — Minimalist whiteboard (rough.js + Loro CRDT + PocketBase)

> Investigation + implementation plan for a **broadcasted minimalist whiteboard**
> demo. Goal: a single shared canvas where multiple browser tabs draw rough.js
> shapes, edits merge conflict-free (Loro CRDT), broadcast live (SSE), and the
> final state persists in PocketBase so it survives restarts.

---

## 1. What already exists (reuse, don't rebuild)

The template already has the hard parts from PLAN.md Phase C:

- **`internal/collab`** (`collab.go`, `publish.go`, `sync.go`) — wraps
  `github.com/aholstenson/loro-go` (already in `go.mod` @ v0.5.0):
  - `collab.NewDoc(id)` → mutex-guarded `Doc`
  - `Doc.ApplyUpdate(bytes)` / `Doc.EncodeUpdate(since)` / `Doc.EncodeSnapshot()`
  - `Doc.StateVersion()` (version vector for delta computation)
- **`internal/collab.SyncWorker`** — subscribes `app.sync.<docID>` (JetStream),
  applies updates to the per-doc CRDT, persists snapshot to PocketBase
  `whiteboards` collection (upsert by `doc_id`). Wired in
  `router/collab_jetstream.go` (build tag `jetstream`); no-op otherwise.
- **`db/seed.go`** ensures the `whiteboards` collection exists
  (fields: `doc_id` text, `snapshot` text/base64, `version` int).
- **`internal/queue.SSEHub`** + `TodoBroadcaster` — already broadcast todo
  mutations to all tabs. The whiteboard can reuse the **same SSE hub** for
  live shape updates (no JetStream required for the web-only demo).
- **`Presence`** (`internal/collab`) + `GET /api/collab/presence/{docID}`
  SSE bridge — already streams peer cursors to browsers (jetstream-tagged).
- **`handleSSEStream` / `broadcastTodo` pattern** — the exact shape we copy
  for whiteboard broadcast (exclude originator via `BroadcastExcept`).

**Conclusion:** the CRDT model + PocketBase persistence are DONE. The missing
surface is the **UI scaffold** (canvas + rough.js binding + broadcast wiring)
called out as "PENDING — Whiteboard UI scaffold" in PLAN.md. This plan covers
that surface + a web-only transport path that does NOT require JetStream.

---

## 2. Feasibility of the stack

| Layer | Choice | Feasible? | Notes |
|---|---|---|---|
| Canvas | HTML5 `<canvas>` | ✅ trivial | 2D context, pointer events for draw. |
| Sketchy look | `rough-stuff/rough` (rough.js) | ✅ | Browser JS lib. Load via CDN (`https://unpkg.com/roughjs@latest/bundled/rough.js`) or add to `package.json` + bundle. Renders hand-drawn rects/ellipses/lines/freehand. |
| CRDT merge | `loro-go` (already dep) | ✅ | `Doc.ApplyUpdate` merges concurrent edits conflict-free. No LWW loss. |
| Broadcast (web) | existing `SSEHub` + `InMemoryBroadcaster` | ✅ | Same mechanism as todos. No JetStream needed for web-only demo. |
| Broadcast (desktop edge) | NATS Leaf Node + `SyncWorker` | ✅ already built | Reuse for the Wails path later. |
| Persistence | PocketBase `whiteboards` collection | ✅ already seeded | `SyncWorker.PocketBasePersister` upserts by `doc_id`. |
| Cursors/presence | existing `Presence` SSE bridge | ✅ | Drop-in for live peer cursors. |

**Verdict: fully feasible.** The only new code is the frontend canvas +
rough.js + a thin Go handler that applies incoming updates to `collab.Doc`
and broadcasts them, plus loading the rough.js asset.

---

## 3. Architecture (web-only path, no JetStream required)

```
[Browser A]                      [Browser B]
  canvas + rough.js                canvas + rough.js
     |  pointer draw                  ^       |
     v                               |       v
  POST /api/whiteboard/{docID}/update   SSE /api/whiteboard/{docID}/stream
     |  (Loro update bytes)             |       (Loro update bytes)
     v                                   |
  [Go handler] ──> collab.Doc.ApplyUpdate
                    │
                    ├─> SSEHub.BroadcastExcept(update, originClientID)  ← live to other tabs
                    └─> (debounced) collab.SyncWorker.persist → PocketBase whiteboards
```

- **Draw**: pointer events on `<canvas>` → build a shape (rough.js `generator`
  or `rough.canvas`) → serialize shape → produce a Loro update → POST.
- **Merge**: handler `ApplyUpdate` into the in-memory `collab.Doc` (keyed by
  `doc_id`). Concurrent edits from B merge conflict-free.
- **Broadcast**: `SSEHub.BroadcastExcept` (exclude origin, like todos) so other
  tabs re-render. Reuse the existing `TodoBroadcaster.PublishTodoUpdateFrom`
  shape — add a `WhiteboardBroadcaster` or extend the interface.
- **Persist**: debounced (e.g. 1s) snapshot to PocketBase via the existing
  `SyncWorker.PocketBasePersister`. For web-only we can call the persister
  directly (no JetStream needed) OR keep JetStream for parity. Recommendation:
  call `PocketBasePersister.Upsert(docID, snapshot)` directly on a timer to
  keep the web demo tag-agnostic (works with default build).
- **Load**: on open, `GET /api/whiteboard/{docID}` returns the latest snapshot
  from PocketBase → `Doc.ApplyUpdate(snapshot)` → render all shapes.
- **Presence**: optional — wire `Presence` SSE bridge for live cursors.
  Low effort, high demo value.

### 3b. Offline-first (why SSEHub + Loro is enough — no JetStream needed for the web path)

**Question this answers:** *"If a user goes offline mid-draw, how does the
server integrate their changes on reconnect — shouldn't we use Loro + JetStream?"*

The server does NOT need JetStream to integrate offline edits, because:

1. **Loro is a CRDT** — every edit is an *update* that merges
   commutatively/associatively. The browser keeps drawing into its local
   Loro doc while offline; on reconnect it exports the accumulated update
   (`doc.export({mode:'update'})` since the last checkpoint) and POSTs it.
   The server calls `Doc.ApplyUpdate` and converges — no central ordering,
   no LWW loss. This is exactly what the CRDT is for.
2. **The web client buffers outgoing ops while offline**
   (`whiteboard.js` outbox + `navigator.onLine`/`window 'online'` replay,
   guarded by a `flushing` flag so replay is idempotent). The server is
the single merge point and is stateless w.r.t. client connectivity — it
just accepts any update at any time and re-broadcasts to peers.
3. **SSEHub is the transport fan-out**, not the source of truth. The
   source of truth is the Loro `Doc` in memory + the PocketBase snapshot.
   SSE carries live deltas; PocketBase carries durable state. Both already
exist and are tag-agnostic (default build).

**Where JetStream actually earns its place** (kept, not removed):
- **DagNats** durable workflows (JSON over JetStream) — opt-in build tag.
- **Desktop/edge replication** (`SyncWorker` over a NATS Leaf Node) — when a
  *desktop* client (Wails) needs to sync a whiteboard across instances via
  a durable stream. This is the `jetstream`-tagged path.
- **Multi-instance todo realtime** — when >1 server instance sits behind a
  load balancer and todos must sync across them (also `jetstream`-tagged).

**Mobile/desktop battery note:** the embedded NATS server is a *server-side*
long-running process. Clients (browser, or a Wails iOS/Android WebView)
consume the whiteboard over SSE, which is cheap (one HTTP/2 connection,
auto-reconnect via `EventSource`). We never run an embedded NATS server on a
battery-powered client, so JetStream adds no mobile battery cost. SSE works
in 97% of browsers and inside Wails' native WebView on iOS/Android.

**Regression guard:** `features/whiteboard/web_test.go` →
`TestWhiteboard_OfflineReplay` proves a late (replayed) op from an
"offline" client still converges on the server and reaches peers + persists.

---

## 4. Data model

Loro doc holds a `LoroMap` or `LoroList` of shapes. Each shape:

```go
type Shape struct {
    ID      string  `json:"id"`      // uuid
    Type    string  `json:"type"`    // rect | ellipse | line | freehand
    Points  [][2]float64 `json:"points"`
    Color   string  `json:"color"`
    Rough   map[string]any `json:"rough"` // roughness, strokeWidth, etc.
}
```

- Store shapes in a Loro `LoroMap` keyed by `Shape.ID` so concurrent edits to
  different shapes merge; same-shape concurrent edit resolves via CRDT
  (last-writer per field inside the shape map).
- Frontend renders by iterating the Loro map → rough.js draw calls.
- On each mutation, `EncodeUpdate` → POST bytes. On receive, `ApplyUpdate` →
  re-render changed shape only (or full re-render for v1 simplicity).

---

## 5. Files to add / change

### New
- `features/whiteboard/handlers/whiteboard.go`
  - `GET /api/whiteboard/{docID}` → load snapshot from PocketBase, return JSON
    of shapes (or Loro bytes).
  - `GET /api/whiteboard/{docID}/stream` → SSE stream (reuse `handleSSEStream`
    pattern; `LoadAppAuth` so it scopes per user like todos).
  - `POST /api/whiteboard/{docID}/update` → body = Loro update bytes; apply to
    `collab.Doc`; `BroadcastExcept` to other tabs; debounce-persist.
  - In-memory `map[docID]*collab.Doc` (mutex) — one CRDT per board.
- `features/whiteboard/components/whiteboard.templ`
  - `<canvas>` + rough.js init + pointer handlers + SSE subscribe + re-render.
- `web/src/js/rough-canvas.js` (or CDN `<script>` in layout head)
  - thin wrapper: `drawShape(ctx, shape)` using `rough.canvas(canvas)`.
- `db/seed.go` already has `whiteboards` — verify fields; add `shapes` JSON
  column if we persist rendered shapes instead of raw Loro bytes (recommend
  raw Loro snapshot in `snapshot` text column; already present).

### Change
- `router/router.go` — register whiteboard routes (guard with auth like todos).
- `internal/nats` `TodoBroadcaster` interface — add a `WhiteboardBroadcaster`
  (or generalize `PublishTodoUpdateFrom` → `Publish(ctx, payload, fromClientID)`
  reused for both). Keep it simple: a second interface method
  `PublishWhiteboardUpdateFrom(ctx, payload, fromClientID)`.
- `layout.templ` — add a nav entry / link to `/whiteboard/{docID}` (or a demo
  board id). Keep it behind a feature flag or always-on (it's a demo template).
- `package.json` — add `roughjs` (or use CDN; CDN is simpler, no build step).
- `docs/PLAN.md` — mark "Whiteboard UI scaffold" PENDING as in-progress/closed
  once shipped.

---

## 6. Implementation steps (ordered)

1. **Load rough.js**: add `<script src="https://unpkg.com/roughjs@4.6.6/bundled/rough.js"></script>`
   to `layout.templ` `<head>` (or `npm i roughjs` + bundle). Verify `rough`
   global exists.
2. **Go handler skeleton**: `whiteboard.go` with the 3 routes + in-memory
   `map[docID]*collab.Doc`. Reuse `auth.LoadAppAuth` on the SSE + update routes.
3. **CRDT apply + broadcast**: on `POST /update`, `doc.ApplyUpdate(bytes)` then
   `broadcaster.PublishWhiteboardUpdateFrom(bytes, clientID)`. Mirror the
   todo `broadcastTodo` exclude-origin pattern exactly.
4. **SSE stream**: `GET /stream` opens persistent SSE (`@get(..., {permanent:true})`
   like the todo fix), dispatches `whiteboard-update` events → frontend
   `ApplyUpdate` + re-render.
5. **Frontend canvas**: `whiteboard.templ` — canvas element, pointer handlers
   that create a `Shape`, serialize, `POST /update`, and optimistically render.
   On SSE `whiteboard-update`, `ApplyUpdate` + full re-render (v1) or targeted.
6. **Load on open**: `GET /api/whiteboard/{docID}` returns PocketBase snapshot;
   apply + render. This is what makes it survive restart.
7. **Debounced persist**: timer (1s) calls `PocketBasePersister.Upsert(docID,
   doc.EncodeSnapshot())`. Web-only, no JetStream dependency.
8. **Presence (optional, high value)**: wire `Presence` SSE bridge for live
   cursors — `GET /api/collab/presence/{docID}` already exists (jetstream tag).
   For web-only without jetstream, a lightweight cursor broadcast over the
   same SSEHub is ~20 lines.
9. **Nav + polish**: link in layout; empty-state hint; clear-board button
   (sends a `clear` update that empties the Loro map).
10. **Tests**: unit test `collab` already guards merge; add a handler test
    that POSTs an update from client A and asserts client B's SSE receives it
    (httptest + real SSEHub, like the todo tests).

---

## 7. Open questions / decisions needed

- **JetStream or not for web?** Recommendation: web demo uses SSEHub only
  (default build, no `-tags jetstream`). Keep JetStream path for the desktop
  Leaf Node sync (already built). This keeps the whiteboard demo runnable on
  the default binary — important for the template's "works out of the box" goal.
- **rough.js delivery**: CDN (`unpkg`) vs npm bundle. CDN = zero build change,
  but needs network at runtime. npm = offline, but adds a build step to
  `web/resources/static/`. Recommend CDN for the demo, with a note.
- **Shape set for v1**: rect / ellipse / line / freehand. Arrows + text as v2.
- **Clear board**: send a Loro update that deletes all shape keys (or a new
  empty doc + version bump). Simplest: client sends `clear` → handler replaces
  the in-memory `Doc` with a fresh one and broadcasts a `reset` event.
- **Multiple boards**: v1 uses a single demo board id (`demo-board`); URL param
  `/whiteboard/{docID}` supports many. PocketBase keys by `doc_id`.

---

## 8. Exit criteria

- [ ] `GET /api/whiteboard/{docID}` returns persisted snapshot (PocketBase).
- [ ] Two browser tabs drawing rough.js shapes see each other's strokes live
      (SSE broadcast, origin excluded).
- [ ] Concurrent edits to the SAME shape merge conflict-free (Loro CRDT) — no
      lost strokes.
- [ ] Reload a tab → board state restored from PocketBase.
- [ ] `golangci-lint` + `go build ./...` + `go test ./...` green (default build,
      no jetstream tag required for the web demo).
- [ ] Optional: live peer cursors via Presence SSE bridge.
- [ ] PLAN.md "Whiteboard UI scaffold" PENDING marked closed.
