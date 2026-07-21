// SCOPE:layer=infra,removal=core — SSE Hub (in-memory fan-out, replay buffer, backpressure)
package queue

import (
	"context"
	"log/slog"
	"sync"

	"github.com/calionauta/gogogo-fullstack-template/config"
)

// Runtime defaults (DefaultReplayBufferSize, DefaultClientQueueSize,
// DefaultSSEHeartbeatInterval) are now defined in config/config.go —
// tune there to affect every consumer.
// SSEHubStats is a snapshot of the hub's current resource use. Returned
// by Stats() so operators can observe backpressure and reconnection
// churn. Cheap to compute; safe to call on a hot path.
type SSEHubStats struct {
	Clients         int // currently connected clients
	BufferedClients int // clients with messages in their replay buffer
	BufferedEvents  int // total events across all replay buffers
}

// SSEHub manages Server-Sent Event connections with three properties
// the todo demo relies on:
//
//  1. Register-before-enqueue: clients register their channel first,
//     so any Send before/during connection is delivered via the
//     replay buffer.
//  2. Replay buffer: short ring-buffer per client ID, so late
//     subscribers receive recent events (e.g. a tab that reconnects
//     after a brief network blip).
//  3. Backpressure: when a client's channel is full, the hub drops
//     the event for that client (logs) rather than blocking the
//     producer. Producer-blocking via SendBlocking(ctx, ...) is the
//     explicit "I want to wait" path.
//
// Concurrency: SSEHub is safe for concurrent use by any number of
// producers and consumers. The internal map is guarded by a
// sync.RWMutex; per-client channels are owned by the caller.
type SSEHub struct {
	mu      sync.RWMutex
	clients map[string]chan []byte
	buffer  map[string][][]byte
	// userOf maps a connected clientID to the owner userID it belongs
	// to. Used by BroadcastToUser to scope record mutations to the
	// owning user's clients only, preventing cross-user todo leaks.
	userOf map[string]string

	// maxBuffer is the per-client replay buffer ring length.
	maxBuffer int

	// onDrop is invoked once per dropped event (slow client or
	// failed delivery). Defaults to a slog.Warn; tests can inject a
	// counter to assert drop behaviour.
	onDrop func(clientID string, data []byte, reason string)
}

// HubOption configures an SSEHub at construction time. Apply with
// NewSSEHub(With...).
type HubOption func(*SSEHub)

// WithReplayBufferSize sets the per-client replay buffer length.
// The oldest event is dropped when the buffer is full (ring
// behaviour). Pass 0 to disable replay entirely.
func WithReplayBufferSize(n int) HubOption {
	return func(h *SSEHub) {
		h.maxBuffer = n
	}
}

// WithDropHandler installs a callback invoked once per dropped event.
// `reason` is "slow-client" for backpressure drops or
// "send-blocking-cancelled" for context-cancelled drops. Used by
// tests; production can use this to emit metrics.
func WithDropHandler(fn func(clientID string, data []byte, reason string)) HubOption {
	return func(h *SSEHub) {
		h.onDrop = fn
	}
}

// NewSSEHub builds a hub with the given options. Defaults match
// what the production todo example needs (64-slot replay buffers,
// slog.Warn on drops).
func NewSSEHub(opts ...HubOption) *SSEHub {
	h := &SSEHub{
		clients:   make(map[string]chan []byte),
		buffer:    make(map[string][][]byte),
		userOf:    make(map[string]string),
		maxBuffer: config.DefaultReplayBufferSize,
		onDrop: func(clientID string, _ []byte, reason string) {
			slog.Warn("ssehub: dropping event", "client_id", clientID, "reason", reason)
		},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register adds a client channel. Must be called BEFORE Send for
// the replay buffer to be useful — late callers still receive any
// events buffered since the hub was created, just synchronously
// (no goroutine is spawned).
//
// Safe to call multiple times with the same clientID; the channel
// is replaced and the buffer is preserved (so a re-connecting tab
// doesn't lose intermediate events).
func (h *SSEHub) Register(clientID, userID string, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[clientID] = ch
	h.userOf[clientID] = userID

	// Drain the replay buffer SYNCHRONOUSLY. We hold the write lock
	// so a concurrent Send either sees us as already-registered and
	// pushes to the channel, or — if it grabbed the lock first —
	// pushes to the buffer (which we'll drain next call).
	//
	// Drop-on-full: a slow client during replay is the same
	// backpressure path as a slow live client; the event is gone,
	// but the producer never blocks. Replay is best-effort.
	for _, msg := range h.buffer[clientID] {
		select {
		case ch <- msg:
		default:
			h.onDrop(clientID, msg, "slow-client-replay")
		}
	}
	delete(h.buffer, clientID)
}

// Unregister removes a client and its buffer. Subsequent Sends to
// the same clientID re-buffer.
func (h *SSEHub) Unregister(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, clientID)
	delete(h.buffer, clientID)
	delete(h.userOf, clientID)
}

// UnregisterIfCurrent removes a client only if the provided channel
// still matches the registered one. This prevents a stale deferred
// Unregister (from a previous EventSource connection) from wiping out
// a newer registration made by a reconnect handler.
//
// Race scenario:
//  1. Tab A's EventSource reconnects → new HTTP request arrives
//  2. New handler creates ch_new, calls Register(clientID, "", ch_new)
//  3. Old handler's context is cancelled (old connection closed)
//  4. Old handler's deferred Unregister(clientID) would DELETE ch_new
//  5. Tab A stops receiving events (ghost connection)
//
// UnregisterIfCurrent checks that h.clients[clientID] == ch before
// removing, so step 4 becomes a no-op and ch_new continues to work.
func (h *SSEHub) UnregisterIfCurrent(clientID string, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[clientID] == ch {
		delete(h.clients, clientID)
		delete(h.buffer, clientID)
		delete(h.userOf, clientID)
	}
}

// Send pushes data to a specific client. Non-blocking: if the
// client's channel is full, the event is dropped (logged via
// onDrop) AND also recorded in the replay buffer. If the client
// is not registered, the event goes straight to the replay buffer.
//
// Use Send (not SendBlocking) on the hot path of background
// workers — they shouldn't block the entire worker pool on a
// slow client. SendBlocking is the explicit "wait" path.
func (h *SSEHub) Send(clientID string, data []byte) {
	h.mu.RLock()
	ch, registered := h.clients[clientID]
	h.mu.RUnlock()

	if registered {
		select {
		case ch <- data:
			return
		default:
			h.onDrop(clientID, data, "slow-client")
		}
	}
	h.bufferEvent(clientID, data)
}

// SendCtx is the same as Send but takes a context. It is non-blocking
// (uses the same drop-on-full path) but skips the call entirely if
// the context is already canceled — useful inside a select with
// other work, where the producer would rather skip the event than
// race to send it.
func (h *SSEHub) SendCtx(ctx context.Context, clientID string, data []byte) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	h.Send(clientID, data)
}

// SendBlocking pushes data to a specific client, blocking until the
// client's channel accepts the chunk or ctx is canceled. Use this
// when the caller (typically a worker running under retry) cannot
// proceed without delivering the chunk — the retry layer treats a
// context cancellation as a transient failure and backs off per
// RetryConfig.
//
// If the client is not registered, the event is buffered for
// later replay and the call returns nil immediately (no waiting
// on a non-existent channel).
func (h *SSEHub) SendBlocking(ctx context.Context, clientID string, data []byte) error {
	h.mu.RLock()
	ch, ok := h.clients[clientID]
	h.mu.RUnlock()

	if !ok {
		h.bufferEvent(clientID, data)
		return nil // buffered for later replay
	}

	select {
	case ch <- data:
		return nil
	case <-ctx.Done():
		h.onDrop(clientID, data, "send-blocking-cancelled")
		return ctx.Err()
	}
}

// BroadcastExcept sends data to all currently connected clients EXCEPT
// excludeClientID. Used by the realtime broadcaster so a mutation's
// originator (which already patched its own DOM via the per-request SSE
// response) does not receive a redundant full-list re-render that would
// clobber its local view. Drop policy is identical to Broadcast.
func (h *SSEHub) BroadcastExcept(data []byte, excludeClientID string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, ch := range h.clients {
		if id == excludeClientID {
			continue
		}
		select {
		case ch <- data:
		default:
			h.onDrop(id, data, "slow-client-broadcast")
		}
	}
}

// Broadcast sends data to all currently connected clients. Drop
// policy: a client whose channel is full has the event dropped
// (logged). Producers never block. Disconnected clients are not
// re-broadcast into (re-register to receive new events).
func (h *SSEHub) Broadcast(data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	slog.Info("ssehub: Broadcast", "clients", len(h.clients))
	for id, ch := range h.clients {
		select {
		case ch <- data:
		default:
			h.onDrop(id, data, "slow-client-broadcast")
		}
	}
}

// BroadcastToUser sends data to all currently connected clients that
// belong to userID, EXCEPT excludeClientID. This is the per-user scoped
// variant of Broadcast used for record mutations: a todo
// create/toggle/delete is delivered only to the owning user's open tabs,
// never to another user's (cross-user leak), and never back to the
// originating tab (which already patched its own DOM via the per-request
// SSE response, so a redundant full-list re-render would clobber its
// local view). Drop policy is identical to Broadcast.
func (h *SSEHub) BroadcastToUser(data []byte, userID, excludeClientID string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, ch := range h.clients {
		if id == excludeClientID {
			continue
		}
		if h.userOf[id] != userID {
			continue
		}
		select {
		case ch <- data:
		default:
			h.onDrop(id, data, "slow-client-broadcast-user")
		}
	}
}

// IsRegistered returns true if clientID has an active registration in
// the hub (i.e. a channel is associated with it). Used by peerLeave to
// detect whether a client reconnected before the old handler's deferred
// cleanup runs, preventing the stale handler from removing a live peer.
func (h *SSEHub) IsRegistered(clientID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clients[clientID]
	return ok
}

// Stats returns a snapshot of the hub's current resource use. Use
// for /health endpoints, /debug/pprof-style metrics, or tests that
// assert on buffered events without reaching into internals.
func (h *SSEHub) Stats() SSEHubStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	stats := SSEHubStats{Clients: len(h.clients)}
	for _, buf := range h.buffer {
		if len(buf) > 0 {
			stats.BufferedClients++
			stats.BufferedEvents += len(buf)
		}
	}
	return stats
}

// CountUserClients returns the number of clients registered with a
// non-empty userID. Clients without a userID (e.g. whiteboard SSE
// connections) are excluded. The todo feature uses this to report
// "X online" so navigating between todo and whiteboard pages does
// not inflate the count with stale whiteboard connections.
func (h *SSEHub) CountUserClients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	count := 0
	for _, uid := range h.userOf {
		if uid != "" {
			count++
		}
	}
	return count
}

// bufferEvent appends data to the per-client replay buffer, dropping
// the oldest event if the buffer is full. Caller must NOT hold the
// mutex (this function acquires it).
func (h *SSEHub) bufferEvent(clientID string, data []byte) {
	if h.maxBuffer <= 0 {
		// Replay disabled — keep the most recent event only in a
		// single-slot "in case a client connects later" buffer? No:
		// honour the contract. Drop.
		h.onDrop(clientID, data, "replay-disabled")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	buf := h.buffer[clientID]
	if len(buf) >= h.maxBuffer {
		// Ring buffer: drop oldest.
		buf = buf[1:]
	}
	h.buffer[clientID] = append(buf, data)
}
