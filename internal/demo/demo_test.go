package demo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/app"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/store"
)

// fixedNow is the moment every fixture is generated for; the other packages'
// tests use the same one.
var fixedNow = time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)

// demoApp is demo mode for w at fixedNow: the real pipeline against the fake
// forges, not yet run. The feed keeps the mtime it was written with, so no
// wall-clock time reaches what the pipeline reads.
func demoApp(t *testing.T, w *World) (*app.App, *Demo) {
	t.Helper()
	d, err := Run(w, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	a, err := app.New(d.Config, app.NewForges(d.Config))
	if err != nil {
		t.Fatal(err)
	}
	a.SetClock(func() time.Time { return fixedNow })
	return a, d
}

func runOnce(t *testing.T, a *app.App) *model.Site {
	t.Helper()
	s, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func project(t *testing.T, s *model.Site, slug string) *model.Project {
	t.Helper()
	p := s.ProjectBySlug(slug)
	if p == nil {
		t.Fatalf("no project %s in %v", slug, slugs(s))
	}
	return p
}

func slugs(s *model.Site) []string {
	var out []string
	for _, p := range s.Projects {
		out = append(out, p.Slug)
	}
	return out
}

func action(p *model.Project, verb, source string) *model.Action {
	for i := range p.Actions {
		if p.Actions[i].Verb == verb && (source == "" || p.Actions[i].Source == source) {
			return &p.Actions[i]
		}
	}
	return nil
}

// TestDemoCoversEveryState assembles the demo once and finds each state the
// world is built to show, by slug and field.
func TestDemoCoversEveryState(t *testing.T) {
	a, d := demoApp(t, Dataset(fixedNow))
	s := runOnce(t, a)

	for _, st := range s.Sources {
		if !st.OK {
			t.Errorf("source %s failed: %s", st.Name, st.Error)
		}
	}
	if got := len(s.Sources); got != 9 {
		t.Errorf("sources = %d, want both forges and every optional source", got)
	}
	if !s.Meta.Demo || !s.Meta.Notes || !s.Meta.Docker || !s.Meta.Ideas || !s.Meta.FDroid {
		t.Errorf("meta = %+v", s.Meta)
	}

	// A lagging deploy, a plan in progress, unreleased changes, a labelled
	// needs-you issue, a stale PR and a relation: Beacon.
	b := project(t, s, "beacon")
	if b.Deploy == nil || !b.Deploy.Lagging || b.Deploy.Version != "v2.3.1" || b.Deploy.Latest != "v2.4.0" || action(b, "Deploy", "") == nil {
		t.Errorf("beacon deploy = %+v, actions %+v", b.Deploy, b.Actions)
	}
	if len(b.Plans) != 1 || b.Plans[0].Done != 3 || b.Plans[0].Total != 7 {
		t.Errorf("beacon plans = %+v, want one at 3 of 7", b.Plans)
	}
	if b.Unreleased == nil || b.Unreleased.Count != 3 || b.Unreleased.LastVersion != "2.4.0" {
		t.Errorf("beacon unreleased = %+v", b.Unreleased)
	}
	if a := action(b, "Decide", "Gitea label"); a == nil || a.Text != "Pick the default sound for new alerts" {
		t.Errorf("beacon label needs-you = %+v", b.Actions)
	}
	if a := action(b, "Review", "Gitea"); a == nil || !strings.HasPrefix(a.Text, "#22 ") {
		t.Errorf("beacon stale PR = %+v", b.Actions)
	}
	if len(b.Relations) == 0 || b.Relations[0].To != "ledger" || b.Relations[0].Kind != "depends-on" || !b.Relations[0].Outgoing {
		t.Errorf("beacon relations = %+v", b.Relations)
	}
	if len(b.Notes) != 2 {
		t.Errorf("beacon notes = %+v, want its own and the one whose front matter names it", b.Notes)
	}

	// Failing CI on main, a review request, an assigned issue and a
	// container matched by its OCI source label: ledger, an auto project.
	l := project(t, s, "ledger")
	if !l.Auto || len(l.CI) == 0 || l.CI[0].Branch != "main" || l.CI[0].Conclusion != "failure" || !strings.HasPrefix(l.Headline, "CI is failing on main") {
		t.Errorf("ledger CI = %+v, headline %q", l.CI, l.Headline)
	}
	if l.Deploy == nil || l.Deploy.Container != "ledger-api" || l.Deploy.State != "running" {
		t.Errorf("ledger deploy = %+v, want ledger-api by its OCI label", l.Deploy)
	}
	// The other two ways a container is found: named like the slug (atlas),
	// and named in the projects file (beacon, and beacon-app's F-Droid server).
	if at := project(t, s, "atlas"); at.Deploy == nil || at.Deploy.Container != "atlas" {
		t.Errorf("atlas deploy = %+v, want the container named like the slug", at.Deploy)
	}
	if action(l, "Review", "Review requested") == nil || action(l, "Decide", "Assigned to you") == nil {
		t.Errorf("ledger actions = %+v", l.Actions)
	}

	// A hold with commits since: Inkwell.
	ik := project(t, s, "inkwell")
	if ik.Tier != model.TierHold || ik.Hold == nil || ik.Hold.Kind != "parked" || !ik.Hold.Contradicted || action(ik, "Confirm", "") == nil {
		t.Errorf("inkwell = %s %+v %+v", ik.Tier, ik.Hold, ik.Actions)
	}

	// A mirror pair: Harbour on Gitea, mirrored to GitHub with stars and issues.
	h := project(t, s, "harbour")
	if h.Repo == nil || h.Forge != "Gitea" || h.GitHub == nil || h.GitHub.Stars != 128 || h.GitHub.LatestRelease != "v3.1.0" {
		t.Errorf("harbour repo %+v mirror %+v", h.Repo, h.GitHub)
	}
	mirrored := 0
	for _, is := range h.Issues {
		if is.Mirror && is.Source == "github" {
			mirrored++
		}
	}
	if mirrored != 2 || h.Tier != model.TierLive {
		t.Errorf("harbour: %d mirror issues, tier %s", mirrored, h.Tier)
	}

	// An empty repo, an archived repo, a private repo.
	if sb := project(t, s, "sandbox"); !sb.Auto || sb.Tier != model.TierDormant || sb.TierReason != "No commits yet" || !sb.LastActivity.IsZero() {
		t.Errorf("sandbox = %s %q %v", sb.Tier, sb.TierReason, sb.LastActivity)
	}
	if old := project(t, s, "old-site"); old.Tier != model.TierArchived || old.TierReason != "Retired in the projects file" || old.Repo == nil || !old.Repo.Archived {
		t.Errorf("old-site = %s %q %+v", old.Tier, old.TierReason, old.Repo)
	}
	v := project(t, s, "vault")
	if v.Repo == nil || !v.Repo.Private || v.Deploy == nil || v.Deploy.State != "exited" || action(v, "Fix", "Docker") == nil {
		t.Errorf("vault repo %+v deploy %+v", v.Repo, v.Deploy)
	}

	// A project with only notes, whose marker items wait on the user.
	on := project(t, s, "office-network")
	if on.Repo != nil || !on.ActivityFromNotes || len(on.Notes) != 1 || on.Tier != model.TierActive {
		t.Errorf("office-network = repo %+v from notes %v notes %+v tier %s", on.Repo, on.ActivityFromNotes, on.Notes, on.Tier)
	}
	if a := action(on, "Decide", "Notes"); a == nil || a.Text != "Mark to choose between the two access point models" || !a.Heuristic {
		t.Errorf("office-network actions = %+v", on.Actions)
	}

	// A probe down, and an F-Droid package.
	at := project(t, s, "atlas")
	if len(at.Probes) != 1 || at.Probes[0].Up || at.Probes[0].Code != 503 || action(at, "Fix", "Probe") == nil {
		t.Errorf("atlas probes %+v actions %+v", at.Probes, at.Actions)
	}
	if ba := project(t, s, "beacon-app"); ba.Deploy == nil || ba.Deploy.Source != "fdroid" || ba.Deploy.State != "published" || ba.Deploy.Version != "1.6.0" || ba.Deploy.Container != "fdroid-repo" {
		t.Errorf("beacon-app deploy = %+v", ba.Deploy)
	}

	// A GitHub primary read in full: tags through GraphQL, a run in progress.
	k := project(t, s, "kite")
	if k.Forge != "GitHub" || k.Repo == nil || k.Repo.LatestTag != "v0.9.0" || len(k.CI) == 0 || k.CI[0].Status != "in_progress" {
		t.Errorf("kite = %s %+v %+v", k.Forge, k.Repo, k.CI)
	}

	// Ideas from labelled issues on both forges, file ideas, one filed.
	var fromIssues, fromFile, filed int
	for _, i := range s.Ideas {
		switch {
		case i.Source == "issue":
			fromIssues++
		case i.Source == "file" && i.FiledIssue == 15 && i.ID == "beacon-01":
			filed++
			fromFile++
		case i.Source == "file":
			fromFile++
		}
	}
	if fromIssues < 2 || fromFile < 2 || filed != 1 {
		t.Errorf("ideas: %d from issues, %d from the file, %d filed", fromIssues, fromFile, filed)
	}
	if !slices.ContainsFunc(s.Ideas, func(i model.Idea) bool { return i.ID == "github:acme/kite#5" && i.Kind == "quick-win" && i.Size == "S" }) {
		t.Error("no kind/size from labels on a GitHub idea")
	}

	// The heatmap reads both forges; the activity feed is seeded.
	if s.Heatmap.Total == 0 || len(s.Events) == 0 {
		t.Errorf("heatmap total %d, %d events", s.Heatmap.Total, len(s.Events))
	}
	if s.Stats.Active == 0 || s.Stats.Live == 0 || s.Stats.Quiet == 0 || s.Stats.Dormant == 0 || s.Stats.OnHold == 0 || s.Stats.Archived == 0 {
		t.Errorf("not every tier has a project: %+v", s.Stats)
	}

	// Nothing in demo mode points past loopback.
	for _, p := range s.Projects {
		for _, pr := range p.Probes {
			if !strings.HasPrefix(pr.ProbeURL, d.SitesURL+"/") {
				t.Errorf("%s probes %s", p.Slug, pr.ProbeURL)
			}
		}
	}
	for _, f := range d.Config.Forges {
		if u, err := url.Parse(f.URL); err != nil || u.Hostname() != "127.0.0.1" {
			t.Errorf("forge %s at %s", f.ID, f.URL)
		}
	}
}

// The fake forge only reads, as the clients only read.
func TestHandlerIsReadOnly(t *testing.T) {
	h := Handler(Dataset(fixedNow))
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/api/v1/repos/acme/beacon/issues", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"query { viewer { contributionsCollection { contributionCalendar { weeks { contributionDays { date contributionCount } } } } } }"}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "contributionDays") {
		t.Errorf("graphql: %d %s", rec.Code, rec.Body.String())
	}
}

// A demo running longer than the feed's stale limit still reads it as fresh.
func TestDemoFeedStaysFresh(t *testing.T) {
	d, err := Run(Dataset(fixedNow), t.TempDir(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	st, _ := os.Stat(d.Config.Docker.Feed)
	first := st.ModTime()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := os.Stat(d.Config.Docker.Feed); st.ModTime().After(first) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the feed's mtime never moved")
}

// A mirror's commits are its source repo's: the GitHub calendar counts only
// the repos that live on GitHub, so the awkward world's GitHub copy of
// broken-thing adds nothing to it.
func TestMirrorCommitsCountOnce(t *testing.T) {
	w := awkward(fixedNow)
	want := 0
	for _, c := range w.Repo("github", "kite").Commits {
		if c.Author.Login == Login {
			want++
		}
	}
	got := 0
	for _, n := range w.dayCounts("github", false) {
		got += n
	}
	if got != want || want == 0 {
		t.Errorf("GitHub contributions = %d, want kite's %d", got, want)
	}
}

// A data dir with state already is not seeded again.
func TestSeedDataOnce(t *testing.T) {
	w, dir := Dataset(fixedNow), t.TempDir()
	for i := 0; i < 2; i++ {
		if err := w.seedData(dir); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Events(time.Time{}, fixedNow); len(got) != len(w.Events) {
		t.Errorf("events = %d, want %d once", len(got), len(w.Events))
	}
	for key := range w.ProbeHistory() {
		if n := len(st.ProbeHistory(key, fixedNow)); n != 144 {
			t.Errorf("%s: %d points, want a day of 144", key, n)
		}
	}
}
