package dashboard

import (
	"testing"
	"time"
)

func TestHub_BroadcastReachesSubscriber(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	defer cancel()

	h.Broadcast([]byte("hello"))
	select {
	case msg := <-ch:
		if string(msg) != "hello" {
			t.Errorf("got %q want hello", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive broadcast")
	}
}

func TestHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	cancel()
	h.Broadcast([]byte("x"))
	// after cancel the channel is closed/drained; a recv must not block forever.
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("closed subscriber channel never returned")
	}
	if got := h.Count(); got != 0 {
		t.Errorf("after unsubscribe Count()=%d want 0", got)
	}
}

func TestHub_SlowSubscriberDropped(t *testing.T) {
	h := NewHub()
	_, _ = h.Subscribe() // never drained; buffer is small
	// Broadcasting more than the buffer must not block the hub.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.Broadcast([]byte("x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on a slow subscriber")
	}
}
