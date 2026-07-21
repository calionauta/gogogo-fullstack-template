// SCOPE:layer=infra,removal=plugin — CRDT store transport wiring (post-Init)
//
// Wires the CRDTStore publisher so every saveSnapshot (local or via
// ApplyRemoteOp from a peer instance) fans out a doc-version-bumped
// event to the SSE Hub. Each connected client of the affected owner
// then re-renders the fragment, closing the loop:
//
//	Create → saveSnapshot → bumpVersion → publisher.PublishDocEvent
//	  → Hub.BroadcastToUser → SSE client → fragment re-fetch.
//
// Without this wiring the cross-instance E2E path stops at the
// in-memory doc (other instances update their own docs, but the UI
// doesn't know). Pair it with WireCRDTStoreTransport for the full
// Phase 2+3 picture.
package server

import (
	"encoding/json"
	"log/slog"
	"sync/atomic"

	"github.com/calionauta/gogogo-fullstack-template/features/store/crdtstore"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// hubPublisher implements crdtstore.DocPublisher by fanning out to
// the SSE Hub. Per-owner envelope: {"docVersionBumped": v}. The SSE
// handler dispatches this envelope type to a Datastar signal merge
// that triggers the client-side resync.
//
// Counter is exposed for diagnostics (tests + the realtime badge).
type hubPublisher struct {
	hub *queue.SSEHub

	count atomic.Uint64 // incremented every PublishDocEvent
}

// Compile-time check we satisfy crdtstore.DocPublisher.
var _ crdtstore.DocPublisher = (*hubPublisher)(nil)

// Name returns the diagnostics label for the publisher. Used by
// CRDTStore.PublisherName() to surface what is wired.
func (p *hubPublisher) Name() string { return "sse-hub" }

// PublishDocEvent fans out a doc-version-bumped envelope to every
// connected client of the owner. excludeClientID is "" —
// cross-store events have no originating client, every tab of the
// affected owner needs to know.
func (p *hubPublisher) PublishDocEvent(ownerID string, version uint64) {
	p.count.Add(1)
	if p.hub == nil {
		return
	}
	payload, err := json.Marshal(struct {
		Type    string `json:"type"`
		Version uint64 `json:"version"`
		Owner   string `json:"owner"`
	}{Type: "doc-version-bumped", Version: version, Owner: ownerID})
	if err != nil {
		slog.Warn("crdtstore publisher: marshal failed", "owner", ownerID, "version", version, "error", err)
		return
	}
	p.hub.BroadcastToUser(payload, ownerID, "")
}

// Count returns the number of PublishDocEvent calls observed by
// this publisher. Tests use this to assert the publisher fires
// when expected; production surfaces it on the diagnostics badge.
func (p *hubPublisher) Count() uint64 { return p.count.Load() }

// WireCRDTStorePublisher attaches a SSE Hub publisher to the
// CRDTStore. Idempotent: calling twice replaces the previous
// publisher (the second wins). Returns the publisher instance so
// tests can read counters; production discards it.
func WireCRDTStorePublisher(store *crdtstore.CRDTStore, hub *queue.SSEHub) *hubPublisher {
	if store == nil {
		return nil
	}
	pub := &hubPublisher{hub: hub}
	store.SetPublisher(pub)
	slog.Info("crdtstore publisher: SSE Hub wired", "name", pub.Name())
	return pub
}
