# Changelog

All notable changes to this template are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

## [0.9.1] - 2026-07-10

### Fixed
- **Demo UI/UX bugs reported in the demo app:**
  - Badge leaked a debug string (`$techDone`/`$techStep`) as `td=false ts=workflow`; now shows only the item count.
  - **AI suggestions buttons never appeared.** The SSE dispatcher only merged the `$suggestions` signal (which toggles container visibility) but never re-rendered the buttons. Now it `RenderAndPatch`-es `#suggestions-region` with the live suggestion buttons.
  - **Realtime badge** hardcoded "NATS JetStream / in-memory"; now reflects the actual transport via `$realtimeKind` (JetStream when NATS is enabled).
  - **Techstack diagnostics stepper** mixed unrelated features in one row; split into two clear steppers — Queue + retry (goqite + retry-go + fake LLM) and Durable workflow (DagNats, 6 steps incl. "waiting for your first todo").
  - **Onboarding "Run durable workflow" button stayed disabled forever.** The progress poller used a fake fixed-sleep countdown disconnected from the real DagNats run and never re-enabled on a `WaitForSignal` suspend. `pollRun` now reads real `RunStatus` (step/total/status), narrates the "waiting for your first todo" suspend, and clears `OnboardingActive` on completion/failure/5-min timeout. Uses the engine's real total (fixes the "2/5" mismatch).
  - **SSE stream listed ALL todos** (single-tenant fallback) because the global auth middleware skips `/api/*` paths, leaving `c.Auth` nil. `handleSSEStream` now calls `auth.LoadAppAuth` explicitly so `listTodos` scopes to the owner.
- **`make check` faster:** dropped the redundant `test-jetstream`/`test-dagnats` stages (the `test-combined` target already covers both tags + their intersection under `-race`). Full gate ~156s vs ~282s.

## [0.9.0] - 2026-07-10

### Added
- **Phase C + D: collaborative whiteboard backend (Loro CRDT + presence).**
  - `internal/collab` (jetstream): a mutex-guarded `Doc` over `aholstenson/loro-go`
    (`EncodeSnapshot`/`EncodeUpdate`/`ApplyUpdate`/`StateVersion`); a `SyncWorker`
    that subscribes `app.sync.>`, merges Loro updates, and persists the resolved
    snapshot to PocketBase (`whiteboards` collection, upsert by `doc_id`); a
    `Publisher` so the desktop edge pushes deltas on `app.sync.<docID>`.
  - Ephemeral multi-user presence over `app.presence.>`: `Presence` (heartbeat
    join/leave + cursor with roster TTL) plus a central SSE bridge
    `GET /api/collab/presence/{docID}` so browser clients receive edge cursors
    live. `cmd/desktop` starts a `Presence` session + demo cursor.
  - Desktop CI bundles (`.github/workflows/build-platforms.yml`): `.dmg`×2 /
    `.exe` / `.AppImage` via `wails build -tags jetstream`.
  - **e2e guards** (in `make test-combined`, `-tags "jetstream dagnats"`):
    `TestCollab_LeafNodeE2E` (real central NATS + separate leaf-node edge
    replicating a Loro update to central + persisting) and
    `TestPresence_SSEBridgeE2E` (SSE bridge carries an edge cursor to a browser
    client). Found + fixed a real SSE hang (handler blocked before writing
    headers). All collab tests finish in ~2s.
  - README "Desktop & Mobile" section; mobile marked experimental.
  - Rejected `ipfs/go-ds-crdt` (LWW-per-key KV CRDT over libp2p — would lose
    concurrent edits; drags libp2p on top of the NATS Leaf Node). Loro stays.

## [0.8.0] - 2026-07-09

### Fixed
- **Console errors on todo list patches.** The static CSS rule `view-transition-name: todo-item` was attached to every `.todo-item` element. The View Transitions API expects per-element unique names, so when the list re-rendered with multiple items, the browser logged `Unexpected duplicate view-transition-name: todo-item` and cascaded into `InvalidStateError: Transition was aborted because of invalid state` (and a stray `Access to storage is not allowed from this context` from the surrounding plugin code). Removed the duplicate-causing rule; per-item entry animations (`todo-enter-self`, `todo-enter-remote`) still cover the entry feel, and Datastar's `WithViewTransitions()` still wraps the patch in a `document.startViewTransition()` for the default root cross-fade.
- **"Suggest (simulated)" button not visible on the public demo.** `SIMULATE_LLM` defaulted to empty in the production compose file, so `signals.SimulatedLLM` was false and the mock-server affordance was hidden. The compose now sets `SIMULATE_LLM: ${SIMULATE_LLM:-true}` so the keyless "Suggest (simulated)" button (which exercises the full queue + retry + SSE pipeline against an in-process fake) is exposed alongside the real LLM button. Set `SIMULATE_LLM=false` to disable for private deployments.

## [0.6.2] - 2026-07-09

### Fixed
- **goconst lint clean.** Collapsed ~10 repeated string-literal warnings (Datastar signal keys `suggestions` / `suggestErr` / `suggestPending` and the `buy milk` test fixture) into shared package-level constants so the `jetstream` CI matrix finishes green. Two tiny files added: `features/todo/handlers/signal_keys.go` (production) and `features/todo/signal_keys_test.go` (test fixture). No behavior change.

## [0.6.1] - 2026-07-09

### Fixed
- **Quality gate green.** Resolved every `make check` issue so the gate is back to fully passing:
  - **datastar-lint**: `data-on-load` → `data-on:load` (the correct colon-separated event syntax).
  - **golangci-lint (15 issues)**: 5× lll + 3× errcheck in `todo_crud.go` / `todo_sse.go` (refactored into a `patchTodoListWithSelfOrigin` helper to keep `RenderAndPatch` calls under the 120-char line limit and to actually check the `MergeSignals` error); 1× goconst + 1× gocyclo(25) in `todo_sse.go` (split the dispatcher into per-type `streamToast`/`streamRetry`/`streamTodo`/`streamClients`/`streamSuggestResult`/`streamProgress` helpers + local `retryStatusSuccess`/`retryStatusAttempt` constants); 1× gocyclo(14) in `fakeserver.handle` (split into `authorize`/`decodeRequest`/`writeStatusOrSuccess` helpers); 1× mnd in `goai.NewSimulated` (extracted `simulatedResponseDelay` const); 3× unused in `sse_test.go` (deleted `sseTestTimeout`, `openSSE`, `pumpSSE`).
- **Vendored `app.min.css`** rebuilt to include the v0.6.0 Tailwind/DaisyUI classes (`todo-item` variants, `progress progress-primary`, `skeleton`, `loading-dots`, `view-transition-name`, `oklch` tint variables, etc.) so the page actually loads the new styles at runtime.

## [0.6.0] - 2026-07-09

### Added
- **Self vs. remote todo animations.** Every todo mutation now carries a `source` tag ("self" or "remote") that the UI uses to pick a distinct entry animation, tint, and highlight:
  - **Self** (you created it): `slide-in-from-top + primary tint` that decays (~320ms).
  - **Remote** (broadcast from another client): `slide-in-from-left + info tint + pulse` that decays (~720ms), plus a small "from someone else" indicator that fades out.
  - The new `lastItemSource` signal is merged by the local HTTP handler ("self") and by the SSE dispatcher on broadcast ("remote"); the `TodoItem` template reads it via `data-attr:data-source` and the CSS variants live in `layout.templ`.
- **View Transitions API** on every list patch (`sdk.WithViewTransitions()` on `RenderAndPatch` for `#todo-list`). Gives delete + reorder a smooth cross-fade for free (Chrome/Edge/Safari; Firefox gracefully falls back to morph).
- **AI suggest queue panel.** A small dashboard mounted when LLM or simulated LLM is active. Pills (DaisyUI `badge-ghost` / `badge-warning` / `badge-success`) flip as the operation transitions enqueue → attempt failed → completed, driven by new structured signals (`lastRetryStatus`, `lastRetryOperation`, `lastRetryAttempt`). Skeleton rows + loading dots while pending.
- **Onboarding progress bar** alongside the Turbine stepper (`<progress class="progress progress-primary">`) so users see the step ratio at a glance while the live stepper advances.

### Changed
- `todoUpdateJob` gained a `source` parameter so every broadcast carries origin information.
- The retry SSE dispatcher now merges structured signals (`lastRetryOperation`, `lastRetryStatus`, `lastRetryAttempt`) alongside the existing raw-JSON `lastRetry` signal, so the UI can drive pill transitions via boolean expressions instead of string matching.

## [0.5.0] - 2026-07-09

### Changed
- **Event-driven onboarding flow** (per user, on login). The single `WelcomeOnboarding` workflow is replaced by two split workflows driven by real app events: `OnboardingStart` (greet + mark `await_todo`) fires automatically on every successful password login via a `features/auth` login hook (scoped to the logged-in user's PocketBase record id); `OnboardingContinue` (todo captured → 1-min `time.Sleep` pause → finalize + completion alert) fires from the create-todo handler when the user has a pending onboarding. Turbine v0.3.0 has no in-workflow suspend primitive, so the split is the idiomatic event-driven alternative to polling or `WithSchedule` cron (which would be recurring, not one-shot). Each browser session gets its OWN onboarding instance — not a global broadcast.
- Removed the now-dead `TodoCreator` interface, `ExampleTodo` struct, `PocketBaseTodoCreator`, `CreateExampleTodo`, and the `onboardingStepCounts` machinery. The workflow no longer writes example todos; the user's first todo is the onboarding trigger.
- README + plan doc updated to describe the event-driven flow.

## [0.4.0] - 2026-07-09

### Added
- **Simulated LLM mode (`SIMULATE_LLM=true`).** A keyless "Suggest (simulated)" button exercises the exact same queue + retry path as the real LLM against an in-process fake server (`internal/llm/fakeserver`) that scripts `500 → 200 + delay`, so visitors can watch the async lifecycle (enqueue → attempt failed → retry → slow → result) with no API key. `llm.NewSimulated()` starts the fake; the worker's retries stream per-attempt toasts to the originating tab via `clientID` routing.
- **Suggest moved onto the queue.** `handleSuggest` now enqueues an async `suggest` job; a worker runs `ChatSuggest` inside `RetryConfig.Do` and streams the 3 completions back over SSE (`suggest_result` case). Result + retry feedback are routed to the originating `clientID`.
- **Add is now synchronous.** Removed the `todo_created` queue path; `handleCreate` patches the list directly and the realtime broadcaster re-renders for other clients. The queue is reserved for slow/flaky work (Suggest, retry-demo) — cleaner mental model.
- **Durable workflow is followable.** `WelcomeOnboarding` now paces each step with a short delay (inside the durable `turbine.Do` step, so recovery replays skip the sleep) and emits a live stepper (1→5) + a final `alert-success` completion banner in the UI.
- **Demo user-account lock.** `db/seed.go` hardens the `users` collection so non-superusers cannot create or delete accounts via the API or the `/_/` admin dashboard (only the superuser can). Keeps the public demo safe from account spam.
- **"Try it live" README section** with linked rows for the Todo demo app and the live PocketBase admin dashboard at `/_/`.
- **Turbine + JetStream auto-enable** (build-tag gated): `-tags turbine` / `-tags jetstream` now enable the feature without an extra env var (`WORKFLOW_ENABLED` / `NATS_ENABLED` default true under the tag, overridable with `=false`). `make build-turbine` / `make build-jetstream` documented.
- **`internal/llm/fakeserver.NewServer`** — test-free constructor so the fake can run in production demos, not just `*testing.T`.
- **`internal/llm`: disable goai's internal `MaxRetries`** so the project's `RetryConfig` (and, for queued work, the worker's retry) is the single retry layer — transient 5xx surface to SSE feedback instead of being absorbed silently.

### Changed
- **README**: translated AI-suggest bullet to describe the queue + simulated mode; documented `SIMULATE_LLM`.
- **`docs/async-demo-sequencing.md`**: detailed tech-sequencing plan for the async demo (Add sync / Suggest queued / Suggest simulated), with an honest note that Turbine v0.3.0 has no in-workflow delay/schedule primitive (only `WithSchedule(cron)` at registration) — the per-step `time.Sleep` is the substitute.

### Fixed
- **Suggest worker bug**: `body, err := json.Marshal(...)` shadowed the `ChatSuggest` error, so the worker always reported success and never retried. Now uses a distinct `marshalErr`, so failures correctly trigger worker retries + SSE feedback.
- **Stale doc comments** referencing the removed `todo_created` job.

## [0.2.0] - 2026-07-07

### Added
- **Async job → SSE pipeline for the Todo example.** `handleCreate` enqueues a `todo_created` job; a worker picks it up and streams a success toast to the right browser tab via `clientID` routing on the SSE Hub.
- **`internal/queue/retry.go`** — exponential backoff with jitter via `avast/retry-go/v4`, SSE-aware (`lastRetry` signal so the UI can show "retrying…").
- **`internal/queue/handlers.go`** — `HandlerRegistry` for job-type → handler dispatch, decoupling workers from business logic.
- **`internal/queue/goqite_schema.sql`** — explicit goqite schema, separate from application data.
- **Toast component** (`features/todo/components/toast.templ`) — stacked, auto-dismiss, manual close, progress bar.
- **Layout component** (`features/todo/components/layout.templ`) — shared page shell.
- **`safejson.go`** — JSON-safe signal marshaling for Datastar.
- **Turbine onboarding workflow** (`features/todo/handlers/onboarding.go`, build-tag `turbine`) — `WelcomeOnboarding` creates 3 example todos via durable steps; resumes after a crash.
- **Build-tag matrix targets**: `make build-turbine`, `make build-all`, `make test-turbine`.
- **CI matrix** — `.github/workflows/ci.yml` now runs lint + test + build across `""`, `jetstream`, and `turbine` tags.

### Changed
- **SSE Hub hardening** (`ssehub.go`): register-before-enqueue, replay buffer for late subscribers, and backpressure (slow clients are dropped, never block the producer).
- **README** translated to English and updated to reflect the new structure, commands, and the async → SSE example.

### Fixed
- **Quality gate**: all `golangci-lint` issues resolved (errcheck `check-blank`, `exitAfterDefer` in `main.go`, gosec G107, govet unused writes/params, gofumpt). `go vet`, `gofumpt`, `goimports`, and `go test -race ./...` all clean.
- **`cmd/web/main.go`**: restructured into `run() error` so deferred `q.Close()` / `shutdownTurbine()` fire on exit (previously skipped by `os.Exit`).

## [0.1.0] - Initial release

- GitHub Template Repository scaffold: PocketBase + goqite + SSE Hub + GoAI + age secrets.
- Datastar + DaisyUI reactive UI, Templ type-safe components.
- Build-tag-gated NATS JetStream and Turbine layers.
- golangci-lint strict config, distroless Docker image, Makefile, Air live reload.
