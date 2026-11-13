package app

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/model"
)

// Without include_private (the default) a repo project shows only once its
// forge has listed it, so a restart while that forge is down leaves its
// probe out of the pass, and every homepage too (the index is empty). Their
// 24 h history stays as it was. Once every forge has listed, a probe no
// target names is really gone and is pruned as before.
func TestProbeHistoryKeptWhileAForgeHasNotListed(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	cfg := w.app.cfg
	cfg.IncludePrivate = false
	w.app = w.newApp(cfg)
	for i := 0; i < 3; i++ {
		if _, err := w.app.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	before := len(w.app.store.ProbeHistory(probeKey(w), fixedNow))
	if before != 3 {
		t.Fatalf("beacon's history before the restart = %d points, want 3 (one per run)", before)
	}
	// A history no target names: while the forge is down it may be a
	// homepage the list will name again.
	const gone = "https://beacon.example.org/"
	w.app.store.AddProbe(gone, model.ProbePoint{At: fixedNow.Add(-time.Hour), Up: true, LatencyMS: 20}, fixedNow)
	if err := w.app.store.Save(fixedNow); err != nil {
		t.Fatal(err)
	}

	w.listFail.Store(true)
	b := w.newApp(cfg)
	if _, err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if after := len(b.store.ProbeHistory(probeKey(w), fixedNow)); after != before {
		t.Errorf("beacon's history %d -> %d points across a restart with Gitea down, want %d", before, after, before)
	}
	if h := b.store.ProbeHistory(gone, fixedNow); len(h) != 1 {
		t.Errorf("a history no target names yet = %v across a restart with Gitea down, want its 1 point", h)
	}

	w.listFail.Store(false)
	if _, err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(b.store.ProbeHistory(probeKey(w), fixedNow)); n != before+1 {
		t.Errorf("beacon's history after Gitea listed = %d points, want %d (probed again)", n, before+1)
	}
	if h := b.store.ProbeHistory(gone, fixedNow); h != nil {
		t.Errorf("a probe no target names after every forge listed was kept: %v", h)
	}
}

// A project Live through its projects-file probe alone, its forge down at a
// restart (include_private false, so the project is left out) and back
// before the next probe pass: neither the list's assemble nor that pass
// writes an event, and the probe's history is kept.
func TestNoTierEventsWhenAForgeReturnsBeforeTheProbes(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.writeFeed(map[string]string{"harbour": "exited"}, "2 days", 0) // not running: Live by its probe alone
	file := writeProjects(t, "owner: acme\nprojects:\n  - slug: harbour\n    name: Harbour\n    repos:\n      - gitea: harbour\n    urls:\n      - {label: Live, url: \"https://registry.example.com\", probe: \""+w.srv.URL+"/probe/harbour\"}\n")
	cfg := w.app.cfg
	cfg.IncludePrivate = false
	cfg.Projects = config.FileRef{File: file}
	cfg.Ideas.File = config.FileRef{}
	a := w.newApp(cfg)
	for i := 0; i < 2; i++ {
		if _, err := a.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if p := find(a.Snapshot(), "harbour"); p.Tier != model.TierLive {
		t.Fatalf("before the restart harbour is %s (%s), want live by its probe", p.Tier, p.TierReason)
	}
	key := w.srv.URL + "/probe/harbour"
	hist := len(a.store.ProbeHistory(key, fixedNow))
	events := len(a.store.Events(time.Time{}, fixedNow))

	w.listFail.Store(true)
	b := w.newApp(cfg)
	if _, err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	w.listFail.Store(false)
	b.collectForges(ctx, false) // the RepoListEvery ticker
	b.assemble()                // its kick, before the next ProbeEvery tick
	if got := b.store.Events(time.Time{}, fixedNow); len(got) != events {
		t.Errorf("the list's assemble wrote %d events, want 0: %+v", len(got)-events, got[:len(got)-events])
	}
	if n := len(b.store.ProbeHistory(key, fixedNow)); n != hist {
		t.Errorf("harbour's probe history %d -> %d points, want it kept", hist, n)
	}
	b.collectProbes(ctx)
	b.assemble()
	if got := b.store.Events(time.Time{}, fixedNow); len(got) != events {
		t.Errorf("the next probe pass wrote %d events, want 0: %+v", len(got)-events, got[:len(got)-events])
	}
	if p := find(b.Snapshot(), "harbour"); p.Tier != model.TierLive {
		t.Errorf("after the probe pass harbour is %s (%s), want live", p.Tier, p.TierReason)
	}
}
