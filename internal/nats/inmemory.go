package nats

import (
	"context"

	"github.com/calionauta/gogogo-template/internal/queue"
)

// InMemoryBroadcaster fans out todo updates via the SSE Hub's Broadcast
// method. Suitable for single-instance deployments; for multi-instance
// or multi-user collaboration build with `-tags jetstream`, which swaps
// in the JetStream-backed broadcaster. Shared by both builds because the
// JetStream variant falls back to it on setup errors.
type InMemoryBroadcaster struct {
	hub *queue.SSEHub
}

// NewInMemoryBroadcaster returns a broadcaster bound to hub.
func NewInMemoryBroadcaster(hub *queue.SSEHub) *InMemoryBroadcaster {
	return &InMemoryBroadcaster{hub: hub}
}

// PublishTodoUpdate sends payload to every client registered on the hub.
func (b *InMemoryBroadcaster) PublishTodoUpdate(_ context.Context, payload []byte) error {
	if b.hub != nil {
		b.hub.Broadcast(payload)
	}
	return nil
}
