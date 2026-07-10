//go:build jetstream

package router

import (
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// newTodoBroadcaster builds the JetStream-backed broadcaster from the
// supplied JetStream context (returned by startNATS) and subscribes it
// to the SSE Hub so every connected client (on any instance) receives
// todo mutations. If js is nil (NATS failed to start) it returns the
// in-memory broadcaster instead.
func newTodoBroadcaster(js nats.JetStreamLike, hub *queue.SSEHub) nats.TodoBroadcaster {
	b := nats.NewTodoBroadcaster(js, hub)
	if sub, ok := b.(interface{ Subscribe(*queue.SSEHub) }); ok {
		sub.Subscribe(hub)
	}
	return b
}
