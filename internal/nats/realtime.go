// SCOPE:layer=infra,removal=plugin — NATS JetStream + Leaf Node + CRUD proxy
// Package nats provides the realtime broadcaster abstraction. It uses
// NATS JetStream: todo mutations are
// published to a durable stream and a single subscriber per process
// re-emits them to the SSE Hub, so every connected browser tab — on any
// instance — receives the update. This is the correct choice for
// multi-user, multi-instance realtime; it complements (never replaces)
// goqite, which still owns the async job + per-client toast path.
package nats

import (
	"bytes"
	"context"
	"log/slog"
	"sync"

	natsio "github.com/nats-io/nats.go"

	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// JetStreamLike is the shape startNATS returns. On jetstream builds it
// is natsio.JetStreamContext; the nojs build aliases it to any. Keeping
// the name lets callers pass the result into NewTodoBroadcaster without
// build-tag branching.
type (
	JetStreamLike = natsio.JetStreamContext
	// receives them in real time. Defined identically in both builds so the
	// caller's type stays stable across tags.
	TodoBroadcaster interface {
		// PublishTodoUpdate broadcasts a serialized todo event (JSON) to all
		// connected clients. Used for ephemeral todo signals (e.g. workflow
		// completion) — record mutations now flow through PocketBase realtime.
		PublishTodoUpdate(ctx context.Context, payload []byte) error
		// Subscribe binds the broadcaster's transport to hub so published
		// events are re-emitted to every connected client. No-op for the
		// in-memory variant (which already holds the hub directly).
		Subscribe(hub *queue.SSEHub)
	}
)

// todoStream is the JetStream stream that carries todo mutations. The
// subject layout `todos.>` lets us subscribe to all mutations with one
// wildcard and keeps the history available for late joiners via
// JetStream replay.
const (
	todoStream  = "TODOS"
	todoSubject = "todos.update"
)

// JetStreamBroadcaster publishes todo updates to a JetStream stream and
// subscribes to that stream, re-emitting every event to the SSE Hub via
// Broadcast so all connected clients receive it.
type JetStreamBroadcaster struct {
	js  natsio.JetStreamContext
	sub *natsio.Subscription
	hub *queue.SSEHub

	mu     sync.Mutex
	closed bool
}

// NewJetStreamBroadcaster ensures the TODOS stream exists, subscribes the
// broadcaster to it, and returns a broadcaster bound to hub. Subscription
// happens here (not in the caller) so the realtime path cannot silently
// miss mutations because a caller forgot to Subscribe — that was the bug
// where broadcasts were registered on the server but never reached remote
// clients. Safe to construct once per process (a second consumer on the
// same durable name is rejected by JetStream).
func NewJetStreamBroadcaster(js natsio.JetStreamContext, hub *queue.SSEHub) (*JetStreamBroadcaster, error) {
	if _, err := js.AddStream(&natsio.StreamConfig{
		Name:     todoStream,
		Subjects: []string{todoSubject},
		Storage:  natsio.FileStorage,
	}); err != nil {
		// AddStream only errors on genuine misconfiguration; an existing
		// stream is not an error.
		if err.Error() != "stream already exists" {
			return nil, err
		}
	}
	b := &JetStreamBroadcaster{js: js, hub: hub}
	// Auto-subscribe so the broadcaster is live the moment it is built.
	b.Subscribe(hub)
	return b, nil
}

// PublishTodoUpdate publishes payload to the TODOS stream. Any process
// subscribed to the stream (including this one) re-emits it to its SSE
// Hub, so all tabs connected to any instance see the change.
func (b *JetStreamBroadcaster) PublishTodoUpdate(_ context.Context, payload []byte) error {
	_, err := b.js.Publish(todoSubject, payload)
	return err
}

// Subscribe registers a durable consumer that pumps every TODOS event
// to the SSE Hub's Broadcast. Safe to call once; subsequent calls are
// idempotent.
func (b *JetStreamBroadcaster) Subscribe(hub *queue.SSEHub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.sub != nil {
		return
	}
	sub, err := b.js.Subscribe(todoSubject, func(msg *natsio.Msg) {
		data := msg.Data
		// Strip the optional \x00<clientID>\x00 origin tag and skip
		// re-emitting to the originator (it already patched locally).
		if len(data) > 0 && data[0] == 0 {
			if idx := bytes.IndexByte(data, 0); idx >= 0 && idx+1 < len(data) && data[idx+1] == 0 {
				from := string(data[1:idx])
				data = data[idx+2:]
				if from != "" {
					b.hub.BroadcastExcept(data, from)
					_ = msg.Ack()
					return
				}
			}
		}
		hub.Broadcast(msg.Data)
		_ = msg.Ack()
	}, natsio.Durable("gogogo-fullstack-template-todos"), natsio.ManualAck())
	if err != nil {
		slog.Error("realtime: subscribe failed", "error", err)
		return
	}
	b.sub = sub
	b.hub = hub
}

// Close drains the subscription. The JetStream stream and its history
// persist for late joiners; only this process's consumer stops.
func (b *JetStreamBroadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	if b.sub != nil {
		_ = b.sub.Unsubscribe()
		b.sub = nil
	}
}

// NewTodoBroadcaster returns a JetStream-backed broadcaster bound to hub.
// It auto-subscribes to the TODOS stream, so published events are
// re-emitted to the SSE Hub without any further caller action. With the
// no JetStream context wired (js == nil) the same call returns an
// in-memory broadcaster; the signature is identical so callers don't branch.
func NewTodoBroadcaster(js JetStreamLike, hub *queue.SSEHub) TodoBroadcaster {
	jsCtx, ok := js.(natsio.JetStreamContext) //nolint:staticcheck // runtime alias unwrap
	if !ok {
		return NewInMemoryBroadcaster(hub)
	}
	b, err := NewJetStreamBroadcaster(jsCtx, hub)
	if err != nil {
		slog.Error("realtime: falling back to in-memory broadcaster", "error", err)
		return NewInMemoryBroadcaster(hub)
	}
	return b
}
