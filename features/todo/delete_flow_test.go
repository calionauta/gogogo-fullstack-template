package todo_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/calionauta/gogogo-fullstack-template/features/todo"
)

// TestIntegration_DeleteConfirmModalFlow verifies the two-step delete:
// GET /confirm-delete opens the modal (sets $confirmingDeleteId), then
// POST /delete performs the removal and clears the signal. This is the
// full client path the DaisyUI dialog drives.
func TestIntegration_DeleteConfirmModalFlow(t *testing.T) {
	base, _, app, _, cleanup := testFixture(t)
	defer cleanup()

	coll, err := app.FindCollectionByNameOrId("todos")
	if err != nil {
		t.Fatalf("find todos collection: %v", err)
	}
	rec := core.NewRecord(coll)
	rec.Set("title", "to delete")
	rec.Set("completed", false)
	if serr := app.Save(rec); serr != nil {
		t.Fatalf("seed: %v", serr)
	}
	id := rec.Id

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	confReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/todos/"+id+"/confirm-delete", nil)
	if err != nil {
		t.Fatalf("confirm-delete request build: %v", err)
	}
	confResp, err := http.DefaultClient.Do(confReq)
	if err != nil {
		t.Fatalf("confirm-delete request: %v", err)
	}
	body := readBody(t, confResp)
	if !strings.Contains(body, "confirmingDeleteId") || !strings.Contains(body, id) {
		t.Fatalf("confirm-delete did not open modal: %s", body)
	}

	delResp, err := postForm(ctx, base+"/api/todos/"+id+"/delete", url.Values{})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	delBody := readBody(t, delResp)
	if !strings.Contains(delBody, "\"confirmingDeleteId\":\"\"") {
		t.Fatalf("delete did not clear modal signal: %s", delBody)
	}
	if _, ferr := app.FindRecordById("todos", id); ferr == nil {
		t.Fatalf("record %s still exists after delete", id)
	}
	t.Logf("delete modal flow OK")
}

// ensure todo import is used (Signals type referenced elsewhere).
var _ = todo.Signals{}
