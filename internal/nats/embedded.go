//go:build jetstream

package nats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsio "github.com/nats-io/nats.go"
)

var (
	NS *server.Server
	NC *natsio.Conn
	JS natsio.JetStreamContext
)

// StartEmbedded starts an embedded NATS server with JetStream enabled
// and connects to it. The realtime broadcaster (the TODOS stream)
// requires JetStream: without it, AddStream has no responder and the
// app falls back to the in-memory broadcaster with the
// "nats: no responders" log line.
func StartEmbedded(storeDir string) error {
	ns, err := server.NewServer(&server.Options{
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
		StoreDir:  storeDir,
		JetStream: true,
	})
	if err != nil {
		return err
	}
	ns.Start()
	NS = ns

	nc, err := natsio.Connect(ns.ClientURL())
	if err != nil {
		return err
	}
	NC = nc

	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	JS = js

	// Wait for the embedded server to accept client connections, then
	// for JetStream to be fully initialized. JetStream's store restore
	// can finish a moment after Start() returns; if a caller issues
	// AddStream before it's up, the request hits "no responders" and
	// the broadcaster falls back to in-memory.
	if !ns.ReadyForConnections(10 * time.Second) {
		return fmt.Errorf("nats: embedded server never became ready")
	}
	if err := waitForJetStream(js, 10*time.Second); err != nil {
		return err
	}

	slog.Info("NATS embedded server started (JetStream enabled)", "url", ns.ClientURL())
	return nil
}

// waitForJetStream polls AccountInfo until the JetStream API is serving
// or the timeout expires. AccountInfo issues a request to $JS.API.INFO,
// which errors with "no responders" until JetStream is initialized.
func waitForJetStream(js natsio.JetStreamContext, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := js.AccountInfo(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("nats: jetstream not ready within %s", timeout)
		case <-ticker.C:
		}
	}
}

func Stop() {
	if NC != nil {
		NC.Close()
	}
	if NS != nil {
		NS.Shutdown()
	}
}

// JetStream returns the JetStream context established by StartEmbedded.
// It is nil until StartEmbedded has succeeded.
func JetStream() natsio.JetStreamContext {
	return JS
}

func ClientURL() string {
	if NS == nil {
		return ""
	}
	return NS.ClientURL()
}
