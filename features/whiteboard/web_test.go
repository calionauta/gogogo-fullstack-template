package whiteboard_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/features/whiteboard"
	"github.com/calionauta/gogogo-fullstack-template/internal/collab"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"

	_ "github.com/ncruces/go-sqlite3/driver"
)

const (
	wbEmail    = "demo@demo.app"
	wbPassword = "demo1234456"
)

// webFixture boots PocketBase + a queue (SSE hub) + the whiteboard handler
// over httptest, mirroring router.Init. Uses an in-memory persister so the
// test asserts persistence without a real whiteboards collection, then the
// same assertions hold for the PocketBase persister in production.
func webFixture(t *testing.T) (string, *collab.MemoryPersister, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "wb-int-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	cfg := &config.Config{
		Host:          "127.0.0.1",
		Port:          0,
		Dev:           true,
		DataDir:       tmpDir,
		DBPath:        tmpDir + "/app.db",
		EncryptionKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	app := pocketbase.NewWithConfig(pocketbase.Config{
		DefaultDataDir:       cfg.DataDir,
		DefaultEncryptionEnv: cfg.EncryptionKey,
		DBConnect: func(dbPath string) (*dbx.DB, error) {
			pragmas := "?_pragma=busy_timeout(10000)" +
				"&_pragma=journal_mode(WAL)" +
				"&_pragma=foreign_keys(ON)" +
				"&_pragma=temp_store(MEMORY)"
			return dbx.Open("sqlite3", "file:"+dbPath+pragmas)
		},
	})
	if bErr := app.Bootstrap(); bErr != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Bootstrap: %v", bErr)
	}

	q, err := queue.New(cfg)
	if err != nil {
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("queue.New: %v", err)
	}

	persister := collab.NewMemoryPersister()
	h := whiteboard.New(app, q.Hub(), persister)

	r := router.NewRouter[*core.RequestEvent](
		func(w http.ResponseWriter, req *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
			return &core.RequestEvent{App: app, Event: router.Event{Response: w, Request: req}}, nil
		},
	)
	auth.CookieSecure = false
	r.BindFunc(auth.LoadAuthFromCookie)
	h.RegisterRoutesOn(r)
	r.POST("/login", auth.HandlePasswordLogin)

	mux, err := r.BuildMux()
	if err != nil {
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
		t.Fatalf("BuildMux: %v", err)
	}
	// Seed a demo user so login works.
	seedUser(t, app)

	server := httptest.NewServer(mux)
	cleanup := func() {
		server.Close()
		q.Close()
		mustReset(t, app)
		os.RemoveAll(tmpDir)
	}
	return server.URL, persister, cleanup
}

func seedUser(t *testing.T, app core.App) {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	if existing, fErr := app.FindAuthRecordByEmail(col.Name, wbEmail); fErr == nil && existing != nil {
		existing.SetPassword(wbPassword)
		if sErr := app.Save(existing); sErr != nil {
			t.Fatalf("save existing user: %v", sErr)
		}
		return
	}
	rec := core.NewRecord(col)
	rec.SetEmail(wbEmail)
	rec.SetPassword(wbPassword)
	if err := app.Save(rec); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func login(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/login",
		strings.NewReader(url.Values{"email": {wbEmail}, "password": {wbPassword}}.Encode()))
	if err != nil {
		t.Fatalf("login req: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()
}

// mustReset rolls back bootstrap state on failure. Best-effort.
func mustReset(t *testing.T, app core.App) {
	t.Helper()
	if err := app.ResetBootstrapState(); err != nil {
		t.Logf("ResetBootstrapState: %v", err)
	}
}

func openWBStream(t *testing.T, client *http.Client, baseURL, docID, clientID string) *wbStream {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		baseURL+"/api/whiteboard/"+docID+"/stream?clientID="+clientID, nil)
	if err != nil {
		t.Fatalf("wb stream req: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("wb stream connect: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("wb stream status = %d", resp.StatusCode)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &wbStream{resp: resp, cancel: cancel, events: make(chan string, 128)}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var buf strings.Builder
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				if buf.Len() > 0 {
					select {
					case s.events <- buf.String():
					case <-ctx.Done():
						return
					}
					buf.Reset()
				}
				continue
			}
			buf.WriteString(line)
			buf.WriteString("\n")
		}
	}()
	return s
}

type wbStream struct {
	resp   *http.Response
	cancel context.CancelFunc
	events chan string
}

func (s *wbStream) close() {
	s.cancel()
	_ = s.resp.Body.Close()
}

func (s *wbStream) drain(window time.Duration) []string {
	var out []string
	timeout := time.After(window)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			return out
		}
	}
}

// TestWhiteboard_ShapeBroadcastAndPersist is the end-to-end regression
// guard for the collaborative whiteboard:
//
//   - clientA draws a shape (POST op). The server merges it into the Loro
//     CRDT, persists the snapshot, and broadcasts the resolved shapes to
//     every OTHER client (exclude-origin).
//   - clientB (a different SSE connection, same doc) MUST receive the
//     shapes event with the new shape.
//   - clientA MUST NOT receive its own shape echoed back (exclude-origin,
//     mirroring the todo fix).
//   - The persister MUST hold the saved snapshot (PocketBase in prod).
//
// This proves the full architecture without a browser: SSE transport +
// CRDT convergence + persistence + exclude-origin.
func TestWhiteboard_ShapeBroadcastAndPersist(t *testing.T) {
	baseURL, persister, cleanup := webFixture(t)
	defer cleanup()

	jarA, errA := cookiejar.New(nil)
	if errA != nil {
		t.Fatalf("cookiejar A: %v", errA)
	}
	jarB, errB := cookiejar.New(nil)
	if errB != nil {
		t.Fatalf("cookiejar B: %v", errB)
	}
	clientA := &http.Client{
		Jar: jarA, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	clientB := &http.Client{
		Jar: jarB, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, clientA, baseURL)
	login(t, clientB, baseURL)

	docID := "doc-e2e-" + time.Now().Format("150405.000")

	streamA := openWBStream(t, clientA, baseURL, docID, "wbA")
	streamB := openWBStream(t, clientB, baseURL, docID, "wbB")
	defer streamA.close()
	defer streamB.close()
	time.Sleep(150 * time.Millisecond)

	// clientA creates a rectangle.
	op := collab.ShapeOp{Op: "add", Shape: collab.Shape{
		ID: "s1", Type: "rect", X: 10, Y: 20, W: 100, H: 50, Color: "#1f2937",
	}}
	body, mErr := json.Marshal(op)
	if mErr != nil {
		t.Fatalf("marshal op: %v", mErr)
	}
	resp, err := postWithClientID(context.Background(), clientA, baseURL+"/api/whiteboard/"+docID+"/update", "wbA", body)
	if err != nil {
		t.Fatalf("update POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d: %s", resp.StatusCode, readAll(resp))
	}
	resp.Body.Close()

	aEvents := streamA.drain(800 * time.Millisecond)
	bEvents := streamB.drain(800 * time.Millisecond)

	if !shapesEventContains(bEvents, "s1") {
		t.Fatalf("PEER (clientB) did not receive the shape broadcast.\nB events:\n%s", debugEvents(bEvents))
	}
	if shapesEventContains(aEvents, "s1") {
		t.Fatalf("ORIGINATOR (clientA) received its own shape echo "+
			"(exclude-origin broken).\nA events:\n%s", debugEvents(aEvents))
	}

	// Persistence: the in-memory persister must hold the snapshot for docID.
	if _, ok := persister.LoadSnapshot(docID); !ok {
		t.Fatalf("persister did not save snapshot for doc %s", docID)
	}

	// The worker's resolved shapes must include s1.
	if !shapeInList(whiteboardShapes(t, baseURL, clientB, docID), "s1") {
		t.Fatalf("resolved shapes from server do not include s1")
	}
}

// TestWhiteboard_PresenceBroadcast proves remote cursor presence is
// broadcast to peers (exclude-origin) so each client sees the others'
// cursors but not an echo of its own.
func TestWhiteboard_PresenceBroadcast(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()

	jarA, errA := cookiejar.New(nil)
	if errA != nil {
		t.Fatalf("cookiejar A: %v", errA)
	}
	jarB, errB := cookiejar.New(nil)
	if errB != nil {
		t.Fatalf("cookiejar B: %v", errB)
	}
	clientA := &http.Client{
		Jar: jarA, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	clientB := &http.Client{
		Jar: jarB, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, clientA, baseURL)
	login(t, clientB, baseURL)

	docID := "doc-pres-" + time.Now().Format("150405.000")
	streamA := openWBStream(t, clientA, baseURL, docID, "wbA")
	streamB := openWBStream(t, clientB, baseURL, docID, "wbB")
	defer streamA.close()
	defer streamB.close()
	time.Sleep(150 * time.Millisecond)

	presence := collab.PresenceMsg{Type: "cursor", Doc: docID, User: "user-A", X: 0.5, Y: 0.5, TS: time.Now().UnixMilli()}
	pbody, mErr := json.Marshal(presence)
	if mErr != nil {
		t.Fatalf("marshal presence: %v", mErr)
	}
	resp, err := postWithClientID(context.Background(), clientA,
		baseURL+"/api/whiteboard/"+docID+"/presence", "wbA", pbody)
	if err != nil {
		t.Fatalf("presence POST: %v", err)
	}
	resp.Body.Close()

	aEvents := streamA.drain(800 * time.Millisecond)
	bEvents := streamB.drain(800 * time.Millisecond)

	if !presenceReceived(bEvents, "user-A") {
		t.Fatalf("PEER (clientB) did not receive cursor presence from user-A.\nB events:\n%s", debugEvents(bEvents))
	}
	if presenceReceived(aEvents, "user-A") {
		t.Fatalf("ORIGINATOR (clientA) received its own cursor echo "+
			"(exclude-origin broken).\nA events:\n%s", debugEvents(aEvents))
	}
}

// TestWhiteboard_OfflineReplay proves the server-side contract that makes
// the client's offline-first outbox safe: ops may arrive LATE (after the
// peer already connected and drew) and the server still merges each into
// the shared Loro CRDT, persists the resolved snapshot, and broadcasts to
// peers. This is what lets the browser buffer draws while offline and
// replay them on reconnect without losing or clobbering work.
//
// It mirrors the real flow: clientA draws + the op is held (simulated by
// delaying the POST), clientB draws in the meantime; then clientA's
// delayed op arrives and must converge on the server and reach clientB.
func TestWhiteboard_OfflineReplay(t *testing.T) {
	baseURL, persister, cleanup := webFixture(t)
	defer cleanup()

	jarA, errA := cookiejar.New(nil)
	if errA != nil {
		t.Fatalf("cookiejar A: %v", errA)
	}
	jarB, errB := cookiejar.New(nil)
	if errB != nil {
		t.Fatalf("cookiejar B: %v", errB)
	}
	clientA := &http.Client{
		Jar: jarA, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	clientB := &http.Client{
		Jar: jarB, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, clientA, baseURL)
	login(t, clientB, baseURL)

	docID := "doc-offline-" + time.Now().Format("150405.000")
	streamA := openWBStream(t, clientA, baseURL, docID, "wbA")
	streamB := openWBStream(t, clientB, baseURL, docID, "wbB")
	defer streamA.close()
	defer streamB.close()
	time.Sleep(150 * time.Millisecond)

	// clientB draws immediately (online peer).
	bOp := collab.ShapeOp{Op: "add", Shape: collab.Shape{ID: "s-b", Type: "rect", X: 5, Y: 5, W: 40, H: 40, Color: "#000"}}
	bBody, mErr := json.Marshal(bOp)
	if mErr != nil {
		t.Fatalf("marshal bOp: %v", mErr)
	}
	resp, err := postWithClientID(context.Background(), clientB, baseURL+"/api/whiteboard/"+docID+"/update", "wbB", bBody)
	if err != nil {
		t.Fatalf("peer update POST: %v", err)
	}
	resp.Body.Close()

	// clientA "goes offline": its op is buffered, not POSTed yet.
	aOp := collab.ShapeOp{Op: "add", Shape: collab.Shape{
		ID: "s-a", Type: "ellipse", X: 60, Y: 60, W: 30, H: 30, Color: "#f00",
	}}
	aBody, mErr2 := json.Marshal(aOp)
	if mErr2 != nil {
		t.Fatalf("marshal aOp: %v", mErr2)
	}
	time.Sleep(200 * time.Millisecond) // simulate offline window

	// clientA "reconnects" and flushes its buffered op.
	updURL := baseURL + "/api/whiteboard/" + docID + "/update"
	respA, errRA := postWithClientID(context.Background(), clientA, updURL, "wbA", aBody)
	if errRA != nil {
		t.Fatalf("replay POST: %v", errRA)
	}
	respA.Body.Close()

	bEvents := streamB.drain(800 * time.Millisecond)
	if !shapesEventContains(bEvents, "s-a") {
		t.Fatalf("PEER did not receive the late (replayed) shape s-a.\nB events:\n%s", debugEvents(bEvents))
	}

	// Persistence must contain BOTH shapes (late op converged + saved).
	if _, ok := persister.LoadSnapshot(docID); !ok {
		t.Fatalf("persister did not save snapshot for %s", docID)
	}
	shapes := whiteboardShapes(t, baseURL, clientB, docID)
	if !shapeInList(shapes, "s-a") || !shapeInList(shapes, "s-b") {
		t.Fatalf("resolved shapes missing a replayed/peer shape: %+v", shapes)
	}
}

// --- helpers ---

func postWithClientID(
	ctx context.Context, client *http.Client, u, clientID string, body []byte,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u+"?clientID="+clientID, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}

func whiteboardShapes(t *testing.T, baseURL string, client *http.Client, docID string) []collab.Shape {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		baseURL+"/api/whiteboard/"+docID+"/snapshot", nil)
	if err != nil {
		t.Fatalf("snapshot req: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer resp.Body.Close()
	var shapes []collab.Shape
	if err := json.NewDecoder(resp.Body).Decode(&shapes); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return shapes
}

func shapeInList(shapes []collab.Shape, id string) bool {
	for _, s := range shapes {
		if s.ID == id {
			return true
		}
	}
	return false
}

// shapesEventContains returns true if any event is a shapes envelope
// carrying the shape id. SSE frames are prefixed with "data: ".
func shapesEventContains(events []string, id string) bool {
	for _, ev := range events {
		raw := strings.TrimPrefix(strings.TrimSpace(ev), "data: ")
		if !strings.Contains(raw, `"type":"shapes"`) {
			continue
		}
		var env collab.WebShapesEvent
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			continue
		}
		for _, s := range env.Shapes {
			if s.ID == id {
				return true
			}
		}
	}
	return false
}

func presenceReceived(events []string, user string) bool {
	for _, ev := range events {
		raw := strings.TrimPrefix(strings.TrimSpace(ev), "data: ")
		var msg collab.PresenceMsg
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			continue
		}
		if msg.User == user && (msg.Type == "cursor" || msg.Type == "join") {
			return true
		}
	}
	return false
}

func debugEvents(events []string) string {
	if len(events) == 0 {
		return "(no events)"
	}
	return strings.Join(events, "\n---\n")
}

func readAll(resp *http.Response) string {
	buf := make([]byte, 0, 1024)
	chunk := make([]byte, 256)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf)
}

// TestWhiteboard_BoardPageRendersValidDocID is the regression guard for
// the "WB_DOC_ID missing / Unexpected token '.'" crash.
//
// Root cause: the board template emitted
//   window.WB_DOC_ID = { templ.URL(docID) };
// templ.URL() escapes the doc id into a full URL (scheme://host/docID),
// which is invalid JS — the browser threw "Unexpected token '.'" and the
// entire whiteboard.js init aborted, so no shapes/presence/cursors ever
// worked. The fix emits a quoted JS string via templ.JSEscape.
//
// This test fetches the board page HTML and asserts the inline script is
// syntactically valid (window.WB_DOC_ID = "<docID>";) — i.e. it contains
// the doc id wrapped in quotes, not a templ.URL artifact like
// "https://".
func TestWhiteboard_BoardPageRendersValidDocID(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, client, baseURL)

	docID := "doc-render-" + time.Now().Format("150405.000")
	resp, err := client.Get(baseURL + "/whiteboard/" + docID)
	if err != nil {
		t.Fatalf("GET /whiteboard/%s: %v", docID, err)
	}
	body := readAll(resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("board page status = %d", resp.StatusCode)
	}

	// The doc id must be carried on <main data-doc-id> (templ escapes
	// attribute values safely). The inline script then reads it into
	// window.WB_DOC_ID from the DOM. A previous bug emitted
	// `window.WB_DOC_ID = { templ.URL(docID) };` which rendered a full
	// URL (invalid JS -> "Unexpected token '.'").
	if !strings.Contains(body, `data-doc-id="`+docID+`"`) {
		t.Fatalf("board page missing data-doc-id attribute for %s\nbody (first 400):\n%s", docID, body[:minLocal(len(body), 400)])
	}
	// The script must read the id from the DOM, not from a templ.URL()
	// artifact. The broken form would contain "window.WB_DOC_ID = https://".
	if strings.Contains(body, "window.WB_DOC_ID = https://") {
		t.Fatalf("WB_DOC_ID still uses templ.URL (invalid JS):\n%s", body[:minLocal(len(body), 400)])
	}
	if !strings.Contains(body, "dataset.docId") {
		t.Fatalf("board page does not read WB_DOC_ID from the DOM")
	}
}

// TestWhiteboard_NewBoardRedirect covers the "New whiteboard does
// nothing" bug. The handler previously returned an HX-Redirect header +
// 204, which a plain <a href> navigation ignores (HTMX only). The fix
// returns a real 302 Location so the browser follows it to the new
// board. This asserts the redirect lands on a valid /whiteboard/<id>.
func TestWhiteboard_NewBoardRedirect(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, client, baseURL)

	resp, err := client.Get(baseURL + "/whiteboard/new")
	if err != nil {
		t.Fatalf("GET /whiteboard/new: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("new board: want 302/303 redirect, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/whiteboard/") || strings.TrimPrefix(loc, "/whiteboard/") == "" {
		t.Fatalf("new board: Location header %q is not a valid /whiteboard/<id>", loc)
	}
}

// TestWhiteboard_BoardPageShowsLoggedInNav is the regression guard for
// "/whiteboard always shows Sign in even when logged in". The board and
// index pages passed auth.Navbar("") unconditionally, so the navbar
// rendered the logged-out state. The fix passes c.Auth.Email().
func TestWhiteboard_BoardPageShowsLoggedInNav(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, client, baseURL)

	docID := "doc-nav-" + time.Now().Format("150405.000")
	resp, err := client.Get(baseURL + "/whiteboard/" + docID)
	if err != nil {
		t.Fatalf("GET /whiteboard/%s: %v", docID, err)
	}
	body := readAll(resp)
	resp.Body.Close()

	if strings.Contains(body, ">Sign in<") {
		t.Fatalf("board page shows 'Sign in' despite being logged in:\n%s", body[:minLocal(len(body), 300)])
	}
	if !strings.Contains(body, wbEmail) {
		t.Fatalf("board page navbar does not show the logged-in email %q", wbEmail)
	}
}

func minLocal(a, b int) int {
	if a < b {
		return a
	}
	return b
}
