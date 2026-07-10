//go:build !jetstream

package router

import (
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// newTodoBroadcaster builds the default in-memory broadcaster. Todo
// mutations are fanned out to every client connected to this process
// via the SSE Hub. The js argument is ignored on non-jetstream builds.
func newTodoBroadcaster(_ nats.JetStreamLike, hub *queue.SSEHub) nats.TodoBroadcaster {
	return nats.NewTodoBroadcaster(nil, hub)
}
