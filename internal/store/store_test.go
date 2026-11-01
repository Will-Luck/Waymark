package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/derive"
	"github.com/Will-Luck/waymark/internal/model"
)

var now = time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)

func ev(id string, at time.Time) model.Event {
	return model.Event{ID: id, At: at, Project: "beacon", Kind: "commits", Level: model.LevelInfo, Text: id}
}

func TestOpenEmptyAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Empty() || s.Dirty() {
		t.Error("a fresh store is empty and clean")
	}
	tag := "v1.25.0"
	s.SetFingerprints(map[string]derive.Fingerprint{"beacon": {Name: "Beacon", LatestTag: &tag}})
	added := s.AddEvents([]model.Event{ev("a", now.Add(-time.Hour)), ev("b", now)}, now)
	if len(added) != 2 {
		t.Errorf("added = %v", added)
	}
	s.AddProbe("http://x/", model.ProbePoint{At: now, Up: true, LatencyMS: 5}, now)
	if !s.Dirty() {
		t.Error("changes must mark the store dirty")
	}
	if err := s.Save(now); err != nil {
		t.Fatal(err)
	}
	if s.Dirty() {
		t.Error("save must clear dirty")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json.tmp")); !os.IsNotExist(err) {
		t.Error("tmp file left behind")
	}
	empty, _ := Open(t.TempDir())
	if err := empty.Save(now); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(empty.path); !strings.Contains(string(b), `"events":[]`) {
		t.Errorf("an empty store writes events as [], got %s", b)
	}

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Empty() {
		t.Error("reopened store lost fingerprints")
	}
	fp := r.Fingerprints()["beacon"]
	if fp.LatestTag == nil || *fp.LatestTag != "v1.25.0" {
		t.Errorf("fingerprint = %+v", fp)
	}
	evs := r.Events(now.Add(-24*time.Hour), now)
	if len(evs) != 2 || evs[0].ID != "b" || evs[1].ID != "a" {
		t.Errorf("events (newest first) = %+v", evs)
	}
	if h := r.ProbeHistory("http://x/", now); len(h) != 1 || h[0].LatencyMS != 5 {
		t.Errorf("probe history = %+v", h)
	}
}

// A change made while Save is writing the file is not in that file, so the
// store must stay dirty and the next save must write it.
func TestChangeDuringSaveStaysDirty(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.AddEvents([]model.Event{ev("a", now)}, now)
	s.beforeWrite = func() { s.AddEvents([]model.Event{ev("late", now)}, now) }
	if err := s.Save(now); err != nil {
		t.Fatal(err)
	}
	s.beforeWrite = nil
	if !s.Dirty() {
		t.Fatal("an event added during the write was marked saved")
	}
	if err := s.Save(now); err != nil {
		t.Fatal(err)
	}
	if s.Dirty() {
		t.Error("a quiet save must clear dirty")
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Events(time.Time{}, now); len(got) != 2 {
		t.Errorf("saved events = %+v", got)
	}
}

func TestAddEventsDedupesAndPrunes(t *testing.T) {
	s, _ := Open(t.TempDir())
	s.AddEvents([]model.Event{ev("old", now.AddDate(0, 0, -91)), ev("keep", now.AddDate(0, 0, -89))}, now)
	if got := s.AddEvents([]model.Event{ev("keep", now), ev("new", now)}, now); len(got) != 1 || got[0].ID != "new" {
		t.Errorf("dedupe: added %+v", got)
	}
	all := s.Events(time.Time{}, now)
	var ids []string
	for _, e := range all {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "new,keep" {
		t.Errorf("after 90-day prune = %v", ids)
	}
	var many []model.Event
	for i := 0; i < MaxEvents+50; i++ {
		many = append(many, ev(fmt.Sprintf("e%05d", i), now.Add(time.Duration(i)*time.Second-time.Hour*3)))
	}
	s.AddEvents(many, now)
	all = s.Events(time.Time{}, now)
	if len(all) != MaxEvents {
		t.Fatalf("ring = %d, want %d", len(all), MaxEvents)
	}
	if all[0].ID != "new" || all[1].ID != fmt.Sprintf("e%05d", MaxEvents+49) {
		t.Errorf("newest kept = %s, %s", all[0].ID, all[1].ID)
	}
	if got := s.Events(now.Add(-time.Minute), now); len(got) != 1 || got[0].ID != "new" {
		t.Errorf("since filter = %+v", got)
	}
}

func TestProbeHistoryWindow(t *testing.T) {
	s, _ := Open(t.TempDir())
	for i := 0; i < 30; i++ {
		s.AddProbe("u", model.ProbePoint{At: now.Add(-time.Duration(29-i) * time.Hour), Up: i%2 == 0}, now)
	}
	h := s.ProbeHistory("u", now)
	if len(h) != 25 {
		t.Fatalf("history = %d points, want 25 (24 h inclusive)", len(h))
	}
	for i := 1; i < len(h); i++ {
		if h[i].At.Before(h[i-1].At) {
			t.Fatal("history not oldest first")
		}
	}
	if h[0].At.Before(now.Add(-24 * time.Hour)) {
		t.Errorf("oldest = %v", h[0].At)
	}
	if s.ProbeHistory("nope", now) != nil {
		t.Error("unknown probe has no history")
	}
}

func TestCorruptStateIsSetAside(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("a corrupt state must not stop the site: %v", err)
	}
	if !s.Empty() {
		t.Error("corrupt state should start empty")
	}
	if s.Warning() == "" {
		t.Error("corrupt state should leave a warning")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "state.json.corrupt-*"))
	if len(matches) != 1 {
		t.Errorf("corrupt copy = %v", matches)
	}
}

func TestOpenRejectsUnwritableDir(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing", "deeper")); err != nil {
		t.Errorf("missing data dir should be created: %v", err)
	}
}
