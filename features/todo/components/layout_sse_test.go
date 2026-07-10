package components

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/calionauta/gogogo-fullstack-template/features/todo"
)

// TestLayoutRendersSSEOpener is a regression guard for the realtime
// wiring. The browser only receives broadcasts if it actually opens the
// persistent SSE stream. Datastar v1 does NOT fire data-on:load on a
// <div>/<body>, so the previous `data-on:load={ "@get('/api/todos/stream')" }`
// card approach silently left every client disconnected — broadcasts were
// registered on the server but never reached the browser, and the
// connected-clients count stayed wrong.
//
// The fix renders a hidden #sse-opener button whose data-on:click opens
// the stream, and a <script> that clicks it on the datastar-ready event
// (with a timeout fallback). This test renders the layout HTML and
// asserts both pieces are present, so a future refactor that
// reintroduces data-on:load (or drops the opener) fails here instead of
// shipping a silently-dead realtime demo.
func TestLayoutRendersSSEOpener(t *testing.T) {
	signals := todo.Signals{
		Todos:            nil,
		Filter:           "all",
		ItemCount:        0,
		ConnectedClients: 1,
		Suggestions:      []string{},
	}

	var buf bytes.Buffer
	if err := Layout("Todos", signals, "demo@example.com").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render layout: %v", err)
	}
	html := buf.String()

	checks := []struct {
		name   string
		needle string
	}{
		{"sse-opener button present", `id="sse-opener"`},
		{"opener opens the SSE stream via @get", `/api/todos/stream`},
		{"datastar-ready click wiring", `datastar-ready`},
		{"fallback timeout click", `setTimeout(openSSE`},
		// Guard against the previously-broken approach resurfacing: a real
		// data-on:load="..." ATTRIBUTE on an element. The word
		// "data-on:load" may appear inside a explanatory JS comment (which is
		// fine); we only fail on the attribute form with an `=` value.
		{"no dead data-on:load attribute", `data-on:load=`},
	}

	for _, c := range checks {
		if c.name == "no dead data-on:load attribute" {
			if strings.Contains(html, c.needle) {
				msg := "layout still uses a %q attribute; the SSE stream would never open " +
					"because Datastar v1 does not fire on:load on <div>/<body>"
				t.Errorf(msg, c.needle)
			}
			continue
		}
		if !strings.Contains(html, c.needle) {
			t.Errorf("layout missing %q (%s)", c.needle, c.name)
		}
	}
}
