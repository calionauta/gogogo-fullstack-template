# Changelog

All notable changes to this template are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

## [0.17.0] - 2026-07-14

### Added
- **OfflineBanner compartilhado (DRY/KISS, pluggable como o Toast).** Extraído o aviso de estado de transporte (online / sincronizando / offline) de um banner inline no `features/todo/components/layout.templ` para um componente central `internal/components/offline_banner.templ` (`SCOPE:core`), no mesmo pacote do `Toast`. Agora todo e whiteboard usam o mesmo mecanismo via `@components.OfflineBanner()` — uma única fonte de verdade para o estado offline. A bridge é o Service Worker: `web/resources/static/sw.js` posta `sync-start` / `sync-end` / `sync-error` para os clientes durante o replay de Background Sync; o componente escuta e troca as classes CSS. Fallback `navigator.onLine` quando o SW não está registrado (`OFFLINE_SYNC_ENABLED=false` em dev).
- **CONTENT_WRITING.md expandido.** Guia de writing style reescrito com 10 ângulos (Functionality, Strategy, Design Philosophy, Skills, Comparison, War Stories, Personas, Formats, Anti-marketing, Numbers) cobrindo todos os layers (goqite, dagnats, loro, pb realtime, sse hub, jetstream), o opt-out por env var vs delete-directory, o split `gogogo_auth`+`pb_auth`, e 10 bug stories do changelog. `posts/` adicionado ao `.gitignore` para que rascunhos gerados não subam ao repo.

### Changed
- **Whiteboard page shells montam `@components.OfflineBanner()`** (`features/whiteboard/components.templ`: `BoardListWithRealtime` e `Board`), fechando o gap onde só o todo tinha feedback de offline.

## [0.14.0] - 2026-07-13

### Fixed
- **Whiteboard shapes e pontos não replicavam entre abas (intermitente).** Race condition no `SSEHub.Unregister`: quando o EventSource reconectava (ex: tab volta ao foreground), o handler NOVO registrava um channel novo com o mesmo `clientID`, mas o handler VELHO (context cancelado) executava `defer Unregister(clientID)` e removia o channel NOVO. A partir daí a tab parava de receber eventos — shapes e presença nunca chegavam. Adicionado `UnregisterIfCurrent(clientID, ch)` que só remove se o channel ainda é o mesmo, prevenindo o race. Aplicado tanto no whiteboard (`features/whiteboard/handler.go`) quanto no todo SSE stream (`features/todo/handlers/todo_sse.go`).
- **Double broadcast no whiteboard.** `ApplyOp` já fazia `BroadcastExcept` para as outras abas, e `handleUpdate` fazia outro `Broadcast` para TODAS as abas — peers recebiam eventos duplicados. Corrigido: `ApplyOp` agora usa `Broadcast` (inclui originator) e `handleUpdate` não chama `Broadcast` novamente (`internal/collab/sync_web.go`, `features/whiteboard/handler.go`).

### Added
- **Testes de regressão para `UnregisterIfCurrent`.** `TestSSEHub_UnregisterIfCurrent_PreventsStaleCleanup` simula a race condition exata de reconexão do EventSource; `TestSSEHub_UnregisterIfCurrent_NormalCleanup` verifica que o caso normal continua funcionando (`internal/queue/ssehub_test.go`).

## [0.13.0] - 2026-07-13

### Fixed
- **Cross-tab realtime quebrado por SyntaxError no PbRealtimeRecords.** A refatoração da v0.12.1 removeu a IIFE `(function() { ... })()` mas deixou o fechamento `})();` órfão no template. Isso causava `Uncaught SyntaxError: Unexpected token '}'` que impedia TODO o módulo de executar — o EventSource para o PocketBase realtime nunca era criado, e nenhuma aba recebia atualizações de records. Removido o `})();` órfão (`features/todo/components/realtime.templ`).
- **Aba "Queue + Retry" não navegava no sidebar.** O `data-on:click` do botão estava `@post('/api/todos', {contentType: 'form'})` em vez de `$sidebarTab = 'queue'` — clicar na aba submetia o formulário de criação em vez de trocar a aba. Corrigido para `$sidebarTab = 'queue'` (`features/todo/components/todo_list.templ`).
- **Toast "Step X/6" repetido a cada 700ms no Durable Workflow.** `pollRun` emitia `publishProgress` em CADA tick do polling sem deduplicação de estado. Adicionadas variáveis `lastStep/lastPhase/lastDetail` para só emitir quando o estado muda (`features/todo/handlers/onboarding.go`).
- **Toast "Step 1/6" duplicado ao iniciar workflow.** `handleStart` chamava `publishProgress(1,6,...)` manualmente, e o primeiro tick do `pollRun` publicava de novo o mesmo estado. Removido o `publishProgress` do `handleStart` — o loop de polling agora cuida de todo progresso (`features/todo/handlers/onboarding.go`).
- **Steps intermediários (3/6, 4/6, 5/6) não apareciam nos toasts.** Quando o workflow executava rápido, o polling de 700ms não capturava os steps intermediários. No `completed`, agora publica retroativamente todos os steps não vistos antes do step final (`features/todo/handlers/onboarding.go`).
- **AI Suggest ficava em loading infinito quando LLM não configurado.** `handleSuggestJob` retornava erro sem enviar `suggest_result` com `suggestPending: false`. Agora envia o resultado de erro antes de retornar, liberando o spinner (`features/todo/handlers/llm_suggest.go`).
- **Connected clients mostrava "4 online" com 2 abas.** Whiteboard e todo compartilhavam o mesmo SSE hub. `Stats().Clients` contava whiteboard connections como clients do todo. Adicionado `CountUserClients()` que filtra por `userID != ""` (whiteboard registra com `""`). `broadcastClientCount` e `handleIndex` agora usam `CountUserClients()` (`internal/queue/ssehub.go`, `features/todo/handlers/todo_sse.go`, `features/todo/handlers/todo.go`).

### Changed
- **Stepper do Durable Workflow expandido de 4 para 6 steps.** Agora mostra todos os steps do workflow (1. Greeting, 2. Waiting, 3-5. Creating examples, 6. Finalize), alinhado com o DagNats workflow de 6 steps (`features/todo/components/todo_list.templ`).

### Added
- **Regressão: PbRealtimeRecords sem IIFE órfã.** `TestRealtimeNoOrphanIIFE` verifica que o bloco de script do `PbRealtimeRecords` não contém `})();` (`features/todo/realtime_propagation_test.go`).
- **Regressão: CountUserClients exclui whiteboard.** `TestSSEHub_CountUserClients_ExcludesEmptyUserID` verifica que `CountUserClients()` só conta clients com `userID` não vazio (`internal/queue/ssehub_test.go`).

## [0.12.1] - 2026-07-13

### Fixed
- **Realtime resync crash — the other tab never updated.** `PbRealtimeRecords.resync()` called `actions.get('/api/todos/fragment')` directly. In Datastar v1.2.2's embedded ESM build `actions` is a Proxy that resolves to the get action's `apply` with **no context**, so `cleanups` was `undefined` and the call threw `Cannot read properties of undefined (reading 'delete')`. The other tab received the PocketBase realtime event but crashed before morphing `#todo-list`. Replaced with a hidden `@get('/api/todos/fragment')` button whose `.click()` the runtime drives with a proper Datastar context (the same proven mechanism as the SSE-opener). The server's `/api/todos/fragment` now sends `datastar-selector: #todo-list` + `datastar-mode: outer` so the outer morph replaces the whole list (deletes disappear, creates/updates merge) instead of clobbering the entire document (`features/todo/components/realtime.templ`, `features/todo/handlers/todo_crud.go`).

### Added
- **Realtime resync regression tests.** `TestRealtimeResyncWiringRendered` asserts the page wires resync through the `@get` button and never reintroduces the crashing `actions.get('/api/todos/fragment')` call. `TestRealtimeResyncFragmentMorphHeaders` asserts `/api/todos/fragment` returns `datastar-selector` + `datastar-mode` headers so the resync morphs `#todo-list` (not the whole document). Together with `TestCrossSessionCreatePropagates` they guard the full create → broadcast → morph path (`features/todo/realtime_propagation_test.go`).

## [0.12.0] - 2026-07-13

### Fixed
- **Realtime resync on (re)connect and tab-visibility.** `PbRealtimeRecords` now refetches the todo fragment on `PB_CONNECT` and on `visibilitychange` (tab becomes visible). PocketBase realtime has **no event replay buffer**, so a create that occurred before the subscription was live (or while the tab was backgrounded) was permanently missed — the other tab stayed out of sync until a full reload. This is the most likely cause of the reported "add doesn't show in the other tab" symptom (`features/todo/components/realtime.templ`).

### Added
- **Cross-session realtime regression test.** `TestCrossSessionCreatePropagates` boots the real dev binary (`-tags "jetstream dagnats"`) and asserts a todo created in one session is delivered as a PocketBase realtime `create` event to a *subscribed* session. `TestCrossSessionFragmentScoped` guards list-fragment owner scoping across sessions. Both run on the `dagnats` build variant that previously had **zero** realtime coverage — the e2e only asserted the `pb_auth` cookie was issued, never that a record change fans out to a second subscriber (`features/todo/realtime_propagation_test.go`).

### Docs
- **Auth cookie design documented.** Clarified *why* the app issues two cookies (`gogogo_auth` + `pb_auth`) instead of reusing `pb_auth`: PocketBase keeps admin (`_superusers`) and users as separate auth namespaces, so sharing `pb_auth` clobbers the admin session in the same browser (known gotcha #5050/#1780). Added the explanation to `README.md`, `AGENTS.md`, and the `features/auth/auth.go` cookie constants (`features/auth/auth.go`, `AGENTS.md`, `README.md`).
- **LICENSE + northstar attribution.** Added `LICENSE` (MIT) and credited [northstar](https://github.com/zangster300/northstar) (by Nicholas Zanghi) as the inspiration in `README.md` — the template shares northstar's Go + NATS + Datastar + Templ + DaisyUI scaffold (`LICENSE`, `README.md`).

## [0.11.0] - 2026-07-13

### Added
- **DagNats enabled by default in dev builds.** `.air.toml` dev command now builds with `-tags "jetstream dagnats"`, so the onboarding durable-workflow demo runs out of the box (`internal/dagnats`, `features/todo/handlers/onboarding.go`).
- **PocketBase realtime for todo record mutations.** Record mutations now broadcast over PocketBase's native realtime (owner-scoped view rule) instead of the SSE hub. The SSE hub now carries only workflow signals (`router`, `features/todo/handlers`, `internal/queue/ssehub`, `db/seed`).
- **datastar-lint config.** `.datastar-lint.yaml` whitelists the `data-tool`/`data-doc-id` custom attributes used by the whiteboard JS; lint is scoped to `./features`.

### Changed
- `SSEHub.Register` now takes `(clientID, userID string, ch)`; added `BroadcastToUser` for owner-scoped fan-out (`internal/queue/ssehub.go`).

### Fixed
- **dagnats test never started embedded NATS.** `NATSPort: 0` disables the nats-server client listener (so `ReadyForConnections` could never succeed); `-1` is the random-port idiom. `startTestServer` now uses `-1` (`internal/dagnats/dagnats_test.go`).
- **`internal/nats` test build failure.** `realtime_test.go` called `hub.Register` with 2 args; now matches the 3-arg `(clientID, userID, ch)` signature (`internal/nats/realtime_test.go`).
- **NATS broadcaster API cleanup.** Removed the unused `PublishTodoUpdateFrom`; unified the `TodoBroadcaster` type across the in-memory and JetStream implementations (`internal/nats`).
- **Auth cookie naming.** Explicit `pbAuthCookieName` constant now used by `LoadAuthFromCookie`/`setAuthCookie`/`clearAuthCookie` (`features/auth/auth.go`).
- **`.gitignore` hygiene.** Ignore agent/dot-dir artifacts (`.pi-subagents`, `.*/`); keep `.github/` (CI) and `.githooks/` (hooks). The committed 706-byte `bin/datastar-lint` wrapper was restored — the 10MB binary was an uncommitted local overwrite and was never committed.

## [0.9.2] - 2026-07-11

### Added
- **DagNats dashboard reachable through the app origin.** A reverse proxy mounts the DagNats console (`:8090`, not Cloudflare-tunneled) under `/dagnats/` with path rewriting, so the dashboard loads without extra tunnel/infra config. The user-menu "DagNats" link now points to `/dagnats/`.
- **Stronger static analysis.** `.golangci.yml` now enables `dupl`, `goconst`, `revive`, `tagliatelle`, `modernize`, `nolintlint` (plus the existing `staticcheck`, `errcheck`, `ineffassign`, `govet`, `gocritic`, `gosec`, `noctx`, `gocyclo`, `lll`, `funlen`, `mnd`). `gofumpt` is configured as a formatter (not a linter); `stylecheck` is bundled into `staticcheck` in golangci-lint v2, so it is no longer a separate entry. Pin golangci-lint **v2.12.2+**.

### Fixed
- **Theme toggle showed both moon and sun.** `iconify-icon` is a custom element whose host stylesheet overrode the `[data-theme]` CSS rule. `theme.js` now has `syncIcons()` that explicitly hides the inactive icon (CSS polarity also corrected: light → sun, dark → moon).
- **Navbar did not highlight the active section.** `Navbar` now takes an `active` param (`todos`/`whiteboard`) and applies `btn-active` via `templ.Classes(... map[string]bool{...})`.
- **Whiteboard was completely dead (`WB_DOC_ID missing`).** `whiteboard.js` loaded as a plain external `<script>`, so it executed *before* the inline `<script>` that sets `window.WB_DOC_ID` from `<main data-doc-id>`. The guard bailed out of the whole IIFE, killing drawing, the color picker wiring, the online counter, and remote cursors. `whiteboard.js` is now `defer`-loaded so it runs after the DOM assignment. Added `cursor-pointer` to the color input.
- **`make ci-local` then ask before push.** Documented in `AGENTS.md`: run the local gate first; the agent asks (via `ask_user_question`) before pushing to master rather than auto-pushing. A `pi-yaml-hooks` `pre-push` hook mirrors this (runs `make ci-local`, then `confirm`).

### Changed
- Intentional API/UI wire contracts (`clientID`, `simulatedLLM` JSON tags bound by Datastar signals) keep `//nolint:tagliatelle` with a reason instead of being renamed.

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
