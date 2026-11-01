package app

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/model"
)

// eventIDs are the ids of a site's events.
func eventIDs(evs []model.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.ID)
	}
	return out
}

// A restart while the first forge is down: until every forge has listed
// once, a pair's project is built from its mirror, which is not a change.
// No event comes of it, then or when the primary's forge answers; a real
// change after that is an event again.
func TestNoEventsUntilEveryForgeListed(t *testing.T) {
	h := newAcmeHost(t)
	dir := t.TempDir()
	tool := &acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.0.0"}, CI: "success", Updated: fixedNow.Add(-day)}
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, tool)
	gh := h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v0.9.0"},
		CI: "failure", Issues: []acmeIssue{{N: 4, Title: "Old bug", Updated: fixedNow.Add(-day)}}, Updated: fixedNow.Add(-2 * day)})
	cfg := func(c *config.Config) { c.DataDir = dir }
	runOnce(t, h.app(cfg, g, gh))

	h.edit(func() { h.down["gitea"] = true })
	a := h.app(cfg, g, gh)
	if s := runOnce(t, a); len(s.Events) != 0 {
		t.Errorf("restart with the first forge down: events %v", eventIDs(s.Events))
	}
	h.edit(func() { h.down["gitea"] = false })
	if s := runOnce(t, a); len(s.Events) != 0 {
		t.Errorf("the first forge answering: events %v", eventIDs(s.Events))
	}
	h.edit(func() { tool.Tags, tool.Updated = []string{"v1.1.0", "v1.0.0"}, fixedNow.Add(-time.Hour) })
	if s := runOnce(t, a); len(s.Events) == 0 {
		t.Error("a new tag once every forge has listed: no event")
	}
}

// A forge that never lists (a wrong token, or offline) holds events back
// for at most EventsGrace after start (ruling 50). Before the grace ends
// nothing is diffed; after it, the working forge's events flow, and the
// silent forge's projects keep their stored state: no events, not
// forgotten, nothing when it answers at last. The end of the grace is
// logged once, naming the forge.
func TestEventsGraceForAForgeThatNeverLists(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newAcmeHost(t)
	dir := t.TempDir()
	app := &acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day)}
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "lib", Tags: []string{"v3.0.0"}, CI: "success", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "t2", []string{"acme"}, app)
	cfg := func(c *config.Config) { c.DataDir = dir }
	runOnce(t, h.app(cfg, g, gh))

	// Restarted at fixedNow with Gitea not answering; then GitHub's repo
	// gets a new tag.
	h.edit(func() { h.down["gitea"] = true })
	a := h.app(cfg, g, gh)
	runOnce(t, a)
	h.edit(func() { app.Tags, app.Updated = []string{"v1.1.0", "v1.0.0"}, fixedNow.Add(-time.Hour) })
	at := func(after time.Duration) []string {
		a.now = func() time.Time { return fixedNow.Add(after) }
		return eventIDs(runOnce(t, a).Events)
	}
	graceLog := func() int { return strings.Count(logged.String(), "after start, forge gitea has not listed its repos") }
	if ids := at(EventsGrace - time.Minute); len(ids) != 0 || graceLog() != 0 {
		t.Errorf("before the grace ends: events %v, grace logged %d times", ids, graceLog())
	}
	ids := at(EventsGrace + time.Minute)
	if !slices.Contains(ids, "app:release:v1.1.0") {
		t.Errorf("after the grace: events %v, want the working forge's new tag", ids)
	}
	for _, id := range ids {
		if strings.HasPrefix(id, "lib:") {
			t.Errorf("after the grace: the silent forge's project raised %s", id)
		}
	}
	if _, ok := a.store.Fingerprints()["lib"]; !ok {
		t.Error("after the grace: the silent forge's project was forgotten")
	}
	at(EventsGrace + 2*time.Minute)
	if n := graceLog(); n != 1 {
		t.Errorf("the end of the grace was logged %d times, want once:\n%s", n, logged.String())
	}
	h.edit(func() { h.down["gitea"] = false })
	for _, id := range at(EventsGrace + 3*time.Minute) {
		if strings.HasPrefix(id, "lib:") {
			t.Errorf("the silent forge answering: its project raised %s", id)
		}
	}
}

// A forced refresh (RunOnce, /api/refresh) lists every forge again. On a
// GitHub forge it reads a repo's detail only when the list shows a change or
// its last read failed; a Gitea forge reads every primary's detail again,
// as 0.1.0 did.
func TestForcedRefreshReadsChangedGitHubDetailOnly(t *testing.T) {
	h := newAcmeHost(t)
	lib := &acmeRepo{Owner: "acme", Name: "lib", CI: "success", Updated: fixedNow.Add(-day)}
	app := &acmeRepo{Owner: "acme", Name: "app", CI: "success", Updated: fixedNow.Add(-day)}
	a := h.app(nil, h.forge("gitea", "gitea", "t1", []string{"acme"}, lib), h.forge("github", "github", "t2", []string{"acme"}, app))
	reads := func(id, repo string) int { return len(h.requests(id, "/repos/acme/"+repo+"/actions/runs")) }
	runOnce(t, a)
	if reads("gitea", "lib") != 1 || reads("github", "app") != 1 {
		t.Fatalf("first cycle: detail reads gitea %d github %d, want one each", reads("gitea", "lib"), reads("github", "app"))
	}
	runOnce(t, a)
	if n := reads("github", "app"); n != 1 {
		t.Errorf("forced refresh, nothing changed: the GitHub repo's detail was read %d times, want once", n)
	}
	if n := len(h.requests("github", "POST /graphql")); n != 1 {
		t.Errorf("forced refresh, nothing changed: %d tag queries, want 1", n)
	}
	if n := reads("gitea", "lib"); n != 2 {
		t.Errorf("forced refresh: the Gitea repo's detail was read %d times, want again", n)
	}
	h.edit(func() { app.Updated = fixedNow.Add(-time.Hour) })
	runOnce(t, a)
	if n := reads("github", "app"); n != 2 {
		t.Errorf("forced refresh after a push: the GitHub repo's detail was read %d times, want 2", n)
	}
}

// A GitHub owner that does not exist (a typo: /orgs/ and /users/ both answer
// 404) is skipped and logged once, as on Gitea; the forge's other owners are
// still listed and its source stays OK.
func TestMissingGitHubOwnerSkipped(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newAcmeHost(t)
	h.edit(func() { h.missing["acmee"] = true })
	a := h.app(nil, h.forge("github", "github", "t", []string{"acme", "acmee"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)}))
	var s *model.Site
	for range 2 {
		s = runOnce(t, a)
	}
	if s.ProjectBySlug("app") == nil {
		t.Errorf("projects = %v, want the other owner's repo", slugsOf(s))
	}
	if st, _ := sourceStatus(s, "github"); !st.OK {
		t.Errorf("github status = %+v, want OK", st)
	}
	if got := h.requests("github", "/users/acmee/repos"); len(got) == 0 {
		t.Error("the missing owner was never looked up, so this proves nothing")
	}
	if n := strings.Count(logged.String(), "forge github: owner acmee"); n != 1 {
		t.Errorf("the missing owner was logged %d times, want once:\n%s", n, logged.String())
	}
}

// A GitHub forge with no token lists nothing. When that hides what the
// config asks for (owners are set) or leaves it nothing to read (no repo of
// the projects file is on it), its source row stays OK but says why, and the
// log says so once in the same words. With no owners and named repos (read
// like mirrors), it says nothing and logs the usual note.
func TestTokenlessForgeSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		owners   []string
		projects string // "" for no projects file
		note     string
		logged   string
	}{
		{"owners, nothing named", []string{"acme"}, "", "listing is off without a token, and no repo the projects file names is on this forge: nothing is read",
			"forge github: no token, so nothing is listed, and no repo the projects file names is on it: nothing is read from it"},
		{"owners, a repo named", []string{"acme"}, "projects:\n  - slug: cli\n    name: CLI\n    repos:\n      - github: acme/cli\n",
			"listing is off without a token: owners are not listed, only the repos the projects file names",
			"forge github: no token, so owners acme are not listed; only the repos the projects file names are read"},
		{"no owners, a repo named", nil, "projects:\n  - slug: cli\n    name: CLI\n    repos:\n      - github: acme/cli\n", "",
			"forge github: GitHub token needed for full detail; only public repo views are read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(os.Stderr) })
			h := newAcmeHost(t)
			g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
			gh := h.forge("github", "github", "", tc.owners, &acmeRepo{Owner: "acme", Name: "cli", Updated: fixedNow.Add(-day)})
			a := h.app(func(c *config.Config) {
				if tc.projects != "" {
					c.Projects = config.FileRef{File: writeProjects(t, tc.projects)}
				}
			}, g, gh)
			var s *model.Site
			for range 2 {
				s = runOnce(t, a)
			}
			if st, _ := sourceStatus(s, "github"); !st.OK || st.Note != tc.note {
				t.Errorf("github status = %+v, want OK with note %q", st, tc.note)
			}
			if n := strings.Count(logged.String(), tc.logged); n != 1 {
				t.Errorf("%q logged %d times, want once:\n%s", tc.logged, n, logged.String())
			}
			if tc.note != "" && strings.Contains(logged.String(), "only public repo views are read") {
				t.Errorf("the log says public views are read, though none are:\n%s", logged.String())
			}
		})
	}
}

// A forge of type forgejo is read with the Gitea API but named Forgejo on
// the page: its source row, its projects' forge and links.
func TestForgejoNamedForgejo(t *testing.T) {
	h := newAcmeHost(t)
	fj := h.forge("fj", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	fj.Type = "forgejo"
	s := runOnce(t, h.app(nil, fj))
	if got := s.Meta.Forges["fj"]; got != "Forgejo" {
		t.Errorf("meta forge name = %q, want Forgejo", got)
	}
	p := s.ProjectBySlug("tool")
	if p == nil || p.Forge != "Forgejo" || p.Repo == nil {
		t.Fatalf("tool = %+v", p)
	}
	for _, l := range p.Links {
		if l.Kind == "repo" && l.Label != "Forgejo" {
			t.Errorf("repo link label = %q", l.Label)
		}
	}
}

// writeIdeasFile writes an empty ideas file, which turns on the filed-idea
// search, and returns its path.
func writeIdeasFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ideas.json")
	if err := os.WriteFile(p, []byte(`{"ideas":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A GitHub forge that answers rate-limited is asked nothing more until the
// reset it names, whichever read met the limit: the list, a primary's
// detail, a mirror or the filed-idea search. Its source says so meanwhile,
// forced refreshes included; after the reset it is read again.
func TestRateLimitedForgeWaitsForReset(t *testing.T) {
	reset := fixedNow.Add(30 * time.Minute)
	for _, hit := range []string{"GET /user/repos", "POST /graphql", "GET /repos/acme/tool/releases/latest", "GET /search/issues"} {
		t.Run(hit, func(t *testing.T) {
			h := newAcmeHost(t)
			g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
			gh := h.forge("github", "github", "t2", []string{"acme"},
				&acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day)},
				&acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v2.0.0"}, Updated: fixedNow.Add(-day)})
			ideas := writeIdeasFile(t)
			a := h.app(func(c *config.Config) { c.Ideas.File = config.FileRef{File: ideas} }, g, gh)
			h.edit(func() { h.limit["github"], h.limitReset = hit, reset })
			runOnce(t, a)
			h.edit(func() {
				if h.limit["github"] != "" {
					t.Fatalf("no %s was made, so the limit was never met", hit)
				}
			})
			before := len(h.requests("github", ""))
			for _, after := range []time.Duration{0, 10 * time.Minute, 29 * time.Minute} {
				a.now = func() time.Time { return fixedNow.Add(after) }
				s := runOnce(t, a)
				if got := h.requests("github", ""); len(got) != before {
					t.Errorf("%v on: asked before the reset: %v", after, got[before:])
					before = len(got)
				}
				if st, _ := sourceStatus(s, "github"); st.OK || !strings.Contains(st.Error, "rate limit reached") || !strings.Contains(st.Error, "resets 22:30 UTC") {
					t.Errorf("%v on: github status = %+v, want the limit and its reset", after, st)
				}
			}
			a.now = func() time.Time { return reset.Add(time.Minute) }
			s := runOnce(t, a)
			if len(h.requests("github", "")) == before {
				t.Error("not read again after the reset")
			}
			if st, _ := sourceStatus(s, "github"); !st.OK {
				t.Errorf("after the reset: github status = %+v", st)
			}
		})
	}
}
