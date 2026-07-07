package todo_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// requestTimeout caps any individual HTTP call in integration tests.
// Generous enough for CI noise without letting a stuck handler hang the
// suite forever.
const requestTimeout = 5 * time.Second

// TestIntegration_CreateListDelete is the canonical happy-path E2E
// for the todo feature: create → list → delete, exercising the real
// PocketBase CRUD path, the real goqite enqueue path, and the real
// HTTP layer.
func TestIntegration_CreateListDelete(t *testing.T) {
	base, _, app, cleanup := testFixture(t)
	defer cleanup()

	ctx := newTestCtx(t)
	mustPost(ctx, t, base, "/api/todos", url.Values{"title": {"buy milk"}}, 200)

	records, err := app.FindRecordsByFilter("todos", "", "", 0, 0)
	if err != nil || len(records) != 1 {
		t.Fatalf("expected 1 record after create, got %d (err=%v)", len(records), err)
	}
	id := records[0].Id

	mustPost(ctx, t, base, "/api/todos/"+id+"/delete", nil, 200)

	records, err = app.FindRecordsByFilter("todos", "", "", 0, 0)
	if err != nil {
		t.Fatalf("find after delete: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 records after delete, got %d", len(records))
	}
}

// TestIntegration_ToggleFlipsCompleted exercises the toggle handler
// through the real HTTP layer and verifies the boolean field mutates
// in PocketBase.
func TestIntegration_ToggleFlipsCompleted(t *testing.T) {
	base, _, app, cleanup := testFixture(t)
	defer cleanup()

	ctx := newTestCtx(t)
	mustPost(ctx, t, base, "/api/todos", url.Values{"title": {"dishes"}}, 200)

	records, err := app.FindRecordsByFilter("todos", "", "", 0, 0)
	if err != nil || len(records) != 1 {
		t.Fatalf("expected 1 record, got %d (err=%v)", len(records), err)
	}
	id := records[0].Id
	if records[0].GetBool("completed") {
		t.Fatal("newly created todo should not be completed")
	}

	mustPost(ctx, t, base, "/api/todos/"+id+"/toggle", nil, 200)

	records, err = app.FindRecordsByFilter("todos", "", "", 0, 0)
	if err != nil || !records[0].GetBool("completed") {
		t.Fatalf("toggle did not flip completed to true (err=%v)", err)
	}

	mustPost(ctx, t, base, "/api/todos/"+id+"/toggle", nil, 200)

	records, err = app.FindRecordsByFilter("todos", "", "", 0, 0)
	if err != nil || records[0].GetBool("completed") {
		t.Fatalf("toggle did not flip completed back to false (err=%v)", err)
	}
}

// TestIntegration_DeleteEmitsInfoToast verifies the delete handler
// emits an info-type toast (different alert class from the create
// success toast) and contains the deleted title in the message.
func TestIntegration_DeleteEmitsInfoToast(t *testing.T) {
	base, _, app, cleanup := testFixture(t)
	defer cleanup()

	ctx := newTestCtx(t)
	mustPost(ctx, t, base, "/api/todos", url.Values{"title": {"trash me"}}, 200)

	records, err := app.FindRecordsByFilter("todos", "", "", 0, 0)
	if err != nil || len(records) != 1 {
		t.Fatalf("expected 1 record, got %d (err=%v)", len(records), err)
	}

	resp, err := postForm(ctx, base+"/api/todos/"+records[0].Id+"/delete", nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}
	body := readBody(t, resp)

	if !strings.Contains(body, "alert-info") {
		t.Fatalf("delete response missing alert-info class (should be info, not success): %s", body)
	}
	if !strings.Contains(body, "trash me") {
		t.Fatalf("delete response missing deleted title in toast: %s", body)
	}
}

// TestIntegration_ClearCompletedRemovesOnlyDone seeds two todos, marks
// one complete via direct DB write, hits the bulk-delete endpoint, and
// verifies only the active one remains.
func TestIntegration_ClearCompletedRemovesOnlyDone(t *testing.T) {
	base, _, app, cleanup := testFixture(t)
	defer cleanup()

	ctx := newTestCtx(t)
	for _, title := range []string{"active", "done"} {
		mustPost(ctx, t, base, "/api/todos", url.Values{"title": {title}}, 200)
	}

	records, err := app.FindRecordsByFilter("todos", "title='done'", "", 0, 0)
	if err != nil || len(records) != 1 {
		t.Fatalf("expected 1 'done' record, got %d (err=%v)", len(records), err)
	}
	records[0].Set("completed", true)
	if saveErr := app.Save(records[0]); saveErr != nil {
		t.Fatalf("mark done: %v", saveErr)
	}

	mustPost(ctx, t, base, "/api/todos/completed/delete", nil, 200)

	remaining, err := app.FindRecordsByFilter("todos", "", "", 0, 0)
	if err != nil {
		t.Fatalf("find after clear: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining, got %d", len(remaining))
	}
	if remaining[0].GetString("title") != "active" {
		t.Fatalf("wrong record remaining: %s", remaining[0].GetString("title"))
	}
}

// newTestCtx returns a fresh context per-test. Bound to requestTimeout
// so any hung handler aborts the test cleanly via the context's
// cancellation rather than the test framework's per-test timeout.
func newTestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	t.Cleanup(cancel)
	return ctx
}

// mustPost sends a form-encoded POST and asserts the status code. Used
// by integration tests that don't care about the response body.
func mustPost(ctx context.Context, t *testing.T, base, path string, values url.Values, wantStatus int) {
	t.Helper()
	resp, err := postForm(ctx, base+path, values)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s status=%d, want %d", path, resp.StatusCode, wantStatus)
	}
}

// postForm wraps http.PostForm with a context-aware client so the
// noctx linter sees a context in the request lifecycle.
func postForm(ctx context.Context, url string, values url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return http.DefaultClient.Do(req)
}
