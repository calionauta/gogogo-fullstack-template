//go:build !jetstream

package main

import (
	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
)

// startNATS is a no-op on non-jetstream builds; it returns nil so the
// caller falls back to the in-memory broadcaster.
func startNATS(cfg *config.Config) nats.JetStreamLike {
	_ = cfg
	return nil
}
