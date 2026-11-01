// Package hub fans model.Change values out to subscribers (the SSE
// endpoint). A slow subscriber drops changes instead of blocking the producer.
package hub

import (
	"sync"

	"github.com/Will-Luck/waymark/internal/model"
)

type Hub struct {
	buf  int
	mu   sync.Mutex
	subs map[chan model.Change]struct{}
}

// New makes a hub whose subscriber channels hold buf changes.
func New(buf int) *Hub {
	if buf < 1 {
		buf = 1
	}
	return &Hub{buf: buf, subs: map[chan model.Change]struct{}{}}
}

// Subscribe returns a channel and a cancel func that closes it (idempotent).
func (h *Hub) Subscribe() (<-chan model.Change, func()) {
	ch := make(chan model.Change, h.buf)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Publish delivers c to every subscriber that has room.
func (h *Hub) Publish(c model.Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- c:
		default:
		}
	}
}

// Len is the subscriber count.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
