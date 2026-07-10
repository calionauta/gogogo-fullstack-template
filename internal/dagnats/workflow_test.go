//go:build dagnats

package dagnats

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOnboardingWorkflow_HandlerNamesMatch asserts the workflow JSON's step
// task names are exactly the ones cmd/web/dagnats.go registers as
// worker handlers. This is the regression guard for the DagNats
// contract: renaming a handler (e.g. onboarding-await-first-todo)
// without updating the JSON leaves an orphan step that blocks forever.
// The test fails loudly instead of silently hanging at runtime.
func TestOnboardingWorkflow_HandlerNamesMatch(t *testing.T) {
	var wf struct {
		Steps []struct {
			ID   string `json:"id"`
			Task string `json:"task"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(OnboardingWorkflowJSON), &wf); err != nil {
		t.Fatalf("workflow JSON invalid: %v", err)
	}

	got := map[string]bool{}
	for _, s := range wf.Steps {
		if s.Task == "" {
			t.Fatalf("step %q has empty task name", s.ID)
		}
		got[s.Task] = true
	}

	want := []string{
		"onboarding-greet",
		"onboarding-await-first-todo",
		"onboarding-create-todo",
		"onboarding-finalize",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("workflow JSON missing step task %q (registered by cmd/web/dagnats.go)", name)
		}
	}

	// The await step must depend on greet and the create steps must depend
	// on it — i.e. the signal-wait sits between greet and the
	// todo creation. This is the durable-suspend shape; if someone
	// rewires the DAG we want to know.
	var raw struct {
		Steps []map[string]json.RawMessage `json:"steps"`
	}
	_ = json.Unmarshal([]byte(OnboardingWorkflowJSON), &raw)
	byID := map[string]struct {
		DependsOn []string `json:"depends_on"`
	}{}
	for _, step := range raw.Steps {
		id := strings.Trim(string(step["id"]), `"`)
		var deps []string
		if d, ok := step["depends_on"]; ok {
			_ = json.Unmarshal(d, &deps)
		}
		byID[id] = struct {
			DependsOn []string `json:"depends_on"`
		}{DependsOn: deps}
	}
	if !dependsOn(byID, "await-first-todo", "greet") {
		t.Error("await-first-todo must depend on greet (signal-wait after greeting)")
	}
	if !dependsOn(byID, "todo-1", "await-first-todo") {
		t.Error("todo-1 must depend on await-first-todo (create after signal)")
	}
	if !dependsOn(byID, "finalize", "todo-3") {
		t.Error("finalize must depend on todo-3 (last create step)")
	}
}

func dependsOn(steps map[string]struct {
	DependsOn []string `json:"depends_on"`
}, id, dep string,
) bool {
	for _, d := range steps[id].DependsOn {
		if d == dep {
			return true
		}
	}
	return false
}
