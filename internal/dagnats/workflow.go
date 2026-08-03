// SCOPE:layer=infra,removal=plugin — DagNats durable workflow engine
package dagnats

// ExampleTodoTexts are the titles the onboarding workflow creates as
// example todos once the user's first todo resumes the flow. They live
// here (Go, not the workflow JSON) because DagNats v0.0.5 does NOT
// deliver per-step config to workers — the engine's live dispatch path
// (TaskPublisher.doPublish) builds the TaskPayload without the Config
// field, so a worker never sees a step's `config` block. The titles
// therefore ride the DAG's input/output chain instead: StartRun
// receives {"user":..., "todos":[...]}, the root step threads it
// forward, and each create-todo step pops one title and passes the
// remainder to the next step. This is durable (each step's output is
// persisted with the run) and reachable from any worker.
var ExampleTodoTexts = []string{
	"Explore the techstack diagnostics panel",
	"Try the Queue + retry demo (simulated LLM)",
	"Open the app settings via the gear icon",
}

// OnboardingWorkflowJSON is the durable onboarding workflow definition.
// It is declarative JSON (not Go), so refactoring the Go handlers below
// never breaks an in-flight run — the workflow references task NAMES
// ("onboarding-greet", "onboarding-await-first-todo",
// "onboarding-create-todo", "onboarding-finalize"), not Go function
// symbols.
//
// Steps:
//  1. onboarding-greet            — welcomes the user (paced).
//  2. onboarding-await-first-todo — a normal task that BLOCKS in-process
//     on ctx.WaitForSignal("first-todo"). This is the dagnats-documented
//     resume pattern: a step handler waits for an external signal, and
//     the app signals it when the user creates their first todo. No
//     polling, no in-memory flag — the durable run simply suspends until
//     the signal arrives (or a 50m timeout).
//  3. onboarding-create-todo (x3, sequential) — each creates one example
//     todo in the main PocketBase collection via the worker handler,
//     once the user's first todo has resumed the flow.
//  4. onboarding-finalize         — marks the run complete.
//
// Data flow: the run input is {"user": <owner>, "todos": [...titles]}.
// The root step (greet) forwards the whole input as its output; every
// downstream step's Input is its single dependency's output (DagNats'
// ResolveInput passes a single dep's output through unchanged). Each
// create-todo handler pops todos[0], creates that todo scoped to the
// owner, and completes with the remaining slice — so the three steps
// create three distinct example todos. DagNats v0.0.5 never delivers
// step config/metadata to workers (the live publish path omits both
// TaskPayload.Config and TaskPayload.Metadata), which is why the
// titles travel as input/output, not as step config.
//
// The worker handlers (registered in cmd/web/dagnats.go) write to the
// app's own SQLite DB, so the todos appear in the user's list exactly as
// if they had typed them. Progress is streamed to the UI via the
// broadcaster by the HTTP handler polling the run status.
const OnboardingWorkflowJSON = `{
  "name": "onboarding",
  "version": "1.0",
  "steps": [
    {
      "id": "greet",
      "task": "onboarding-greet",
      "depends_on": []
    },
    {
      "id": "await-first-todo",
      "task": "onboarding-await-first-todo",
      "depends_on": ["greet"]
    },
    {
      "id": "todo-1",
      "task": "onboarding-create-todo",
      "depends_on": ["await-first-todo"]
    },
    {
      "id": "todo-2",
      "task": "onboarding-create-todo",
      "depends_on": ["todo-1"]
    },
    {
      "id": "todo-3",
      "task": "onboarding-create-todo",
      "depends_on": ["todo-2"]
    },
    {
      "id": "finalize",
      "task": "onboarding-finalize",
      "depends_on": ["todo-3"]
    }
  ]
}`
