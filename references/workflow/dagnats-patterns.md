# DagNats patterns (build tag `dagnats`)

DagNats is a DAG-based durable workflow engine built on NATS JetStream.
Workflows are **declarative JSON** — not Go code. This is the key advantage
over function-name based engines (Turbine / go-workflows / ebind): renaming a
Go handler never orphans an in-flight run, because the workflow references
task *names* (strings), not Go symbols.

## Wiring (this template)

- `cmd/web/dagnats.go` (`//go:build dagnats`) boots `server.New(cfg).Run()`
  on its own HTTP port (`DAGNATS_HTTP_ADDR`, default `127.0.0.1:8090`)
  and registers worker handlers via `server.EmbeddedWorker(srv).Handle(...)`.
- `internal/dagnats/workflow.go` holds the onboarding workflow as a JSON
  string constant (`OnboardingWorkflowJSON`). It is registered idempotently
  on startup, so renaming Go handlers never breaks the saved workflow.
- `internal/dagnats/dagnats.go` is a thin `net/http` client: register
  workflow, start run, signal, get run status. Kept tiny on purpose — the
  app controls retry/timeout and error handling explicitly.

## Single NATS convention

DagNats owns the embedded NATS on the conventional port `127.0.0.1:4222`.
Under `-tags "jetstream dagnats"` the realtime `TodoBroadcaster` connects
to that existing NATS via `nats.ConnectExisting("127.0.0.1:4222")`
(`internal/nats/embedded.go`) instead of starting a second server. One NATS
process, two consumers: DagNats workflows + JetStream realtime.

## Patterns

- **Task names, not Go symbols.** The JSON step `task` is a string the
  worker handler is keyed by. Refactor Go freely; the engine never looks up
  a function by name.
- **Suspend with `WaitForSignal`.** A step handler calls
  `ctx.WaitForSignal("first-todo", timeout)` to block in-process until the
  app delivers a signal (`POST /runs/{id}/signal/{name}`). This is the
  native durable-suspend primitive — no polling, no in-memory flag. On
  crash the signal KV retains the value and the step resumes on redelivery.
- **Idempotent registration.** Re-register the workflow definition on every
  startup; DagNats accepts re-registration of the same name+version.
- **Progress to the UI.** The HTTP handler polls `GET /runs/{id}` and
  streams step progress to connected clients via the broadcaster, so the UI
  stepper lights up live (durable workflow observed in real time).
