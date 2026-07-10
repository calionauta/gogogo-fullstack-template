# gogogo-fullstack-template

<p align="center">
  <img src="web/resources/static/logo.png" alt="gogogo-fullstack-template" width="512">
</p>

A starting point for web projects in Go. Single binary, zero external services, LLM-friendly.

This template is my attempt at a starting point that already resolves those choices without locking you into a closed ecosystem.

> **One binary, one process, one image.** ~30 MB, no shell, no libc, no CDN.
> Runs on `scratch` (or `gcr.io/distroless/static-debian12:nonroot` if you need a debug base). All CSS is compiled at build time via Tailwind v4 + DaisyUI v5 and embedded via `//go:embed` — no JS runtime.

> **Made with intent to be useful, not to be right.** This template optimizes for shipping something that runs today over being philosophically correct. Decisions here are pragmatic, not dogmatic.

---

Every Go web project I start ends up in the same conversation: pick a database, auth, router, reactive UI framework, task queue… and the project stalls at the decisions, installations, and configurations — not the code.

## What's in the package

Everything you need to build a modern web app, in a single binary:

| Layer | Choice | Why |
|-------|--------|-----|
| **Language** | Go 1.26 | Fast compilation, easy deploy, lean runtime |
| **Database + Auth + API** | [PocketBase](https://pocketbase.io) (embedded, on `ncruces/go-sqlite3`) | Zero-config auth, REST, [admin UI at `/_/`](https://<your-domain>/_/), file storage — all in SQLite |

> **Why `ncruces/go-sqlite3`?** It's the pure-Go (no cgo) SQLite build that bundles the extensions this template leans on — FTS5, `spellfix1`, `unicode` collations — which the stock driver leaves out. That's why the `//go:build`/driver init in `db/pocketbase.go` pins it instead of `modernc.org/sqlite` directly.
| **Templating** | [Templ](https://templ.guide) | Type-safe Go components, generated at build time |
| **Reactive UI** | [Datastar](https://data-star.dev) (SSE) | Server-rendered over SSE, single ~12 KiB client. CSS built once via Tailwind v4 CLI; no JS runtime. |
| **CSS** | [DaisyUI v5](https://daisyui.com) + TailwindCSS | Ready components, customizable, ~34kB minified |
| **Task queue** | [goqite](https://github.com/maragudk/goqite) + SSE Hub | Background jobs streamed to the browser, no Redis |
| **Retries** | [avast/retry-go v4](https://github.com/avast/retry-go) | Exponential backoff with jitter, no boilerplate |
| **Workflows** | [DagNats](https://github.com/danmestas/dagnats) | Multi-step durable workflows as declarative JSON over NATS JetStream (build-tag gated) |
| **LLM SDK** | [GoAI](https://github.com/zendev-sh/goai) | Any provider: OpenAI, Anthropic, Groq, Ollama… |
| **Real-time** | [NATS JetStream](https://nats.io) (opt-in) | Multi-user real-time, only enabled when you need it |
| **Secrets** | [age](https://age-encryption.org) + `~/.secrets/` | Local encryption, no vault, no cloud |
| **IDs** | [google/uuid](https://github.com/google/uuid) | Stable request/job IDs |
| **Live reload** | [Air](https://github.com/air-verse/air) | `make dev` regenerates templ and restarts the binary |
| **Linting** | [golangci-lint](https://golangci-lint.run) | `errcheck`, `staticcheck`, `gosec`, `revive`, `gocritic` (see `.golangci.yml`) |
| **CI/CD** | GitHub Actions | `ci.yml` (lint + test + build, tag matrix `""`/`jetstream`/`dagnats`) + `deploy.yml` (multi-arch Docker to ghcr.io, runs on `master`) |

## Stack in layers, not silos

Most templates force you to pick one async strategy — usually a queue, sometimes a workflow runtime, rarely both. Real apps need **a queue for background jobs**, **a workflow runtime for durable multi-step processes**, and **a real-time layer for cross-client state** — each solving a different problem. This template ships all three as build-tagged layers so you only pay for what you use.

This template solves it with **three complementary async layers**:

```
goqite    → background jobs + SSE to the browser (default, always on)
dagnats   → durable multi-step workflows as JSON (opt-in, build tag)
JetStream → multi-user real-time (opt-in, build tag)
```

They coexist in the same binary. They don't compete.

The `goqite` queue is the **only async layer with a build tag off** — it's core and always on. DagNats and JetStream are opt-in heavyweight features: enable them when you actually need durable multi-step workflows or multi-instance realtime. The default build (`go build ./cmd/web`) is the recommended starting point for almost every project.

## The example: Todo App with SSE

The template ships with a working Todo App:

- Full CRUD via PocketBase
- Reactive UI with Datastar + DaisyUI
- Real-time SSE streaming. Mutations (`create`/`toggle`/`delete`) publish to `nats.TodoBroadcaster`; default build fans out via the in-process SSE Hub (single-instance), `-tags jetstream` fans out via a durable JetStream stream (multi-instance). Late joiners get a replay buffer; slow clients are dropped, never block the producer.
- Stacked toast notifications (auto-dismiss, manual close, progress bar)
- Async jobs: `handleCreate` enqueues a `todo_created` job; a worker picks it up and streams a success toast to the right browser tab via the SSE Hub (`clientID` routing)
- Retries with exponential backoff and jitter (`internal/queue/retry.go`, retry-go v4) — SSE-aware: a retry emits a `lastRetry` signal so the UI can show "retrying…"
- `WelcomeOnboarding` DagNats workflow (with `-tags dagnats`) that creates 3 example todos via durable steps — kill the server mid-run, restart, watch it resume at the last incomplete step. The workflow is declarative JSON (`internal/dagnats/workflow.go`), so renaming Go handlers never orphans an in-flight run.
- **Admin unlock** via `age` + `~/.secrets/`. The Todo example wires a master-password path: when `ADMIN_UNLOCK_TOKEN` is set (in the age-encrypted secrets file), the UI shows a "Clear all" form; the handler compares constant-time and clears all todos on match. Demonstrates the age flow end-to-end.
- **AI suggest** via GoAI. When `GOAI_API_KEY` is set, the input gets a "Suggest" button that enqueues an async suggest job (see queue below) and streams the 3 completions back via SSE. Provider is configurable (Groq, OpenRouter, Together, Cloudflare, OpenAI). Retries with exponential backoff; same `internal/queue/retry.go` used by the SSE toast path. For a **keyless** demo of the exact same queue + retry path, set `SIMULATE_LLM=true`: a "Suggest (simulated)" button enqueues a job that hits an in-process fake LLM scripting 500 → 200 + delay, so you can watch the retry feedback toasts (enqueued → attempt failed → slow → result).
- Tests run with `-race`

> **This is the contract you should imitate when adding a new feature:**
> 1. **Pure HTTP + Datastar** for the user-facing surface.
> 2. **goqite job** for any work that takes more than ~50ms (LLM, email, exports).
> 3. **SSE toast** for async feedback to the originating client via `clientID` routing.
> 4. **age-encrypted secret** if the feature needs a credential.
> Every existing feature (toast on create, AI suggest, admin unlock, DagNats onboarding) follows this exact shape.

Enough to understand the pattern and start your own feature module.

## PocketBase admin UI (built in)

PocketBase ships a full admin UI for free — it runs at **`/_/`** on the same domain. Once deployed, your admin UI lives at `https://<your-domain>/_/`.

What you get out of the box:

- **Visual data browser** for every collection (todos, users, etc.) with sort/filter/CSV export
- **REST + JS SDK playground** for the API endpoints PocketBase generated from your schema
- **Superuser management** (create the first one via the install link printed in the server logs)
- **File storage** (S3-compatible uploads, images, attachments)
- **Backups** (SQLite snapshot, download + restore)
- **Logs** (requests, errors, slow queries)

This is **not a custom admin panel** — it's the upstream PocketBase UI, embedded in the same binary, on the same port. No extra service to deploy, no extra auth to wire. For production, point a Cloudflare Tunnel / Caddy ingress at the same `/_/` path and lock it down (IP allowlist, oauth2-proxy in front, or just PocketBase's own superuser auth).

## Try it live

A running deployment of this exact template is live. You can touch every feature from the README without cloning:

| What | URL | What you can do |
|------|-----|-----------------|
| **Todo demo app** | `https://<your-demo-domain>/` | Log in with the seeded demo account (`demo@demo.app` / `demo`), then watch the **event-driven DagNats onboarding** kick off automatically: step 1 greet → step 2 awaits your first todo (blocks on a `WaitForSignal`) → create a todo → the signal resumes the flow → steps 3–5 create 3 example todos + complete. Per-user (only your browser session is scoped to you, not global), event-driven (the create-todo event signals the waiting step), durable (a crash mid-wait resumes on restart because the signal KV retains the value). With `GOAI_API_KEY` set, the AI "Suggest" button appears; with `SIMULATE_LLM=true`, a keyless "Suggest (simulated)" button exercises the same queue + retry path against an in-process fake LLM (error → retry → slow). Open two browser tabs to see the **self vs. remote** animations: items you create slide in from the top with a primary tint; items other tabs create slide in from the left with an info pulse + "from someone else" indicator. Delete and reorder use the browser View Transitions API for a free cross-fade. |
| **Live PocketBase admin dashboard** | `https://<your-demo-domain>/_/` | Open the embedded PocketBase UI to browse the `todos` + `users` collections, run the REST/JS SDK playground, and inspect logs. The demo's `users` collection is **locked** — visitors can log in as the demo user but cannot create or delete accounts through the API or this dashboard (only the superuser can). |

> The demo runs `-tags dagnats` (durable workflows on `:8090`) combined with `-tags jetstream` (multi-user realtime). The two share a **single embedded NATS** on `:4222` — DagNats boots it and the realtime broadcaster attaches to it, so there is only one NATS process in the binary. To stand up your own, see [Deploy](#deploy).

## Free LLM providers (OpenAI-compatible, no card required)

The GoAI client uses the `compat` provider, so any OpenAI-compatible endpoint works. Set `GOAI_BASE_URL` + `GOAI_API_KEY` + `GOAI_MODEL` in your environment or secrets file.

| Provider | Free tier | How to get a key |
|----------|-----------|------------------|
| **[Groq](https://console.groq.com)** | Generous free tier, very fast inference. Recommended default. | Sign up → API Keys → copy |
| **[OpenRouter](https://openrouter.ai)** | Several free models (smaller). | Keys page |
| **[Together AI](https://api.together.xyz)** | $5 free credit, no card. | API Keys |
| **[Cloudflare Workers AI](https://developers.cloudflare.com/workers-ai/)** | 10k neurons/day free. | Account → Workers AI |
| **OpenAI** (default) | Pay-as-you-go. | platform.openai.com |

Example: switch to Groq in your env:

```bash
GOAI_BASE_URL=https://api.groq.com/openai/v1
GOAI_MODEL=llama-3.3-70b-versatile
GOAI_API_KEY=gsk_...
```

If `GOAI_API_KEY` is empty, the AI suggest route is **not registered** and the UI button is hidden. The Todo example keeps working — AI is opt-in, not required.

**Note on truly keyless services:** [mlvoca.com](https://mlvoca.github.io/free-llm-api/) offers a free, keyless LLM API, but it exposes the **Ollama** API shape (`POST /api/generate`), not OpenAI Chat Completions. GoAI's `compat` provider speaks OpenAI; a thin Ollama-shim would be needed to use mlvoca. The mlvoca terms also forbid commercial use, so it would not be a sensible default for a reusable template.

## Who this template is for

- **You who get tired of configuring the same stack over and over**
- **You who want a single binary for deploy, with no Redis, Postgres, or SaaS**
- **You who want an LLM client wired in without pulling in a whole orchestration framework** — `internal/llm` wraps GoAI (any provider: OpenAI, Anthropic, Groq, Ollama) behind an injectable interface, callable from handlers. It calls a *remote* provider API; it is **not** a local-model runtime.
- **You who prefer server-rendered HTML over 2MB SPAs**

It's not a framework. There's no lock-in. Each piece can be replaced individually.

## Getting started

Use this template (green **Use this template** button above) or clone it:

```bash
git clone https://github.com/calionauta/gogogo-fullstack-template.git my-project
cd my-project
make dev
```

Open `http://localhost:8080` and see the Todo App running.

> The default port is `8080` (override with `PORT`). The default branch is `master`.

### Other commands

```bash
make build            # Build binary (default: goqite + SSE Hub)
make build-jetstream  # Build with NATS JetStream (multi-user real-time). JetStream is enabled automatically under this tag — no extra env needed; set NATS_ENABLED=false to opt out.
make build-dagnats    # Build with DagNats durable workflows (JSON over NATS). DagNats is enabled automatically under this tag — no extra env needed; set DAGNATS_ENABLED=false to opt out. The engine listens on DAGNATS_HTTP_ADDR (default 127.0.0.1:8090). When built WITH -tags jetstream, the realtime broadcaster reuses the NATS that DagNats already owns on :4222 (single NATS).
make build-all        # Build with both JetStream + DagNats
make test             # Run tests with race detector (default tags)
make test-jetstream   # Run tests with JetStream tag
make test-dagnats     # Run tests with DagNats tag
make check            # Lint + size limits + dead code + CSS check
make css              # Build app.min.css (Tailwind v4 + DaisyUI v5)
make dev              # Live reload with Air
make templ            # Regenerate templ components
make setup            # Install pre-commit hooks
make docker-image     # Build and push multi-arch image to ghcr.io
```

### Build pipeline

The single compile step **outside** `go build` is the CSS build. It runs
once in the Docker builder stage and the result is embedded into the Go
binary via `//go:embed` — there is no runtime CSS build step, no JS
runtime, and no CDN.

```
src/css/input.css  →  tailwindcss v4 CLI  →  web/resources/static/app.min.css
                                                    │
                                                    └─ //go:embed in the Go binary
```

The pre-commit hook regenerates `app.min.css` automatically whenever
`.templ` or `.go` files change, and `make check` includes a `css-check`
step that fails the gate if the working CSS file is out of date.

## Deploy to your own box

The default workflow is to **clone + `make dev`** for local work. For a permanent
demo, the project ships a production deploy workflow that publishes to a
server of your choosing (recommended: a small Linux box + Tailscale + a
Cloudflare-tunneled domain). No registry, no cold starts, full control.

### Server layout (multi-project standard)

Pick a directory on your server (e.g. `/opt`) and follow this layout for
**every** project that adopts the pattern — siblings share the same shape:

```
/opt/
└── gogogo-fullstack-template/                  ← this project
    ├── bin/
    │   ├── gogogo-fullstack-template             ← current binary (chmod 755)
    │   └── gogogo-fullstack-template.previous    ← prior binary, kept for fast rollback
    ├── compose/
    │   └── docker-compose.prod.yml
    ├── env/
    │   └── .env                       ← non-secret env (DATABASE_URL, APP_URL, ...)
    ├── secrets/
    │   └── gogogo-fullstack-template.env         ← mode 600, regenerated every deploy from GH Secrets
    ├── data/
    │   └── pb_data/                    ← persistent volume, survives restarts
    ├── repo/                           ← git clone of this repo (for re-syncing on each deploy)
    ├── scripts/
    │   └── deploy-prod.sh             ← the on-server deploy runner
    └── README.md                       ← operator's guide (link to this section)

/opt/<other-project>/                  ← siblings follow the same shape
    ├── bin/
    ├── compose/
    ├── env/
    ├── secrets/
    └── data/
```

### First-time setup on the server

1. Install Docker + create a `deploy` user with SSH key access.
2. Add the box to your Tailscale tailnet.
3. Configure a Cloudflare Tunnel that routes your domain (e.g. `fullstack.example.com`)
   to the Tailscale hostname on port 8080.
4. Clone the repo at `/opt/gogogo-fullstack-template/repo/` and run `./repo/scripts/setup-server.sh`
   (we ship this in a follow-up; the manual steps are: `mkdir -p bin compose env secrets data/pb_data scripts`).
5. Add the GitHub Actions secrets (see `.github/workflows/deploy.yml` for the full list).

### After setup, every push to `master` deploys

The workflow at `.github/workflows/deploy.yml` runs on every push to `master` and:

1. Builds the project (lint + race tests + CSS build).
2. Builds the production Docker image (linux/amd64 scratch) in the GH Action runner.
3. SCPs the new binary to the server as `gogogo-fullstack-template.new` (atomic swap).
4. Writes the secrets file (`/opt/gogogo-fullstack-template/secrets/gogogo-fullstack-template.env`) with mode 600.
5. SSHes in and runs `scripts/deploy-prod.sh` which:
   - Atomically renames `gogogo-fullstack-template.new` → `gogogo-fullstack-template` and keeps the old binary as `.previous`.
   - Restarts the container via `docker compose -f docker-compose.prod.yml up -d`.
   - Waits up to 30s for `/health` to return 200.
6. Prints the new container status + last 20 log lines for confirmation.

Secrets are **never stored long-term on the server**: every deploy
re-renders `/opt/gogogo-fullstack-template/secrets/gogogo-fullstack-template.env` from GitHub
Actions secrets. The file is `chmod 600`, owned by the `deploy` user,
and overwritten on every run — there is no history of secrets on disk.

## Structure

```
cmd/web/
  main.go               # Entry point — only initializes and wires pieces
  nats.go               # NATS JetStream bootstrap (build-tag gated)
  dagnats.go            # DagNats runtime bootstrap (build-tag gated)
  dagnats_noop.go       # No-op stub when -tags dagnats absent
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
  dagnats/              # DagNats durable workflow client + onboarding JSON (build-tag gated)
  llm/                  # GoAI LLM SDK helpers
  datastar/             # Datastar rendering helpers
features/
  todo/                 # Working example: Todo MVC
    handlers/           #   HTTP + SSE handlers, onboarding (dagnats-gated)
    components/         #   Templ components (layout, todo_list, todo_item, toast)
web/resources/          # Static assets (embedded JS)
router/                 # Routes registered on PocketBase
references/             # Reference documentation
docs/                   # Decision logs and guides
```

## License, feedback

This project is open to feedback, PRs, and adaptations. If something doesn't make sense, if the stack doesn't fit your problem, or if you have a better idea — open an issue.

---

Made with intent to be useful, not to be right. — feedback, PRs, and adaptations welcome.
