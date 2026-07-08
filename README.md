# cali-go-stack

A starting point for web projects in Go. Single binary, zero SaaS, LLM-friendly.

---

Every Go web project I start ends up in the same conversation: pick a database, queue, template engine, auth, deploy… and the project stalls on the decision, not the code.

This template is my attempt at a starting point that already resolves those choices without locking you into a closed ecosystem.

## What's in the package

Everything you need to build a modern web app, in a single binary:

| Layer | Choice | Why |
|-------|--------|-----|
| **Language** | Go 1.26 | Fast compilation, easy deploy, lean runtime |
| **Database + Auth + API** | [PocketBase](https://pocketbase.io) (embedded, on `ncruces/go-sqlite3`) | Zero-config auth, REST, admin UI, file storage — all in SQLite |
| **Templating** | [Templ](https://templ.guide) | Type-safe Go components, generated at build time |
| **Reactive UI** | [Datastar](https://data-star.dev) (SSE) | Server-rendered over SSE, ~12 KiB JS client, no JS build step |
| **CSS** | [DaisyUI v5](https://daisyui.com) + TailwindCSS | Ready components, customizable, ~34kB minified |
| **Task queue** | [goqite](https://github.com/maragudk/goqite) + SSE Hub | Background jobs streamed to the browser, no Redis |
| **Retries** | [avast/retry-go v4](https://github.com/avast/retry-go) | Exponential backoff with jitter, no boilerplate |
| **Workflows** | [Turbine](https://turbine.yakir.io) | Multi-step durable workflows embedded in PocketBase SQLite (build-tag gated) |
| **LLM SDK** | [GoAI](https://github.com/zendev-sh/goai) | Any provider: OpenAI, Anthropic, Groq, Ollama… |
| **Real-time** | [NATS JetStream](https://nats.io) (opt-in) | Multi-user real-time, only enabled when you need it |
| **Secrets** | [age](https://age-encryption.org) + `~/.secrets/` | Local encryption, no vault, no cloud |
| **IDs** | [google/uuid](https://github.com/google/uuid) | Stable request/job IDs |
| **Live reload** | [Air](https://github.com/air-verse/air) | `make dev` regenerates templ and restarts the binary |
| **Linting** | [golangci-lint](https://golangci-lint.run) | `errcheck`, `staticcheck`, `gosec`, `revive`, `gocritic` (see `.golangci.yml`) |
| **CI/CD** | GitHub Actions | `ci.yml` (lint + test + build, tag matrix `""`/`jetstream`/`turbine`) + `deploy.yml` (multi-arch Docker to ghcr.io, runs on `master`) |
| **Container** | distroless `static-debian12:nonroot` | No shell, no package manager, ~20MB final image |

Fully CGO-free. Single binary. `make build` and you're done.

## Stack in layers, not silos

One thing that bothered me most in ready-made templates is that they assume **one** solution per problem. In reality you need a queue **and** workflows **and** real-time — each for a different thing.

This template solves it with **three complementary async layers**:

```
goqite    → background jobs + SSE to the browser (default, always on)
turbine   → durable multi-step workflows (opt-in, build tag)
JetStream → multi-user real-time (opt-in, build tag)
```

They coexist in the same binary. They don't compete.

## The example: Todo App with SSE

The template ships with a working Todo App:

- Full CRUD via PocketBase
- Reactive UI with Datastar + DaisyUI
- Real-time SSE streaming (register-before-enqueue, replay buffer for late subscribers, backpressure on slow clients)
- Stacked toast notifications (auto-dismiss, manual close, progress bar)
- Async jobs: `handleCreate` enqueues a `todo_created` job; a worker picks it up and streams a success toast to the right browser tab via the SSE Hub (`clientID` routing)
- Retries with exponential backoff and jitter (`internal/queue/retry.go`, retry-go v4) — SSE-aware: a retry emits a `lastRetry` signal so the UI can show "retrying…"
- `WelcomeOnboarding` Turbine workflow (with `-tags turbine`) that creates 3 example todos via durable steps — kill the server mid-run, restart, watch it resume at the last incomplete step
- Tests run with `-race`

Enough to understand the pattern and start your own feature module.

## Who this template is for

- **You who get tired of configuring the same stack over and over**
- **You who want a single binary for deploy, with no Redis, Postgres, or SaaS**
- **You who want an LLM client wired in without pulling in a whole orchestration framework** — `internal/llm` wraps GoAI (any provider: OpenAI, Anthropic, Groq, Ollama) behind an injectable interface, callable from handlers. It calls a *remote* provider API; it is **not** a local-model runtime.
- **You who prefer server-rendered HTML over 2MB SPAs**

It's not a framework. There's no lock-in. Each piece can be replaced individually.

## Getting started

```bash
git clone https://github.com/calionauta/cali-go-stack.git my-project
cd my-project
make dev
```

Open `http://localhost:8080` and see the Todo App running.

> The default port is `8080` (override with `PORT`). The default branch is `master`.

### Other commands

```bash
make build            # Build binary (default: goqite + SSE Hub)
make build-jetstream  # Build with NATS JetStream (multi-user real-time)
make build-turbine    # Build with Turbine durable workflows
make build-all        # Build with both JetStream + Turbine
make test             # Run tests with race detector (default tags)
make test-jetstream   # Run tests with JetStream tag
make test-turbine     # Run tests with Turbine tag
make check            # Lint + size limits + dead code
make dev              # Live reload with Air
make templ            # Regenerate templ components
make setup            # Install pre-commit hooks
make docker-image     # Build and push multi-arch image to ghcr.io
```

## Structure

```
cmd/web/
  main.go               # Entry point — only initializes and wires pieces
  nats.go               # NATS JetStream bootstrap (build-tag gated)
  turbine.go            # Turbine runtime bootstrap (build-tag gated)
  turbine_noop.go       # No-op stub when -tags turbine absent
config/                 # Per-environment config (dev/prod)
db/                     # PocketBase setup + seed
internal/
  secrets/              # age-decrypted secrets loader
  queue/                # goqite + SSE Hub + workers + retry + handler registry
    goqite.go           #   goqite setup, schema (goqite_schema.sql), graceful shutdown
    ssehub.go           #   register-before-enqueue, replay buffer, backpressure
    workers.go          #   worker pool with context cancellation
    retry.go            #   exponential backoff + jitter (retry-go v4)
    handlers.go         #   HandlerRegistry: job-type to handler dispatch
  nats/                 # JetStream (build-tag gated)
  workflow/             # Turbine durable workflows (build-tag gated)
    turbine.go          #   embedded in PocketBase SQLite, WithName step decoupling
  llm/                  # GoAI LLM SDK helpers
  datastar/             # Datastar rendering helpers
features/
  todo/                 # Working example: Todo MVC
    handlers/           #   HTTP + SSE handlers, onboarding (turbine-gated)
    components/         #   Templ components (layout, todo_list, todo_item, toast)
web/resources/          # Static assets (embedded JS)
router/                 # Routes registered on PocketBase
references/             # Reference documentation
docs/                   # Decision logs and guides
```

## License, feedback

This project is open to feedback, PRs, and adaptations. If something doesn't make sense, if the stack doesn't fit your problem, or if you have a better idea — open an issue.

Made with intent to be useful, not to be right.
