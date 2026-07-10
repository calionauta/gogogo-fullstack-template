# PLAN.md — multi-session roadmap for gogogo-fullstack-template

> Living document. Each numbered section is a **future session**.
> Read "What we'll do" before opening the session to know the starting
> point; "Exit criteria" tells you when the session is done.
>
> Update this file at the end of each session, not during. The session
> that closes work also commits the updated PLAN.md.

---

## 1. Wails v3 — desktop + mobile builds (Loro CRDT + NATS Leaf Node sync)

### Scope decision (2026-07-10)

- **Desktop first** (macOS / Windows / Linux). Mobile (Android/iOS) is a
  stretch goal — Wails v3 mobile is still experimental in 2026 and may
  not yield runnable artifacts reliably.
- **Add edge sync** in the same effort: the desktop app embeds NATS
  (Leaf Node) to sync with the central server when connectivity returns,
  and uses **Loro CRDT** as the collaboration model for shared state
  (whiteboard, multi-cursor presence, conflict-free offline edits).
- **Web demo stays Datastar/SSE** (server-driven, not offline-first).
  Loro is for the desktop edge; the web uses Yjs if/when collaborative
  features land there (browser-native, no Go bridge needed).

### Why Loro (not Yjs) for the desktop/Wails target

- `loro-go` (official Rust-core binding via CGO) runs **natively in the
  Wails Go backend** — no JS bridge. Yjs has no mature Go core; it would
  force a webview-JS path inside a desktop app.
- Smaller, faster binary (Rust core ~200-400KB) than Yjs-at-scale;
  better delta encoding for large whiteboard docs.
- **Offline-first is first-class**: `Repo` (local-first store) +
  `PartialLoad` let the app load/edit a slice of a doc offline and sync
  only the diff on reconnect.
- CRDT merge resolves concurrent edits of the same node (whiteboard),
  which **LWW/UUID cannot** (LWW discards one writer's work). For the
  todo-list we already have DagNats event-sourcing (LWW-by-version is
  fine there); for collaborative whiteboard, CRDT is the right model.

### Architecture

```
[Wails v3 desktop app]
  ├─ Go backend (reuses router/handlers, adds cmd/desktop)
  │    ├─ embedded NATS server (Leaf Node)  ← edge
  │    ├─ loro-go Repo (local-first CRDT store)
  │    └─ PocketBase client (source of truth on server)
  └─ webview (Tailwind/DaisyUI UI, Datastar signals for local UI)
        │
        │ Leaf Node sync (auto, when net returns)
        ▼
[Central NATS on server]  (same NATS the jetstream/dagnats tags boot)
        │
        ▼
[Sync Worker (Go, on server)] — applies Loro updates → PocketBase
  whiteboards collection (snapshot base64) = final source of truth
```

**Transport vs merge are separate concerns:**
- **NATS Leaf Node** = transport (delivers Loro updates to the central
  NATS; replays buffered edge events on reconnect). Native, no custom
  code.
- **Loro CRDT** = merge (resolves concurrent edits locally, conflict-
  free). The Worker just persists the resolved doc.
- **PocketBase** = final source of truth (survives restarts; web
  clients read from it).

### What we'll do (phased)

**Phase A — Wails v3 desktop shell (no sync yet)**
1. Add `cmd/desktop/main.go` (< 200 lines) that boots the existing
   `router.Init` + PocketBase inside a Wails v3 `app.Run()`. Reuse
   `config`, `db`, `features/*` unchanged.
2. `wails.json` at repo root: build web assets (`npm run build`), set
   `frontend: web/`, output `build/bin/`. `make desktop` →
   `wails build` producing a macOS `.app` (and `.exe`/`.deb` on those
   hosts).
3. Verify the app serves the same UI on `http://localhost:34115`
   (Wails dev server) and the demo todo flow works end-to-end.

**Phase B — Embedded NATS Leaf Node**
4. In `cmd/desktop`, start an embedded NATS server with a `leaf`
   remote pointing at the central server's NATS port (same port the
   `jetstream`/`dagnats` tags use). Use `nats-server.conf`
   (`leaf { remotes: [{ url: "nats://<server>:7422" }] }`).
5. Add a `sync` subject (`app.sync.>`) for Loro doc updates; the edge
   publishes, the central receives, no custom replay logic.

**Phase C — Loro CRDT + sync worker**
6. Add `internal/collab` package: `loro-go` `Doc` per whiteboard doc;
   `EncodeSnapshot`/`EncodeUpdate`/`ApplyUpdate` helpers; `DocID` = UUID v7.
7. Desktop publishes Loro updates on `app.sync.<docID>`; on reconnect
   Leaf Node replays buffered events to the central NATS.
8. Server-side `SyncWorker` (new `cmd/web` handler or standalone) subscribes
   to `app.sync.>`, merges into the Loro `Repo`, persists snapshot to
   PocketBase `whiteboards` collection (base64). Idempotent by `DocID`+
   version.
9. Presence/cursors: NATS pub/sub on `app.presence.>` (ephemeral, no
   persistence) — lightweight multi-user cursor broadcast. Done: a
   `Presence` type (heartbeat join/leave + cursor with roster TTL) plus a
   central SSE bridge so browser clients receive edge cursors live.

**Phase D — CI + mobile stretch**
10. `.github/workflows/build-platforms.yml`: matrix builds `.dmg`,
    `.exe`, `.deb`, `.AppImage` on push; upload as release artifacts.
11. Mobile: attempt `wails build -platform android` / `ios` in a
    follow-up session; mark experimental, do not block desktop exit.

### Exit criteria

- [x] `cmd/desktop/main.go` (< 200 lines) boots server.Run + PocketBase
      goroutine + Wails webview via reverse proxy (Phase A done 2026-07-10)
- [x] `go build ./cmd/desktop` compiles; `make desktop` produces the
      `gogogo-desktop` binary (full `.app` bundle is Phase D via CI)
- [x] Desktop reuses existing router/handlers via internal/server.Boot
- [x] Embedded NATS Leaf Node connects to central server NATS (Phase B done 2026-07-10)
      - `internal/nats.StartLeafNode(storeDir, centralURL)` boots an embedded
        NATS as a Leaf Node; `config.NATS.LeafNodeURL` (env `NATS_LEAFNODE_URL`)
        selects it. `TestLeafNode_ConnectsToCentral` guards the attachment.
      - `cmd/desktop` built with -tags jetstream; if `NATS_LEAFNODE_URL` is set
        it becomes a Leaf Node, else standalone embedded NATS.
- [x] `internal/collab` (Loro) compiles; doc create/encode/apply works
      in a unit test (Phase C done 2026-07-10)
      - `internal/collab` wraps `aholstenson/loro-go`: `Doc` (mutex-guarded
        `LoroDoc`), `EncodeSnapshot`/`EncodeUpdate`/`ApplyUpdate`, and
        `StateVersion` (version vector). `TestCollab_SyncWorkerPersists`
        guards the full edge→central path against a real embedded NATS.
- [x] SyncWorker persists Loro snapshot to PocketBase on `app.sync.>`
      receive (Phase C done 2026-07-10)
      - `internal/collab.SyncWorker` subscribes `app.sync.>`, applies each
        update to the per-doc CRDT, and persists via `Persister`
        (`PocketBasePersister` → `whiteboards` collection, upsert by
        `doc_id`). Wired in `router/collab_jetstream.go` (build tag
        jetstream); no-op otherwise. `db/seed.go` ensures the
        `whiteboards` collection exists.
- [x] Desktop publishes Loro updates via `collab.Publisher` on
      `app.sync.<docID>` (Phase C done 2026-07-10)
      - `internal/collab.Publisher.PublishUpdate` exports the delta since a
        version vector and publishes it. `cmd/desktop` (jetstream) builds
        a publisher over the Leaf Node connection and publishes on boot.
- [x] Presence/cursors broadcast over `app.presence.>` (Phase C done 2026-07-10)
      - `internal/collab.Presence` (jetstream): pub/sub on `app.presence.<docID>`:
        heartbeat join/leave + cursor `{doc,x,y}` with roster TTL expiry.
        `TestPresence_TwoPeersConverge` guards two peers receiving each
        other's cursor + join over a real embedded NATS.
      - Central SSE bridge `GET /api/collab/presence/{docID}`
        (`router/collab_jetstream.go`) streams NATS presence to browser
        clients. Desktop edge starts a `Presence` session + demo cursor.
- [x] `.github/workflows/build-platforms.yml` builds 4 desktop
      artifacts and uploads them (Phase D done 2026-07-10)
      - Matrix over darwin/amd64+arm64 (.dmg), windows/amd64 (.exe),
        linux/amd64 (.AppImage) on native runners. Uses `wails build
        -app ./cmd/desktop -config ./wails.json -tags jetstream
        -platform <p>`. Artifacts uploaded via upload-artifact.
      - The full test/lint gate (including the collab e2e guards in
        `make test-combined`) runs in ci.yml — a green CI is the
        prerequisite for a trustworthy desktop binary, so the platform
        workflow only builds bundles (no duplicated tests).
- [x] e2e regression guards for all Phase C surfaces (Phase D done 2026-07-10)
      - `TestCollab_LeafNodeE2E`: REAL central NATS (JetStream + leaf
        listen port) + a SEPARATE leaf-node server (the desktop edge)
        publishing a Loro update on `app.sync.<docID>`. Asserts the leaf
        replicates to central and the SyncWorker persists a valid snapshot.
        Exercises Phase B transport + Phase C merge/persist, no mocks.
      - `TestPresence_SSEBridgeE2E`: drives `collab.PresenceSSEHandler`
        (the exact handler registered at `GET /api/collab/presence/{docID}`)
        over httptest; publishes an edge cursor on `app.presence.<docID>`;
        asserts the SSE response carries it to the browser client.
      - Both run in `make test-combined` (`-tags "jetstream dagnats"`), so
        they are covered by CI's combined matrix. All collab tests finish
        in ~2s (performant; no flaky sleeps beyond tight deadlines).
- [x] Mobile marked experimental; not required for exit (Phase D done 2026-07-10)
      - `wails build -platform android` / `ios` is a documented stretch
        goal. Not attempted in CI; non-blocking. See README "Desktop &
        Mobile".
- [x] README has a "Desktop & Mobile" section (Phase D done 2026-07-10)
- [ ] PENDING (post-Phase D follow-ups, not started):
  - [ ] **Whiteboard UI scaffold** — real canvas component binding to
    `collab.Doc` (Loro) with multi-cursor rendering from `collab.Presence`.
    The transport + CRDT + presence layers are done (e2e-guarded); the UI is
    the remaining surface. Should render/edit nodes, broadcast via
    `Publisher.PublishUpdate`, and show live peer cursors from the SSE bridge.
  - [ ] **Wire desktop Leaf Node to a real central** — a smoke/integration
    test (or manual run) where `cmd/desktop` boots with `NATS_LEAFNODE_URL`
    pointing at a running central and offline edits replicate + persist. The
    transport path is e2e-guarded in `TestCollab_LeafNodeE2E`; the live
    desktop→central wiring is only lightly exercised (demo cursor publish).
  - [ ] **Mobile (experimental)** — `wails build -platform android`/`ios`
    documented stretch goal; native UI bindings unproven, non-blocking.

### Open questions resolved

- **LWW vs CRDT**: CRDT (Loro) for whiteboard/cursors (concurrent edits
  must merge); LWW/event-sourcing (DagNats) already covers todo-list.
- **Yjs vs Loro**: Loro wins for Wails/Go (native binding, lighter,
  offline-first ergonomics). Yjs stays the web-only option.
- **Leaf Node vs custom replay**: Leaf Node is native NATS — no custom
  code. Use it.
- **PocketBase as source of truth**: yes — Worker persists resolved
  Loro snapshot; web + desktop both read final state from it.
- **go-ds-crdt vs Loro (rejected 2026-07-10)**: evaluated
  `ipfs/go-ds-crdt` as an alternative CRDT layer. Rejected: it is a
  **LWW-per-key KV CRDT over libp2p/datastore**, not a document CRDT —
  concurrent edits to the same whiteboard node would LWW-lose one edit
  (exactly the failure we chose Loro to avoid). It also drags in libp2p/
  IPFS (~50+ modules, CGO) on top of the NATS Leaf Node we already have,
  whereas Loro is a single prebuilt binding and transport-agnostic. Loro
  stays.

- [ ] README has a "Desktop & Mobile" section with screenshot

---

## 2. Bug #6 — RESOLVED in commit 23d69b5 ✅

**Status**: fixed. No further action.

The 303 redirect loop on `/` and `/login` was caused by registering
routes inside `app.OnServe().BindFunc(...)` from inside another
`OnServe` handler. PocketBase's `Hook.Trigger` snapshots registered
handlers before any of them run, so nested `BindFunc`s are silently
a no-op. Fix: `RegisterAuth` now takes `*core.ServeEvent` and wires
routes directly on `se.Router` inside the existing top-level
router.OnServe hook. See AGENTS.md §"Wiring HTTP routes" for the
full rule and `http://localhost:8080/<path>` curl recipes.

**Follow-up (2026-07-08):** the first superuser for
`gogogo.calionauta.com` was created via the install UI this session,
and the app was verified working (after Bug #8). The site is
reachable at `gogogo.calionauta.com` (Cloudflare Tunnel terminates
at the server). Remaining operator action is **Cloudflare Access**
(step 4): put an Access policy / login wall in front of
`gogogo.calionauta.com` so the PocketBase superuser / install UI
can't be brute-forced from the public internet. See the step-4 notes
in the session handoff.

---

## 3. Local-first repo CI as reusable workflow

The current `make check` / `make gate` works locally. Next session
extracts this into a reusable GitHub Actions workflow
(`.github/workflows/lint-test.yml`) that downstream forks can
inherit with zero config.

---

## 4. Multi-project promotion

If `gogogo-fullstack-template` becomes the foundation for `stelow`
or `datastar-lint`, the canonical `/var/lib/<name>/data/` bind-mount
layout is the shared convention. Extract `scripts/deploy-prod.sh`
into a separate `gogogo-deploy` repo so the three projects share
the same deploy runner.

---

## 5. Pending cleanups

- [x] `/home/deploy/gogogo-template/` (legacy deploy dir on the
      server) removed 2026-07-08 — `gogogo-fullstack-template` is
      stable (superuser created, app verified working).
- [x] First superuser for `gogogo.calionauta.com` created via the
      install UI 2026-07-08 (see Bug #6 status).
- [ ] **Cloudflare Access (step 4):** put an Access policy / login
      wall in front of `gogogo.calionauta.com` (same pattern as
      `server-treinador.calionauta.com`) so the PB superuser / install
      UI can't be brute-forced. See session handoff notes.
- [ ] Old `gogogo.calionauta.com` install link JWTs have all expired
      — current install link printed by `docker logs` on every
      restart; no longer needed now that the superuser exists.

---

## 6. On-prem deploy permission gotcha (real bug, real fix)

**Symptom** (this session, 2026-07-08): PocketBase container fails
on every restart with `sqlite3: unable to open database file:
permission denied`. The data dir is on a Docker named volume
(`gogogo-fullstack-template-data:/var/lib/.../data`). Docker
creates new volumes with `root:root` ownership, but the container
runs as `USER 65532:65532`. Result: crash loop on first deploy to a
fresh server.

**Root cause**: Docker volume permissions are not negotiated against
the container's `USER`. Bind mounts work because `deploy-prod.sh`
can `chown 65532:65532` the host directory before starting.

**Fix shipped in commit `2c8a1f6`** (this session):
- `deploy/docker-compose.prod.yml`: switched to bind mount
  `/var/lib/${APP_NAME}/data:/var/lib/${APP_NAME}/data`
- `scripts/deploy-prod.sh`: creates `/var/lib/${APP_NAME}/data` with
  `chown 65532:65532` before bringing the container up

**Lesson for other projects**: prefer bind mounts over named volumes
when the container runs as a non-root UID. Bind mounts give the
operator full file-system access for inspection, backup, and
permission repair. Named volumes are appropriate only when the
container runs as root (rare for production).

---

## 7. Bug #7 — deploy permission gotcha: `chown 65532:65532` fails as non-root (RESOLVED 2026-07-08)

**Symptom**: `scripts/deploy-prod.sh` aborts at
`chown 65532:65532 /var/lib/<APP>/data` — the `deploy` user is
non-root and **cannot chown to an arbitrary UID**. The GitHub Action
run goes red, and if the container is later started by hand the data
dir is wrong-owned → `sqlite3: permission denied` crash loop. (This
is what broke run `28975753469`.)

**Why the earlier "fix" (commit `2c8a1f6`) was incomplete**: it
switched to a bind mount but *still* ran `chown 65532:65532`, which
the non-root `deploy` user cannot do. That chown only ever worked
when someone manually `sudo`-ed it once on the server.

**Real fix (commit `8ff3f12`, this session)**:
- `scripts/deploy-prod.sh`: data dir moved to
  `/home/deploy/<APP>/data` (deploy-owned; `/var/lib` is root-owned
  and untouchable). Write access for container uid 65532 granted via
  `setfacl -R -m u:65532:rwx -d -m u:65532:rwx` (or `chmod -R 0777`
  fallback). No `chown` to 65532 anywhere.
- `deploy/docker-compose.prod.yml`: host source is now
  `/home/deploy/<APP>/data`; container target stays
  `/var/lib/<APP>/data` (so the in-container `DATA_DIR` env is
  unchanged).
- Recursive + default ACL matters: the workflow's "Ensure layout"
  step pre-creates `data/pb_data` as the deploy user, so the default
  ACL lets uid 65532 still write into it.

**Second, silent failure mode**: the GitHub Action does
`git -C <repo> pull --ff-only` in "Ensure layout". If you `scp`
edited files into the server's repo clone (`/home/deploy/<APP>/repo`)
to test a deploy, the dirty tree makes `git pull` abort and the whole
deploy fails. **Always push to `master`; never scp into the repo
clone.** (This bit us during the 2026-07-08 session and was fixed by
`git -C repo checkout -- . && reset --hard origin/master` then
re-running the workflow.)

See AGENTS.md §Deploy and §"Wiring HTTP routes".

---

## 8. Bug #8 — static assets 303-redirect + auth middleware never wired (RESOLVED 2026-07-08)

**Symptom**: app loads with no styling; console shows
`Refused to apply style from '…/login' because its MIME type
('text/html') is not a supported stylesheet MIME type` and
`Failed to load module script … MIME type of "text/html"`. The
sign-in button looked dead.

**Root cause (two independent bugs)**:
1. PocketBase registers a catch-all dashboard route
   `e.Router.GET("/{path...}", apis.Static(...))`
   (`apis/base.go`) that requires superuser and redirects
   unauthenticated requests to `/login`. That catch-all SHADOWS any
   wildcard route we register (e.g. `/static/*`) — the handler is
   never reached, so every `/static/*` request is 303-redirected to
   `/login` and the browser gets HTML where it expected CSS/JS.
2. `LoadAuthFromCookie` was only wired in the *test* harness
   (`fixture_test.go`), never in production. So `e.Auth` was always
   nil → every authed page (`/`) bounced to `/login` even with a
   valid `pb_auth` cookie. (This was the real reason the install
   debug log showed `e.Auth` nil — not a stale JWT.)

**Fix (this session, same commit wave as Bug #7)**:
- `router/router.go`: serve embedded static assets via one EXACT
  route per file (`fs.WalkDir` over `resources.StaticFS()`, register
  `/static/<file>` exactly). Exact routes beat PB's catch-all.
- `web/resources/resources.go`: `StaticFS()` now returns `fs.FS`
  (was `http.FileSystem`).
- `router/router.go`: wire
  `se.Router.BindFunc(auth.LoadAuthFromCookie)` as a global
  middleware so `e.Auth` is populated on every request.
- Removed the temporary debug `BindFunc` from
  `features/auth/wiring.go`.

**Lesson**: never register a wildcard route to serve unauthenticated
files — PB's `{path...}` catch-all owns every unmatched path. Use
exact routes, or serve from PB's own `pb_public` dir. And any
middleware a test harness wires (like `LoadAuthFromCookie`) MUST also
be wired in production, or `e.Auth` is silently nil everywhere.

---

## Notes for whoever picks this up

- The deploy pipeline is **Pattern B** (shell key, image built on
  server) — see `~/.agents/skills/cali-ops-deploy-github-tailscale/`
  for the two patterns and when to use each.
- The redirect-loop and `http.ServeMux` subtree-matching gotchas are
  documented in AGENTS.md §"Wiring HTTP routes". The static-asset
  catch-all + unwired-auth-middleware gotchas are Bug #8.
- The privacy filter-repo pass is documented in the git history;
  backup mirror at `~/backups/gogogo-template-*.git/`.
- For PocketBase permissions, the rule is: **data dir lives under
  `/home/deploy/<APP>/data`** (deploy-owned), and
  `scripts/deploy-prod.sh` grants container uid 65532 rwx via
  `setfacl -R -m u:65532:rwx -d -m u:65532:rwx` (or `chmod -R 0777`).
  Never `chown 65532:65532` (the deploy user is non-root and it
  fails). `deploy/docker-compose.prod.yml` bind-mounts
  `/home/deploy/<APP>/data` → `/var/lib/<APP>/data`.

---

## 9. Bug #9 — CSS looked unstyled: HTML loaded DaisyUI v4 standalone, not the Tailwind v5 build (RESOLVED 2026-07-08)

**Symptom**: after login the UI was flat/ugly — `btn-primary` vs
`btn-secondary` made no visual difference; links and typography looked
unstyled.

**Root cause**: the `.templ` `<head>` linked `/static/daisyui.min.css`
(DaisyUI **v4** standalone) while the project uses DaisyUI **v5** via the
Tailwind plugin. v4's component CSS doesn't style v5 markup, so
`btn-primary`/themes rendered bare.

**Fix**: link the real build output `/static/app.min.css` instead. That
file is produced by `npm run build` (Tailwind v4 CLI + DaisyUI v5 plugin)
and regenerated by the Dockerfile before `go build` embeds it. `daisyui.min.css` was deleted (dead relic); `app.css` still loads after it.

**Lesson**: the canonical stylesheet is the Tailwind build (`app.min.css`), NOT a hand-dropped DaisyUI standalone. See AGENTS.md §"DaisyUI Components".

---

## 10. Bug #10 — PocketBase admin threw "The authorized record is not allowed to perform this action" (RESOLVED 2026-07-08)

**Symptom**: after logging into the app, opening `/_/` (PB admin) and
authenticating as superuser showed a toast `The authorized record is not
allowed to perform this action` and loaded no data.

**Root cause (two coupled issues)**:
1. The app reused PocketBase's `pb_auth` cookie name for its own login.
   PB uses `pb_auth` for BOTH regular users and the superuser session, so
   app login overwrote the superuser cookie (and vice-versa).
2. The global `auth.LoadAuthFromCookie` middleware set `e.Auth` from the
   app cookie on EVERY request, including PB's admin API under `/_/` and
   `/api/*`. PB's admin handlers then saw `e.Auth` = a regular user and
   rejected the action.

**Fix**: (1) app cookie renamed to `gogogo_auth` (`cookieName` in
`features/auth/auth.go`), separate from PB's `pb_auth`. (2)
`LoadAuthFromCookie` now skips PB's own surfaces (`/_/`, `/api/`,
`/health`, `/static/`) via a path guard and returns `e.Next()` so PB
authenticates those itself. App page routes are NOT skipped, so they
still get `e.Auth`.

**Lesson**: never share a cookie name with the framework you build on,
and never let a global auth middleware claim `e.Auth` on the framework's
reserved admin/API paths. See AGENTS.md §"Wiring HTTP routes".

---

## 11. Bug #11 — Datastar `PatchElementsNoTargetsFound` on add/toggle/delete todo (RESOLVED 2026-07-08)

**Symptom**: clicking Add (or toggle/delete) logged
`Error: PatchElementsNoTargetsFound` and the list didn't update.

**Root cause**: `renderTodoList` returned the ENTIRE `TodoList`
component, whose top-level `<div>` has NO `id`. `RenderAndPatch` calls
`sse.PatchElements(fragment)` with no selector; the JS client, receiving a
`patch-elements` event with no `Selector` dataline and a fragment whose
top-level element has no `id`, has no target to merge into →
`PatchElementsNoTargetsFound`.

**Fix**: (1) extracted `TodoListRegion` whose top-level element is
`<div id="todo-list" ...>` (list only — form/tabs/signals stay outside,
surviving the patch). (2) `renderTodoList` returns `TodoListRegion`. (3)
all four SSE call sites pass `sdk.WithSelector("#todo-list")`.

`datastar-go` v1.2.2 has `PatchElements` but NOT `MergeFragments`, so
merging by top-level `id` + explicit selector is the right pattern. This
was the ONLY place using `RenderAndPatch` — no other feature had the bug,
but the same trap applies anywhere you emit `patch-elements`: always give
the fragment a target `id` (or pass a selector). See AGENTS.md §"Don'ts".

---

## 12. Bug #12 — container stuck `(unhealthy)`: healthcheck used `wget` in a `scratch` image (RESOLVED 2026-07-08)

**Symptom**: `docker ps` showed the container `Up ... (unhealthy)` even
though `/health` returned `ok`.

**Root cause**: `deploy/docker-compose.prod.yml` set
`healthcheck.test: ["CMD-SHELL", "wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1"]`. The image is `scratch` — no shell, no `wget` — so the
healthcheck command can't run, and Docker marks the container unhealthy
forever.

**Fix**: added a scratch-compatible `health` subcommand to the binary
(`cmd/web/main.go`: `main` checks `os.Args[1] == "health"` and runs
`runHealthcheck()`, which GETs `http://127.0.0.1:<port>/health` and exits
0/1). The compose healthcheck now uses exec form `CMD ["/app", "health"]`
(no shell). Container reports healthy once `/health` answers 200.

**Lesson**: in a `scratch` image, healthchecks must be exec-form
(`CMD ["/app", ...]`) and self-contained — a Go binary can implement its
own HTTP probe subcommand. See AGENTS.md §"Deploy".