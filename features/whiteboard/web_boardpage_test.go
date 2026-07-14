// SCOPE:feature - Whiteboard board-page tests + shared test helpers.
package whiteboard_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/internal/collab"
)

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
//
//	window.WB_DOC_ID = { templ.URL(docID) };
//
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

	jar, jarErr := cookiejar.New(nil)
	if jarErr != nil {
		t.Fatalf("cookiejar: %v", jarErr)
	}
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, client, baseURL)

	docID := "doc-render-" + time.Now().Format("150405.000")
	resp, err := client.Do(mustReq(t, http.MethodGet, baseURL+"/whiteboard/"+docID))
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
		t.Fatalf("board page missing data-doc-id for %s\nbody (first 400):\n%s",
			docID, body[:minLocal(len(body), 400)])
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

	jar, jarErr := cookiejar.New(nil)
	if jarErr != nil {
		t.Fatalf("cookiejar: %v", jarErr)
	}
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, client, baseURL)

	resp, err := client.Do(mustReq(t, http.MethodGet, baseURL+"/whiteboard/new"))
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

	jar, jarErr := cookiejar.New(nil)
	if jarErr != nil {
		t.Fatalf("cookiejar: %v", jarErr)
	}
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	login(t, client, baseURL)

	docID := "doc-nav-" + time.Now().Format("150405.000")
	resp, err := client.Do(mustReq(t, http.MethodGet, baseURL+"/whiteboard/"+docID))
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

// mustReq builds an HTTP request with a background context (golangci-lint
// noctx forbids client.Get, so callers use client.Do(mustReq(...))).
func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	return req
}
