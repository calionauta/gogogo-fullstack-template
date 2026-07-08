//go:build jetstream

package main

import (
	"log"

	"github.com/calionauta/gogogo-template/config"
	"github.com/calionauta/gogogo-template/internal/nats"
)

func startNATS(cfg *config.Config) {
	if !cfg.NATS.Enabled {
		return
	}
	if err := nats.StartEmbedded(cfg.NATS.StoreDir); err != nil {
		log.Fatalf("NATS startup: %v", err)
	}
}
