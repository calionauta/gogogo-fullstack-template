// SCOPE:layer=infra,removal=plugin — NATS JetStream + Leaf Node + CRUD proxy
// Starts embedded NATS server or connects as Leaf Node.
package nats

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
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
	NS, NC, JS = nil, nil, nil
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
	const natsReadyTimeout = 10 * time.Second
	if !ns.ReadyForConnections(natsReadyTimeout) {
		return fmt.Errorf("nats: embedded server never became ready")
	}
	if err := waitForJetStream(js, natsReadyTimeout); err != nil {
		return err
	}

	slog.Info("NATS embedded server started (JetStream enabled)", "url", ns.ClientURL())
	return nil
}

// StartLeafNode starts an embedded NATS server configured as a Leaf Node
// that syncs with a central NATS server at centralURL. The local server
// persists JetStream streams on disk and replays buffered events to the
// central automatically when connectivity returns — this is the edge
// sync primitive for the desktop/mobile app (offline-first).
//
// storeDir holds the local JetStream file storage; centralURL is the
// ws/nats URL of the central server (e.g. nats://demo.example.com:7422).
func StartLeafNode(storeDir, centralURL string) error {
	u, err := url.Parse(centralURL)
	if err != nil {
		return fmt.Errorf("parse leaf node central url: %w", err)
	}
	ns, err := server.NewServer(&server.Options{
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
		StoreDir:  storeDir,
		JetStream: true,
		LeafNode: server.LeafNodeOpts{
			Remotes: []*server.RemoteLeafOpts{
				{
					URLs: []*url.URL{u},
				},
			},
		},
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

	if !ns.ReadyForConnections(10 * time.Second) {
		return fmt.Errorf("nats: leaf node server never became ready")
	}
	if err := waitForJetStream(js, 10*time.Second); err != nil {
		return err
	}
	slog.Info("NATS leaf node started (syncing with central)", "central", centralURL, "local", ns.ClientURL())
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

// ConnectExisting connects to an already-running NATS server (e.g. the
// one booted by the DagNats engine) and wires the
// package-level NC/JS singletons. This is the single-NATS setup: instead
// of starting a second embedded server, the realtime broadcaster reuses
// the JetStream instance DagNats already owns on the conventional port.
//
// RetryOnFailedConnect makes nats.Connect block until the server is
// reachable (the library retries internally) rather than returning
// immediately — so callers get a clean synchronous "NATS is ready" point
// with no polling loop of their own. If the server never comes up, the
// call returns an error after the configured timeout and the caller
// falls back to the in-memory broadcaster.
func ConnectExisting(url string) error {
	nc, err := natsio.Connect(
		url,
		natsio.RetryOnFailedConnect(true),
		natsio.Timeout(15*time.Second), //nolint:mnd // 15s is the standard NATS connection timeout
	)
	if err != nil {
		return err
	}
	NC = nc

	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	JS = js

	if err := waitForJetStream(js, 10*time.Second); err != nil {
		return err
	}
	slog.Info("NATS connected to existing server (JetStream enabled)", "url", url)
	return nil
}

// Stop shuts down the embedded server (if one was started) and closes
// the connection.
func Stop() {
	if NC != nil {
		NC.Close()
		NC = nil
	}
	if NS != nil {
		NS.Shutdown()
		NS = nil
	}
	JS = nil
}

// JetStream returns the JetStream context established by StartEmbedded
// (or ConnectExisting). It is nil until one of them has succeeded.
func JetStream() natsio.JetStreamContext {
	return JS
}

func ClientURL() string {
	if NS == nil {
		return ""
	}
	return NS.ClientURL()
}

// Conn returns the underlying *natsio.Conn established by StartEmbedded or
// ConnectExisting. It is nil until one of them has succeeded. Callers
// that need the raw connection (e.g. to subscribe a SyncWorker) use this.
func Conn() *natsio.Conn {
	return NC
}
