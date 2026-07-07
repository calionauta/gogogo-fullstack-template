# cali-go-stack

**Plan:** A GitHub Template Repository + CLI Scaffolding Tool for Go Web Applications

---

## 1. Vision

A single `go run` binary that ships with everything: database, auth, task queue, durable workflows, real-time multi-user messaging, LLM integration, secrets management, and a deploy pipeline — all pre-configured and ready to use.

The stack is **opinionated with educated choices**. The three async layers are **complementary, not alternatives**:

| Layer | Default | Alternative | Complements |
|-------|---------|-------------|-------------|
| **UI** | Datastar + Templ + DaisyUI | — | — |
| **Database** | PocketBase (SQLite, auth, REST, file storage, realtime) | Plain SQLite | — |
| **Task queue** | goqite (SQLite, SSE streaming) | — | goqite is the default; NATS JetStream can run alongside for different concerns |
| **Workflow engine** | None by default; turbine when multi-step durability needed | go-workflows, ebind, dagnats | turbine co-exists with goqite and/or JetStream |
| **Multi-user real-time** | None by default; NATS JetStream when collaboration needed | NATS Core (ephemeral) | JetStream **adds to** goqite + turbine, does not replace either |
| **LLM SDK** | GoAI (tools, structured output, streaming, MCP) | — | — |
| **Secrets** | age + `~/.secrets/` | env vars, Doppler, Vault | — |

### The Three Async Layers — How They Coexist

```
┌──────────────────────────────────────────────────────────────────┐
│                    goqite (task queue)                             │
│  "I need to run something in the background and notify the user"   │
│                                                                    │
│  🔑 Single-step, fire-and-forget, SSE streaming via Hub           │
│  → LLM calls, send email, resize image                            │
│  → 1 server process, SQLite-backed                                │
└──────────────────────────────────────────────────────────────────┘
                              │ (independent, parallel)
┌──────────────────────────────────────────────────────────────────┐
│               turbine (workflow engine)                            │
│  "I need N steps that together form a durable transaction"        │
│                                                                    │
│  🔑 Multi-step, resume after crash, deterministic replay          │
│  → Onboarding, report pipeline, webhook integration               │
│  → Embeds in PocketBase SQLite                                    │
└──────────────────────────────────────────────────────────────────┘
                              │ (independent, parallel)
┌──────────────────────────────────────────────────────────────────┐
│          NATS JetStream (multi-user real-time)                     │
│  "Multiple users need to see and modify the same live state"      │
│                                                                    │
│  🔑 Multi-publisher, multi-subscriber, persisted streams + KV     │
│  → Collaborative whiteboard (Fabric.js + JetStream KV)            │
│  → Presence, shared UI, event sourcing                            │
│  → Late joiners, horizontal scaling                                │
└──────────────────────────────────────────────────────────────────┘
```

---

## 2. How the Skill Maps into the Template

The `cali-coding-go-stack` skill is the **knowledge base** — it contains decision trees, reference tables, architecture patterns, and pitfalls. The template is the **concrete implementation** of those decisions.

### Skill → Template Mapping

| Skill Section | Template Artifact |
|---------------|-------------------|
| **Decision Tree** (goqite vs turbine vs ebind vs dagnats vs go-workflows) | `docs/queue-decision.md` (included in template), CLI prompts |
| **Canonical Pattern** (goqite + turbine coexistence) | Default project layout with `internal/queue/` and `internal/workflow/` stubs |
| **NATS Core vs JetStream decision** | `docs/nats-decision.md`, `references/nats/when-to-use-jetstream.md`, CLI prompts |
| **SSE Hub patterns** | `internal/queue/ssehub.go` — register-before-enqueue, replay buffer, backpressure |
| **JetStream collaborative patterns** | `references/nats/jetstream-patterns.md` — KV for state, streams for history, presence |
| **Quick Start Checklist** | Actual files to enable/disable via build tags or config |
| **References** table (18+ reference docs) | `references/` directory with all docs pre-populated |
| **Datastar Patterns** | `internal/datastar/` helpers, `references/datastar/patterns.md` |
| **Secrets: age + ~/.secrets/** | `bin/init-secrets` script, `internal/secrets/` package, `references/secrets/age-patterns.md` |
| **GoAI integration** | `internal/llm/` stub with GoAI setup, `references/llm-streaming.md` |
| **PocketBase embedding** | `cmd/web/main.go` with PocketBase embedded, `db/` package layer |
| **CI/CD & Docker** | `Dockerfile`, `.github/workflows/ci.yml`, `.github/workflows/deploy.yml` |
| **Datastar lint** | `bin/datastar-lint` script or `tools/datastar-lint/main.go` |
| **age secrets setup** | `bin/init-secrets` script | 
| **Deploy docs** | `docs/deploy.md`, `references/deploy.md` |

### What the Skill Keeps (Not in Template)

- **Engineering standards** — `cali-coding-go-standards` (golangci-lint config, file size limits, concurrency patterns, security rules)
- **Ongoing monitoring** — Datastar SDK v2 watch (Issue #8, PR #18 status)
- **Testing protocol** — browser-use + dogfood skill instructions
- **Dynamic decision trees** — these stay in the skill because they evolve with the ecosystem

---

## 3. Delivery: Two Complementary Formats

### 3A. GitHub Template Repository (`cali-go-stack`)

**What it is:** A repo with the "Use this template" button. Contains the **fully opinionated default** — everything pre-configured, ready to `go run`. JetStream is **opt-in** via build tags.

**Structure (default — no JetStream):**

```
cali-go-stack/
├── .github/
│   ├── workflows/
│   │   ├── ci.yml              # lint, test, build on PR/push
│   │   └── deploy.yml          # Docker build + push + deploy
│   └── dependabot.yml
├── bin/
│   ├── init-secrets             # age keypair + encrypted env
│   └── datastar-lint            # Datastar attribute validation
├── cmd/
│   └── web/
│       └── main.go              # Entry: PocketBase + goqite + SSE Hub
├── config/
│   ├── config.go                # Env-based config loader
│   ├── config_dev.go            # Dev defaults (//go:build dev)
│   └── config_prod.go           # Prod defaults (//go:build !dev)
├── db/
│   ├── pocketbase.go            # Embedded PB setup
│   ├── repository.go            # Repository interface
│   └── seed.go                  # Default data seeding
├── internal/
│   ├── secrets/
│   │   ├── secrets.go           # age-decrypt loader
│   │   └── secrets_test.go
│   ├── queue/
│   │   ├── goqite.go            # goqite setup + middleware
│   │   ├── ssehub.go            # SSE Hub: register-before-enqueue, replay buffer, backpressure
│   │   └── workers.go           # Worker pool with context cancellation
│   ├── nats/                    # Only present with JetStream build tag
│   │   ├── embedded.go          # Embedded NATS server
│   │   ├── jetstream.go         # JetStream: streams, KV, consumers
│   │   └── presence.go          # User presence tracking
│   ├── workflow/
│   │   └── turbine.go           # Turbine setup (stub — opt-in)
│   ├── llm/
│   │   ├── goai.go              # GoAI client factory
│   │   └── streaming.go         # SSE streaming helpers
│   ├── datastar/
│   │   ├── render.go            # renderAndPatch helper
│   │   └── signals.go           # JSON-safe signal helpers
│   └── brand/
│       └── brand.go             # Product name config
├── web/
│   ├── resources/
│   │   ├── static/
│   │   │   ├── js/
│   │   │   └── datastar/
│   │   └── resources.go         # Embed FS
│   └── ... (Templ components)
├── features/
│   └── app/
│       ├── routes.go            # Feature routes
│       ├── handlers/            # HTTP handlers
│       ├── components/          # Templ components (DaisyUI)
│       └── services/            # Business logic
├── router/
│   └── router.go                # PB router setup
├── docs/
│   ├── decisions.md             # Why each choice was made
│   ├── nats-decision.md         # NATS Core vs JetStream vs SSE Hub
│   ├── architecture.md          # System architecture diagram
│   └── getting-started.md       # From clone to running
├── references/
│   ├── templ/rules.md
│   ├── datastar/
│   │   ├── patterns.md
│   │   ├── pitfalls.md
│   │   ├── toast.md
│   │   └── versus_javascript.md
│   ├── daisyui/datastar-integration.md
│   ├── nats/
│   │   ├── when-to-use-jetstream.md
│   │   └── jetstream-patterns.md    # KV, streams, presence, whiteboard
│   ├── queue/
│   │   ├── goqite-patterns.md
│   │   ├── sse-hub-patterns.md
│   │   ├── nats-workflow-patterns.md
│   │   └── workflow-decision.md
│   ├── database/
│   ├── secrets/age-patterns.md
│   ├── deploy.md
│   ├── ci/docker-cache.md
│   └── llm-streaming.md
├── .air.toml                     # Air live-reload config
├── .golangci.yml                 # Linter config
├── Makefile                      # build, test, lint, dev, docker-image
├── Dockerfile                    # Multi-stage Docker build
├── go.mod
├── AGENTS.md                     # Project-specific agent instructions
└── PLAN.md                       # This plan (removed in user projects)
```

### 3B. CLI Tool (`cali-new`)

**What it is:** A standalone Go CLI that asks **educated questions** with context, then customizes the template.

```
$ cali-new my-project

  ┌── cali-go-stack ──────────────────────────────┐
  │                                                 │
  │  Welcome! Let's set up your Go web app.         │
  │  Each question includes context to help you     │
  │  make an informed decision.                     │
  │                                                 │
  └─────────────────────────────────────────────────┘
```

#### CLI Questions (with inline context)

**Q1: Module path**
```
? Go module path: github.com/me/my-project
```

**Q2: Task queue**
```
? How do you handle background jobs?

  ▸ goqite (recommended) — SQLite-based queue, ~18.5k msg/s
    Fire-and-forget, SSE streaming via Hub, short-lived jobs.
    Same binary, no external dependencies.
    Best for: LLM calls with streaming, send email, resize image.

    none — Skip queue entirely
```

**Q3: Workflow engine**
```
? Do you need durable multi-step workflows?

  ▸ none (recommended) — Keep it simple
    goqite handles single-step jobs. Add workflows later if needed.

    turbine — Durable workflows in PocketBase SQLite
    Multi-step, resume after crash, embeds in PB DB.
    WithName() decouples step names from Go function names
    (safe against LLM rewrites).
    Best for: 5-step onboarding, report pipeline, webhook integration.

    go-workflows — Full Temporal-like engine
    Mature (500★, 4.5y), signals, child workflows, diagnostics UI.
    Needs extra DB (SQLite/PG/Redis).

    ebind — NATS-native DAG
    Function-first, multi-worker, requires NATS.
```

**Q4: Multi-user real-time collaboration (NATS JetStream)**
```
? Do you need multiple users to see and modify the same state live?

  ▸ no (recommended) — SSE Hub handles 1→N for the current user
    Single-user-per-session. No extra infrastructure.

    jetstream — NATS JetStream (embedded, persistent pub/sub + KV)
    ╔══════════════════════════════════════════════════════════════╗
    ║ NATS JetStream is a third layer — it complements goqite     ║
    ║ and turbine; it does not replace either. All 3 can coexist.  ║
    ╚══════════════════════════════════════════════════════════════╝

    Multi-publisher, multi-subscriber. Embeds in the same binary.

    What it enables:
    ┌─────────────────────┬──────────────────────────────────────┐
    │ Collaborative       │ Fabric.js + JetStream KV: every      │
    │ whiteboard          │ stroke updates the canvas state.     │
    │                     │ Stream: action history (undo).       │
    │                     │ Late joiners read KV for current      │
    │                     │ state + Stream for history.          │
    ├─────────────────────┼──────────────────────────────────────┤
    │ Room presence       │ Who's online, join/leave,             │
    │                     │ "typing...", current step            │
    ├─────────────────────┼──────────────────────────────────────┤
    │ Shared UI           │ Multiple supervisors see the same     │
    │                     │ live view (cursor, zoom, state)      │
    ├─────────────────────┼──────────────────────────────────────┤
    │ Event sourcing      │ Every action lands in an immutable   │
    │                     │ stream. Useful for audit, replay,    │
    │                     │ multi-session analytics.             │
    └─────────────────────┴──────────────────────────────────────┘

    NATS Core vs JetStream:
      ┌────────────────┬──────────────────┬────────────────────┐
      │ Need           │ NATS Core        │ JetStream          │
      ├────────────────┼──────────────────┼────────────────────┤
      │ Broadcast 1→N  │ ✅ Latency <1ms   │ ✅ (via stream)    │
      │ History        │ ❌                │ ✅ Durable streams │
      │ Work queues    │ ❌                │ ✅ Consumer groups │
      │ Key-Value      │ ❌ (3rd party KV) │ ✅ Built-in KV     │
      │ Late joiners   │ ❌ (only future)  │ ✅ Replay from seq │
      │ Low latency    │ ✅ Best           │ ✅ (slightly more) │
      └────────────────┴──────────────────┴────────────────────┘

    When to choose Core instead of JetStream (CLI option below):
      - You only need ephemeral broadcast (e.g., notify all users
        about an event and don't care who missed it)
      - Ultra-low latency is critical
      - You do not need history, KV, or late joiners
```

**Q5: LLM integration**
```
? Do you need LLM capabilities?

  ▸ yes — Include GoAI (recommended)
    Tools, structured output, streaming, MCP.
    Supports OpenAI, OpenRouter, Groq, Ollama, Custom.
    Includes SSE streaming helpers for Datastar.

    no — Skip LLM entirely
```

**Q6: Deploy target**
```
? Deploy target:

  ▸ docker — Docker image + compose (recommended)
    Multi-stage build, ~15MB distroless image.
    Works with any Docker host.

    vercel — Vercel (Go functions)
    Serverless, limited binary size.

    none — Manual deploy
```

**Q7: Secrets**
```
? Secrets management:

  ▸ age — age + ~/.secrets/ (recommended)
    3-layer model: env vars → ~/.secrets/ (encrypted) → provider dashboard.
    Single binary, no external service.
    Best for: 1-2 devs, <20 secrets.

    env — Plain env vars only
    Simple, good for dev.

    sops — SOPS + age (git-backed)
    Encrypted in git, CI-friendly.

    doppler — Doppler (multi-team)
    External service, audit, team management.
```

#### How the CLI Works

1. **Download/extract template** — fetch latest template from GitHub releases or copy from embedded version
2. **Apply decisions** — set build tags (`jetstream` tag for NATS, `turbine` tag for workflows), remove unnecessary files
3. **Replace module path** — `sed`/`goreplace` `github.com/cali/cali-go-stack` → user's module path
4. **Generate secrets** — `bin/init-secrets`, add `AGE_SECRET_KEY` to shell config
5. **Print next steps** — `cd my-project && make dev`

---

## 4. Canonical Project Structure (Default — no JetStream)

```
my-project/
├── .air.toml
├── .golangci.yml
├── Makefile
├── Dockerfile
├── go.mod
├── bin/
│   ├── init-secrets
│   └── datastar-lint
├── cmd/
│   └── web/
│       └── main.go
├── config/
├── db/
│   ├── pocketbase.go
│   ├── repository.go
│   └── seed.go
├── internal/
│   ├── secrets/
│   │   ├── secrets.go
│   │   └── secrets_test.go
│   ├── queue/
│   │   ├── goqite.go
│   │   ├── ssehub.go
│   │   └── workers.go
│   ├── llm/
│   │   ├── goai.go
│   │   └── streaming.go
│   └── datastar/
│       ├── render.go
│       └── signals.go
├── features/
│   └── app/
│       ├── routes.go
│       ├── handlers/
│       ├── components/
│       └── services/
├── web/
│   └── resources/
├── router/
│   └── router.go
├── references/
│   └── (all reference docs from skill)
├── .github/
│   └── workflows/
│       ├── ci.yml
│       └── deploy.yml
└── docs/
    ├── decisions.md
    ├── architecture.md
    └── getting-started.md

# With JetStream (+jetstream build tag), these files activate:
#   internal/nats/
#   ├── embedded.go
#   ├── jetstream.go
#   └── presence.go
```

### What Changes with JetStream Enabled

| Aspect | Without JetStream | With JetStream |
|--------|-------------------|----------------|
| **One-to-one SSE** | goqite SSE Hub | goqite SSE Hub (unchanged) |
| **Multi-user broadcast** | Not available | NATS Core via JetStream |
| **Shared state (KV)** | Not available | JetStream KV |
| **Event history** | Not available | JetStream Stream |
| **Presence** | Not available | JetStream + NATS Core |
| **Binary size** | ~15MB | ~18MB (+3MB NATS server) |
| **Build tag** | (none) | `go build -tags jetstream` |

---

## 5. Key Integration Points

### 5.1 goqite + SSE Hub (Default Task Queue)

The default project includes a fully functional task queue with SSE streaming:

```go
// internal/queue/ssehub.go
// - Register-before-enqueue: consumers register before producers enqueue
// - Replay buffer: last N events buffered for late subscribers
// - Backpressure: slow consumers get dropped, not blocked

// internal/queue/goqite.go
// - Queue setup with separate queue.db
// - Middleware for logging, metrics, recovery
// - Worker pool with graceful shutdown

// internal/queue/workers.go
// - SSE streaming workers: write to SSE Hub, then delete from queue
// - LLM call wrapper: stream tokens via SSE, mark done
```

### 5.2 Turbine (Optional Workflow Engine)

When selected via CLI, turbine is initialized in PocketBase's DB:

```go
// internal/workflow/turbine.go
// - Embeds in PocketBase SQLite (same DB)
// - Uses WithName() for step decoupling
// - Register workflow registries
// - Not present when workflow=none
```

### 5.3 PocketBase as Framework

PocketBase is embedded as a library, **not** as a standalone binary:

```go
// cmd/web/main.go
pocketbase.NewWithConfig(&pocketbase.Config{
    DefaultDB:  dbPath,
    DataDir:    dataDir,
    Encryption: encryptionKey,
    // Custom router, hooks, etc.
})
```

### 5.4 NATS Core (Optional Ephemeral Broadcast)

When only lightweight broadcast is needed (no persistence, no KV), NATS Core runs embedded:

```go
// internal/nats/embedded.go — gated by //go:build jetstream
ns, _ := server.NewServer(&server.Options{
    Port: -1, // random port
})
ns.Start()
nc, _ := nats.Connect(ns.ClientURL())

// NATS Core: ephemeral pub/sub
nc.Publish("room.123.cursor", cursorData)
nc.Subscribe("room.123.cursor", handler)
```

### 5.5 NATS JetStream (Multi-User Real-Time)

When JetStream is enabled (via `//go:build jetstream`), it activates alongside goqite and turbine — **not instead of them**.

```go
// internal/nats/jetstream.go — gated by //go:build jetstream
js, _ := nc.JetStream()

// KV store: shared canvas state, room status, user presence
kv, _ := js.CreateKeyValue(&nats.KeyValueConfig{
    Bucket: "canvas-state",
})
kv.Put("room.123.canvas", canvasJSON)
entry, _ := kv.Get("room.123.canvas")

// Stream: event log for history, audit, replay
js.AddStream(&nats.StreamConfig{
    Name:     "room-123-events",
    Subjects: []string{"room.123.>"},
})
js.Publish("room.123.stroke", strokeData)

// Consumer: real-time push to connected clients
js.Subscribe("room.123.stroke", handler)

// Presence: NATS Core for ephemeral, JetStream KV for current state
nc.Subscribe("room.123.presence", presenceHandler)
kv.Put("room.123.users", usersJSON)
```

**Usage examples in the template:**

| Feature | JetStream Primitive |
|---------|---------------------|
| Collaborative whiteboard | KV for canvas state, Stream for stroke history |
| Room/session presence | KV for active users, Core for join/leave events |
| Shared UI (multi-supervisor) | KV for shared state, Core for cursor broadcast |
| Event sourcing | Stream per room/session |
| Late joiner catch-up | KV read for current state, Stream replay for history |

### 5.6 age Secrets Layer

```go
// internal/secrets/secrets.go
// Called by config.Load() before os.Getenv
// Decrypts ~/.secrets/<project>.env.age if AGE_SECRET_KEY is set
// Silent skip if ~/.secrets/ missing
```

### 5.7 Datastar Lint

```go
// bin/datastar-lint or tools/datastar-lint/main.go
// Validates Datastar attributes in .templ files:
// - data-on:* events reference valid endpoints
// - data-signals is valid JSON
// - No HTML in Go source (enforce Templ rule)
```

### 5.8 CI/CD Pipeline

```yaml
# .github/workflows/ci.yml
# - golangci-lint
# - datastar-lint
# - go test -tags jetstream ./...  (runs with JetStream tag)
# - go test ./... (runs without)
# - go build ./...

# .github/workflows/deploy.yml
# - Docker build + push (Docker Hub or GHCR)
# - Deploy via SSH + Docker compose
# - age-secret key injected via GitHub Actions secrets
```

---

## 6. Files That Change Per Decision

| Decision | Files Added | Files Removed/Stubbed |
|----------|-------------|-----------------------|
| Queue=none | — | `internal/queue/`, `references/queue/` |
| Workflow=turbine | `internal/workflow/turbine.go`, `features/workflows/` | — |
| Workflow=go-workflows | `internal/workflow/goworkflows.go` | — |
| Workflow=none | — | `internal/workflow/` |
| Multi-user=jetstream | `internal/nats/embedded.go`, `internal/nats/jetstream.go`, `internal/nats/presence.go` | — (adds to goqite, doesn't replace) |
| LLM=no | — | `internal/llm/`, `internal/zenflow/`, `references/llm-streaming.md` |
| LLM=zenflow | `internal/zenflow/` | — |
| Voice=yes | `internal/voice/`, `references/voice-ai/` | — |
| Secrets=env | `internal/secrets/secrets.go` (stub) | `bin/init-secrets` |

### The Core Stack (Always Present)

These files are included regardless of any decision:

```
cmd/web/main.go
config/*.go
db/*.go
internal/secrets/*.go
internal/datastar/*.go
web/resources/*
features/app/*
router/router.go
```

### JetStream: A Special Case

JetStream is the only option that **adds files without removing any**. This is intentional:

- goqite stays for fire-and-forget + SSE streaming
- JetStream adds multi-user real-time on top
- They communicate via NATS client, not shared memory
- Build tag `jetstream` gates the NATS server embed + JetStream API

---

## 7. Next Steps to Build

### Phase 1: Template Repository (MVP)

- [x] 1. Create `github.com/calionauta/cali-go-stack` — GitHub Template Repository
- [x] 2. Scaffold the **default project** (goqite + SSE Hub, GoAI, age secrets)
- [x] 3. Add build-tag gated `internal/nats/` package for JetStream activation
- [x] 4. Add `Makefile` with `templ`, `build`, `test`, `lint`, `dev`, `docker-image` targets
- [x] 5. Add `.air.toml` with hot reload for `.go`, `.templ`, `.yaml`
- [x] 6. Add `.golangci.yml` with strict rules
- [x] 7. Add `Dockerfile` with multi-stage build (distroless)
- [x] 8. Populate `references/` directory (by topic)
- [x] 9. Write `docs/getting-started.md`, `docs/decisions.md`, and `docs/nats-decision.md`
- [x] 10. Set up CI via `.github/workflows/ci.yml` — matrix across build tags
- [x] 11. Add Turbine as build-tag-gated workflow layer (`-tags turbine`)
- [x] 12. Smoke test verified by CI matrix (`go test -race -tags ""` + `-tags jetstream` + `-tags turbine` + `-tags "jetstream turbine"` all green) plus the integration tests in `features/todo/integration_test.go` and `internal/workflow/turbine_test.go`
- [x] 13. `datastar-lint` not added — the tool is not yet published as a Go binary. Tracked as a future enhancement, not a blocker.

### Phase 2 & 3: Out of Scope

Phase 2 (`cali-new` CLI scaffolder) and Phase 3 (branch variants like `with-jetstream`) were considered and **explicitly deferred**:

- The GitHub "Use this template" button already provides clone + customize without a custom CLI.
- The build-tag matrix (`""`, `jetstream`, `turbine`, `jetstream+turbine`) gives users the same choice a branch variant would, with less maintenance.
- A separate scaffolder would require its own release pipeline, versioning, and documentation — a 3x cost increase for a feature with minimal adoption.

**If a future use case demands them**, revisit Phase 2/3 with concrete user data first. Don't build speculatively.

---

## 8. Skill → Template Hygiene

| Aspect | Skill (`cali-coding-go-stack`) | Template (`cali-go-stack`) |
|--------|-------------------------------|---------------------------|
| Frequency | Updated as ecosystem evolves | Updated on breaking changes |
| Content | Decision trees, reference docs, patterns, pitfalls, test cases | Working code, config files, docs subset |
| Audience | AI agents (agents.md) | Developers (humans + AI agents) |
| Gen | `.agents/skills/cali-coding-go-stack/SKILL.md` | GitHub Template Repository |
| Update cycle | As needed (knowledge) | Versioned releases |

---

## 9. Resolved Questions

**Q: Should `cali-new` be a standalone binary or a `go run` script?**
A: Neither — Phase 2 deferred. GitHub's "Use this template" + build tags cover the use case.

**Q: How to handle template versioning?**
A: GitHub Releases with semantic tags. `make docker-image` already pushes multi-arch images to `ghcr.io/calionauta/cali-go-stack`. Source tarballs come for free with each release.

**Q: What about AGENTS.md — pre-populated with Cali skills or empty?**
A: Reference only. AGENTS.md installs skills via `npx skills add https://github.com/calionauta/agent-sync-public/tree/main/skills/cali-coding-go-standards --yes` rather than bundling them. Keeps the template decoupled from the skills repo's release cycle.

**Q: Should `cali-new` support `--branch` to select branch variants directly?**
A: N/A — no `cali-new`, no branches. Build tags are the equivalent.

**Q: How to verify the template works on macOS, Linux, Windows?**
A: CI runs on `ubuntu-latest` (Linux). Local `make dev` runs on macOS during development. Windows is **not supported** — `cgo` exclusions and the ncruces SQLite driver are POSIX-only by design. Document this in the README's prerequisites section rather than chasing cross-platform parity.

**Q: How to demo JetStream capabilities without a real second browser?**
A: Out of scope for the template. Users who enable JetStream write their own demo or feature test. The template provides the wiring (`internal/nats/` + `cmd/web/nats.go`), not the demo.

---

*Generated from `cali-coding-go-stack` skill v2 — goqite default, turbine for workflows, JetStream for multi-user real-time (complementary, not alternative).*
