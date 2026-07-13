//go:build dagnats

package todo_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCrossSessionCreatePropagates is the regression guard for the exact
// bug the user hit: creating a todo in one tab must surface in another
// already-open tab. It boots the REAL production binary (the dev variant
// the user runs: -tags "jetstream dagnats") and drives PocketBase's native
// /api/realtime — the path the browser actually uses. The custom-router
// test fixture (r.BuildMux) does NOT mount /api/realtime, so every prior
// "realtime" test exercised only the SSE hub, never the record-mutation
// broadcast. That blind spot is why create-vs-delete asymmetry slipped
// through: the e2e only asserted the pb_auth cookie was issued, not that a
// record change fans out to a second subscriber.
//
// DagNats is disabled here (DAGNATS_ENABLED=false) — the create event is
// delivered by PocketBase realtime regardless, and ResumeOnboarding
// early-returns when no durable run is active, so the test stays focused
// on the realtime fan-out that regressed.
func TestCrossSessionCreatePropagates(t *testing.T) {
	base, cleanup := bootLiveServer(t)
	defer cleanup()
	ctx := context.Background()

	jarA, _ := cookiejar.New(nil)
	jarB, _ := cookiejar.New(nil)
	clientA := &http.Client{Jar: jarA, Timeout: 30 * time.Second}
	clientB := &http.Client{Jar: jarB, Timeout: 30 * time.Second}
	loginUser(ctx, t, clientA, base, demoEmail, demoPassword)
	loginUser(ctx, t, clientB, base, demoEmail, demoPassword)

	// Tab B opens the realtime SSE stream (pb_auth cookie authenticates).
	// PocketBase assigns its own clientId and echoes it in PB_CONNECT —
	// that is the id the subscribe call must use (the URL clientId is
	// ignored), mirroring the browser's subscribe(msg.clientId).
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/realtime?clientId=cross-session-sub", nil)
	resp, err := clientB.Do(req)
	if err != nil {
		t.Fatalf("open realtime SSE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("realtime SSE status=%d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var realClientID string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "event:PB_CONNECT" {
			if scanner.Scan() {
				data := scanner.Text()
				if strings.HasPrefix(data, "data:") {
					var m struct {
						ClientID string `json:"clientId"`
					}
					if e := json.Unmarshal([]byte(strings.TrimPrefix(data, "data:")), &m); e == nil {
						realClientID = m.ClientID
					}
				}
			}
			break
		}
	}
	if realClientID == "" {
		t.Fatalf("never received PB_CONNECT with a clientId")
	}

	// Tab B subscribes to the `todos` topic using PB's assigned clientId.
	subBody, _ := json.Marshal(map[string]any{
		"clientId":      realClientID,
		"action":        "subscribe",
		"subscriptions": []string{"todos"},
	})
	if subResp, e := clientB.Post(base+"/api/realtime", "application/json", bytes.NewReader(subBody)); e != nil {
		t.Fatalf("subscribe: %v", e)
	} else {
		subResp.Body.Close()
	}

	// Tab A creates a todo.
	createResp, err := doPostForm(ctx, clientA, base+"/api/todos", url.Values{titleField: {"PropagateMe"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	createResp.Body.Close()

	// Tab B must receive the `create` event carrying the new record.
	transcript := ""
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && scanner.Scan() {
		line := scanner.Text()
		transcript += line + "\n"
		if strings.Contains(line, `"action":"create"`) && strings.Contains(line, "PropagateMe") {
			return
		}
	}
	t.Fatalf("cross-session create event not delivered to subscriber; transcript tail:\n%s", tailString(transcript, 800))
}

// TestCrossSessionFragmentScoped guards the app-side half of the same bug:
// after one session creates a todo, a DIFFERENT session's list fragment
// (the endpoint the realtime handler refetches on create) must return that
// todo. If listTodos' owner scoping regresses, the other tab would refetch
// and still see nothing — the create "doesn't show" even though the event
// arrived. This runs on the custom-router fixture (no realtime needed) so
// it is fast and a stable unit-level guard.
func TestCrossSessionFragmentScoped(t *testing.T) {
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()
	ctx := context.Background()

	jarA, _ := cookiejar.New(nil)
	jarB, _ := cookiejar.New(nil)
	clientA := &http.Client{Jar: jarA, Timeout: 15 * time.Second}
	clientB := &http.Client{Jar: jarB, Timeout: 15 * time.Second}
	loginUser(ctx, t, clientA, base, demoEmail, demoPassword)
	loginUser(ctx, t, clientB, base, demoEmail, demoPassword)

	createResp, err := doPostForm(ctx, clientA, base+"/api/todos", url.Values{titleField: {"SharedItem"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	createResp.Body.Close()

	fragResp, err := clientB.Get(base + "/api/todos/fragment")
	if err != nil {
		t.Fatalf("fragment GET: %v", err)
	}
	defer fragResp.Body.Close()
	body, _ := io.ReadAll(fragResp.Body)
	if !strings.Contains(string(body), "SharedItem") {
		t.Fatalf("cross-session fragment missing the other session's todo; tail:\n%s", tailString(string(body), 500))
	}
}

// TestRealtimeResyncFragmentMorphHeaders guards the client-side half of the
// same bug: the realtime handler refetches /api/todos/fragment on a record
// change and the browser morphs #todo-list in place. For that morph to target
// the right region (and NOT replace the whole document), the server must send
// datastar-selector + datastar-mode headers. If these regress, resync would
// either no-op (no selector) or blow away the page (whole-document morph).
func TestRealtimeResyncFragmentMorphHeaders(t *testing.T) {
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()
	ctx := context.Background()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	loginUser(ctx, t, client, base, demoEmail, demoPassword)

	resp, err := client.Get(base + "/api/todos/fragment")
	if err != nil {
		t.Fatalf("fragment GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("datastar-selector"); got != "#todo-list" {
		t.Fatalf("expected datastar-selector=#todo-list, got %q", got)
	}
	if got := resp.Header.Get("datastar-mode"); got != "outer" {
		t.Fatalf("expected datastar-mode=outer, got %q", got)
	}
}

// TestRealtimeResyncWiringRendered guards against reintroducing the exact
// crash the user hit: the page must wire the resync through a hidden @get
// button (whose click the runtime drives with a proper Datastar context),
// never a bare actions.get(url) call. A bare actions.get crashes with
// "Cannot read properties of undefined (reading 'delete')" because the get
// action expects a context (el/cleanups) only the runtime synthesizes when
// invoked via an attribute — so the other tab would never update.
func TestRealtimeResyncWiringRendered(t *testing.T) {
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()
	ctx := context.Background()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	loginUser(ctx, t, client, base, demoEmail, demoPassword)

	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "pb-realtime-resync") {
		t.Fatalf("page missing pb-realtime-resync resync button")
	}
	// Templ's ResolveAttributeValue HTML-escapes the single quotes in the
	// attribute value ( ' -> &#39; ), so the rendered form is
	// @get(&#39;/api/todos/fragment&#39;). The browser unescapes it when reading
	// the attribute, so Datastar still sees @get('/api/todos/fragment').
	// Accept either rendering.
	plain := "@get('/api/todos/fragment')"
	escaped := "@get(&#39;/api/todos/fragment&#39;)"
	if !strings.Contains(html, plain) && !strings.Contains(html, escaped) {
		if i := strings.Index(html, "pb-realtime-resync"); i >= 0 {
			t.Logf("button context: %q", html[i-60:i+220])
		}
		t.Fatalf("page missing @get resync wiring for /api/todos/fragment (checked %q and %q)", plain, escaped)
	}
	if strings.Contains(html, "actions.get('/api/todos/fragment')") {
		t.Fatalf("regression: page still uses crashing actions.get('/api/todos/fragment') instead of the @get button")
	}
}

// bootLiveServer builds and runs the production binary (dev variant) as a
// subprocess and waits for it to accept /health. Returns the base URL and a
// cleanup that kills the process. This is the only faithful way to exercise
// PocketBase realtime (/api/realtime), which the unit fixture does not
// mount.
func bootLiveServer(t *testing.T) (string, func()) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "gogogo_live")
	build := exec.Command("go", "build", "-tags", "jetstream dagnats", "-o", bin, "github.com/calionauta/gogogo-fullstack-template/cmd/web")
	build.Stderr = os.Stderr
	if out, err := build.Output(); err != nil {
		t.Fatalf("build live binary: %v\n%s", err, out)
	}

	tmpDir := t.TempDir()
	port := 8291
	env := []string{
		"ENVIRONMENT=development",
		"HOST=127.0.0.1",
		"PORT=" + strconv.Itoa(port),
		"DATA_DIR=" + tmpDir,
		"DATABASE_PATH=" + filepath.Join(tmpDir, "app.db"),
		"ENCRYPTION_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"DAGNATS_ENABLED=false",
		"NATS_ENABLED=false",
	}
	proc := exec.Command(bin, "serve", "--http", "127.0.0.1:"+strconv.Itoa(port))
	proc.Env = append(os.Environ(), env...)
	proc.Stderr = os.Stderr
	if err := proc.Start(); err != nil {
		t.Fatalf("start live server: %v", err)
	}

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, e := http.Get(base + "/health")
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return base, func() { _ = proc.Process.Kill(); _, _ = proc.Process.Wait() }
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = proc.Process.Kill()
	t.Fatalf("live server did not become healthy on %s within 60s", base)
	return "", nil
}
