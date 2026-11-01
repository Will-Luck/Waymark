package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Change is pushed to subscribers after each refresh cycle.
type Change struct {
	Kind  string    `json:"kind"` // project | activity | tick
	Slug  string    `json:"slug,omitempty"`
	Hash  string    `json:"hash,omitempty"`
	Event *Event    `json:"event,omitempty"`
	At    time.Time `json:"at"`
}

// Source is what the web layer reads. Snapshot never returns nil and the
// returned Site must be treated as immutable.
type Source interface {
	Snapshot() *Site
	// Subscribe returns a channel of changes and a cancel func. Slow
	// subscribers drop changes rather than block the producer.
	Subscribe() (<-chan Change, func())
	// Refresh asks for an immediate cycle. It returns once the cycle is
	// queued, not when it finishes.
	Refresh(ctx context.Context) error
}

// StaticSource serves a fixed Site, e.g. testdata/site.json. It is used for
// web development (-fixture) and handler tests.
type StaticSource struct {
	site *Site
	mu   sync.Mutex
	subs map[chan Change]struct{}
}

func NewStaticSource(site *Site) *StaticSource {
	return &StaticSource{site: site, subs: map[chan Change]struct{}{}}
}

func LoadFixture(path string) (*StaticSource, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Site
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("fixture %s: %w", path, err)
	}
	return NewStaticSource(&s), nil
}

func (s *StaticSource) Snapshot() *Site { return s.site }

func (s *StaticSource) Subscribe() (<-chan Change, func()) {
	ch := make(chan Change, 16)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

// Refresh on a fixture emits a tick so the live path can be exercised.
func (s *StaticSource) Refresh(ctx context.Context) error {
	s.Publish(Change{Kind: "tick", At: time.Now()})
	return nil
}

// Publish sends a change to every subscriber without blocking.
func (s *StaticSource) Publish(c Change) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- c:
		default:
		}
	}
}
