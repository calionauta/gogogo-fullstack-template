package queue

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewSSEHub_DefaultReplaySize asserts the documented default
// (64 slots) is the cap when no option overrides it.
func TestNewSSEHub_DefaultReplaySize(t *testing.T) {
	h := NewSSEHub()
	if h.maxBuffer != DefaultReplayBufferSize {
		t.Fatalf("default buffer = %d, want %d",
			h.maxBuffer, DefaultReplayBufferSize)
	}
}

// TestNewSSEHub_WithReplayBufferSizeZeroDisablesReplay asserts the
// contract: replay buffer 0 means events to unconnected clients
// are dropped (no silent unbounded growth).
func TestNewSSEHub_WithReplayBufferSizeZeroDisablesReplay(t *testing.T) {
	h := NewSSEHub(WithReplayBufferSize(0))

	// Send 5 events to a never-registered client. With buffer=0,
	// each fires the drop callback and nothing is stored.
	for range 5 {
		h.Send("never-connected", []byte("event"))
	}

	stats := h.Stats()
	if stats.BufferedEvents != 0 {
		t.Fatalf("expected 0 buffered events, got %d", stats.BufferedEvents)
	}
	if stats.BufferedClients != 0 {
		t.Fatalf("expected 0 buffered clients, got %d", stats.BufferedClients)
	}
}

// TestSSEHub_ReplacedChannel_PreservesBuffer asserts the
// "Register twice = reconnect" contract: when a client re-registers
// with a new channel, they still get their buffered events, not a
// fresh empty state. Events sent while connected go directly to the
// channel (and are not buffered); events sent while disconnected DO
// buffer; re-register drains the buffer into the new channel.
func TestSSEHub_ReplacedChannel_PreservesBuffer(t *testing.T) {
	h := NewSSEHub()

	// Send 2 events to an unregistered client. Both go to the buffer.
	h.Send("c1", []byte("a"))
	h.Send("c1", []byte("b"))

	// Reconnect with a fresh channel. Should receive both replayed.
	newCh := make(chan []byte, 10)
	h.Register("c1", "", newCh)

	got := drainN(newCh, 2, time.Second)
	want := []string{"a", "b"}
	for i, w := range want {
		if string(got[i]) != w {
			t.Errorf("event %d: got %q, want %q", i, string(got[i]), w)
		}
	}
}

// TestSSEHub_SynchronousReplay_NoGoroutineLeak asserts the design
// choice: replay is SYNCHRONOUS at Register() time. After Register
// returns, no goroutine is alive. This is observable: if Register
// spawned a goroutine, this test would still pass (it doesn't
// assert) — but the runtime check below uses runtime.NumGoroutine
// as a coarse sanity check that the simpler code path doesn't grow
// goroutines unboundedly.
func TestSSEHub_SynchronousReplay_NoGoroutineLeak(t *testing.T) {
	before := countGoroutines()
	for range 200 {
		hub := NewSSEHub()
		ch := make(chan []byte, 10)
		hub.Send("x", []byte("a"))
		hub.Register("x", "", ch)
		drain(ch)
		hub.Unregister("x")
	}
	time.Sleep(10 * time.Millisecond) // let any leaked goroutines settle
	after := countGoroutines()
	if after > before+5 {
		t.Errorf("goroutine count grew %d -> %d; possible leak from Register",
			before, after)
	}
}

// TestSSEHub_Stats_ReflectsState asserts Stats() is the
// observability primitive: it counts clients, buffered clients,
// and total buffered events. Events sent to a REGISTERED client go
// directly to the channel (not the buffer); only events to
// unregistered clients fill the buffer.
func TestSSEHub_Stats_ReflectsState(t *testing.T) {
	h := NewSSEHub()
	c1 := make(chan []byte, 10) // large enough that Send doesn't block
	c2 := make(chan []byte, 10)
	h.Register("c1", "", c1)
	h.Register("c2", "", c2)
	// These two go to c1's channel (registered → direct).
	h.Send("c1", []byte("a"))
	h.Send("c1", []byte("b"))
	// c3 is unregistered → goes to c3's buffer.
	h.Send("c3", []byte("c"))

	stats := h.Stats()
	if stats.Clients != 2 {
		t.Errorf("Clients = %d, want 2 (c1, c2)", stats.Clients)
	}
	if stats.BufferedClients != 1 {
		t.Errorf("BufferedClients = %d, want 1 (c3)", stats.BufferedClients)
	}
	if stats.BufferedEvents != 1 {
		t.Errorf("BufferedEvents = %d, want 1 (only c3's)", stats.BufferedEvents)
	}
}

// TestSSEHub_WithDropHandler_InvokedOnBackpressure asserts the
// drop-handler hook fires for backpressure drops and is NOT
// invoked on successful sends.
func TestSSEHub_WithDropHandler_InvokedOnBackpressure(t *testing.T) {
	var drops atomic.Int32
	var lastReason atomic.Value
	h := NewSSEHub(WithDropHandler(func(_ string, _ []byte, reason string) {
		drops.Add(1)
		lastReason.Store(reason)
	}))

	// Successful send → no drop.
	ch := make(chan []byte, 1)
	h.Register("c", "", ch)
	h.Send("c", []byte("ok"))
	if got := drops.Load(); got != 0 {
		t.Errorf("expected 0 drops on success, got %d", got)
	}

	// Slow client → drop.
	h.Send("c", []byte("overflow"))
	if got := drops.Load(); got != 1 {
		t.Errorf("expected 1 drop on backpressure, got %d", got)
	}
	if r, ok := lastReason.Load().(string); !ok || r != "slow-client" {
		t.Errorf("expected reason %q, got %q (ok=%v)", "slow-client", r, ok)
	}
}

// TestSSEHub_SendCtx_SkipsOnCanceledContext asserts the explicit
// context shortcut: if the producer's context is already done, the
// event is dropped without touching the buffer or the channel.
func TestSSEHub_SendCtx_SkipsOnCanceledContext(t *testing.T) {
	h := NewSSEHub()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	h.SendCtx(ctx, "any-client", []byte("never-stored"))

	stats := h.Stats()
	if stats.BufferedEvents != 0 {
		t.Errorf("expected 0 buffered, got %d", stats.BufferedEvents)
	}
}

// TestSSEHub_BufferRing_DropsOldest asserts the ring-buffer policy:
// when the buffer is full, the OLDEST event is dropped to make room
// for the new one (not the newest).
func TestSSEHub_BufferRing_DropsOldest(t *testing.T) {
	h := NewSSEHub(WithReplayBufferSize(2))

	h.Send("c", []byte("first"))  // buf = [first]
	h.Send("c", []byte("second")) // buf = [first, second]
	h.Send("c", []byte("third"))  // buf = [second, third]; "first" dropped

	ch := make(chan []byte, 10)
	h.Register("c", "", ch)

	got := drainN(ch, 2, time.Second)
	if string(got[0]) != "second" {
		t.Errorf("expected oldest dropped, got %q first", string(got[0]))
	}
	if string(got[1]) != "third" {
		t.Errorf("expected %q second, got %q", "third", string(got[1]))
	}
}

// TestSSEHub_Broadcast_SkipsUnregisteredClients asserts the
// documented contract: Broadcast only goes to REGISTERED clients.
// Unregistered IDs are not even counted in the iteration.
func TestSSEHub_Broadcast_SkipsUnregisteredClients(t *testing.T) {
	h := NewSSEHub()
	ch := make(chan []byte, 10)
	h.Register("only-connected", "", ch)

	// "ghost" never registered. Broadcast must not enqueue to a
	// buffer for it (the buffer is for late-joiners, not for
	// recipients we'll never see).
	h.Broadcast([]byte("hi"))

	select {
	case msg := <-ch:
		if string(msg) != "hi" {
			t.Errorf("got %q, want %q", string(msg), "hi")
		}
	case <-time.After(time.Second):
		t.Fatal("broadcast never delivered to connected client")
	}

	stats := h.Stats()
	if stats.BufferedClients != 0 {
		t.Errorf("expected 0 buffered clients (broadcast skips unreg), got %d",
			stats.BufferedClients)
	}
}

// TestSSEHub_CountUserClients_ExcludesEmptyUserID asserts that
// CountUserClients only counts clients with a non-empty userID.
// Clients registered with an empty userID (e.g. whiteboard SSE
// connections) must be excluded so the todo feature's "X online"
// counter does not inflate when users navigate between pages.
func TestSSEHub_CountUserClients_ExcludesEmptyUserID(t *testing.T) {
	h := NewSSEHub()
	c1 := make(chan []byte, 10)
	c2 := make(chan []byte, 10)
	c3 := make(chan []byte, 10)

	// Register two clients with userIDs and one without.
	h.Register("todo-tab-a", "user1", c1)
	h.Register("todo-tab-b", "user1", c2)
	h.Register("whiteboard-tab", "", c3) // whiteboard uses empty userID

	if got := h.CountUserClients(); got != 2 {
		t.Fatalf("CountUserClients = %d, want 2 (only clients with non-empty userID)", got)
	}
	// Stats().Clients still counts all 3.
	if stats := h.Stats(); stats.Clients != 3 {
		t.Fatalf("Stats().Clients = %d, want 3 (all clients including whiteboard)", stats.Clients)
	}
}

// TestSSEHub_BroadcastToUser_ScopesByOwner asserts the per-user
// contract: a record mutation is delivered only to clients owned by the
// same user, and the originating client (excludeClientID) is skipped.
// This guards the userOf map initialization bug where BroadcastToUser
// silently delivered to nobody because userOf was a nil map.
func TestSSEHub_BroadcastToUser_ScopesByOwner(t *testing.T) {
	h := NewSSEHub()
	u1a := make(chan []byte, 10) // user u1, originator
	u1b := make(chan []byte, 10) // user u1, other tab
	u2c := make(chan []byte, 10) // user u2, different user
	h.Register("u1a", "u1", u1a)
	h.Register("u1b", "u1", u1b)
	h.Register("u2c", "u2", u2c)

	const msg = "mutation"
	h.BroadcastToUser([]byte(msg), "u1", "u1a")

	// u1b (same user, not excluded) must receive.
	select {
	case got := <-u1b:
		if string(got) != msg {
			t.Fatalf("u1b: expected %q, got %q", msg, string(got))
		}
	case <-time.After(time.Second):
		t.Fatal("u1b: same-user client did not receive scoped broadcast")
	}

	// u1a (excluded originator) must NOT receive.
	select {
	case got := <-u1a:
		t.Fatalf("u1a: excluded originator unexpectedly received %q", string(got))
	case <-time.After(100 * time.Millisecond):
	}

	// u2c (different user) must NOT receive — no cross-user leak.
	select {
	case got := <-u2c:
		t.Fatalf("u2c: cross-user leak, received %q", string(got))
	case <-time.After(100 * time.Millisecond):
	}
}

// --- helpers ---

// countGoroutines is a coarse runtime check. Excludes the current
// goroutine (returns 1 less than runtime.NumGoroutine) to make
// the before/after comparison more meaningful.
func countGoroutines() int {
	return runtime.NumGoroutine() - 1
}

func drain(ch <-chan []byte) [][]byte {
	var out [][]byte
	for {
		select {
		case m := <-ch:
			out = append(out, m)
		default:
			return out
		}
	}
}

func drainN(ch <-chan []byte, n int, timeout time.Duration) [][]byte {
	out := make([][]byte, 0, n)
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case m := <-ch:
			out = append(out, m)
		case <-deadline:
			return out
		}
	}
	return out
}
