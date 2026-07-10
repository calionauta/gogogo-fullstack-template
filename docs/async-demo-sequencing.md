# Tech Sequencing Plan — Async Demo: Add (sync) + Suggest (queue) + Suggest Simulated

## Goal

Make the todo example demonstrate the template's three async layers in a way
that is **intuitive and needs no API key**:

- **Add** button → **synchronous**, no queue. The realtime broadcaster
  already re-renders the list, so the queue adds nothing but latency here.
- **Suggest** button → **queued job + retry** (real LLM). This is where the
  queue earns its place: LLM calls are slow and fail.
- **Suggest Simulated** button → same queued + retry path, but against an
  **in-process fake LLM** (`internal/llm/fakeserver`, already used by tests)
  that scripts *error → auto-retry → slow response*. Toasts narrate every
  phase so the user sees the async lifecycle without a token.

This replaces today's state where `handleSuggest` is synchronous and the only
queued path is the cosmetic `todo_created` toast.

## Why this sequencing

The three changes share one mechanism (enqueue → worker → `retry.Do` → SSE
toast to `clientID`), so they build on each other. Removing the queue from Add
first simplifies the mental model ("queue == slow/flaky work"). Then Suggest
moves onto the queue. Then the simulated variant reuses the exact same worker
code with a different LLM backend — the lowest-risk way to showcase the
pattern keyless.

## Sequencing (ordered)

### Phase 0 — Prereqs already done (this branch)
- JetStream realtime fixed + auto-enabled under `-tags jetstream`
  (`internal/nats/embedded.go`, `config/nats_default.go`).
- DagNats durable workflows auto-enabled under `-tags dagnats`
  (`config/dagnats_default.go`). DagNats replaces the old Turbine layer:
  workflows are declarative JSON (not Go), so handler renames never orphan
  an in-flight run, and it has a native in-step signal/wait primitive
  (`WaitForSignal`) that Turbine lacked.
- JetStream broadcasting e2e test (`internal/nats/realtime_test.go`,
  `//go:build jetstream`) — guards the path that was previously silently
  broken.
- DagNats stepper + per-step toasts (`onboarding.go`, `todo_sse.go`
  `"progress"` case, `todo_list.templ`) — shows durable workflow live.

### Phase 1 — Add becomes synchronous (no queue)
**Files:** `features/todo/handlers/todo_crud.go`, `features/todo/handlers/todo.go`.

- Delete `enqueueCreatedEvent` and the `todo_created` job wiring from
  `handleCreate`. `handleCreate` already returns the list patch synchronously
  and calls `broadcastTodo` (realtime), so the todo appears instantly.
- Remove `queue.Job` `todo_created` handling from `internal/queue/handlers.go`
  (`handleTodoCreatedJob`) and any `todo_created` registration, OR keep the
  worker but stop emitting it. Recommend: remove the job entirely to avoid
  dead code; keep `retry_demo` (it's the standalone queue+retry demo).
- `renderTodoList` after create can still emit an inline success toast
  (`emitToast(sse, "Added", "success")`) if we want a confirmation — emitted
  directly in the HTTP response, not via worker.

**Build tags:** none (default package). **Tests:** `features/todo/sse_test.go`
(`TestIntegration_CreateEnqueuesNotification`, `TestIntegration_CreateEmitsToast`)
must be rewritten/removed — they assert the queue path. Replace with a
synchronous e2e asserting the new todo appears in the list patch.

### Phase 2 — Suggest moves onto the queue + retry
**Files:** `features/todo/handlers/llm_suggest.go` → `llm_suggest_job.go`;
`internal/queue/handlers.go`; `features/todo/handlers/todo.go` (register route).

- New job type `suggest` (`queue.Job{Type:"suggest", ClientID, Payload}`).
- `handleSuggest` becomes a thin enqueue: parse `partial`, build the job,
  `h.q.Enqueue(ctx, body)`, return `MergeSignals` with `suggestPending:true`.
- New worker `handleSuggestJob` (in `handlers.go` registry) runs
  `h.llm.ChatSuggest` inside `h.retry.Do(ctx, hub, clientID, "llm.suggest", fn)`
  so every retry attempt emits the existing `retry` SSE feedback (toast
  "attempt N failed / retrying"). On success it `hub.Send(clientID,
  suggestResultJob(suggestions))`; the SSE dispatcher merges `suggestions`
  into signals (reuse the existing `suggest` signal merge — add a `case "suggest"`
  in `dispatchStreamMessage`).

**Build tags:** none. **Tests:** add `features/todo/suggest_test.go`
(`//go:build dagnats` not needed) using `internal/llm/fakeserver` with a real
key-free client pointed at the fake; assert suggestions land via SSE.

### Phase 3 — Simulated LLM mode (fake server in-process)
**Files:** `internal/llm/goai.go` (`NewSimulated`), `cmd/web/main.go` (start
fake when `SIMULATE_LLM=true`), `features/todo/handlers/todo.go` (new route
`/api/todos/suggest-simulated`), `features/todo/components/todo_list.templ`
(new button).

- `internal/llm/fakeserver` already supports `WithStatusSequence` (e.g.
  `[500,200]` → first call errors, second succeeds) and `WithResponseDelay`
  (slowness). Reuse it: `NewSimulated()` starts an `httptest.Server` with
  `WithStatusSequence([]int{500,200})` + `WithResponseDelay(2*time.Second)`,
  and returns a `*Client` whose `GOAI_BASE_URL` points at it (dummy key).
- Start it once at boot when `SIMULATE_LLM=true` (no key needed → great demo
  + CI-friendly). The simulated `Client` is injected into `TodoHandler` the
  same way the real one is.
- `handleSuggestSimulated` == `handleSuggest` but uses the simulated client
  and a distinct job type `suggest_simulated` so the UI can label it. Same
  worker code (Phase 2 handler, branched on client) → same retry + SSE path.

**Build tags:** none. **Tests:** `suggest_test.go` covers both real-fake and
`SIMULATE_LLM` injection; assert the error→retry→slow→success narration appears
as retry toasts and the final `suggestions` signal.

### Phase 4 — Toast narration across phases
**Files:** `internal/queue/retry.go` (already emits `retry` SSE),
`features/todo/handlers/todo_sse.go` (ensure `retry` + `suggest` cases render
toasts), `todo_list.templ` (suggest UI states).

- The `retry.Do` callback already broadcasts `Job{Type:"retry", ...}` with
  `operation/attempt/status/error`; `dispatchStreamMessage` merges it as
  `lastRetry` (shown today next to "Test queue + retry"). For Suggest we add
  a friendlier toast: on each failed attempt emit `toastJob("Suggest: attempt
  N failed, retrying…", "warning")`; on success `toastJob("Suggest: got 3
  completions", "success")`.
- Sequence the user sees for **Suggest Simulated**: *enqueued → attempt 1
  failed (simulated 500) → retrying → slow response… → got suggestions*.
  This is exactly the async lifecycle, keyless.

**Build tags:** none.

## Risks / tradeoffs

- **Removing `todo_created` job** deletes the only "create toast" demo; Phase 1
  replaces it with an inline toast, and Phase 4's suggest toasts are the new
  queue showcase. Net: clearer story.
- **`SIMULATE_LLM` fake server in prod binary**: gate it behind the env var
  and keep it out of the default path; it's a demo aid, not a feature. Don't
  start it unless explicitly enabled.
- **fakeserver is an `httptest.Server`**: fine in-process; it adds a listener
  on a random port. Acceptable for a demo. If we ever want zero-network, swap
  for a pure in-memory `LLMClient` stub implementing the same interface — but
  that loses the real GoAI transport exercise. Prefer the fake server.
- **clientID routing**: Suggest must carry `clientID` (from the form, like
  `enqueueCreatedEvent` does today) so the worker's toasts hit the right tab.
  Reuse the existing `clientID` query-param pattern.

## Suggested commit order

1. Phase 1 (Add sync) — isolated, removes dead queue path + updates
   `sse_test.go`.
2. Phase 2 (Suggest → queue+retry) — new job type + worker + SSE `suggest`
   case + test.
3. Phase 3 (Simulated mode) — `NewSimulated` + boot gate + new route/button +
   test.
4. Phase 4 (toast narration) — polish, can land with Phase 2/3.

Each phase is independently testable and keeps `make check` green.

## Known limitation: onboarding uses a signal-wait, not a long sleep

The event-driven onboarding flow pauses at the `onboarding-await-first-todo`
step via `ctx.WaitForSignal("first-todo")`, which blocks the step in-process
until the app delivers the signal (when the user creates their first todo).
Under the old Turbine layer this was impossible — Turbine v0.3.0 had no
in-workflow suspend primitive, so the flow was split into two workflows plus
an in-memory flag. DagNats' `WaitForSignal` makes the durable suspend
explicit and crash-safe: if the process restarts while the step is waiting,
the signal KV retains the value and the step resumes on redelivery. The
per-step `time.Sleep` pacing in the `greet`/`create-todo` handlers is a demo
concern only and is skipped on crash-recovery replay.
