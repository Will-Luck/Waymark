package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/store"
)

// savedAt is the saved_at stamp of state.json on disk. Every write puts the
// app's clock there and these tests move the clock on between ticks, so a
// new stamp is one more write.
func savedAt(t *testing.T, dir string) time.Time {
	t.Helper()
	var st struct {
		SavedAt time.Time `json:"saved_at"`
	}
	if err := json.Unmarshal(readT(t, filepath.Join(dir, "state.json")), &st); err != nil {
		t.Fatal(err)
	}
	return st.SavedAt
}

// settle runs the world once, then one more probe pass and assemble, and
// writes the state at fixedNow: from here an unchanged world changes
// nothing in the file. It returns the app's clock for the test to move.
func settle(t *testing.T, w *world) *time.Time {
	t.Helper()
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock := fixedNow
	w.app.now = func() time.Time { return clock }
	w.app.collectProbes(ctx)
	w.app.assemble()
	if err := w.app.store.Save(clock); err != nil {
		t.Fatal(err)
	}
	return &clock
}

// Ten assembles over a world that has not changed, each followed by a save
// tick, write state.json 0 times.
func TestUnchangedAssemblesDoNotWriteState(t *testing.T) {
	w := newWorld(t)
	clock := settle(t, w)
	last := savedAt(t, w.dataDir)
	writes := 0
	for i := 0; i < 10; i++ {
		*clock = clock.Add(SaveEvery)
		w.app.assemble()
		w.app.save()
		if s := savedAt(t, w.dataDir); !s.Equal(last) {
			writes++
			last = s
		}
	}
	if writes != 0 {
		t.Errorf("%d of 10 save ticks wrote state.json after assembles that changed nothing, want 0", writes)
	}
}

// Probe points alone are written once every 10 minutes: 30 simulated
// minutes of probe passes (one every 2 minutes, each with its assemble) and
// save ticks (one every 10 seconds) write the file 3 times, at the 10, 20
// and 30 minute marks.
func TestProbePointsAloneWriteEveryTenMinutes(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	clock := settle(t, w)
	start := *clock
	last := savedAt(t, w.dataDir)
	var writes []time.Duration
	for tick := 1; tick <= 180; tick++ {
		*clock = start.Add(time.Duration(tick) * 10 * time.Second)
		if tick%12 == 0 {
			w.app.collectProbes(ctx)
			w.app.assemble()
		}
		w.app.save()
		if s := savedAt(t, w.dataDir); !s.Equal(last) {
			writes = append(writes, s.Sub(start))
			last = s
		}
	}
	want := []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute}
	if len(writes) != len(want) {
		t.Fatalf("probe points alone wrote state.json %d times in 30 minutes, at %v; want 3, at %v", len(writes), writes, want)
	}
	for i := range want {
		if writes[i] != want[i] {
			t.Errorf("write %d at %v, want %v", i+1, writes[i], want[i])
		}
	}
}

// An event (and the fingerprint change with it) is written at the next save
// tick, as before.
func TestEventWrittenAtTheNextTick(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	clock := settle(t, w)
	w.writeFeed(map[string]string{"beacon": "exited"}, "3 minutes", 0)
	w.app.collectDocker(ctx)
	w.app.assemble()
	*clock = clock.Add(SaveEvery)
	w.app.save()
	if got := savedAt(t, w.dataDir); !got.Equal(*clock) {
		t.Fatalf("state.json saved at %v, want the next tick %v", got, *clock)
	}
	r, err := store.Open(w.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range r.Events(time.Time{}, clock.Add(time.Hour)) {
		found = found || e.Project == "beacon" && e.Kind == "container"
	}
	if !found {
		t.Error("the file does not hold beacon's stop event")
	}
}

// Shutdown writes what the tick held back: probe points alone, 2 minutes
// after the last write, are written once by flush (the call Run makes after
// its collectors stop), and a second flush with nothing new writes nothing.
func TestShutdownWritesProbePointsOnce(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	clock := settle(t, w)
	before := len(w.app.store.ProbeHistory(probeKey(w), *clock))
	*clock = clock.Add(2 * time.Minute)
	w.app.collectProbes(ctx)
	w.app.assemble()
	w.app.save()
	if got := savedAt(t, w.dataDir); got.Equal(*clock) {
		t.Fatal("the save tick wrote probe points alone 2 minutes after the last write")
	}
	w.app.flush()
	if got := savedAt(t, w.dataDir); !got.Equal(*clock) {
		t.Fatalf("shutdown did not write: saved at %v, want %v", got, *clock)
	}
	r, err := store.Open(w.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.ProbeHistory(probeKey(w), *clock)); n != before+1 {
		t.Errorf("the file holds %d of beacon's probe points, want %d", n, before+1)
	}
	*clock = clock.Add(time.Second)
	w.app.flush()
	if got := savedAt(t, w.dataDir); got.Equal(*clock) {
		t.Error("a second shutdown write with nothing changed")
	}
}

// Run's shutdown writes what the save tick held back: probe points alone,
// moments after the last write, are in state.json once Run returns. (The
// test above calls flush itself; this one proves Run calls it.)
func TestRunFlushesProbePointsAtShutdown(t *testing.T) {
	w := newWorld(t)
	clock := settle(t, w)
	before := len(w.app.store.ProbeHistory(probeKey(w), *clock))
	cycles := w.app.Cycles()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.app.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for w.app.Cycles() == cycles {
		select {
		case <-deadline:
			cancel()
			t.Fatal("Run did not assemble")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if w.app.store.Dirty() || !w.app.store.ProbesDirty() {
		cancel()
		t.Fatalf("the scenario needs probe points alone: dirty %v, probes dirty %v", w.app.store.Dirty(), w.app.store.ProbesDirty())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	r, err := store.Open(w.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.ProbeHistory(probeKey(w), *clock)); n != before+1 {
		t.Errorf("after shutdown the file holds %d of beacon's probe points, want %d (Run's pass written)", n, before+1)
	}
}
