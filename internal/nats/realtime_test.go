//go:build jetstream

package nats_test

import (
	"context"
	"testing"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// TestJetStreamBroadcasterFanout guards the realtime path that was
// previously broken: it silently fell back to the in-memory broadcaster
// with "nats: no responders" because the embedded server started without
// JetStream. This test exercises the REAL JetStream path (durable TODOS
// stream + durable consumer + pub/sub + fan-out through the SSE Hub) and
// asserts that a todo mutation reaches every connected client.
//
// It spins up the embedded NATS server (StartEmbedded already enables
// JetStream and waits for readiness) with an ephemeral StoreDir, so it
// validates the production path without a standing server.
//
// NOTE: NewJetStreamBroadcaster auto-subscribes in its constructor (the
// fix for the "broadcasts registered but never reached remote clients"
// bug). This test therefore does NOT call Subscribe explicitly — if a
// regression removes the auto-subscribe, this test fails because the
// published event would sit unread in JetStream.
func TestJetStreamBroadcasterFanout(t *testing.T) {
	if err := nats.StartEmbedded(t.TempDir()); err != nil {
		t.Fatalf("start embedded nats: %v", err)
	}
	defer nats.Stop()

	hub := queue.NewSSEHub()
	b, err := nats.NewJetStreamBroadcaster(nats.JS, hub)
	if err != nil {
		t.Fatalf("new broadcaster: %v", err)
	}
	t.Cleanup(b.Close)

	const payload = `{"event":"created","id":"abc123","title":"hello"}`

	// Two independent clients, like two browser tabs on different instances.
	c1 := make(chan []byte, 1)
	c2 := make(chan []byte, 1)
	hub.Register("c1", c1)
	hub.Register("c2", c2)

	if err := b.PublishTodoUpdate(context.Background(), []byte(payload)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	want := []byte(payload)
	for name, ch := range map[string]chan []byte{"c1": c1, "c2": c2} {
		select {
		case got := <-ch:
			if string(got) != string(want) {
				t.Fatalf("%s: got %q, want %q", name, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: timed out waiting for broadcast (JetStream fan-out broken?)", name)
		}
	}
}
