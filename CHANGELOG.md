# Changelog

All notable changes to this template are documented here. The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

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
