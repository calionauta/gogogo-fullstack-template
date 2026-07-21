// SCOPE:layer=infra,removal=plugin — Loro CRDT + DocStore + sync workers + presence
// To remove: delete internal/collab/ + features/whiteboard/ + router/whiteboard.go + router/collab_jetstream.go.
// Package collab wraps the Loro CRDT for collaborative features
// (whiteboard, shared docs). This is the local CRDT model; transport
// is NATS Leaf Node — collab produces bytes, the SyncWorker
// publishes/applies them over JetStream.
package collab

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	loro "github.com/aholstenson/loro-go"
)

// Doc is a single collaborative document (whiteboard, shared list, etc.)
// backed by a Loro CRDT. It is safe for concurrent use: all mutations go
// through the embedded mutex so the desktop UI thread and the sync
// publisher never race on the LoroDoc.
type Doc struct {
	mu   sync.Mutex
	id   string
	loro *loro.LoroDoc
}

// NewDoc creates an empty collaborative doc with the given ID (UUID v7 in
// practice). The ID is the JetStream subject key (app.sync.<id>) so
// updates route to the right doc on every peer.
func NewDoc(id string) *Doc {
	return &Doc{id: id, loro: loro.NewLoroDoc()}
}

// ID returns the document identifier.
func (d *Doc) ID() string {
	return d.id
}

// ApplyUpdate merges a Loro update (bytes from EncodeUpdate) into this
// doc. Concurrent-safe. Returns an error if the bytes are not a valid
// Loro update.
func (d *Doc) ApplyUpdate(update []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err := d.loro.Import(update); err != nil {
		return err
	}
	return nil
}

// EncodeSnapshot returns a self-contained snapshot of the full doc state.
// Used to bootstrap a peer or to persist the resolved state to PocketBase.
func (d *Doc) EncodeSnapshot() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, err := d.loro.Export(loro.SnapshotMode())
	if err != nil {
		// Export only errors on a malformed doc; with a freshly created
		// LoroDoc this is unreachable. Propagate so callers can log.
		panic(err)
	}
	return b
}

// EncodeUpdate returns the delta from the given peer version vector to
// the current state — i.e. "what changed since this peer last synced".
// Use an empty VersionVector (loro.NewVersionVector) to export all changes
// from the doc's origin (the common case for small collaborative docs).
func (d *Doc) EncodeUpdate(since *loro.VersionVector) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if since == nil {
		since = loro.NewVersionVector()
	}
	return d.loro.Export(loro.UpdatesMode(since))
}

// StateVersion returns this doc's current version vector, used by the
// sync layer to compute deltas for the next publish.
func (d *Doc) StateVersion() *loro.VersionVector {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loro.StateVv()
}

// ApplyShapeOp applies a whiteboard shape op to the doc's "shapes"
// LoroMap. Op "add" inserts/updates one shape (keyed by its id); op
// "clear" empties the map. Because the map is a CRDT, concurrent adds
// from different clients merge without data loss. Returns the resolved
// shape list after the op. Must be called with the doc mutex held by the
// caller path; this method locks internally for safety.
func (d *Doc) ApplyShapeOp(op ShapeOp) ([]Shape, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	shapesMap := d.loro.GetMap(loro.AsContainerId("shapes"))
	switch op.Op {
	case "add":
		var entry *loro.LoroMap
		vc := shapesMap.Lookup(op.Shape.ID)
		if vc != nil && vc.IsContainer() {
			if m := vc.AsLoroMap(); m != nil && *m != nil {
				entry = *m
			}
		}
		if entry == nil {
			child, err := shapesMap.InsertMapContainer(op.Shape.ID, loro.NewLoroMap())
			if err != nil {
				return nil, err
			}
			entry = child
		}
		if err := writeShape(entry, op.Shape); err != nil {
			return nil, err
		}
	case "clear":
		if err := shapesMap.Clear(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown shape op %q", op.Op)
	}
	d.loro.Commit()
	return readShapes(shapesMap)
}

// Shapes returns the current resolved shape list (read-only snapshot).
func (d *Doc) Shapes() []Shape {
	d.mu.Lock()
	defer d.mu.Unlock()
	shapesMap := d.loro.GetMap(loro.AsContainerId("shapes"))
	out, err := readShapes(shapesMap)
	if err != nil {
		slog.Warn("collab: read shapes", "doc", d.id, "error", err)
		return nil
	}
	return out
}

// writeShape stores a Shape's primitive fields into a LoroMap. Points are
// serialized to a JSON string to avoid a nested LoroList (simpler +
// fully round-trippable).
func writeShape(m *loro.LoroMap, s Shape) error {
	if err := m.InsertAny("id", s.ID); err != nil {
		return err
	}
	if err := m.InsertAny("type", s.Type); err != nil {
		return err
	}
	if err := m.InsertAny("x", s.X); err != nil {
		return err
	}
	if err := m.InsertAny("y", s.Y); err != nil {
		return err
	}
	if err := m.InsertAny("w", s.W); err != nil {
		return err
	}
	if err := m.InsertAny("h", s.H); err != nil {
		return err
	}
	pts, mErr := json.Marshal(s.Points)
	if mErr != nil {
		return mErr
	}
	if err := m.InsertAny("points", string(pts)); err != nil {
		return err
	}
	if err := m.InsertAny("color", s.Color); err != nil {
		return err
	}
	if err := m.InsertAny("author", s.Author); err != nil {
		return err
	}
	return nil
}

// readShapes reconstructs the Shape slice from a shapes LoroMap.
func readShapes(shapesMap *loro.LoroMap) ([]Shape, error) {
	if shapesMap == nil {
		return nil, nil
	}
	out := make([]Shape, 0)
	for id, vc := range shapesMap.All() {
		if vc == nil || !vc.IsContainer() {
			continue
		}
		m := vc.AsLoroMap()
		if m == nil || *m == nil {
			continue
		}
		entry := *m
		var pts []float64
		if raw, ok := entry.GetString("points"); ok && raw != "" {
			if uErr := json.Unmarshal([]byte(raw), &pts); uErr != nil {
				return nil, uErr
			}
		}
		out = append(out, Shape{
			ID:     id,
			Type:   getStr(entry, "type"),
			X:      getF64(entry, "x"),
			Y:      getF64(entry, "y"),
			W:      getF64(entry, "w"),
			H:      getF64(entry, "h"),
			Points: pts,
			Color:  getStr(entry, "color"),
			Author: getStr(entry, "author"),
		})
	}
	return out, nil
}

func getStr(m *loro.LoroMap, k string) string {
	v, _ := m.GetString(k)
	return v
}

func getF64(m *loro.LoroMap, k string) float64 {
	v, _ := m.GetFloat64(k)
	return v
}
