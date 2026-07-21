// SCOPE:layer=infra,removal=plugin — DagNats durable workflow engine
package dagnats

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
      "depends_on": ["await-first-todo"],
      "config": {"text": "Explore the techstack diagnostics panel"}
    },
    {
      "id": "todo-2",
      "task": "onboarding-create-todo",
      "depends_on": ["todo-1"],
      "config": {"text": "Try the Queue + retry demo (simulated LLM)"}
    },
    {
      "id": "todo-3",
      "task": "onboarding-create-todo",
      "depends_on": ["todo-2"],
      "config": {"text": "Open the app settings via the gear icon"}
    },
    {
      "id": "finalize",
      "task": "onboarding-finalize",
      "depends_on": ["todo-3"]
    }
  ]
}`
