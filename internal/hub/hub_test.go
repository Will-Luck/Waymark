package hub

import (
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

func TestPublishFanOut(t *testing.T) {
	h := New(4)
	a, cancelA := h.Subscribe()
	b, cancelB := h.Subscribe()
	defer cancelB()
	h.Publish(model.Change{Kind: "tick"})
	for _, ch := range []<-chan model.Change{a, b} {
		select {
		case c := <-ch:
			if c.Kind != "tick" {
				t.Errorf("got %+v", c)
			}
		case <-time.After(time.Second):
			t.Fatal("no change delivered")
		}
	}
	cancelA()
	cancelA() // idempotent
	if _, ok := <-a; ok {
		t.Error("cancel must close the channel")
	}
	if h.Len() != 1 {
		t.Errorf("subscribers = %d", h.Len())
	}
	h.Publish(model.Change{Kind: "tick"}) // must not panic on the closed one
}

func TestSlowSubscriberDrops(t *testing.T) {
	h := New(2)
	ch, cancel := h.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.Publish(model.Change{Kind: "project", Slug: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a slow subscriber")
	}
	if n := len(ch); n != 2 {
		t.Errorf("buffered = %d, want 2", n)
	}
}

func TestConcurrentSubscribeCancelPublish(t *testing.T) {
	h := New(1)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, cancel := h.Subscribe()
			cancel()
		}()
		go func() {
			defer wg.Done()
			h.Publish(model.Change{Kind: "tick"})
		}()
	}
	wg.Wait()
	if h.Len() != 0 {
		t.Errorf("leaked subscribers: %d", h.Len())
	}
}
