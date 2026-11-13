package derive

import (
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/probe"
)

// siteAt is a project Live through its probe alone (no repo, no notes) as
// seen at at: 12 hours of up checks two minutes apart, then the last fails
// checks failed (0: the check at at answered). The history holds the check
// at at too, as the store's does once the collector has written it.
func siteAt(slug string, at time.Time, fails int, prev *Fingerprint) Input {
	u := "https://" + slug + ".example.org/"
	last := probe.Result{URL: u, At: at, Up: fails == 0, Code: 200, LatencyMS: 20}
	if fails > 0 {
		last.Code, last.LatencyMS, last.Err = 0, 0, "context deadline exceeded"
	}
	var hist []model.ProbePoint
	for i := 360; i >= 1; i-- {
		pt := model.ProbePoint{At: at.Add(-time.Duration(i) * 2 * time.Minute), Up: i >= fails}
		if pt.Up {
			pt.LatencyMS = 20
		}
		hist = append(hist, pt)
	}
	hist = append(hist, model.ProbePoint{At: at, Up: last.Up, LatencyMS: last.LatencyMS})
	return Input{
		Project:    registry.Project{Slug: slug, Name: "Site " + slug, URLs: []registry.URL{{Label: "Live site", URL: u, Probe: u}}},
		GiteaKnown: true,
		Probes:     []ProbeData{{Label: "Live site", URL: u, ProbeURL: u, Last: &last, History: hist}},
		Prev:       prev,
	}
}

// step is one assemble of one project at at, as the app runs it: the
// project built, the events Diff gives against the stored fingerprint, and
// the fingerprint stored after it.
func step(t *testing.T, at time.Time, in Input) (Built, []model.Event, Fingerprint) {
	t.Helper()
	e := env(t)
	e.Now = at
	b := BuildProject(e, in)
	var prev Fingerprint
	var evs []model.Event
	if in.Prev != nil {
		prev = *in.Prev
		evs = Diff(map[string]Fingerprint{in.Project.Slug: prev}, map[string]Fingerprint{in.Project.Slug: b.FP}, at)
	}
	return b, evs, Merge(prev, b.FP)
}

// needsYou is the site's Needs you count with this one project.
func needsYou(t *testing.T, at time.Time, in Input) int {
	t.Helper()
	e := env(t)
	e.Now = at
	return Assemble(SiteInput{Env: e, Registry: registry.Empty(), Projects: []Input{in}}).Site.Stats.NeedsYou
}

func fixes(p model.Project) []model.Action {
	var out []model.Action
	for _, a := range p.Actions {
		if a.Verb == "Fix" {
			out = append(out, a)
		}
	}
	return out
}

func ofKind(evs []model.Event, kind string) []model.Event {
	var out []model.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// One failed check after a run of up checks, then an answer: 0 Fix, 0
// events, the tier and the Needs you count unchanged. The page says it is
// checking again; the uptime and the raw history still count the sample.
func TestOneFailedCheckIsNotAnOutage(t *testing.T) {
	t0 := testNow
	_, _, fp := step(t, t0, siteAt("site", t0, 0, nil))
	if fp.Tier != model.TierLive {
		t.Fatalf("the answering site is %q, want live by its probe", fp.Tier)
	}
	t1 := t0.Add(2 * time.Minute)
	once := siteAt("site", t1, 1, &fp)
	b, evs, fp := step(t, t1, once)
	p := b.Project
	if len(evs) != 0 {
		t.Errorf("one failed check gave events %+v, want 0", evs)
	}
	if n := needsYou(t, t1, once); p.Tier != model.TierLive || len(fixes(p)) != 0 || n != 0 {
		t.Errorf("one failed check: tier %s (%s), Fix %+v, needs you %d; want live, none, 0", p.Tier, p.TierReason, fixes(p), n)
	}
	if pr := p.Probes[0]; pr.Up || !pr.Unconfirmed || pr.UptimePct >= 100 || len(pr.History) != 361 {
		t.Errorf("probe: up %v, unconfirmed %v, uptime %.1f, history %d; want down, unconfirmed, under 100, all 361 points", pr.Up, pr.Unconfirmed, pr.UptimePct, len(pr.History))
	}
	if p.Headline != "Live site: failed once, checking again" || p.HeadlineLevel != model.LevelWarn {
		t.Errorf("headline = %q %s", p.Headline, p.HeadlineLevel)
	}
	if s := signal(p, "probe"); s == nil || s.Level != model.LevelWarn || s.Text != "failed once" {
		t.Errorf("probe chip = %+v, want warn \"failed once\"", s)
	}
	t2 := t1.Add(2 * time.Minute)
	b, evs, _ = step(t, t2, siteAt("site", t2, 0, &fp))
	if len(evs) != 0 || b.Project.Tier != model.TierLive || len(fixes(b.Project)) != 0 || b.Project.Probes[0].Unconfirmed {
		t.Errorf("then an answer: events %+v, tier %s, Fix %+v; want none, live, none", evs, b.Project.Tier, fixes(b.Project))
	}
}

// Two failed checks in a row confirm the outage: 1 Fix and 1 down event,
// both dated at the first failed check, and the tier rule as before. The
// next answer gives 1 up event.
func TestTwoFailedChecksConfirmAnOutage(t *testing.T) {
	t0 := testNow
	_, _, fp := step(t, t0, siteAt("site", t0, 0, nil))
	t1 := t0.Add(2 * time.Minute)
	_, _, fp = step(t, t1, siteAt("site", t1, 1, &fp))
	t2 := t1.Add(2 * time.Minute)
	b, evs, fp := step(t, t2, siteAt("site", t2, 2, &fp))
	if f := fixes(b.Project); len(f) != 1 || !f[0].At.Equal(t1) {
		t.Errorf("Fix = %+v, want one dated at the first failed check %v", f, t1)
	}
	// Dated at the first failed check; the ID keeps the minute it was confirmed (t2, 22:04).
	if down := ofKind(evs, "probe"); len(down) != 1 || down[0].Text != "Live site is down" || !down[0].At.Equal(t1) ||
		down[0].ID != "site:probe:Live site:down:202610312204" {
		t.Errorf("probe events = %+v, want one down event dated at the first failed check %v", down, t1)
	}
	if b.Project.Tier == model.TierLive || len(ofKind(evs, "tier")) != 1 {
		t.Errorf("a confirmed outage keeps the tier rule: tier %s, tier events %+v", b.Project.Tier, ofKind(evs, "tier"))
	}
	t3 := t2.Add(2 * time.Minute)
	b, evs, _ = step(t, t3, siteAt("site", t3, 0, &fp))
	if up := ofKind(evs, "probe"); len(up) != 1 || up[0].Text != "Live site is back up" || len(fixes(b.Project)) != 0 {
		t.Errorf("then an answer: probe events %+v, Fix %+v; want one up event, no Fix", up, fixes(b.Project))
	}
}

// A reboot wave: 8 probes fail once each, then answer. 0 events and 0
// Needs you at every step.
func TestRebootWaveGivesNoEvents(t *testing.T) {
	slugs := []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8"}
	stored := map[string]Fingerprint{}
	wave := func(at time.Time, fails int) ([]model.Event, int) {
		var ins []Input
		for _, s := range slugs {
			var prev *Fingerprint
			if fp, ok := stored[s]; ok {
				prev = &fp
			}
			ins = append(ins, siteAt(s, at, fails, prev))
		}
		e := env(t)
		e.Now = at
		out := Assemble(SiteInput{Env: e, Registry: registry.Empty(), Projects: ins})
		var evs []model.Event
		if len(stored) > 0 {
			evs = Diff(stored, out.Fingerprints, at)
		}
		next := map[string]Fingerprint{}
		for s, fp := range out.Fingerprints {
			next[s] = Merge(stored[s], fp)
		}
		stored = next
		return evs, out.Site.Stats.NeedsYou
	}
	t0 := testNow
	wave(t0, 0)
	for i, fails := range []int{1, 0} {
		at := t0.Add(time.Duration(i+1) * 2 * time.Minute)
		if evs, needs := wave(at, fails); len(evs) != 0 || needs != 0 {
			t.Errorf("wave step %d (%d failed): %d events, needs you %d; want 0 and 0: %+v", i+1, fails, len(evs), needs, evs)
		}
	}
}

// A probe with no stored state (just added to the projects file, or the
// first run) that fails its first check was never seen up: it does not
// stand as up for the tier, and the tier is not fingerprinted until the
// failure is confirmed, so no "Now Live" then "Now Dormant" pair follows.
func TestNewProbeFailingOnceMakesNoTierPair(t *testing.T) {
	t1 := testNow
	prev := Fingerprint{Name: "Site site", Tier: model.TierDormant}
	in := siteAt("site", t1, 1, &prev)
	in.Probes[0].History = in.Probes[0].History[len(in.Probes[0].History)-1:] // its first check only
	b, evs, fp := step(t, t1, in)
	if b.Project.Tier != model.TierDormant || len(evs) != 0 || !b.Project.Probes[0].Unconfirmed {
		t.Errorf("first check failed: tier %s, events %+v, unconfirmed %v; want dormant, none, unconfirmed", b.Project.Tier, evs, b.Project.Probes[0].Unconfirmed)
	}
	t2 := t1.Add(2 * time.Minute)
	in = siteAt("site", t2, 2, &fp)
	in.Probes[0].History = in.Probes[0].History[len(in.Probes[0].History)-2:]
	b, evs, _ = step(t, t2, in)
	if len(ofKind(evs, "tier")) != 0 || len(fixes(b.Project)) != 1 {
		t.Errorf("second check failed: tier events %+v, Fix %+v; want none, one", ofKind(evs, "tier"), fixes(b.Project))
	}
}

// An outage that already counted stays confirmed on the next failed check
// even when the history no longer holds the checks that confirmed it (a
// crash can lose up to 10 minutes of probe points): no false "back up"
// event, and the Fix keeps its date.
func TestCountedProbeOutageSurvivesLostHistory(t *testing.T) {
	at := testNow
	began := at.Add(-10 * time.Minute)
	prev := Fingerprint{Name: "Site site", Tier: model.TierDormant, Probes: map[string]bool{"Live site": false}, ProbeDown: map[string]time.Time{"Live site": began}}
	b, evs, _ := step(t, at, siteAt("site", at, 1, &prev))
	f := fixes(b.Project)
	if len(evs) != 0 || len(f) != 1 || !f[0].At.Equal(began) || b.Project.Probes[0].Unconfirmed {
		t.Errorf("events %+v, Fix %+v, unconfirmed %v; want none, one at %v, confirmed", evs, f, b.Project.Probes[0].Unconfirmed, began)
	}
}

// The last result counts once whether or not the history holds it yet (the
// collector keeps the result just before it writes the point), and only
// failed checks in a row count.
func TestFailRunCountsTheLastResultOnce(t *testing.T) {
	at := testNow
	// pts are checks two minutes apart, oldest first, the newest at end.
	pts := func(end time.Time, ups ...bool) []model.ProbePoint {
		var out []model.ProbePoint
		for i, up := range ups {
			out = append(out, model.ProbePoint{At: end.Add(-time.Duration(len(ups)-1-i) * 2 * time.Minute), Up: up})
		}
		return out
	}
	failed := &probe.Result{At: at, Err: "context deadline exceeded"}
	cases := []struct {
		name  string
		last  *probe.Result
		hist  []model.ProbePoint
		n     int
		first time.Time
	}{
		{"history holds the last check", failed, pts(at, true, false, false), 2, at.Add(-2 * time.Minute)},
		{"history not written yet", failed, pts(at.Add(-2*time.Minute), true, false), 2, at.Add(-2 * time.Minute)},
		{"not in a row", failed, pts(at, false, true, false), 1, at},
		{"no history", failed, nil, 1, at},
		{"answered", &probe.Result{At: at, Up: true, Code: 200}, pts(at, false, false, true), 0, time.Time{}},
	}
	for _, tc := range cases {
		n, first := failRun(ProbeData{Label: "Live", Last: tc.last, History: tc.hist})
		if n != tc.n || !first.Equal(tc.first) {
			t.Errorf("%s: run of %d from %v, want %d from %v", tc.name, n, first, tc.n, tc.first)
		}
	}
	if probeConfirmed(nil, ProbeData{Label: "Live", Last: failed, History: pts(at, false, true, false)}) {
		t.Error("fail, up, fail confirmed an outage")
	}
}

// liveContainer is a project Live through its running container alone
// (commits 40 days old), its container in state with docker's status, in a
// feed written at at.
func liveContainer(at time.Time, state, status string, prev *Fingerprint) Input {
	in := synth{lastCommit: 40 * 24 * time.Hour, container: "app:v1.24.0", state: state, status: status, declared: true, dockerKnown: true}.input()
	in.FeedAt, in.Prev = at, prev
	return in
}

// A container stopped for about a minute, then running again: 0 events,
// 0 Fix, the tier unchanged, and the page says it is checking again.
// Stopped for 3 minutes: 1 stopped event dated at the stop, then 1 running
// event.
func TestContainerStopConfirmedAfterTwoMinutes(t *testing.T) {
	t0 := testNow
	_, _, fp := step(t, t0, liveContainer(t0, "running", "Up 3 days", nil))
	t1 := t0.Add(time.Minute)
	b, evs, fp := step(t, t1, liveContainer(t1, "exited", "Exited (1) About a minute ago", &fp))
	p := b.Project
	if len(evs) != 0 || len(fixes(p)) != 0 || p.Tier != model.TierLive {
		t.Errorf("stopped a minute: events %+v, Fix %+v, tier %s; want none, none, live", evs, fixes(p), p.Tier)
	}
	if p.Deploy == nil || !p.Deploy.Unconfirmed || p.Headline != "Container app is stopped (exit code 1), checking again" || p.HeadlineLevel != model.LevelWarn {
		t.Errorf("stopped a minute: deploy %+v, headline %q %s", p.Deploy, p.Headline, p.HeadlineLevel)
	}
	if s := signal(p, "deploy"); s == nil || s.Level != model.LevelWarn || s.Text != "v1.24.0 stopped, checking again" {
		t.Errorf("deploy chip = %+v, want warn \"v1.24.0 stopped, checking again\"", s)
	}
	t2 := t1.Add(time.Minute)
	b, evs, fp = step(t, t2, liveContainer(t2, "running", "Up 30 seconds", &fp))
	if len(evs) != 0 || len(fixes(b.Project)) != 0 || fp.ContainerDownSince != nil {
		t.Errorf("running again: events %+v, Fix %+v, first sighting %v; want none, none, none", evs, fixes(b.Project), fp.ContainerDownSince)
	}
	t3 := t2.Add(10 * time.Minute)
	stop := t3.Add(-3 * time.Minute)
	b, evs, fp = step(t, t3, liveContainer(t3, "exited", "Exited (1) 3 minutes ago", &fp))
	if c := ofKind(evs, "container"); len(c) != 1 || c[0].Text != "Container app stopped" || !c[0].At.Equal(stop) {
		t.Errorf("stopped 3 minutes: container events %+v, want one dated at the stop %v", c, stop)
	}
	if f := fixes(b.Project); len(f) != 1 || !f[0].At.Equal(stop) || b.Project.Deploy.Unconfirmed {
		t.Errorf("stopped 3 minutes: Fix %+v, want one at %v", f, stop)
	}
	t4 := t3.Add(time.Minute)
	_, evs, _ = step(t, t4, liveContainer(t4, "running", "Up 20 seconds", &fp))
	if c := ofKind(evs, "container"); len(c) != 1 || c[0].Text != "Container app is running again" {
		t.Errorf("running again: container events %+v, want one", c)
	}
}

// A paused (or missing) container gives docker no stop time: the first feed
// that shows it dates it, the stored fingerprint keeps that, and a feed 2
// minutes later confirms it, dated at the first sighting.
func TestPausedContainerConfirmedFromFirstSighting(t *testing.T) {
	t0 := testNow
	_, _, fp := step(t, t0, liveContainer(t0, "running", "Up 3 days", nil))
	seen := t0.Add(time.Minute)
	var b Built
	var evs []model.Event
	for i, at := range []time.Time{seen, seen.Add(time.Minute)} {
		b, evs, fp = step(t, at, liveContainer(at, "paused", "Up 3 days (Paused)", &fp))
		if len(evs) != 0 || len(fixes(b.Project)) != 0 || !b.Project.Deploy.Unconfirmed {
			t.Errorf("look %d, paused %v: events %+v, Fix %+v; want unconfirmed, none, none", i+1, at.Sub(seen), evs, fixes(b.Project))
		}
	}
	at := seen.Add(2 * time.Minute)
	b, evs, _ = step(t, at, liveContainer(at, "paused", "Up 3 days (Paused)", &fp))
	f, c := fixes(b.Project), ofKind(evs, "container")
	if len(f) != 1 || !f[0].At.Equal(seen) || len(c) != 1 || !c[0].At.Equal(seen) || b.Project.Deploy.Unconfirmed {
		t.Errorf("paused 2 minutes: Fix %+v, container events %+v; want one each, dated at the first sighting %v", f, c, seen)
	}
}

// A crash-looping container ("Restarting (1) 5 seconds ago" on every feed):
// docker's age restarts with each restart, so the stored first sighting
// confirms it 2 minutes on.
func TestCrashLoopConfirmsFromFirstSighting(t *testing.T) {
	t0 := testNow
	_, _, fp := step(t, t0, liveContainer(t0, "running", "Up 3 days", nil))
	first := t0.Add(time.Minute - 5*time.Second)
	var b Built
	for i := 1; i <= 3; i++ {
		at := t0.Add(time.Duration(i) * time.Minute)
		b, _, fp = step(t, at, liveContainer(at, "restarting", "Restarting (1) 5 seconds ago", &fp))
		if want := i < 3; b.Project.Deploy.Unconfirmed != want {
			t.Errorf("look %d: unconfirmed %v, want %v", i, b.Project.Deploy.Unconfirmed, want)
		}
	}
	if f := fixes(b.Project); len(f) != 1 || !f[0].At.Equal(first) {
		t.Errorf("Fix %+v, want one dated at the first sighting %v", f, first)
	}
}

// The first sighting survives an assemble that does not see the container
// (the feed not read yet after a restart), is kept for a first look with
// nothing stored, and is dropped once the container runs.
func TestMergeKeepsTheFirstSighting(t *testing.T) {
	seen := testNow.Add(-time.Minute)
	running := "running"
	prev := Fingerprint{ContainerName: "app", ContainerState: &running, ContainerDownSince: &seen}
	if m := Merge(prev, Fingerprint{Name: "App"}); m.ContainerDownSince == nil || !m.ContainerDownSince.Equal(seen) || m.ContainerState == nil {
		t.Errorf("feed unknown: merged %+v, want the stored state and first sighting kept", m)
	}
	if m := Merge(prev, Fingerprint{Name: "App", ContainerName: "app", ContainerState: &running}); m.ContainerDownSince != nil {
		t.Errorf("running: merged first sighting %v, want none", m.ContainerDownSince)
	}
	if m := Merge(Fingerprint{}, Fingerprint{Name: "App", ContainerName: "app", ContainerDownSince: &seen}); m.ContainerDownSince == nil || m.ContainerName != "app" {
		t.Errorf("first look with nothing stored: merged %+v, want the sighting kept", m)
	}
}

// While nothing is diffed (the events grace) the stored fingerprint keeps
// all it holds but its own container's first not-running sighting: taken
// while not confirmed, kept from the outage's start once confirmed (the
// stored state still says running), dropped once it runs, and left alone
// when the feed did not see it or it is another container.
func TestKeepSightingTakesOnlyTheFirstSighting(t *testing.T) {
	stored := testNow.Add(-time.Hour)
	seen := testNow.Add(-3 * time.Minute)
	running, missing := "running", "missing"
	prev := Fingerprint{Name: "App", Tier: model.TierLive, ContainerName: "app", ContainerState: &running, ContainerSince: &stored}
	held := Fingerprint{Name: "App", Tier: model.TierQuiet, ContainerName: "app", ContainerState: &running, ContainerSince: &stored, ContainerDownSince: &seen}
	kept := KeepSighting(prev, held)
	if kept.Tier != model.TierLive || kept.ContainerDownSince == nil || !kept.ContainerDownSince.Equal(seen) {
		t.Errorf("not confirmed yet: kept tier %s, first sighting %v; want live (stored), %v", kept.Tier, kept.ContainerDownSince, seen)
	}
	confirmed := Fingerprint{Name: "App", ContainerName: "app", ContainerState: &missing, ContainerSince: &seen}
	kept = KeepSighting(kept, confirmed)
	if *kept.ContainerState != "running" || !kept.ContainerSince.Equal(stored) || kept.ContainerDownSince == nil || !kept.ContainerDownSince.Equal(seen) {
		t.Errorf("confirmed: kept state %s since %v, first sighting %v; want running since %v (stored), %v",
			*kept.ContainerState, kept.ContainerSince, kept.ContainerDownSince, stored, seen)
	}
	if k := KeepSighting(kept, Fingerprint{Name: "App"}); k.ContainerDownSince == nil || !k.ContainerDownSince.Equal(seen) {
		t.Errorf("feed did not see it: first sighting %v, want %v kept", k.ContainerDownSince, seen)
	}
	if k := KeepSighting(kept, Fingerprint{ContainerName: "other", ContainerState: &running}); k.ContainerDownSince == nil {
		t.Error("another container running dropped this one's first sighting")
	}
	now := testNow
	if k := KeepSighting(kept, Fingerprint{ContainerName: "app", ContainerState: &running, ContainerSince: &now}); k.ContainerDownSince != nil || !k.ContainerSince.Equal(stored) {
		t.Errorf("running again: first sighting %v, since %v; want none, %v (stored)", k.ContainerDownSince, k.ContainerSince, stored)
	}
}

// A container never seen running (the first run, or one just declared)
// that is first seen paused, restarting or missing does not stand as
// running for the tier, and the tier is not fingerprinted until the outage
// is confirmed, so no "Now Live" then "Now Quiet" pair follows.
func TestNewContainerFirstSeenDownMakesNoTierPair(t *testing.T) {
	t1 := testNow
	prev := Fingerprint{Name: "App", Tier: model.TierQuiet}
	b, evs, fp := step(t, t1, liveContainer(t1, "paused", "Up 3 days (Paused)", &prev))
	if b.Project.Tier != model.TierQuiet || len(evs) != 0 || b.Project.Deploy == nil || !b.Project.Deploy.Unconfirmed {
		t.Errorf("first look, paused: tier %s, events %+v, deploy %+v; want quiet, none, unconfirmed", b.Project.Tier, evs, b.Project.Deploy)
	}
	t2 := t1.Add(2 * time.Minute)
	b, evs, _ = step(t, t2, liveContainer(t2, "paused", "Up 3 days (Paused)", &fp))
	if len(ofKind(evs, "tier")) != 0 || len(fixes(b.Project)) != 1 {
		t.Errorf("paused 2 minutes: tier events %+v, Fix %+v; want none, one", ofKind(evs, "tier"), fixes(b.Project))
	}
}

// An outage that already counted stays confirmed when its state changes
// (exited, then restarting): the Fix stays, with no 2-minute gap and no
// event.
func TestCountedContainerOutageStaysConfirmed(t *testing.T) {
	at := testNow
	stopped := at.Add(-time.Hour)
	exited := "exited"
	prev := Fingerprint{Name: "App", Tier: model.TierQuiet, ContainerName: "app", ContainerState: &exited, ContainerSince: &stopped}
	b, evs, _ := step(t, at, liveContainer(at, "restarting", "Restarting (1) 5 seconds ago", &prev))
	if b.Project.Deploy.Unconfirmed || len(fixes(b.Project)) != 1 || len(ofKind(evs, "container")) != 0 {
		t.Errorf("exited, then restarting: unconfirmed %v, Fix %+v, container events %+v; want confirmed, one Fix, none",
			b.Project.Deploy.Unconfirmed, fixes(b.Project), ofKind(evs, "container"))
	}
	if b.Project.Headline != "Container app is restarting (exit code 1)" || b.Project.HeadlineLevel != model.LevelAlert {
		t.Errorf("headline = %q %s", b.Project.Headline, b.Project.HeadlineLevel)
	}
}
