// SCOPE:layer=infra,removal=plugin — collaboration (whiteboard depends on this)
package collab

import (
	"encoding/json"
	"log/slog"

	natsio "github.com/nats-io/nats.go"

	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// Shape is one whiteboard primitive stored in the Loro CRDT. The server
// owns the Doc; the browser only sends ops and renders the plain JSON
// the server broadcasts, so the client needs NO JS CRDT library.
type Shape struct {
	ID     string    `json:"id"`
	Type   string    `json:"type"` // "rect" | "ellipse" | "line" | "pen"
	X      float64   `json:"x"`
	Y      float64   `json:"y"`
	W      float64   `json:"w"`
	H      float64   `json:"h"`
	Points []float64 `json:"points,omitempty"` // pen: flat [x0,y0,x1,y1,...]
	Color  string    `json:"color"`
	Author string    `json:"author,omitempty"`
}

// ShapeOp is a single mutation a browser sends. Op is "add" | "clear".
type ShapeOp struct {
	Op    string `json:"op"`
	Shape Shape  `json:"shape"`
}

// WebShapesEvent is the wire envelope broadcast to peers: the resolved
// shapes list after applying an op. Clients re-render from it.
type WebShapesEvent struct {
	Type   string  `json:"type"` // "shapes"
	Doc    string  `json:"doc"`
	From   string  `json:"from"`
	Shapes []Shape `json:"shapes"`
}

// WebSyncWorker is the SSE-only transport (works WITHOUT NATS).
// WebSyncWorker is the unified transport for collaborative docs. It uses
// the SSE hub for in-process fan-out (same as the todo feature) AND
// publishes updates to NATS (subject app.sync.<docID>) so that other
// instances (or the SyncWorker subscriber) can converge the same docs.
//
// With NATS always compiled in (unified build), shape ops are dual-broadcast:
//  1. Via the SSE Hub to every connected browser on this instance
//  2. Via NATS to the SyncWorker (and other instances behind a LB)
//
// Flow per doc:
//   - The browser POSTs its Loro update bytes to /api/whiteboard/<id>/update.
//   - The handler calls ApplyOp, which merges into the in-memory Doc
//     (shared via DocStore with the NATS SyncWorker) and persists the
//     resolved snapshot via the Persister (PocketBase).
//   - The worker broadcasts to the SSE hub (all instances) AND publishes
//     to NATS (other instances via SyncWorker).
type WebSyncWorker struct {
	hub       *queue.SSEHub
	persister Persister
	nc        *natsio.Conn // optional NATS connection for cross-instance sync
	docs      *DocStore    // shared with SyncWorker
}

// NewWebSyncWorker builds a web transport worker bound to the SSE hub.
// The Persister is typically the PocketBase whiteboards collection.
// docs is the shared doc store (pass collab.NewDocStore() or reuse the
// one passed to SyncWorker). nc is the NATS connection for cross-instance
// publishing; pass nil for SSE-only mode.
func NewWebSyncWorker(hub *queue.SSEHub, p Persister, docs *DocStore, nc *natsio.Conn) *WebSyncWorker {
	if docs == nil {
		docs = NewDocStore()
	}
	return &WebSyncWorker{
		hub:       hub,
		persister: p,
		nc:        nc,
		docs:      docs,
	}
}

// doc returns the in-memory CRDT for docID from the shared DocStore.
func (w *WebSyncWorker) doc(docID string) *Doc {
	return w.docs.GetOrCreate(docID)
}

// natsSubject returns the NATS subject for a doc's sync updates.
func natsSyncSubject(docID string) string { return "app.sync." + docID }

// ApplyUpdate merges an incoming Loro update for docID, persists the
// resolved snapshot, and broadcasts the update to every other connected
// client on the doc's stream (SSE hub) and to NATS (cross-instance).
// fromClientID is the originator (excluded from the broadcast).
func (w *WebSyncWorker) ApplyUpdate(docID, fromClientID string, update []byte) (int, error) {
	d := w.doc(docID)
	if err := d.ApplyUpdate(update); err != nil {
		return 0, err
	}
	snapshot := d.EncodeSnapshot()
	if w.persister != nil {
		if err := w.persister.SaveSnapshot(docID, snapshot); err != nil {
			slog.Warn("collab: persist snapshot failed", "doc", docID, "error", err)
		}
	}
	// SSE Hub broadcast (in-process, to every browser on this instance).
	payload, err := json.Marshal(WebUpdateEvent{
		Type: "update",
		Doc:  docID,
		From: fromClientID,
		Data: update,
	})
	if err != nil {
		slog.Warn("collab: marshal update", "doc", docID, "error", err)
		return len(snapshot), err
	}
	w.hub.BroadcastExcept(payload, fromClientID)

	// NATS broadcast (cross-instance): publish the raw Loro update so the
	// SyncWorker on other instances (subscribed to app.sync.>) also
	// converges this doc. Published regardless of fromClientID because
	// other instances did NOT originate this op.
	if w.nc != nil {
		if nErr := w.nc.Publish(natsSyncSubject(docID), update); nErr != nil {
			slog.Warn("collab: nats publish update", "doc", docID, "error", nErr)
		}
	}

	return len(snapshot), nil
}

// ApplyOp applies a shape op (add / clear) to the doc's LoroMap, persists
// the snapshot, and broadcasts to the SSE hub AND NATS.
func (w *WebSyncWorker) ApplyOp(docID, fromClientID string, op ShapeOp) ([]Shape, error) {
	d := w.doc(docID)
	shapes, err := d.ApplyShapeOp(op)
	if err != nil {
		return nil, err
	}
	snapshot := d.EncodeSnapshot()
	if w.persister != nil {
		if sErr := w.persister.SaveSnapshot(docID, snapshot); sErr != nil {
			slog.Warn("collab: persist snapshot failed", "doc", docID, "error", sErr)
		}
	}
	payload, err := json.Marshal(WebShapesEvent{
		Type:   "shapes",
		Doc:    docID,
		From:   fromClientID,
		Shapes: shapes,
	})
	if err != nil {
		slog.Warn("collab: marshal shapes", "doc", docID, "error", err)
		return shapes, err
	}
	w.hub.BroadcastExcept(payload, fromClientID)

	// NATS broadcast: publish the raw Loro update so the SyncWorker on
	// other instances converges this doc too.
	if w.nc != nil {
		// Encode the shape op as a Loro update for the NATS sync path.
		// The SyncWorker persists and re-broadcasts to its own SSE hub.
		if nErr := w.nc.Publish(natsSyncSubject(docID), d.EncodeSnapshot()); nErr != nil {
			slog.Warn("collab: nats publish shapes", "doc", docID, "error", nErr)
		}
	}

	return shapes, nil
}

// Shapes returns the current resolved shape list for a doc.
func (w *WebSyncWorker) Shapes(docID string) []Shape {
	return w.doc(docID).Shapes()
}

// WebUpdateEvent is the wire envelope for a web transport update, sent
// (if a snapshot exists). Called when a client opens the doc so it starts
// from the latest saved state before receiving live updates.
func (w *WebSyncWorker) LoadSnapshot(docID string) ([]byte, bool) {
	if w.persister == nil {
		return nil, false
	}
	snap, ok := w.persister.LoadSnapshot(docID)
	if !ok {
		return nil, false
	}
	d := w.doc(docID)
	if err := d.ApplyUpdate(snap); err != nil {
		slog.Warn("collab: rehydrate failed", "doc", docID, "error", err)
		return nil, false
	}
	return snap, true
}

// WebUpdateEvent is the wire envelope for a web transport update, sent
// over the SSE hub to peers. Data is the raw Loro update bytes.
type WebUpdateEvent struct {
	Type string `json:"type"` // "update"
	Doc  string `json:"doc"`
	From string `json:"from"` // originator client id (for diagnostics)
	Data []byte `json:"data"` // Loro update bytes
}
