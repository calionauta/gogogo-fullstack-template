package crdtstore

// SCOPE:pluggable - E2E test for cross-process CRDT convergence.
//
// Simulates two CRDTStore instances sharing the same JetStream. A
// creates a todo via Store A's API; the publisher ships the Loro op
// to JetStream; Store B's subscriber applies it via ApplyRemoteOp;
// Store B's Get then returns the todo.
//
// Pattern mirrors the working TestCRDTTransport_CrossProcessConvergence
// in transport_test.go: a single shared JetStream, two transports
// with distinct PublisherIDs, each subscribing for the same owner
// so they "see" each other's publishes. The CRDTStore layer is added
// on top: stores hold transports, the publish path runs through
// Create/Update/Delete op bytes, and the consume path runs through
// ApplyRemoteOp.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/calionauta/gogogo-fullstack-template/features/todo"
	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
)

func TestCRDTStore_CrossProcessConvergence(t *testing.T) {
	js := newTestJetStream(t)
	// newTestJetStream sets up the embedded NATS + registers t.Cleanup.

	ctx := context.Background()
	appA := newTestApp(t, t.TempDir())
	appB := newTestApp(t, t.TempDir()) // separate PB namespace on a fresh dir
	storeA := New(appA)
	storeB := New(appB)
	if err := storeA.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema A: %v", err)
	}
	if err := storeB.EnsureSchema(); err != nil {
		t.Fatalf("EnsureSchema B: %v", err)
	}

	// Distinct PublisherIDs so the in-process loop filter doesn't
	// drop peer ops (the loop filter only fires within one process,
	// but two processes here share one JS, so we still want distinct
	// IDs to keep the doc consistent if/when both publish).
	trA := NewTransport(TransportConfig{JetStream: js, PublisherID: "instance-A"})
	trB := NewTransport(TransportConfig{JetStream: js, PublisherID: "instance-B"})
	if err := trA.EnsureStream(ctx); err != nil {
		t.Fatalf("EnsureStream A: %v", err)
	}
	storeA.SetTransport(trA)
	storeB.SetTransport(trB)

	ownerID := "owner-conv-CRDT-1"

	// Each store subscribes for the same owner via its OWN transport
	// (mirrors two binary instances each holding a copy of the same
	// user's doc). The per-transport PublisherID is the loop filter:
	// it drops only ops that THIS subscriber also published, so each
	// peer is automatically included.
	var muA, muB sync.Mutex
	var gotA, gotB []Op
	subA, err := trA.Subscribe(ctx, ownerID, func(op Op) error {
		muA.Lock()
		gotA = append(gotA, op)
		muA.Unlock()
		t.Logf("subA received op id=%s owner=%s pubID=%s", op.ID, op.OwnerID, op.PublisherID)
		return storeA.ApplyRemoteOp(ctx, op.OwnerID, op)
	})
	if err != nil {
		t.Fatalf("A.Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = subA.Unsubscribe() })

	subB, err := trB.Subscribe(ctx, ownerID, func(op Op) error {
		muB.Lock()
		gotB = append(gotB, op)
		muB.Unlock()
		t.Logf("subB received op id=%s owner=%s pubID=%s", op.ID, op.OwnerID, op.PublisherID)
		return storeB.ApplyRemoteOp(ctx, op.OwnerID, op)
	})
	if err != nil {
		t.Fatalf("B.Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	// Give subscriptions a beat to settle.
	time.Sleep(500 * time.Millisecond)

	// A creates a todo.
	var (
		errA, errB error
	)
	if _, errA = storeA.Create(ctx, todo.Todo{ID: "todo-a-1", Title: "from A"}, ownerID, ""); errA != nil {
		t.Fatalf("A.Create: %v", errA)
	}
	// B creates a todo for the same owner (A should see it).
	if _, errB = storeB.Create(ctx, todo.Todo{ID: "todo-b-1", Title: "from B"}, ownerID, ""); errB != nil {
		t.Fatalf("B.Create: %v", errB)
	}

	// Wait up to 3s for A to receive B's op.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := storeA.Get(ctx, ownerID, "todo-b-1"); got.Title == "from B" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	gotX, err := storeA.Get(ctx, ownerID, "todo-b-1")
	if err != nil {
		t.Fatalf("A never received B's op: %v", err)
	}
	if gotX.Title != "from B" {
		t.Errorf("A got title %q, want %q", gotX.Title, "from B")
	}

	// And B should see A's op too.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := storeB.Get(ctx, ownerID, "todo-a-1"); got.Title == "from A" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	gotY, err := storeB.Get(ctx, ownerID, "todo-a-1")
	if err != nil {
		t.Fatalf("B never received A's op: %v", err)
	}
	if gotY.Title != "from A" {
		t.Errorf("B got title %q, want %q", gotY.Title, "from A")
	}

	// Watch-like check: each store should have bumped its version
	// counter for the owner at least once (Phase 3 hookup). Allow
	// a beat for the ApplyRemoteOp callbacks to land before reading
	// the version counter.
	deadlineV := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadlineV) {
		if storeB.Version(ownerID) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v := storeA.Version(ownerID); v < 1 {
		t.Errorf("storeA.Version(%s) = %d, want >= 1", ownerID, v)
	}
	if v := storeB.Version(ownerID); v < 2 {
		t.Errorf("storeB.Version(%s) = %d, want >= 2 (received 1 from A + 1 own)", ownerID, v)
	}
}

// nats.JS is package-internal here; newTestJetStream is shared with
// transport_test.go (declared there).
var _ = nats.JS
