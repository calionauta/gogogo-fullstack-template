package todo

import (
	"testing"
	"time"

	"github.com/calionauta/gogogo-template/internal/queue"
)

func TestSSEHubRegisterAndSend(t *testing.T) {
	hub := queue.NewSSEHub()
	ch := make(chan []byte, 10)

	hub.Register("client-1", ch)
	hub.Send("client-1", []byte("hello"))

	select {
	case msg := <-ch:
		if string(msg) != "hello" {
			t.Fatalf("expected 'hello', got %q", string(msg))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestSSEHubBufferThenReplay(t *testing.T) {
	hub := queue.NewSSEHub()

	// Send before register (goes to buffer)
	hub.Send("client-late", []byte("buffered-msg"))

	// Now register — should replay
	ch := make(chan []byte, 10)
	hub.Register("client-late", ch)

	select {
	case msg := <-ch:
		if string(msg) != "buffered-msg" {
			t.Fatalf("expected 'buffered-msg', got %q", string(msg))
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: buffered message not replayed")
	}
}

func TestSSEHubBroadcast(t *testing.T) {
	hub := queue.NewSSEHub()
	ch1 := make(chan []byte, 10)
	ch2 := make(chan []byte, 10)
	hub.Register("a", ch1)
	hub.Register("b", ch2)

	hub.Broadcast([]byte("broadcast!"))

	for name, ch := range map[string]chan []byte{"a": ch1, "b": ch2} {
		select {
		case msg := <-ch:
			if string(msg) != "broadcast!" {
				t.Fatalf("client %s: expected 'broadcast!', got %q", name, string(msg))
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout: client %s didn't receive broadcast", name)
		}
	}
}

func TestSSEHubBackpressure(t *testing.T) {
	t.Helper()
	hub := queue.NewSSEHub()
	// Full channel (capacity 1, already has message)
	ch := make(chan []byte, 1)
	ch <- []byte("existing")
	hub.Register("slow", ch)

	// This should not block
	hub.Send("slow", []byte("overflow"))

	// Should still have the original message
	got := <-ch
	if string(got) != "existing" {
		t.Fatalf("expected original 'existing' message, got %q", string(got))
	}
}

func TestSSEHubUnregister(t *testing.T) {
	t.Helper()
	hub := queue.NewSSEHub()
	ch := make(chan []byte, 10)
	hub.Register("gone", ch)
	hub.Unregister("gone")

	// Should not panic
	hub.Send("gone", []byte("ghost"))
	hub.Broadcast([]byte("nothing"))
}
