# Architectural Decisions

## Why goqite (Not NATS JetStream) as Default Task Queue

goqite is a SQLite-backed queue that runs in-process. Zero external dependencies.
- ✅ Fire-and-forget jobs with SSE streaming via Hub
- ✅ No network calls, no broker process
- ✅ ~18.5k msgs/s — enough for LLM calls, email, etc.

JetStream is available as an **additional** layer for multi-user real-time, when needed.

## Why DagNats for Durable Workflows

[DagNats](https://github.com/danmestas/dagnats) is a DAG-based durable workflow engine built on NATS JetStream. Workflows are **declarative JSON** (not Go code), which is the key advantage over function-name based engines (Turbine / go-workflows / ebind): renaming a Go handler never orphans an in-flight run, because the workflow references task *names* (strings), not Go symbols. Each step's result is recorded in the event-sourced history; on crash, the workflow resumes from the last completed step.
- ✅ Multi-step transactions that survive restarts
- ✅ Native in-step suspend via `WaitForSignal` (the engine blocks a step until an external signal arrives) — something Turbine lacked, which forced the old onboarding flow to fake suspension with two workflows + an in-memory flag
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
