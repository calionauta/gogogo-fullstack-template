// SCOPE:layer=infra,removal=plugin — Loro CRDT + DocStore + sync workers + presence
package collab

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

// WebPresence provides ephemeral multi-user cursor/presence for the
// web-only transport. It uses the SSE hub (same fan-out as the todo
// feature) instead of NATS pub/sub: cursors are broadcast to every other
// client on the doc's stream. Presence is volatile — there is no
// PocketBase persistence.
//
// Coordinates are normalized (0..1) so any viewport can place a remote
// cursor regardless of the peer's canvas size. A heartbeat keeps the
// roster fresh; stale entries expire after TTL.
type WebPresence struct {
	hub  *queue.SSEHub
	doc  string
	user string
	hb   time.Duration
	ttl  time.Duration

	mu       sync.Mutex
	roster   map[string]PresenceMsg
	handlers []func(PresenceMsg)
}

// NewWebPresence builds a web presence session for one user on one doc.
// hb/ttl default to 3s/8s when zero.
func NewWebPresence(hub *queue.SSEHub, docID, user string, hb, ttl time.Duration) *WebPresence {
	if hb <= 0 {
		hb = 3 * time.Second
	}
	if ttl <= 0 {
		ttl = 8 * time.Second
	}
	return &WebPresence{
		hub:    hub,
		doc:    docID,
		user:   user,
		hb:     hb,
		ttl:    ttl,
		roster: make(map[string]PresenceMsg),
	}
}

// OnChange registers a callback fired for every presence event.
func (p *WebPresence) OnChange(fn func(PresenceMsg)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers = append(p.handlers, fn)
}

// PublishCursor broadcasts the local user's cursor (normalized 0..1),
// refreshes the local roster entry, and forwards to peers.
func (p *WebPresence) PublishCursor(clientID string, x, y float64) error {
	msg := PresenceMsg{Type: presenceCursor, Doc: p.doc, User: p.user, X: x, Y: y, TS: time.Now().UnixMilli()}
	p.mu.Lock()
	p.roster[msg.User] = msg
	p.mu.Unlock()
	p.broadcast(clientID, msg)
	return nil
}

// Subscribe starts receiving presence for the doc and begins heartbeating.
const (
	presenceJoin   = "join"
	presenceLeave  = "leave"
	presenceCursor = "cursor"
)

// Subscribe starts receiving presence for the doc and begins heartbeating.
// It blocks until ctx is cancelled, then publishes a "leave" and returns.
func (p *WebPresence) Subscribe(ctx context.Context, clientID string) error {
	p.broadcast(clientID, PresenceMsg{Type: presenceJoin, Doc: p.doc, User: p.user, TS: time.Now().UnixMilli()})

	expire := time.NewTicker(p.ttl / 2)
	beat := time.NewTicker(p.hb)
	defer expire.Stop()
	defer beat.Stop()
	defer func() {
		p.broadcast(clientID, PresenceMsg{Type: presenceLeave, Doc: p.doc, User: p.user, TS: time.Now().UnixMilli()})
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-beat.C:
			p.broadcast(clientID, PresenceMsg{Type: presenceJoin, Doc: p.doc, User: p.user, TS: time.Now().UnixMilli()})
		case <-expire.C:
			p.expireStale()
		}
	}
}

// apply merges an incoming presence event and notifies handlers.
func (p *WebPresence) apply(msg PresenceMsg) {
	p.mu.Lock()
	switch msg.Type {
	case presenceLeave:
		delete(p.roster, msg.User)
	case presenceCursor, presenceJoin:
		p.roster[msg.User] = msg
	}
	handlers := append([]func(PresenceMsg){}, p.handlers...)
	p.mu.Unlock()
	for _, h := range handlers {
		h(msg)
	}
}

// HandleIncoming applies a presence event received from the SSE hub.
// Exposed so the SSE handler can route cursor/join/leave frames into the
// local roster.
func (p *WebPresence) HandleIncoming(data []byte) {
	var msg PresenceMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	p.apply(msg)
}

// expireStale drops roster entries older than ttl and notifies leaves.
func (p *WebPresence) expireStale() {
	now := time.Now().UnixMilli()
	p.mu.Lock()
	var expired []PresenceMsg
	for u, m := range p.roster {
		if u == p.user {
			continue
		}
		if now-m.TS > p.ttl.Milliseconds() {
			delete(p.roster, u)
			expired = append(expired, PresenceMsg{Type: "leave", Doc: p.doc, User: u, TS: now})
		}
	}
	handlers := append([]func(PresenceMsg){}, p.handlers...)
	p.mu.Unlock()
	for _, m := range expired {
		for _, h := range handlers {
			h(m)
		}
	}
}

// broadcast fans a presence event to every other connected client on the
// doc's stream (exclude the originator to avoid an echo loop). Marshal
// errors are logged and dropped — a single malformed cursor must not
// break the presence loop.
func (p *WebPresence) broadcast(clientID string, msg PresenceMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("collab: marshal presence", "error", err)
		return
	}
	p.hub.BroadcastExcept(data, clientID)
}

// Roster returns a snapshot copy of currently-present peers (excluding self).
func (p *WebPresence) Roster() []PresenceMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PresenceMsg, 0, len(p.roster))
	for u, m := range p.roster {
		if u == p.user {
			continue
		}
		out = append(out, m)
	}
	return out
}
