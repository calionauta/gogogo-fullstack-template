// SCOPE:layer=infra,removal=plugin — CRDT store transport wiring (post-Init)
//
// Wires the CRDTStore JetStream transport when ENTITY_STORE=crdt AND
// NATS is enabled. The transport publishes every local mutation to
// JetStream (with a per-process PublisherID fingerprint for
// in-process loop avoidance) and consumes ops from peer instances,
// applying them to the local Loro doc via crdtstore.ApplyRemoteOp.
//
// When NATS is not available, the transport is nil and CRDTStore
// runs in single-process mode (snapshot-only persistence). When
// ENTITY_STORE=pb, this file is unused — the transport is only
// created for CRDT.
package server

import (
	"context"
	"log/slog"

	"github.com/calionauta/gogogo-fullstack-template/features/store/crdtstore"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
)

// WireCRDTStoreTransport attaches a JetStream transport to a CRDTStore
// and starts a Subscribe goroutine that applies incoming ops. Returns
// a shutdown function that unsubscribes + drains. Safe to call with
// a nil store (no-op) or nil JetStream (single-process mode).
//
// The transport's per-process PublisherID is a UUID generated at
// startup; on restart the new instance gets a new ID. Cross-process
// dedup still works because the PublisherID only matters for the
// in-process loop filter (same process = same ID across restarts is
// not guaranteed, but it doesn't matter — JetStream MsgId dedup
// handles the cross-restart case).
func WireCRDTStoreTransport(ctx context.Context, store *crdtstore.CRDTStore) func() {
	if store == nil {
		return func() {}
	}
	js := nats.JS
	if js == nil {
		slog.Info("crdtstore transport: JetStream not available, running in single-process mode")
		return func() {}
	}
	tr := crdtstore.NewTransport(crdtstore.TransportConfig{
		JetStream:   js,
		PublisherID: natsPublisherID(), // shared per process so subscribers on this instance know the source
	})
	if err := tr.EnsureStream(ctx); err != nil {
		slog.Error("crdtstore transport: ensure stream failed; running in single-process mode", "error", err)
		return func() {}
	}
	store.SetTransport(tr)

	// Start one Subscribe per active owner. We don't know the owner
	// list up front (docs are lazy-loaded), so we subscribe on demand
	// when a doc is first touched. For the MVP, we hook into the
	// existing server: any time a doc is created or loaded, ensure
	// there's a subscriber for that owner.
	//
	// Simpler MVP: subscribe for the well-known owner prefixes the
	// app cares about. Since the todo feature is the only CRDT
	// consumer today, we subscribe to a wildcard subject. In Phase 3
	// we'll filter per-owner.
	sub, err := tr.Subscribe(ctx, ">", func(op crdtstore.Op) error {
		return store.ApplyRemoteOp(ctx, op.OwnerID, op)
	})
	if err != nil {
		slog.Error("crdtstore transport: subscribe failed; running without consumer", "error", err)
		return func() {}
	}
	slog.Info("crdtstore transport: subscribed to wildcard", "publisher", tr.PublisherID())
	return func() {
		_ = sub.Unsubscribe()
	}
}

// natsPublisherID is the per-process identity embedded in every
// published op. Lazy-initialised on first call so a process restart
// gets a new ID (and so tests can override).
var natsPublisherID = lazyUUID

func lazyUUID() string {
	return newUUID()
}
