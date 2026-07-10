//go:build jetstream

package collab

import (
	"context"
	"sync"
	"testing"
	"time"

	natsio "github.com/nats-io/nats.go"

	"github.com/calionauta/gogogo-fullstack-template/internal/nats"
)

// TestPresence_TwoPeersConverge is the Phase C presence regression guard:
// two Presence sessions on the same doc over a real NATS JetStream must
// each receive the other's cursor (and a join on subscribe), proving the
// ephemeral cursor broadcast works end-to-end. No persistence involved.
func TestPresence_TwoPeersConverge(t *testing.T) {
	if err := nats.StartEmbedded(t.TempDir()); err != nil {
		t.Fatalf("nats start: %v", err)
	}
	defer nats.Stop()

	nc, err := natsio.Connect(nats.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()

	const docID = "wb-presence"

	// Peer A.
	var aMu sync.Mutex
	var aSeen []PresenceMsg
	a := NewPresence(nc, docID, "alice", 200*time.Millisecond, time.Second)
	a.OnChange(func(m PresenceMsg) {
		aMu.Lock()
		aSeen = append(aSeen, m)
		aMu.Unlock()
	})
	aCtx, aCancel := context.WithCancel(context.Background())
	defer aCancel()
	go func() { _ = a.Subscribe(aCtx) }()

	// Peer B.
	var bMu sync.Mutex
	var bSeen []PresenceMsg
	b := NewPresence(nc, docID, "bob", 200*time.Millisecond, time.Second)
	b.OnChange(func(m PresenceMsg) {
		bMu.Lock()
		bSeen = append(bSeen, m)
		bMu.Unlock()
	})
	bCtx, bCancel := context.WithCancel(context.Background())
	defer bCancel()
	go func() { _ = b.Subscribe(bCtx) }()

	// Let both subscribe + exchange joins.
	time.Sleep(300 * time.Millisecond)

	// Bob moves his cursor; Alice must see it.
	if err := b.PublishCursor(0.5, 0.25); err != nil {
		t.Fatalf("bob publish cursor: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		aMu.Lock()
		got := false
		for _, m := range aSeen {
			if m.User == "bob" && m.Type == "cursor" && m.X == 0.5 && m.Y == 0.25 {
				got = true
			}
		}
		aMu.Unlock()
		if got {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	aMu.Lock()
	foundCursor := false
	foundJoin := false
	for _, m := range aSeen {
		if m.User == "bob" && m.Type == "cursor" {
			foundCursor = true
		}
		if m.User == "bob" && m.Type == "join" {
			foundJoin = true
		}
	}
	aMu.Unlock()
	if !foundCursor {
		t.Fatal("alice never received bob's cursor")
	}
	if !foundJoin {
		t.Fatal("alice never received bob's join on subscribe")
	}

	// Bob's roster should include alice (and himself).
	if len(b.Roster()) == 0 {
		t.Fatal("bob's roster has no other peers")
	}
}
