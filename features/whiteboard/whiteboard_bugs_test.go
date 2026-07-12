package whiteboard_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/internal/collab"
)

// TestWhiteboard_PresenceToleratesStringCoords is the regression guard for
// the "POST /api/whiteboard/<doc>/presence 400 (Bad Request)" bug.
//
// Root cause: the client posted cursor coordinates as JSON strings
// (x: "0.5" via .toFixed), but PresenceMsg.X/Y are float64, so
// json.Unmarshal failed and the handler returned 400. That 400 also broke
// remote cursors (BUG: cursors never arrived). The fix makes the server
// tolerant of numeric strings AND the client now sends numbers. This test
// proves both directions: a string-coords payload is accepted (200) and a
// number-coords payload is accepted (200). The server must never 400 a
// well-formed cursor just because a client (or a fork) stringified a number.
func TestWhiteboard_PresenceToleratesStringCoords(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, client, baseURL)

	docID := "doc-presstr-" + time.Now().Format("150405.000")

	// String coords — the exact payload that previously 400'd.
	strBody := []byte(`{"type":"cursor","doc":"` + docID + `","user":"u-str","x":"0.1234","y":"0.4321","ts":1}`)
	presenceURL := baseURL + "/api/whiteboard/" + docID + "/presence"
	resp, err := postWithClientID(context.Background(), client, presenceURL, "wb-str", strBody)
	if err != nil {
		t.Fatalf("presence (string) POST: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("presence (string coords) should be 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Number coords — the corrected client payload.
	numBody, err := json.Marshal(collab.PresenceMsg{Type: "cursor", Doc: docID, User: "u-num", X: 0.5, Y: 0.5, TS: 1})
	if err != nil {
		t.Fatalf("marshal num presence: %v", err)
	}
	resp2, err := postWithClientID(context.Background(), client, presenceURL, "wb-num", numBody)
	if err != nil {
		t.Fatalf("presence (number) POST: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		resp2.Body.Close()
		t.Fatalf("presence (number coords) should be 200, got %d", resp2.StatusCode)
	}
	resp2.Body.Close()
}

// TestWhiteboard_PeerCountAuthoritative is the regression guard for the
// "online count is wrong / inconsistent across tabs (one shows 3, another
// shows 1, but only 2 tabs)" bug.
//
// Root cause: each tab computed its own count from client-side join/leave
// increments, which drifted when a leave was missed or a tab reconnected
// with a fresh client id. The fix makes the server the authority: on every
// join and leave it broadcasts a "count" event carrying the FULL
// peer set to ALL clients, and every tab renders the count directly from
// that event. This test asserts two distinct SSE connections on the same
// doc both receive a "count" event whose peer set contains both of them
// (size 2) — so they always agree on "2 online".
func TestWhiteboard_PeerCountAuthoritative(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()

	clientA := newWBClient(t)
	clientB := newWBClient(t)
	login(t, clientA, baseURL)
	login(t, clientB, baseURL)

	docID := "doc-count-" + time.Now().Format("150405.000")
	streamA := openWBStream(t, clientA, baseURL, docID, "wbA")
	streamB := openWBStream(t, clientB, baseURL, docID, "wbB")
	defer streamA.close()
	defer streamB.close()
	time.Sleep(200 * time.Millisecond)

	peersA := countPeersFromEvents(streamA.drain(400 * time.Millisecond))
	peersB := countPeersFromEvents(streamB.drain(400 * time.Millisecond))

	if len(peersA) != 2 {
		t.Fatalf("clientA expected authoritative count of 2 peers, got %v", peersA)
	}
	if len(peersB) != 2 {
		t.Fatalf("clientB expected authoritative count of 2 peers, got %v", peersB)
	}
	// Each side must see the other's client id in the server's set.
	if !contains(peersA, "wbB") || !contains(peersB, "wbA") {
		t.Fatalf("peer sets disagree: A=%v B=%v", peersA, peersB)
	}
}

// TestWhiteboard_SingleOnlineLabel is the regression guard for the
// "badge shows '1 online' and then ANOTHER 'online' to the right" bug.
// Asserts the board page renders exactly ONE "online" label.
func TestWhiteboard_SingleOnlineLabel(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()
	client := newWBClient(t)
	login(t, client, baseURL)

	docID := "doc-online-" + time.Now().Format("150405.000")
	resp, err := client.Do(mustReq(t, http.MethodGet, baseURL+"/whiteboard/"+docID))
	if err != nil {
		t.Fatalf("GET /whiteboard/%s: %v", docID, err)
	}
	body := readAll(resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("board page status = %d", resp.StatusCode)
	}
	n := strings.Count(body, ">online<")
	if n != 1 {
		t.Fatalf("expected exactly 1 'online' label on the board page, got %d", n)
	}
}

// TestWhiteboard_ThemeToggleWired is the regression guard for the
// "dark/light toggle does nothing on the whiteboard page" bug. The
// whiteboard page does NOT load Datastar, so the toggle must work purely
// via theme.js binding the .theme-toggle button. Asserts the board page
// renders the toggle button and references theme.js.
func TestWhiteboard_ThemeToggleWired(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()
	client := newWBClient(t)
	login(t, client, baseURL)

	docID := "doc-theme-" + time.Now().Format("150405.000")
	resp, err := client.Do(mustReq(t, http.MethodGet, baseURL+"/whiteboard/"+docID))
	if err != nil {
		t.Fatalf("GET /whiteboard/%s: %v", docID, err)
	}
	body := readAll(resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("board page status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "theme-toggle") {
		t.Fatalf("board page missing .theme-toggle button (dark/light toggle would be inert)")
	}
	if !strings.Contains(body, "/static/theme.js") {
		t.Fatalf("board page does not load theme.js (toggle binding never runs)")
	}
}

// --- extra helpers (distinct from web_test.go) ---

func newWBClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func countPeersFromEvents(events []string) []string {
	var latest []string
	for _, ev := range events {
		raw := strings.TrimPrefix(strings.TrimSpace(ev), "data: ")
		var msg collab.PresenceMsg
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			continue
		}
		if msg.Type == "count" {
			latest = msg.Peers // keep the most recent authoritative count
		}
	}
	return latest
}

func contains(xs []string, s string) bool {
	return slices.Contains(xs, s)
}

// TestWhiteboard_CursorBroadcastsToPeer is the regression guard for the
// "remote peer cursors/pointers not showing" bug. Root cause was the
// presence POST returning 400 (client stringified cursor coords), so the
// cursor event never reached peers. This test posts a cursor from wbA and
// asserts wbB's stream receives a "cursor" presence event carrying wbA's
// coords — i.e. the pointer actually renders on the other tab.
func TestWhiteboard_CursorBroadcastsToPeer(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()
	clientA := newWBClient(t)
	clientB := newWBClient(t)
	login(t, clientA, baseURL)
	login(t, clientB, baseURL)
	docID := "doc-cursor-" + time.Now().Format("150405.000")
	streamA := openWBStream(t, clientA, baseURL, docID, "wbA")
	streamB := openWBStream(t, clientB, baseURL, docID, "wbB")
	defer streamA.close()
	defer streamB.close()
	time.Sleep(200 * time.Millisecond)
	streamA.drain(200 * time.Millisecond) // drop join/leave noise
	streamB.drain(200 * time.Millisecond)

	body, err := json.Marshal(collab.PresenceMsg{Type: "cursor", Doc: docID, User: "wbA", X: 0.25, Y: 0.75, TS: 1})
	if err != nil {
		t.Fatalf("marshal cursor presence: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := postWithClientID(ctx, clientA, baseURL+"/api/whiteboard/"+docID+"/presence", "wbA", body)
	if err != nil {
		t.Fatalf("post cursor: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post cursor status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	evs := streamB.drain(2 * time.Second)
	cur, ok := cursorFromEvents(evs)
	if !ok {
		t.Fatalf("clientB never received a cursor event from wbA; events=%s", tailEvents(evs, 400))
	}
	if cur.User != "wbA" {
		t.Fatalf("cursor event user = %q, want wbA", cur.User)
	}
	if math.Abs(cur.X-0.25) > 1e-6 || math.Abs(cur.Y-0.75) > 1e-6 {
		t.Fatalf("cursor coords = (%v,%v), want (0.25,0.75)", cur.X, cur.Y)
	}
}

// TestWhiteboard_LocalClientReceivesOwnShape is the regression guard for
// the "drawing disappears on the local tab but persists on others" bug.
// The fix switched handleUpdate to hub.Broadcast (include origin) so the
// originator receives its own resolved shapes back and stays convergent.
// This test posts a shape from wbA and asserts wbA's OWN stream receives a
// "shapes" event containing that shape id — the local tab does not lose it.
func TestWhiteboard_LocalClientReceivesOwnShape(t *testing.T) {
	baseURL, _, cleanup := webFixture(t)
	defer cleanup()
	clientA := newWBClient(t)
	login(t, clientA, baseURL)
	docID := "doc-local-" + time.Now().Format("150405.000")
	streamA := openWBStream(t, clientA, baseURL, docID, "wbA")
	defer streamA.close()
	time.Sleep(200 * time.Millisecond)
	streamA.drain(200 * time.Millisecond)

	op := collab.ShapeOp{Op: "add", Shape: collab.Shape{
		ID: "s-fix", Type: "rect", X: 10, Y: 10, W: 50, H: 50, Color: "#ff0000",
	}}
	body, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal shape op: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := postWithClientID(ctx, clientA, baseURL+"/api/whiteboard/"+docID+"/update", "wbA", body)
	if err != nil {
		t.Fatalf("post update: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post update status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	evs := streamA.drain(2 * time.Second)
	sev, ok := shapesEventFromEvents(evs)
	if !ok {
		t.Fatalf("originator (wbA) never received its own shape back; events=%s", tailEvents(evs, 400))
	}
	found := false
	for _, s := range sev.Shapes {
		if s.ID == "s-fix" {
			found = true
		}
	}
	if !found {
		t.Fatalf("local shape s-fix not present in broadcast shapes; got %v", sev.Shapes)
	}
}

func cursorFromEvents(events []string) (collab.PresenceMsg, bool) {
	for _, ev := range events {
		raw := strings.TrimPrefix(strings.TrimSpace(ev), "data: ")
		var msg collab.PresenceMsg
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			continue
		}
		if msg.Type == "cursor" {
			return msg, true
		}
	}
	return collab.PresenceMsg{}, false
}

func shapesEventFromEvents(events []string) (collab.WebShapesEvent, bool) {
	for _, ev := range events {
		raw := strings.TrimPrefix(strings.TrimSpace(ev), "data: ")
		var msg collab.WebShapesEvent
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			continue
		}
		if msg.Type == "shapes" {
			return msg, true
		}
	}
	return collab.WebShapesEvent{}, false
}

func tailEvents(evs []string, n int) string {
	if len(evs) > n {
		evs = evs[len(evs)-n:]
	}
	return strings.Join(evs, "\n")
}
