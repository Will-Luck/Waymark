package app

import (
	"bytes"
	"context"
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
				if st, _ := sourceStatus(s, "github"); st.OK || !strings.Contains(st.Error, "rate limit reached") || !strings.Contains(st.Error, "trying again after 22:30 UTC") {
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

// limitAt is a spent rate limit that resets at the time it holds, as a forge
// client reports one (forge.RateLimited).
type limitAt time.Time

func (limitAt) Error() string               { return "github: rate limit reached" }
func (l limitAt) RateLimitReset() time.Time { return time.Time(l) }

// A forge's status names the time the app will ask it again, which is the
// time it waits for: never the forge's own reset when that is more than
// RateLimitMaxWait away, and never a later, shorter limit's reset while the
// longer wait stands.
func TestRateLimitStatusNamesTheRetry(t *testing.T) {
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v2.0.0"}, Updated: fixedNow.Add(-day)})
	a := h.app(nil, gh)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /user/repos", fixedNow.Add(3*time.Hour) })
	s := runOnce(t, a)
	if st, _ := sourceStatus(s, "github"); st.OK || !strings.Contains(st.Error, "rate limit reached") || !strings.Contains(st.Error, "trying again after 23:00 UTC") ||
		strings.Contains(st.Error, "01:00") {
		t.Errorf("a reset 3 h away: github status %q, want the retry an hour on (23:00 UTC) only", st.Error)
	}
	before := len(h.requests("github", ""))
	a.now = func() time.Time { return fixedNow.Add(59 * time.Minute) }
	s = runOnce(t, a)
	if got := h.requests("github", ""); len(got) != before {
		t.Errorf("asked before the time the status names: %v", got[before:])
	}
	if st, _ := sourceStatus(s, "github"); !strings.Contains(st.Error, "trying again after 23:00 UTC") {
		t.Errorf("still waiting: github status %q", st.Error)
	}
	a.now = func() time.Time { return fixedNow.Add(61 * time.Minute) }
	s = runOnce(t, a)
	if len(h.requests("github", "")) == before {
		t.Error("not asked again at the time the status named")
	}
	if st, _ := sourceStatus(s, "github"); !st.OK {
		t.Errorf("after the retry: github status %+v", st)
	}

	// Two limits in one pass (reads run in parallel): the shorter one, met
	// second, neither shortens the wait nor changes the time shown.
	fs := a.byID["github"]
	a.now = func() time.Time { return fixedNow.Add(2 * time.Hour) }
	a.mu.Lock()
	a.rateLimitLocked(fs, limitAt(fixedNow.Add(2*time.Hour+40*time.Minute)))
	a.rateLimitLocked(fs, limitAt(fixedNow.Add(2*time.Hour+10*time.Minute)))
	until, st := fs.limitUntil, a.status["github"]
	a.mu.Unlock()
	if want := fixedNow.Add(2*time.Hour + 40*time.Minute); !until.Equal(want) {
		t.Errorf("waits until %v, want %v", until, want)
	}
	if !strings.Contains(st.Error, "trying again after 00:40 UTC") {
		t.Errorf("after a longer then a shorter limit: github status %q, want the longer wait (00:40 UTC)", st.Error)
	}
}

// Every row a rate limit stops names the time after which the forge is
// asked again, not only the forge's own: the heatmap and the filed-idea
// search too, whether they met the limit or wait for it. The time is the
// wait's minute (22:30 for 22:30:45), never later than the retry, so a row
// set during the wait still holds once the wait is over and the read that
// met the limit waits for its next tick.
func TestLimitedRowsNameTheRetry(t *testing.T) {
	reset := fixedNow.Add(30*time.Minute + 45*time.Second)
	const retry = "rate limit reached; trying again after 22:30 UTC"
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)})
	gh.Heatmap = true
	ideas := writeIdeasFile(t)
	a := h.app(func(c *config.Config) { c.Ideas.File = config.FileRef{File: ideas} }, gh)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /search/issues", reset })
	s := runOnce(t, a)
	if st, _ := sourceStatus(s, "ideas"); st.OK || !strings.Contains(st.Error, retry) {
		t.Errorf("met the limit: ideas status %q", st.Error)
	}
	a.now = func() time.Time { return fixedNow.Add(10 * time.Minute) }
	s = runOnce(t, a)
	for _, name := range []string{"github", "heatmap", "ideas"} {
		if st, _ := sourceStatus(s, name); st.OK || !strings.Contains(st.Error, retry) {
			t.Errorf("waiting: %s status %q", name, st.Error)
		}
	}

	// The list met it. The wait is over, the list is not read again yet,
	// and something else sets the forge's row (a mirror check, say).
	h = newAcmeHost(t)
	gh = h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)})
	a = h.app(nil, gh)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /user/repos", reset })
	runOnce(t, a)
	a.now = func() time.Time { return reset.Add(10 * time.Second) }
	a.mu.Lock()
	a.forgeStatusLocked(a.byID["github"])
	st := a.status["github"]
	a.mu.Unlock()
	if st.OK || !strings.Contains(st.Error, retry) {
		t.Errorf("wait over, list not read again: github status %q", st.Error)
	}
}

// Mirrors are read every MirrorEvery. A pass that a rate limit cut short is
// read again at the first check (every MirrorRetryEvery) after the limit
// ends, so the forge does not show the limit for up to MirrorEvery more;
// only that forge's mirrors are read, not those on a forge with no limit
// (Gitea mirrors cli here). With no limit the check asks nothing.
func TestMirrorsReadSoonAfterRateLimit(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "cli", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "t2", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "cli", Updated: fixedNow.Add(-day)})
	file := filepath.Join(t.TempDir(), "projects.yaml")
	body := "projects:\n  - slug: cli\n    name: CLI\n    repos:\n      - github: acme/cli\n      - gitea: acme/cli\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g, gh)
	ctx := context.Background()
	runOnce(t, a)
	if len(h.requests("gitea", "/repos/acme/cli")) == 0 {
		t.Fatal("gitea's mirror of cli was never read")
	}
	asked := func() (gitea, github int) { return len(h.requests("gitea", "")), len(h.requests("github", "")) }
	g0, gh0 := asked()
	a.now = func() time.Time { return fixedNow.Add(MirrorRetryEvery) }
	a.retryMirrors(ctx)
	if g1, gh1 := asked(); g1 != g0 || gh1 != gh0 {
		t.Fatalf("no limit: the check asked gitea %d and github %d times", g1-g0, gh1-gh0)
	}

	reset := fixedNow.Add(20 * time.Minute)
	h.edit(func() { h.limit["github"], h.limitReset = "GET /repos/acme/tool/releases/latest", reset })
	a.now = func() time.Time { return fixedNow.Add(2 * time.Minute) }
	a.collectMirrors(ctx)
	h.edit(func() {
		if h.limit["github"] != "" {
			t.Fatal("the mirror was not read, so the limit was never met")
		}
	})
	a.mu.Lock()
	st := a.status["github"]
	a.mu.Unlock()
	if st.OK || !strings.Contains(st.Error, "trying again after 22:20 UTC") {
		t.Fatalf("the limit is not shown: github status %+v", st)
	}

	g0, gh0 = asked()
	tool0 := len(h.requests("github", "/repos/acme/tool"))
	a.now = func() time.Time { return reset.Add(-time.Second) }
	a.retryMirrors(ctx)
	if g1, gh1 := asked(); g1 != g0 || gh1 != gh0 {
		t.Errorf("before the reset: the check asked gitea %d and github %d times", g1-g0, gh1-gh0)
	}
	// The wait is over and the mirror not read again yet: its row still
	// names the time.
	a.now = func() time.Time { return reset }
	a.mu.Lock()
	a.forgeStatusLocked(a.byID["github"])
	st = a.status["github"]
	a.mu.Unlock()
	if st.OK || !strings.Contains(st.Error, "trying again after 22:20 UTC") {
		t.Errorf("wait over, mirror not read again: github status %+v", st)
	}
	a.now = func() time.Time { return reset.Add(MirrorRetryEvery / 2) }
	a.retryMirrors(ctx)
	g1, gh1 := asked()
	if g1 != g0 {
		t.Errorf("after the reset: the check read gitea's mirror too (%d requests)", g1-g0)
	}
	if len(h.requests("github", "/repos/acme/tool")) == tool0 || gh1 == gh0 {
		t.Fatal("after the reset: the mirror was not read again before the next MirrorEvery")
	}
	a.mu.Lock()
	st, m := a.status["github"], a.mirrors[repoKey("github", "acme/tool")]
	a.mu.Unlock()
	if !st.OK {
		t.Errorf("after the mirror read: github status %+v", st)
	}
	if m == nil || !m.FetchedAt.Equal(reset.Add(MirrorRetryEvery/2)) {
		t.Errorf("mirror view %+v, want one read at the check", m)
	}
	// Read once: the next check asks nothing.
	a.now = func() time.Time { return reset.Add(2 * MirrorRetryEvery) }
	a.retryMirrors(ctx)
	if g2, gh2 := asked(); g2 != g1 || gh2 != gh1 {
		t.Errorf("after the retry: the next check asked gitea %d and github %d times", g2-g1, gh2-gh1)
	}
}
