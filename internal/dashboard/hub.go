package dashboard

import "sync"

// hubBuffer is the per-subscriber send buffer. Broadcast never blocks: a
// subscriber whose buffer is full simply drops the frame (it will catch up on
// the next snapshot, which is a full state, not a delta).
const hubBuffer = 8

// Hub fans out serialized state snapshots to all connected SSE clients.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewHub builds an empty Hub.
func NewHub() *Hub { return &Hub{subs: make(map[chan []byte]struct{})} }

// Subscribe registers a new client. The returned cancel removes the
// subscription and closes the channel (idempotent).
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, hubBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Broadcast sends msg to every subscriber, dropping the frame for any whose
// buffer is full. Never blocks.
func (h *Hub) Broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default: // slow client: drop this frame
		}
	}
}

// Count returns the number of active subscribers.
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
