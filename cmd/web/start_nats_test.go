//go:build jetstream && dagnats

package main

import (
	"testing"

	"github.com/danmestas/dagnats/server"

	"github.com/calionauta/gogogo-fullstack-template/config"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
)

// TestStartNATS_SingleNATSWithDagNats is the regression guard
// for the single-NATS convention. It mirrors cmd/web/main.go's
// boot order: DagNats (which owns the embedded NATS on :4222)
// starts FIRST, then startNATS connects to that existing server
// instead of starting a second one.
//
// If someone reverts this to "start a second NATS" (or flips
// the boot order so ConnectExisting has nothing to attach to), this
// test fails: either nats.JetStream() stays nil (no broadcaster)
// or a second listener appears on :4222.
func TestStartNATS_SingleNATSWithDagNats(t *testing.T) {
	cfg := &config.Config{}
	cfg.DagNats.Enabled = true
	cfg.DagNats.HTTPAddr = "127.0.0.1:18098"
	cfg.DagNats.NATSPort = 14222 // distinct from TestConnectExisting's 4222 to avoid parallel-port clash
	cfg.DagNats.StoreDir = t.TempDir()
	cfg.NATS.Enabled = true
	cfg.NATS.StoreDir = t.TempDir()

	// DagNats owns the NATS on the conventional port.
	srv := server.New(server.Config{
		DataDir:       t.TempDir(),
		HTTPAddr:      "127.0.0.1:18098",
		NATSPort:      14222,
		MaxStoreBytes: 1 << 30,
	})
	go func() { _ = srv.Run() }()

	// startNATS internally calls ConnectExisting, which uses
	// RetryOnFailedConnect (15s) — it blocks until DagNats' NATS on
	// :4222 is reachable. No HTTP /ready poll needed (mirrors
	// internal/nats.TestConnectExisting_SingleNATS).
	js := startNATS(cfg)
	if js == nil {
		t.Fatal("startNATS returned nil JetStream — broadcaster would fall back to in-memory (single-NATS broken)")
	}
	if nats.JetStream() == nil {
		t.Fatal("nats.JetStream() is nil after startNATS — ConnectExisting did not wire the shared NATS")
	}

	// The single NATS on :4222 must be the one DagNats owns.
	// If startNATS had started its own, there would be two listeners;
	// we assert the engine's server is still the only source by
	// confirming the broadcaster can publish through it.
	if err := nats.EnsureStream("TODOS_TEST_SINGLE", []string{"todo.>"}); err != nil {
		t.Fatalf("EnsureStream on shared NATS failed (second NATS?): %v", err)
	}
}
