// Package store keeps Waymark's only writable state, DATA_DIR/state.json:
// the event history, probe history and the last per-project fingerprints.
// Writes are atomic (temp file, fsync, rename).
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Will-Luck/waymark/internal/derive"
	"github.com/Will-Luck/waymark/internal/model"
)

const (
	MaxEvents     = 5000
	EventDays     = 90
	ProbeWindow   = 24 * time.Hour
	stateFile     = "state.json"
	schemaVersion = 1
)

type state struct {
	Version      int                           `json:"version"`
	SavedAt      time.Time                     `json:"saved_at"`
	Events       []model.Event                 `json:"events"` // oldest first
	Probes       map[string][]model.ProbePoint `json:"probes"`
	Fingerprints map[string]derive.Fingerprint `json:"fingerprints"`
}

type Store struct {
	path    string
	mu      sync.Mutex
	st      state
	ids     map[string]bool
	dirty   bool
	changes uint64 // bumped on every change; Save clears dirty only if it held
	warning string
	saveMu  sync.Mutex // one Save at a time (they share the temp file)

	// beforeWrite, when set (tests), runs between the snapshot and the write.
	beforeWrite func()
}

// Open reads dir/state.json, creating dir if needed. A missing file is an
// empty store; a corrupt one is renamed aside and the store starts empty.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	s := &Store{path: filepath.Join(dir, stateFile), ids: map[string]bool{}}
	s.reset()
	b, err := os.ReadFile(s.path)
	switch {
	case os.IsNotExist(err):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("state: %w", err)
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		aside := fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().Unix())
		if rerr := os.Rename(s.path, aside); rerr != nil {
			return nil, fmt.Errorf("state is corrupt (%v) and could not be set aside: %w", err, rerr)
		}
		s.warning = fmt.Sprintf("state.json was unreadable (%v); moved to %s and started fresh", err, filepath.Base(aside))
		return s, nil
	}
	if st.Probes == nil {
		st.Probes = map[string][]model.ProbePoint{}
	}
	if st.Fingerprints == nil {
		st.Fingerprints = map[string]derive.Fingerprint{}
	}
	s.st = st
	for _, e := range st.Events {
		s.ids[e.ID] = true
	}
	return s, nil
}

func (s *Store) reset() {
	s.st = state{Version: schemaVersion, Probes: map[string][]model.ProbePoint{}, Fingerprints: map[string]derive.Fingerprint{}}
}

// Warning is set when Open had to discard a corrupt state file.
func (s *Store) Warning() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warning
}

// Empty reports whether no fingerprints have ever been stored (so the next
// assemble must not emit events).
func (s *Store) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.st.Fingerprints) == 0
}

// markLocked records a change; s.mu is held.
func (s *Store) markLocked() {
	s.dirty = true
	s.changes++
}

func (s *Store) Dirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// Fingerprints returns a copy of the stored fingerprints.
func (s *Store) Fingerprints() map[string]derive.Fingerprint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]derive.Fingerprint, len(s.st.Fingerprints))
	for k, v := range s.st.Fingerprints {
		out[k] = v
	}
	return out
}

func (s *Store) SetFingerprints(m map[string]derive.Fingerprint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string]derive.Fingerprint, len(m))
	for k, v := range m {
		cp[k] = v
	}
	s.st.Fingerprints = cp
	s.markLocked()
}

// AddEvents stores events whose IDs are new and returns those. Events older
// than 90 days are dropped and at most MaxEvents are kept.
func (s *Store) AddEvents(evs []model.Event, now time.Time) []model.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var added []model.Event
	for _, e := range evs {
		if e.ID == "" || s.ids[e.ID] {
			continue
		}
		s.ids[e.ID] = true
		s.st.Events = append(s.st.Events, e)
		added = append(added, e)
	}
	sort.SliceStable(s.st.Events, func(i, j int) bool { return s.st.Events[i].At.Before(s.st.Events[j].At) })
	cutoff := now.AddDate(0, 0, -EventDays)
	start := 0
	for start < len(s.st.Events) && s.st.Events[start].At.Before(cutoff) {
		start++
	}
	if n := len(s.st.Events) - start; n > MaxEvents {
		start += n - MaxEvents
	}
	if start > 0 {
		for _, e := range s.st.Events[:start] {
			delete(s.ids, e.ID)
		}
		s.st.Events = append([]model.Event(nil), s.st.Events[start:]...)
	}
	if len(added) > 0 || start > 0 {
		s.markLocked()
	}
	return added
}

// Events returns events at or after since, newest first.
func (s *Store) Events(since, now time.Time) []model.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.Event{}
	for i := len(s.st.Events) - 1; i >= 0; i-- {
		e := s.st.Events[i]
		if e.At.Before(since) {
			break
		}
		if e.At.After(now) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// AddProbe records a probe result and trims that probe's history to 24 h.
func (s *Store) AddProbe(key string, pt model.ProbePoint, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := append(s.st.Probes[key], pt)
	sort.SliceStable(h, func(i, j int) bool { return h[i].At.Before(h[j].At) })
	cutoff := now.Add(-ProbeWindow)
	start := 0
	for start < len(h) && h[start].At.Before(cutoff) {
		start++
	}
	s.st.Probes[key] = append([]model.ProbePoint(nil), h[start:]...)
	s.markLocked()
}

// ProbeHistory is the last 24 h of a probe, oldest first (nil when unknown).
func (s *Store) ProbeHistory(key string, now time.Time) []model.ProbePoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.st.Probes[key]
	if !ok {
		return nil
	}
	cutoff := now.Add(-ProbeWindow)
	out := []model.ProbePoint{}
	for _, pt := range h {
		if !pt.At.Before(cutoff) {
			out = append(out, pt)
		}
	}
	return out
}

// PruneProbes forgets probes not in keep (removed from the registry).
func (s *Store) PruneProbes(keep map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.st.Probes {
		if !keep[k] {
			delete(s.st.Probes, k)
			s.markLocked()
		}
	}
}

// Save writes state.json atomically. A change made while the file is being
// written is not in it, so the store then stays dirty.
func (s *Store) Save(now time.Time) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	snap := s.changes
	s.st.SavedAt = now.UTC()
	if s.st.Events == nil {
		s.st.Events = []model.Event{} // [] rather than null for anyone reading the file
	}
	b, err := json.Marshal(s.st)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.mu.Lock()
	if s.changes == snap {
		s.dirty = false
	}
	s.mu.Unlock()
	return nil
}
