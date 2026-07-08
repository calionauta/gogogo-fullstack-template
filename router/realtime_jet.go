//go:build jetstream

package router

import (
	"github.com/calionauta/gogogo-template/internal/nats"
	"github.com/calionauta/gogogo-template/internal/queue"
)

// newTodoBroadcaster builds the JetStream-backed broadcaster from the
// global embedded connection and subscribes it to the SSE Hub so every
// connected client (on any instance) receives todo mutations.
func newTodoBroadcaster(hub *queue.SSEHub) nats.TodoBroadcaster {
	b := nats.NewTodoBroadcaster(nats.JS, hub)
	if sub, ok := b.(interface{ Subscribe(*queue.SSEHub) }); ok {
		sub.Subscribe(hub)
	}
	return b
}
