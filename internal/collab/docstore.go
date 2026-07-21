// SCOPE:layer=infra,removal=plugin — Loro CRDT + DocStore + sync workers + presence
package collab

import (
	"sync"
)

// DocStore is a shared, thread-safe repository of collaborative CRDT docs.
// Both WebSyncWorker (SSE Hub transport + NATS publishing) and SyncWorker
// (NATS subscriber) reference the same DocStore, so applying a shape op
// from one browser instance converges the same Doc that a NATS-delivered
// update from another process mutates — no split-brain.
type DocStore struct {
	mu   sync.Mutex
	docs map[string]*Doc
}

// NewDocStore creates an empty store.
func NewDocStore() *DocStore {
	return &DocStore{docs: make(map[string]*Doc)}
}

// GetOrCreate returns the Doc for id, creating an empty one if necessary.
func (s *DocStore) GetOrCreate(id string) *Doc {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[id]
	if !ok {
		d = NewDoc(id)
		s.docs[id] = d
	}
	return d
}
