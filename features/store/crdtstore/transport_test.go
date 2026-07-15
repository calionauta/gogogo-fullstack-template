// SCOPE:pluggable - E2E test for CRDTTransport over JetStream.
//
// Boots a real embedded NATS server (via internal/nats.StartEmbedded),
// creates two CRDTTransports in the same process (simulating two
// binary instances), and asserts:
//   - ops published by transport A are delivered to transport B
//   - ops published by B are delivered to A
//   - the in-process loop filter drops self-published ops
//   - the JetStream MsgId dedup drops duplicate publishes
//   - Publish returns nil when JetStream is nil (single-process mode)
package crdtstore

import (
	"context"
	"sync"
	"testing"
	"time"

	natsio "github.com/nats-io/nats.go"

	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
)

func newTestJetStream(t *testing.T) natsio.JetStreamContext {
	t.Helper()
	storeDir := t.TempDir()
	if err := nats.StartEmbedded(storeDir); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	t.Cleanup(func() { nats.Stop() })
	if nats.JS == nil {
		t.Fatal("embedded JetStream not available")
	}
	return nats.JS
}

func TestCRDTTransport_PublishWithoutJetStreamIsNoOp(t *testing.T) {
	// nil JetStream = single-process mode. Publish should return nil
	// without erroring.
	tr := NewTransport(TransportConfig{JetStream: nil})
	if err := tr.Publish(context.Background(), Op{ID: "k1", OwnerID: "o1", Updates: []byte("x")}); err != nil {
		t.Errorf("Publish with nil JS should be no-op, got: %v", err)
	}
}

func TestCRDTTransport_CrossProcessConvergence(t *testing.T) {
	js := newTestJetStream(t)

	// Two transports in the same process simulate two binary
	// instances. Each has its own PublisherID.
	trA := NewTransport(TransportConfig{JetStream: js, PublisherID: "instance-A"})
	trB := NewTransport(TransportConfig{JetStream: js, PublisherID: "instance-B"})
	if err := trA.EnsureStream(context.Background()); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	ownerID := "owner-conv-1"

	// B subscribes BEFORE A publishes so we don't miss the first op.
	var (
		gotA, gotB []Op
		muA, muB   sync.Mutex
	)
	wait := time.Second

	subA, err := trB.Subscribe(context.Background(), ownerID, func(op Op) error {
		// A's ops arrive here (B subscribes for A's stream so B
		// receives A's publishes).
		muA.Lock()
		gotA = append(gotA, op)
		muA.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("B.Subscribe for A: %v", err)
	}
	t.Cleanup(func() { _ = subA.Unsubscribe() })

	subB, err := trA.Subscribe(context.Background(), ownerID, func(op Op) error {
		muB.Lock()
		gotB = append(gotB, op)
		muB.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("A.Subscribe for B: %v", err)
	}
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	// Give subscriptions a beat to settle.
	time.Sleep(wait)

	// A publishes an op; B should receive it.
	if err := trA.Publish(context.Background(), Op{
		ID:      "op-a-1",
		OwnerID: ownerID,
		Updates: []byte("update-a"),
	}); err != nil {
		t.Fatalf("trA.Publish: %v", err)
	}
	// B publishes a different op; A should receive it.
	if err := trB.Publish(context.Background(), Op{
		ID:      "op-b-1",
		OwnerID: ownerID,
		Updates: []byte("update-b"),
	}); err != nil {
		t.Fatalf("trB.Publish: %v", err)
	}

	// Wait for delivery.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		muA.Lock()
		muB.Lock()
		aLen, bLen := len(gotA), len(gotB)
		muA.Unlock()
		muB.Unlock()
		if aLen >= 1 && bLen >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	muA.Lock()
	defer muA.Unlock()
	if len(gotA) == 0 {
		t.Fatal("B never received A's op (gotA empty)")
	}
	if gotA[0].ID != "op-a-1" {
		t.Errorf("B got op %q, want op-a-1", gotA[0].ID)
	}

	muB.Lock()
	defer muB.Unlock()
	if len(gotB) == 0 {
		t.Fatal("A never received B's op (gotB empty)")
	}
	if gotB[0].ID != "op-b-1" {
		t.Errorf("A got op %q, want op-b-1", gotB[0].ID)
	}
}

func TestCRDTTransport_InProcessLoopFilter(t *testing.T) {
	js := newTestJetStream(t)
	trA := NewTransport(TransportConfig{JetStream: js, PublisherID: "instance-A"})
	trB := NewTransport(TransportConfig{JetStream: js, PublisherID: "instance-B"})
	if err := trA.EnsureStream(context.Background()); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	ownerID := "owner-loop-1"

	// B subscribes to A's publishes. A also subscribes to its own
	// publishes (simulating a real second-binary subscription). The
	// in-process loop filter should drop A's self-published ops.
	var gotAfromB, gotBfromA []Op
	var muA, muB sync.Mutex
	subA, _ := trA.Subscribe(context.Background(), ownerID, func(op Op) error {
		muA.Lock()
		gotAfromB = append(gotAfromB, op)
		muA.Unlock()
		return nil
	})
	t.Cleanup(func() { _ = subA.Unsubscribe() })
	subB, _ := trB.Subscribe(context.Background(), ownerID, func(op Op) error {
		muB.Lock()
		gotBfromA = append(gotBfromA, op)
		muB.Unlock()
		return nil
	})
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	time.Sleep(time.Second)

	// B publishes an op. A should NOT receive it (loop filter, A's
	// PublisherID != B's, so actually A SHOULD receive it — the
	// loop filter only drops self-published). Wait, this test should
	// flip: let A publish, then verify A's own subscriber drops it.
	// A's self-publish → A's self-sub drops. B's cross-publish → B
	// receives (the test we want). Let me redo.
	//
	// Simpler: A publishes; A's subscriber should drop (same ID).
	// B's subscriber should receive. B's subscriber does NOT need
	// the loop filter because B != A.
	if err := trA.Publish(context.Background(), Op{ID: "op-A-only", OwnerID: ownerID, Updates: []byte("x")}); err != nil {
		t.Fatalf("trA.Publish: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		muB.Lock()
		bLen := len(gotBfromA)
		muB.Unlock()
		if bLen >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// B should have received A's op.
	muB.Lock()
	defer muB.Unlock()
	if len(gotBfromA) == 0 {
		t.Fatal("B never received A's op")
	}
	if gotBfromA[0].ID != "op-A-only" {
		t.Errorf("B got op %q, want op-A-only", gotBfromA[0].ID)
	}

	// A's self-subscriber should have dropped the op (loop filter).
	// Give it a moment to confirm no late delivery.
	time.Sleep(300 * time.Millisecond)
	muA.Lock()
	defer muA.Unlock()
	for _, op := range gotAfromB {
		if op.ID == "op-A-only" {
			t.Errorf("A's self-subscriber received A's own op %q (loop filter failed)", op.ID)
		}
	}
}

func TestCRDTTransport_DuplicateIdDedup(t *testing.T) {
	js := newTestJetStream(t)
	trA := NewTransport(TransportConfig{JetStream: js, PublisherID: "instance-A"})
	trB := NewTransport(TransportConfig{JetStream: js, PublisherID: "instance-B"})
	if err := trA.EnsureStream(context.Background()); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	ownerID := "owner-dedup-1"

	var gotB []Op
	var muB sync.Mutex
	subB, _ := trB.Subscribe(context.Background(), ownerID, func(op Op) error {
		muB.Lock()
		gotB = append(gotB, op)
		muB.Unlock()
		return nil
	})
	t.Cleanup(func() { _ = subB.Unsubscribe() })

	time.Sleep(time.Second)

	// A publishes the same op ID twice (e.g. a retry scenario).
	if err := trA.Publish(context.Background(), Op{ID: "op-dup", OwnerID: ownerID, Updates: []byte("first")}); err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	if err := trA.Publish(context.Background(), Op{
		ID:      "op-dup",
		OwnerID: ownerID,
		Updates: []byte("second"),
	}); err != nil {
		t.Fatalf("second Publish: %v", err)
	}

	// Wait for delivery.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		muB.Lock()
		bLen := len(gotB)
		muB.Unlock()
		if bLen >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Wait a bit more to catch any late duplicates.
	time.Sleep(500 * time.Millisecond)

	muB.Lock()
	defer muB.Unlock()
	count := 0
	for _, op := range gotB {
		if op.ID == "op-dup" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("B received %d copies of op-dup, want 1 (JetStream MsgId dedup failed)", count)
	}
}
