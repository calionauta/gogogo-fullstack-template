//go:build !jetstream

package router

import (
	"github.com/calionauta/gogogo-template/internal/nats"
	"github.com/calionauta/gogogo-template/internal/queue"
)

// newTodoBroadcaster builds the default in-memory broadcaster. Todo
// mutations are fanned out to every client connected to this process
// via the SSE Hub.
func newTodoBroadcaster(hub *queue.SSEHub) nats.TodoBroadcaster {
	return nats.NewTodoBroadcaster(nil, hub)
}
