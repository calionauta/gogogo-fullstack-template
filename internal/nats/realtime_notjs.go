//go:build !jetstream

// Package nats provides the realtime broadcaster abstraction. Without the
// `jetstream` build tag this is an in-memory fan-out backed by the SSE
// Hub: every connected client on the same process sees the update. This
// is the right default for a single-binary, single-instance deployment
// where "all clients" means "all tabs on this server".
package nats

import (
	"context"

	"github.com/calionauta/gogogo-template/internal/queue"
)

// TodoBroadcaster publishes todo mutations so every connected client
// receives them in real time. The nats package owns this interface and
// provides two implementations: an in-memory fan-out (default build) and
// a JetStream-backed one (-tags jetstream) for multi-instance / multi-user
// sharing. The todo feature imports this interface without depending on
// the transport.
type TodoBroadcaster interface {
	// PublishTodoUpdate broadcasts a serialized todo event (JSON) to all
	// connected clients.
	PublishTodoUpdate(ctx context.Context, payload []byte) error
}

// NewTodoBroadcaster returns the default (in-memory) broadcaster bound to
// hub. With -tags jetstream the same call returns a JetStream-backed
// broadcaster; the signature is identical so callers don't branch.
func NewTodoBroadcaster(_ any, hub *queue.SSEHub) TodoBroadcaster {
	return NewInMemoryBroadcaster(hub)
}
