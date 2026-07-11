package todo_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

// TestIntegration_ListFiltersByOwner is the regression guard for the
// "filter tabs reveal other users' todos" bug.
//
// Root cause: handleList did NOT load the app auth session (the global
// LoadAuthFromCookie middleware skips /api/* paths), so c.Auth was nil
// and listTodos returned EVERY user's records (no owner filter). The
// page itself loads auth (so the initial render showed only your 6
// todos), but clicking Active/Completed hit the /api/todos GET with no
// auth → the owner filter was skipped → all users' todos leaked.
//
// This test logs in as user A, creates A's todos, seeds a SECOND user B
// with their own todos directly in the DB, then asserts that
// GET /api/todos?filter=all|active|completed only returns A's items.
func TestIntegration_ListFiltersByOwner(t *testing.T) {
	base, _, app, _, cleanup := testFixture(t)
	defer cleanup()

	// 1) Log in as the demo user to get the gogogo_auth cookie (stored
	//    in the client's cookie jar).
	client := loginClient(t, base)
	if c := cookieFor(client, base); c == "" {
		t.Fatal("login did not yield gogogo_auth cookie")
	}

	// 2) User A creates 3 todos.
	ctx := context.Background()
	for _, title := range []string{"A1", "A2", "A3"} {
		mustPostCtx(ctx, t, client, base, "/api/todos", url.Values{titleField: {title}}, 200)
	}

	// 3) Seed a completely different user B and give B 5 todos in the
	//    same collection. If the owner filter is broken, these leak into
	//    A's list responses.
	if err := seedOtherUserWithTodos(app, "other@example.com", "O", 5); err != nil {
		t.Fatalf("seed other user: %v", err)
	}

	// 4) Every filter variant must return exactly A's 3 todos.
	for _, filter := range []string{"all", "active", "completed"} {
		resp, err := client.Get(base + "/api/todos?filter=" + filter)
		if err != nil {
			t.Fatalf("GET /api/todos?filter=%s: %v", filter, err)
		}
		body := readBody(t, resp)
		t.Logf("GET /api/todos?filter=%s -> %d body[0:120]=%q", filter, resp.StatusCode, body[:120])
		// Count occurrences of A's titles (should be present) and B's
		// titles (should be absent). B's titles are "O1".."O5".
		aSeen := 0
		for _, title := range []string{"A1", "A2", "A3"} {
			if strings.Contains(body, title) {
				aSeen++
			}
		}
		bSeen := 0
		for i := 1; i <= 5; i++ {
			if strings.Contains(body, "O"+itoaLocal(i)) {
				bSeen++
			}
		}
		// A's 3 todos are uncompleted, so they appear under all + active
		// and must NOT appear under completed. B's todos must never appear.
		wantA := 3
		if filter == "completed" {
			wantA = 0
		}
		if aSeen != wantA {
			t.Errorf("filter=%s: expected A's %d todos, saw %d (bodylen=%d)", filter, wantA, aSeen, len(body))
		}
		if bSeen != 0 {
			t.Errorf("filter=%s: OTHER user's %d todos leaked into A's list", filter, bSeen)
		}
	}
}

// loginClient logs in with the demo creds via a redirect-following
// client (with a cookie jar) and returns the client carrying the
// gogogo_auth cookie.
func loginClient(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			return nil
		},
	}
	ctx := context.Background()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/login",
		strings.NewReader(url.Values{"email": {demoEmail}, "password": {demoPassword}, "next": {"/"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /login: unexpected status %d", resp.StatusCode)
	}
	return client
}

// cookieFor returns the gogogo_auth cookie value for base, or "".
func cookieFor(client *http.Client, base string) string {
	u, _ := url.Parse(base)
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == "gogogo_auth" {
			return c.Value
		}
	}
	return ""
}

// mustPostCtx posts with a caller-supplied *http.Client (carrying auth).
func mustPostCtx(ctx context.Context, t *testing.T, client *http.Client, base, path string, values url.Values, wantStatus int) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path,
		strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatalf("build POST %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s: want status %d, got %d", path, wantStatus, resp.StatusCode)
	}
}

// seedOtherUserWithTodos creates a distinct auth user and N todos owned
// by that user in the todos collection.
func seedOtherUserWithTodos(app core.App, email, prefix string, n int) error {
	col, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		return err
	}
	user := core.NewRecord(col)
	user.SetEmail(email)
	user.SetPassword("otherpass123")
	if err := app.Save(user); err != nil {
		return err
	}
	todos, err := app.FindCollectionByNameOrId("todos")
	if err != nil {
		return err
	}
	for i := 1; i <= n; i++ {
		rec := core.NewRecord(todos)
		rec.Set(titleField, prefix+itoaLocal(i))
		rec.Set("completed", false)
		rec.Set("owner", user.Id)
		if err := app.Save(rec); err != nil {
			return err
		}
	}
	return nil
}

func itoaLocal(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

