package derive

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

func fpBase() Fingerprint {
	last := testNow.Add(-48 * time.Hour)
	return Fingerprint{
		Name: "Beacon", Icon: "bell-ring", Tier: model.TierActive, RepoURL: "https://g/acme/beacon",
		LatestTag: ptr("v2.3.1"), TagURL: "https://g/acme/beacon/releases/tag/v2.3.1",
		DeployVersion: ptr("v2.3.1"), ContainerName: "beacon", ContainerState: ptr("running"),
		CI:           map[int64]CIState{10: {Conclusion: "success", Title: "old", Branch: "main"}},
		LastCommitAt: &last, OpenIssues: ptr(18),
		Probes:     map[string]bool{"Live": true},
		Notes:      map[string]time.Time{"beacon.md": testNow.Add(-72 * time.Hour)},
		NotesLabel: "Field notes",
	}
}

func kinds(evs []model.Event) map[string]model.Event {
	out := map[string]model.Event{}
	for _, e := range evs {
		out[e.Kind] = e
	}
	return out
}

func TestDiffFirstSightEmitsNothing(t *testing.T) {
	cur := map[string]Fingerprint{"beacon": fpBase()}
	if evs := Diff(nil, cur, testNow); len(evs) != 0 {
		t.Errorf("empty state emitted %+v", evs)
	}
	if evs := Diff(map[string]Fingerprint{"other": fpBase()}, cur, testNow); len(evs) != 0 {
		t.Errorf("a project's first sighting emitted %+v", evs)
	}
	if evs := Diff(cur, cur, testNow); len(evs) != 0 {
		t.Errorf("no change emitted %+v", evs)
	}
}

func TestDiffEveryKind(t *testing.T) {
	prev := fpBase()
	cur := fpBase()
	cur.LatestTag = ptr("v2.4.0")
	cur.TagURL = "https://g/acme/beacon/releases/tag/v2.4.0"
	cur.DeployVersion = ptr("v2.4.0")
	cur.ContainerState = ptr("exited")
	cur.CI = map[int64]CIState{10: {Conclusion: "success"}, 11: {Conclusion: "failure", Title: "Sweep", Branch: "main", URL: "https://g/run/11"}, 12: {Title: "running"}}
	newest := testNow.Add(-time.Hour)
	cur.LastCommitAt = &newest
	cur.LastCommitSHA = "abcdef1234"
	cur.Commits = []time.Time{newest, newest.Add(-time.Minute), newest.Add(-2 * time.Minute), testNow.Add(-49 * time.Hour)}
	cur.OpenIssues = ptr(19)
	cur.Probes = map[string]bool{"Live": false}
	cur.Notes = map[string]time.Time{"beacon.md": testNow.Add(-time.Hour)}
	cur.Tier = model.TierLive
	evs := Diff(map[string]Fingerprint{"beacon": prev}, map[string]Fingerprint{"beacon": cur}, testNow)
	k := kinds(evs)
	want := map[string]struct {
		id, text string
		level    model.Level
	}{
		"release":   {"beacon:release:v2.4.0", "Beacon v2.4.0 tagged", model.LevelGood},
		"deploy":    {"beacon:deploy:v2.4.0:202610312200", "Beacon now runs v2.4.0 (was v2.3.1)", model.LevelGood},
		"container": {"beacon:container:exited:202610312200", "Container beacon stopped", model.LevelAlert},
		"ci":        {"beacon:ci:11", "CI failed on main: Sweep", model.LevelAlert},
		"commits":   {"beacon:commits:abcdef1234", "3 new commits", model.LevelInfo},
		"issues":    {"beacon:issues:19:202610312200", "Open issues up by 1 (19 open)", model.LevelInfo},
		"probe":     {"beacon:probe:Live:down:202610312200", "Live is down", model.LevelAlert},
		"note":      {"beacon:note:beacon.md:" + testNow.Add(-time.Hour).Format("20060102150405"), "Field note beacon.md was updated", model.LevelInfo},
		"tier":      {"beacon:tier:live:202610312200", "Now Live (was Active)", model.LevelInfo},
	}
	if len(evs) != len(want) {
		t.Errorf("events = %d, want %d: %+v", len(evs), len(want), evs)
	}
	for kind, w := range want {
		e, ok := k[kind]
		if !ok {
			t.Errorf("no %s event", kind)
			continue
		}
		if e.ID != w.id || e.Text != w.text || e.Level != w.level || e.Project != "beacon" || e.ProjectName != "Beacon" || e.Icon != "bell-ring" || !e.At.Equal(testNow) {
			t.Errorf("%s = %+v, want id %q text %q level %s", kind, e, w.id, w.text, w.level)
		}
	}
	if k["ci"].Href != "https://g/run/11" || k["release"].Href != cur.TagURL {
		t.Errorf("hrefs: ci %q release %q", k["ci"].Href, k["release"].Href)
	}
}

func TestDiffRecoveriesAndUnknowns(t *testing.T) {
	prev := fpBase()
	prev.ContainerState = ptr("exited")
	prev.Probes = map[string]bool{"Live": false}
	prev.CI = map[int64]CIState{11: {Title: "in progress"}}
	cur := fpBase()
	cur.CI = map[int64]CIState{11: {Conclusion: "success", Title: "done", Branch: "main"}}
	k := kinds(Diff(map[string]Fingerprint{"e": prev}, map[string]Fingerprint{"e": cur}, testNow))
	if e := k["container"]; e.Text != "Container beacon is running again" || e.Level != model.LevelGood {
		t.Errorf("container = %+v", e)
	}
	if e := k["probe"]; e.Text != "Live is back up" || e.Level != model.LevelGood {
		t.Errorf("probe = %+v", e)
	}
	if e := k["ci"]; e.Text != "CI passed on main: done" || e.Level != model.LevelGood {
		t.Errorf("a run that was in progress and completed = %+v", e)
	}

	// Unknown on either side never emits.
	unk := fpBase()
	unk.LatestTag, unk.DeployVersion, unk.ContainerState, unk.CI, unk.OpenIssues, unk.Probes, unk.Notes, unk.LastCommitAt = nil, nil, nil, nil, nil, nil, nil, nil
	if evs := Diff(map[string]Fingerprint{"e": unk}, map[string]Fingerprint{"e": fpBase()}, testNow); len(evs) != 0 {
		t.Errorf("unknown -> known emitted %+v", evs)
	}
	if evs := Diff(map[string]Fingerprint{"e": fpBase()}, map[string]Fingerprint{"e": unk}, testNow); len(evs) != 0 {
		t.Errorf("known -> unknown emitted %+v", evs)
	}
	// Deploy to an unknown version is not a deploy.
	nov := fpBase()
	nov.DeployVersion = ptr("")
	if evs := Diff(map[string]Fingerprint{"e": fpBase()}, map[string]Fingerprint{"e": nov}, testNow); len(evs) != 0 {
		t.Errorf("version lost emitted %+v", evs)
	}
}

func TestMergeKeepsKnownValues(t *testing.T) {
	prev := fpBase()
	cur := Fingerprint{Name: "Beacon", Tier: model.TierLive, CI: map[int64]CIState{12: {Conclusion: "success"}}}
	m := Merge(prev, cur)
	if m.LatestTag == nil || *m.LatestTag != "v2.3.1" || m.OpenIssues == nil || m.Probes["Live"] != true || m.Tier != model.TierLive {
		t.Errorf("merge = %+v", m)
	}
	if _, ok := m.CI[10]; !ok {
		t.Error("seen runs must stay known")
	}
	// A tier worked out without forge data is unknown: the stored one stays.
	if m := Merge(prev, Fingerprint{Name: "Beacon"}); m.Tier != model.TierActive {
		t.Errorf("unknown tier replaced the stored one: %q", m.Tier)
	}
	if _, ok := m.CI[12]; !ok {
		t.Error("new runs must be kept")
	}
	big := Fingerprint{CI: map[int64]CIState{}}
	for i := int64(0); i < 40; i++ {
		big.CI[i] = CIState{Conclusion: "success"}
	}
	bm := Merge(Fingerprint{}, big)
	if len(bm.CI) != 20 {
		t.Errorf("CI memory = %d, want the newest 20", len(bm.CI))
	} else if _, ok := bm.CI[39]; !ok {
		t.Error("newest run dropped")
	}
	// Fingerprints persist as JSON.
	b, err := json.Marshal(bm)
	if err != nil {
		t.Fatal(err)
	}
	var back Fingerprint
	if err := json.Unmarshal(b, &back); err != nil || len(back.CI) != 20 {
		t.Errorf("round trip: %v %d", err, len(back.CI))
	}
}
