# Architectural Decisions

## Why goqite (Not NATS JetStream) as Default Task Queue

goqite is a SQLite-backed queue that runs in-process. Zero external dependencies.
- ✅ Fire-and-forget jobs with SSE streaming via Hub
- ✅ No network calls, no broker process
- ✅ ~18.5k msgs/s — enough for LLM calls, email, etc.

JetStream is available as an **additional** layer for multi-user real-time, when needed.

## Why Turbine for Durable Workflows

[Turbine](https://turbine.yakir.io) is a SQLite-backed durable workflow engine. Each step's result is recorded; on crash, the workflow resumes from the last completed step.
- ✅ Multi-step transactions that survive restarts
- ✅ Step retries with exponential backoff
- ✅ Queues, scheduling (cron), and human-in-the-loop approvals
- ✅ Embeds as a library — no external service

Turbine is opt-in via the `turbine` build tag. Workflows live in their own SQLite file under `data/workflow/`, separate from the main PocketBase database.

## Why Three Async Layers (Complementary, Not Alternatives)

| Layer | Problem It Solves | When You Need It |
|-------|-------------------|------------------|
| goqite | "I need to run background tasks and notify the user" | Always (default) |
| turbine | "I need N steps that survive a crash" | Complex onboarding, pipelines (opt-in build tag) |
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
