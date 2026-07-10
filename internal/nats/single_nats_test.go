//go:build jetstream && dagnats

package nats

import (
	"testing"
	"time"

	"github.com/danmestas/dagnats/server"
)

// TestConnectExisting_SingleNATS proves the single-NATS convention: when
// DagNats owns the embedded NATS on :4222, the realtime broadcaster
// connects to that existing server (via ConnectExisting) instead of
// starting a second one. A published update must round-trip through the
// shared JetStream, confirming the broadcaster and DagNats share one NATS.
func TestConnectExisting_SingleNATS(t *testing.T) {
	srv := server.New(server.Config{
		DataDir:       t.TempDir(),
		HTTPAddr:      "127.0.0.1:18099",
		NATSPort:      4222,
		MaxStoreBytes: 1 << 30,
	})
	go func() { _ = srv.Run() }()
	// ConnectExisting uses RetryOnFailedConnect, so it blocks until the
	// engine's NATS is reachable — no polling loop needed here.
	if err := ConnectExisting("127.0.0.1:4222"); err != nil {
		t.Fatalf("ConnectExisting failed to wire JS against DagNats-owned NATS: %v", err)
	}
	if JS == nil {
		t.Fatal("ConnectExisting returned nil JS")
	}

	// The broadcaster's stream setup must succeed on the shared JetStream
	// (no "no responders" — the engine's JetStream is the same instance).
	const stream = "TODOS_SINGLE_NATS_TEST"
	if err := EnsureStream(stream, []string{"todo.single.>"}); err != nil {
		t.Fatalf("EnsureStream on shared NATS failed: %v", err)
	}

	sub, err := JS.SubscribeSync("todo.single.>")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Publish directly on the stream's subject (same path the real
	// JetStreamBroadcaster uses: JS.Publish("todo.<id>", data)).
	if _, err := JS.Publish("todo.single.update", []byte("hello-from-shared-nats")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("did not receive published event on shared NATS: %v", err)
	}
	if string(msg.Data) != "hello-from-shared-nats" {
		t.Fatalf("unexpected payload: %q", string(msg.Data))
	}
}
