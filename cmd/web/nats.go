//go:build jetstream

package main

import (
	"log"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
)

// startNATS boots the embedded NATS server (when NATS is enabled) and
// returns a JetStreamContext wired to it, or nil if NATS is disabled or
// failed to start (the caller falls back to the in-memory broadcaster).
func startNATS(cfg *config.Config) nats.JetStreamLike {
	if !cfg.NATS.Enabled {
		return nil
	}
	if err := nats.StartEmbedded(cfg.NATS.StoreDir); err != nil {
		// Don't take the whole app down if embedded NATS can't start
		// (e.g. a read-only or full store dir). Fall back to the
		// in-memory broadcaster so realtime still works within the
		// instance.
		log.Printf("WARN: NATS startup failed, falling back to in-memory broadcaster: %v", err)
		return nil
	}
	js := nats.JetStream()
	return js
}
