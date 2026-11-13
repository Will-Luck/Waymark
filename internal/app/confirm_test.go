package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

// writeBeacon writes the world's feed with beacon's container in state with
// docker's status, the file's mtime at (as the host cron writes it).
func writeBeacon(t *testing.T, w *world, state, status string, at time.Time) {
	t.Helper()
	w.writeFeed(nil, "", 0)
	p := filepath.Join(w.feedDir, "docker.json")
	lines := strings.Split(string(readT(t, p)), "\n")
	for i, l := range lines {
		if strings.Contains(l, `"Names":"beacon"`) {
			l = strings.Replace(l, `"State":"running"`, `"State":"`+state+`"`, 1)
			lines[i] = statusRe.ReplaceAllLiteralString(l, `"Status":"`+status+`"`)
		}
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// writeWithout writes the world's feed without name's container, the file's
// mtime at.
func writeWithout(t *testing.T, w *world, name string, at time.Time) {
	t.Helper()
	w.writeFeed(nil, "", 0)
	p := filepath.Join(w.feedDir, "docker.json")
	var kept []string
	for _, l := range strings.Split(string(readT(t, p)), "\n") {
		if !strings.Contains(l, `"Names":"`+name+`"`) {
			kept = append(kept, l)
		}
	}
	if err := os.WriteFile(p, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// beaconFixes are beacon's Fix actions from Docker on the current site.
func beaconFixes(w *world) []model.Action {
	var out []model.Action
	for _, a := range find(w.app.Snapshot(), "beacon").Actions {
		if a.Verb == "Fix" && a.Source == "Docker" {
			out = append(out, a)
		}
	}
	return out
}

// The real path, feed file to store. Beacon stopped about a minute, then
// running again: 0 events, 0 Fix, Needs you unchanged, and the headline
// says it is checking again. Stopped 3 minutes: 1 stopped event dated at
// the stop, then 1 running event. Paused (no docker age): confirmed by a
// later feed alone, each new mtime kicking an assemble.
func TestContainerOutageConfirmedThroughTheFeed(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock := fixedNow
	w.app.now = func() time.Time { return clock }
	beaconEvents := func() []model.Event {
		var out []model.Event
		for _, e := range w.app.store.Events(time.Time{}, clock) {
			if e.Project == "beacon" && e.Kind == "container" {
				out = append(out, e)
			}
		}
		return out
	}
	// look writes a feed at feedAt, reads it as the DockerEvery tick does,
	// checks that the new mtime kicked an assemble (D11: the trigger that
	// confirms a stop with no other change), and runs that assemble.
	look := func(state, status string, feedAt time.Time) *model.Project {
		clock = feedAt.Add(5 * time.Second)
		writeBeacon(t, w, state, status, feedAt)
		for len(w.app.kick) > 0 {
			<-w.app.kick
		}
		w.app.collectDocker(ctx)
		if len(w.app.kick) != 1 {
			t.Fatalf("the feed written at %v did not kick an assemble", feedAt)
		}
		w.app.assemble() // what the kick runs (the assembler, after Debounce)
		return find(w.app.Snapshot(), "beacon")
	}
	needs := w.app.Snapshot().Stats.NeedsYou

	t1 := fixedNow.Add(time.Minute)
	b := look("exited", "Exited (1) About a minute ago", t1)
	if n := w.app.Snapshot().Stats.NeedsYou; len(beaconEvents()) != 0 || len(beaconFixes(w)) != 0 || n != needs ||
		b.HeadlineLevel != model.LevelWarn || !strings.HasSuffix(b.Headline, ", checking again") {
		t.Errorf("stopped about a minute: events %+v, Fix %+v, needs you %d (was %d), headline %q %s",
			beaconEvents(), beaconFixes(w), n, needs, b.Headline, b.HeadlineLevel)
	}
	look("running", "Up 5 seconds", t1.Add(time.Minute))
	if len(beaconEvents()) != 0 || len(beaconFixes(w)) != 0 {
		t.Errorf("running again: events %+v, Fix %+v; want none", beaconEvents(), beaconFixes(w))
	}

	t2 := t1.Add(10 * time.Minute)
	stop := t2.Add(-3 * time.Minute)
	look("exited", "Exited (1) 3 minutes ago", t2)
	if evs, f := beaconEvents(), beaconFixes(w); len(evs) != 1 || !evs[0].At.Equal(stop) || len(f) != 1 || !f[0].At.Equal(stop) {
		t.Errorf("stopped 3 minutes: events %+v, Fix %+v; want one each, dated at the stop %v", evs, f, stop)
	}
	look("running", "Up 5 seconds", t2.Add(time.Minute))
	if evs := beaconEvents(); len(evs) != 2 || evs[0].Text != "Container beacon is running again" {
		t.Errorf("running again: events %+v, want the stop and one running event", evs)
	}

	t3 := t2.Add(10 * time.Minute)
	look("paused", "Up 4 days (Paused)", t3)
	look("paused", "Up 4 days (Paused)", t3.Add(time.Minute))
	if f := beaconFixes(w); len(f) != 0 {
		t.Errorf("paused a minute: Fix %+v, want none yet", f)
	}
	look("paused", "Up 4 days (Paused)", t3.Add(2*time.Minute))
	if f := beaconFixes(w); len(f) != 1 || !f[0].At.Equal(t3) {
		t.Errorf("paused 2 minutes, only the feed moved on: Fix %+v, want one dated at the first sighting %v", f, t3)
	}
}

// A restart while Gitea has not listed (the events grace: nothing is diffed
// and the stored state is kept). Beacon, stored as running, is missing from
// every feed: it is confirmed 2 minutes after it was first seen, the Fix
// dated then, not when the grace ends. Once Gitea lists, its event is dated
// at that first sighting too.
func TestContainerConfirmedWhileAForgeHasNotListed(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	w.listFail.Store(true)
	w.app = w.newApp(w.app.cfg)
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock := fixedNow
	w.app.now = func() time.Time { return clock }
	look := func(feedAt time.Time) {
		clock = feedAt.Add(5 * time.Second)
		writeWithout(t, w, "beacon", feedAt)
		w.app.collectDocker(ctx)
		w.app.assemble()
	}
	inGrace := func() bool {
		w.app.mu.Lock()
		defer w.app.mu.Unlock()
		ready, _ := w.app.eventsReadyLocked(clock)
		return !ready
	}
	first := fixedNow.Add(time.Minute)
	look(first)
	look(first.Add(time.Minute))
	if f := beaconFixes(w); len(f) != 0 {
		t.Errorf("missing a minute: Fix %+v, want none yet", f)
	}
	look(first.Add(2 * time.Minute))
	if !inGrace() {
		t.Fatal("events are diffed already: this test needs the grace before Gitea lists")
	}
	if f := beaconFixes(w); len(f) != 1 || !f[0].At.Equal(first) {
		t.Errorf("missing 2 minutes, Gitea not listed yet: Fix %+v, want one dated at the first sighting %v", f, first)
	}

	w.listFail.Store(false)
	w.app.collectForges(ctx, false)
	look(first.Add(3 * time.Minute))
	var evs []model.Event
	for _, e := range w.app.store.Events(time.Time{}, clock) {
		if e.Project == "beacon" && e.Kind == "container" {
			evs = append(evs, e)
		}
	}
	if len(evs) != 1 || evs[0].Text != "Container beacon is missing" || !evs[0].At.Equal(first) {
		t.Errorf("after Gitea listed: events %+v, want one missing event dated at the first sighting %v", evs, first)
	}
}
