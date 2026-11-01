package demo

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/derive"
	"github.com/Will-Luck/waymark/internal/model"
)

var update = flag.Bool("update", false, "rewrite the testdata fixtures from the demo world")

// ---- fixtures -------------------------------------------------------------

// fixtureBase stands in for the fake servers' ports in fixtures.
const (
	fixtureSites  = "http://sites.example.net"
	fixtureGitea  = "http://gitea.example.net"
	fixtureGitHub = "http://github.example.net"
)

// get is the fake forge's answer to one GET.
func get(t *testing.T, w *World, target string) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(w).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK && !strings.Contains(target, "missing") {
		t.Fatalf("%s: %d %s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func indent(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		t.Fatal(err)
	}
	return append(buf.Bytes(), '\n')
}

// forgeFixtures are the client and parser fixtures: what the fake forge
// answers to the calls the clients make, the feed, the F-Droid index and the
// projects and ideas files.
func forgeFixtures(t *testing.T) map[string][]byte {
	w := Dataset(fixedNow)
	w.SetBase(fixtureSites)
	since := url.QueryEscape(fixedNow.Add(-84 * day).Format(time.RFC3339))
	plan := date(fixedNow.Add(-9*day)) + "-incident-timeline.md"
	g := "/api/v1/repos/acme/"
	out := map[string][]byte{
		"gitea/user_repos.json":       get(t, w, "/api/v1/user/repos?limit=50&page=1"),
		"gitea/beacon_tags.json":      get(t, w, g+"beacon/tags?limit=50&page=1"),
		"gitea/beacon_releases.json":  get(t, w, g+"beacon/releases?limit=50"),
		"gitea/beacon_issues.json":    get(t, w, g+"beacon/issues?limit=10&state=open&type=issues"),
		"gitea/beacon_pulls.json":     get(t, w, g+"beacon/pulls?limit=10&sort=recentupdate&state=open"),
		"gitea/beacon_commits.json":   get(t, w, g+"beacon/commits?files=false&limit=50&page=1&sha=main&since="+since+"&stat=false&verification=false"),
		"gitea/beacon_runs.json":      get(t, w, g+"beacon/actions/runs?limit=5"),
		"gitea/beacon_CHANGELOG.md":   get(t, w, g+"beacon/raw/CHANGELOG.md"),
		"gitea/beacon_plans_dir.json": get(t, w, g+"beacon/contents/docs/plans"),
		"gitea/beacon_plan.md":        get(t, w, g+"beacon/raw/docs/plans/"+plan),
		"gitea/ledger_pulls.json":     get(t, w, g+"ledger/pulls?limit=10&sort=recentupdate&state=open"),
		"gitea/ledger_runs.json":      get(t, w, g+"ledger/actions/runs?limit=5"),
		"gitea/heatmap.json":          get(t, w, "/api/v1/users/mark/heatmap"),
		"gitea/issue_search.json":     get(t, w, "/api/v1/repos/issues/search?limit=50&q=waymark-idea&state=all&type=issues"),
		"gitea/raw_404.json":          get(t, w, g+"beacon/raw/missing.md"),
		"fdroid/index-v1.json":        get(t, w, "/fdroid/repo/index-v1.json"),
		"docker/docker_ps.jsonl":      w.FeedJSONL(),
		"registry/seed.yaml":          w.ProjectsYAML(),
		"registry/seed_ideas.json":    w.IdeasJSON(),
	}
	for name, b := range out {
		if strings.HasSuffix(name, ".json") {
			out[name] = indent(t, b)
		}
	}
	return out
}

var stampRe = regexp.MustCompile(`"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)"`)

// pin makes a generated site reproducible: the fake servers' random ports
// become fixed names, and the run's live probe checks, whose time and
// latency are the machine's, are set to the run's own clock and 21 ms.
// Every other time comes from the world, so a time after the run's clock is
// one of those checks. The project hashes are worked out again after.
func pin(t *testing.T, s *model.Site, d *Demo) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte(d.SitesURL), []byte(fixtureSites))
	b = bytes.ReplaceAll(b, []byte(d.GiteaURL), []byte(fixtureGitea))
	b = bytes.ReplaceAll(b, []byte(d.GitHubURL), []byte(fixtureGitHub))
	gen := `"` + s.GeneratedAt.UTC().Format(time.RFC3339) + `"`
	b = stampRe.ReplaceAllFunc(b, func(m []byte) []byte {
		if at, err := time.Parse(time.RFC3339Nano, string(m[1:len(m)-1])); err == nil && at.After(s.GeneratedAt) {
			return []byte(gen)
		}
		return m
	})
	var out model.Site
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	for i := range out.Projects {
		p := &out.Projects[i]
		for j := range p.Probes {
			pr := &p.Probes[j]
			if pr.NotProbed != "" || pr.CheckedAt.IsZero() {
				continue
			}
			pr.CheckedAt, pr.LatencyMS = out.GeneratedAt, 0
			if pr.Up {
				pr.LatencyMS = 21
			}
			for k := range pr.History {
				if h := &pr.History[k]; h.At.Equal(out.GeneratedAt) {
					h.LatencyMS = 0
					if h.Up {
						h.LatencyMS = 21
					}
				}
			}
		}
		for j := range p.Signals {
			if sg := &p.Signals[j]; sg.Kind == "probe" && strings.HasPrefix(sg.Text, "up ") {
				sg.Text = "up 21 ms"
			}
		}
		sortActions(p.Actions)
		p.Hash = derive.Hash(*p)
	}
	sortActions(out.NeedsYou)
	b, err = json.MarshalIndent(&out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// sortActions is derive's order with a fixed tie-break: two checks pinned
// to one time must not keep the order the machine happened to finish them in.
func sortActions(as []model.Action) {
	slices.SortStableFunc(as, func(a, b model.Action) int { return strings.Compare(a.Project+"\x00"+a.Text, b.Project+"\x00"+b.Text) })
	derive.SortActions(as)
}

// awkward is the demo world plus two projects built to break things: one
// with markup in its words, an unknown icon and category and no data, and
// one with everything wrong at once (a stopped container, a probe down, a
// contradicted hold, failing and running CI, markup in an issue and the
// changelog, a bad label colour, a stale note, relations in and out and to
// a project that does not exist).
func awkward(now time.Time) *World {
	w := Dataset(now)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	odd := &builder{now: now, r: &Repo{Forge: "gitea", Owner: Owner, Name: "odd-repo", ID: 190, Empty: true, Created: ago(240 * day), Files: map[string]string{}}}
	bt := &builder{now: now, r: &Repo{Forge: "gitea", Owner: Owner, Name: "broken-thing", ID: 191, Description: "A service that is having a bad day.",
		Created: ago(300 * day), Actions: true, Files: map[string]string{}}}
	for i := 0; i < 12; i++ {
		bt.commit(time.Duration(2+i*4)*day, priya, fillerMessages[i%len(fillerMessages)])
	}
	bt.commit(70*day, mark, "Release v0.9.0")
	bt.filler(90*day, 200*day, 10*day, fillerMessages)
	bt.tag("v0.9.0", 70*day, true, false)
	bt.r.Issues = []Issue{
		{Number: 3, Title: "<img src=x onerror=alert(1)>", Author: "tom", Labels: []string{"weird"}, Comments: 4, Created: ago(9 * day), Updated: ago(4 * day)},
		{Number: 2, Title: "Crashes when the disk is full", Author: "lena", Labels: []string{"bug"}, Created: ago(20 * day), Updated: ago(20 * day)},
	}
	bt.r.PRs = []Issue{{Number: 5, Title: "Retry the upload", Author: "priya", Branch: "retry", Created: ago(15 * day), Updated: ago(9 * day)}}
	bt.r.Runs = []CIRun{
		{ID: 9902, Number: 302, Title: "Retry the upload", Branch: "main", Status: "in_progress", Started: ago(10 * time.Minute)},
		{ID: 9901, Number: 301, Title: "Fix the cache key", Branch: "main", Status: "completed", Conclusion: "failure", Started: ago(2 * day), Completed: ago(2*day - 5*time.Minute)},
	}
	var unreleased []string
	for _, s := range []string{"**Fixed** the cache key", "Read `config.go` once", "See the [docs](https://example.com/docs)", "Escape <b>tags</b> in titles",
		"Smaller images", "Faster start-up", "Retry uploads", "Quieter logs", "A health page"} {
		unreleased = append(unreleased, "- "+s)
	}
	bt.r.Files["CHANGELOG.md"] = "# Changelog\n\n## [Unreleased]\n\n" + strings.Join(unreleased, "\n") + "\n\n## [0.9.0] - " + date(ago(70*day)) + "\n\n- First cut\n"
	var steps []string
	for i := 1; i <= 12; i++ {
		box := " "
		if i <= 7 {
			box = "x"
		}
		steps = append(steps, "- ["+box+"] Step "+string(rune('A'-1+i)))
	}
	bt.r.Files["docs/plans/"+date(ago(5*day))+"-recovery.md"] = "# Recovery\n\n" + strings.Join(steps, "\n") + "\n"
	broken := bt.done()
	ghBroken := &Repo{Forge: "github", Owner: Owner, Name: "broken-thing", ID: 9191, MirrorOf: "gitea:" + broken.FullName(), Description: broken.Description, Stars: 3, Created: ago(290 * day),
		Commits: broken.Commits, Files: map[string]string{},
		Issues: []Issue{{Number: 8, Title: "Does it run on arm64?", Author: "tom", Comments: 1, Created: ago(6 * day), Updated: ago(6 * day)}}}
	w.Labels["weird"] = "zzzzzz"
	w.Repos = append(w.Repos, odd.done(), broken, ghBroken)
	w.Probes["broken-thing"] = 502
	w.Containers = append(w.Containers, Container{ID: "b0b0b0b0b0b0", Name: "broken-thing", Image: "registry.example.com/acme/broken-thing:v0.9.0",
		State: "exited", Status: "Exited (1) 2 hours ago", Created: ago(10 * day)})
	w.Notes = append(w.Notes, Note{File: "broken-thing.md", Updated: ago(30 * day), Body: "# Broken thing\n\nIt was fine last month.\n"})
	cfg := w.Repo("gitea", ConfigRepo)
	cfg.Files["projects.yaml"] += `  - slug: odd-repo
    name: Odd repo <b>bold</b>
    category: no-such-category
    icon: no-such-icon
    summary: <script>alert('x')</script> & "quotes"
    repos:
      - gitea: odd-repo
    relations:
      - {to: broken-thing, kind: monitors}
  - slug: broken-thing
    name: Broken thing
    category: services
    icon: container
    summary: A service that is having a bad day.
    repos:
      - gitea: broken-thing
      - github: broken-thing
    containers: [broken-thing]
    urls:
      - {label: Web UI, url: "https://broken.example.com", probe: "{base}/probe/broken-thing"}
    hold: {kind: parked, since: "` + date(ago(50*day)) + `", reason: Waiting for a new disk}
    relations:
      - {to: beacon, kind: depends-on}
      - {to: gone-project, kind: depends-on}
`
	return w
}

// siteFixtures are the three Site fixtures the web tests read, each the
// real pipeline's output: site.json one cycle of the demo; site.live.json
// two cycles with the world moving on between them, so its events are the
// pipeline's own; site-awkward.json the awkward world with Gitea's repo list
// failing on its second cycle.
func siteFixtures(t *testing.T) map[string][]byte {
	out := map[string][]byte{}

	a, d := demoApp(t, Dataset(fixedNow))
	out["site.json"] = pin(t, runOnce(t, a), d)

	w := Dataset(fixedNow)
	a, d = demoApp(t, w)
	runOnce(t, a)
	later := fixedNow.Add(10 * time.Minute)
	w.Edit(func() {
		w.Container("beacon").Image = "registry.example.com/acme/beacon:v2.4.0"
		w.Container("beacon").Status = "Up 2 minutes"
		w.Probes["atlas"] = 200
		k := w.Repo("github", "kite")
		k.Runs[0].Status, k.Runs[0].Conclusion, k.Runs[0].Completed = "completed", "success", later.Add(-time.Minute)
	})
	if err := w.WriteFeed(d.Config.Docker.Feed, later.Add(-20*time.Second)); err != nil {
		t.Fatal(err)
	}
	a.SetClock(func() time.Time { return later })
	out["site.live.json"] = pin(t, runOnce(t, a), d)

	aw := awkward(fixedNow)
	a, d = demoApp(t, aw)
	runOnce(t, a)
	aw.Edit(func() { aw.FailList = true })
	out["site-awkward.json"] = pin(t, runOnce(t, a), d)
	return out
}

// TestRegenerateFixtures keeps testdata the demo world's: with -update it
// writes every generated fixture, and without it fails when one differs
// from what the world and the pipeline make now.
func TestRegenerateFixtures(t *testing.T) {
	all := forgeFixtures(t)
	for name, b := range siteFixtures(t) {
		all[name] = b
	}
	root := filepath.Join("..", "..", "testdata")
	for name, want := range all {
		p := filepath.Join(root, name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("testdata/%s is not what the demo world makes now (%v); run: go test ./internal/demo -run TestRegenerateFixtures -update", name, err)
		}
	}
}
