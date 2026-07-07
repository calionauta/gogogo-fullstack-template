# Turbine Patterns

Requires `go build -tags turbine`. Set `WORKFLOW_ENABLED=true` at runtime.

## What Turbine Solves

goqite is for single-step, fire-and-forget jobs. When you need **N steps that
form one durable transaction** — and a crash mid-way should not re-run the
expensive steps — use Turbine. Each step's result is recorded in SQLite; on
recovery, recorded steps replay their saved result instead of re-executing.

## Enable in the Binary

```bash
make build-turbine           # single tag
make build-all               # jetstream + turbine
WORKFLOW_ENABLED=true ./cali-go-stack
```

State is persisted under `data/workflow/`, separate from PocketBase's DB.

## Minimal Workflow

```go
// internal/workflow/turbine.go
func Hello(ctx turbine.Context, name string) (string, error) {
    greeting, err := turbine.Do(ctx, func(ctx context.Context) (string, error) {
        return fmt.Sprintf("hello, %s", name), nil
    }, turbine.WithStepName("greet"))
    if err != nil {
        return "", err
    }
    return turbine.Do(ctx, func(ctx context.Context) (string, error) {
        return greeting + " (recorded)", nil
    }, turbine.WithStepName("finalize"))
}
```

## Register + Run

```go
turbine.Register(rt, Hello)
if err := rt.Launch(); err != nil { log.Fatal(err) }

handle, _ := turbine.Run(rt, Hello, "world")
result, _ := handle.GetResult()
```

## Key Rules

- **Steps must be pure** (or side-effect-isolated). They may be replayed on recovery.
- **Step name must be stable** across versions. Renaming invalidates the recorded result.
- **Use `context.Context`, not `turbine.Context`, inside `turbine.Do`** — prevents nested `Do`/`Sleep` calls at compile time.
- **Turbine owns a separate PocketBase app** (`NewStandalone`) — does not share the main app's collections.

## When NOT to Use Turbine

- Single fire-and-forget jobs → use goqite + SSE Hub (cheaper, no overhead).
- Real-time multi-user state → use NATS JetStream KV/Stream.
- Read-mostly data → use PocketBase collections directly.
