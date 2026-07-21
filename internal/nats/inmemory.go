// SCOPE:layer=infra,removal=plugin — NATS JetStream + Leaf Node + CRUD proxy
// Single-instance. Part of internal/nats package.
// If you keep TODO without NATS (in-memory only), this file is needed.
package nats

import (
	"context"

	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// InMemoryBroadcaster fans out todo updates via the SSE Hub's Broadcast
// method. Suitable for single-instance deployments; for multi-instance
// or multi-user collaboration use the JetStream-backed broadcaster (wired
// when NATS is enabled). Shared by both builds because the
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

// Subscribe is a no-op for the in-memory broadcaster: it already holds
// the SSE Hub directly and fans out through it, so there is no separate
// transport to bind. Declared so callers can call Subscribe uniformly
// across both implementations.
func (b *InMemoryBroadcaster) Subscribe(_ *queue.SSEHub) {}
