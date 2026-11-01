package derive

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/docker"
	"github.com/Will-Luck/waymark/internal/sources/fdroid"
	"github.com/Will-Luck/waymark/internal/sources/notes"
	"github.com/Will-Luck/waymark/internal/sources/probe"
)

var testNow = time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)

func load(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func env(t *testing.T) Env {
	return Env{Now: testNow, Loc: zone(t), HostLabel: "web-01", NotesLabel: "Field notes",
		ForgeKinds: map[string]string{"gitea": "gitea", "github": "github"}}
}

// reg is the seed registry pinned in testdata (the demo world's projects file).
func reg(t *testing.T) *registry.Registry {
	t.Helper()
	b, err := os.ReadFile("../../testdata/registry/seed.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := registry.Parse(b, []string{"gitea", "github"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// beaconInput is Beacon as the testdata fixtures (the demo world's forge
// answers, feed and memory notes) describe it.
func beaconInput(t *testing.T) Input {
	t.Helper()
	g := "../../testdata/gitea/"
	var repos []forge.Repo
	load(t, g+"user_repos.json", &repos)
	var repo forge.Repo
	for _, r := range repos {
		if r.Name == "beacon" {
			repo = r
		}
	}
	d := &forge.Detail{Repo: repo, Since: testNow.Add(-84 * 24 * time.Hour), FetchedAt: testNow}
	load(t, g+"beacon_tags.json", &d.Tags)
	load(t, g+"beacon_releases.json", &d.Releases)
	load(t, g+"beacon_issues.json", &d.Issues)
	load(t, g+"beacon_pulls.json", &d.PRs)
	load(t, g+"beacon_commits.json", &d.Commits)
	var runs struct {
		WorkflowRuns []forge.Run `json:"workflow_runs"`
	}
	load(t, g+"beacon_runs.json", &runs)
	d.Runs = runs.WorkflowRuns
	d.LastCommit = &d.Commits[0]
	feed, err := docker.ReadFeed("../../testdata/docker/docker_ps.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	p := *reg(t).Project("beacon")
	ns, err := notes.NewClaudeMemory("../../testdata/notes", []string{"Mark"}).Load([]registry.Project{p})
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Project:     p,
		Repos:       []RepoData{{Repo: repo, Detail: d, Files: &forge.Files{Changelog: &forge.Changelog{LastVersion: "2.4.0"}, ChangelogURL: repo.HTMLURL + "/src/branch/main/CHANGELOG.md", FetchedAt: testNow}}},
		GiteaKnown:  true,
		DockerKnown: true,
		FeedAt:      testNow.Add(-30 * time.Second),
		Containers:  []ContainerRef{{Name: "beacon", Declared: true, C: feed.Get("beacon")}},
		Probes: []ProbeData{{Label: "Live", URL: "https://status.example.com", ProbeURL: "http://sites.example.net/probe/beacon",
			Last: &probe.Result{Up: true, Code: 200, LatencyMS: 23, At: testNow.Add(-time.Minute)}}},
		Notes:   ns["beacon"],
		NotesAt: testNow,
		Me:      map[string]string{"gitea": "mark"},
	}
}

func signal(p model.Project, kind string) *model.Signal {
	for i := range p.Signals {
		if p.Signals[i].Kind == kind {
			return &p.Signals[i]
		}
	}
	return nil
}

func TestProjectBeaconFromFixtures(t *testing.T) {
	in := beaconInput(t)
	if len(in.Notes) != 1 || len(in.Notes[0].Clauses) != 2 {
		t.Fatalf("beacon notes = %+v, want one note with two waiting clauses", in.Notes)
	}
	b := BuildProject(env(t), in)
	p := b.Project
	if p.Tier != model.TierActive || p.TierReason != "Commits in the last 3 weeks" {
		t.Errorf("tier = %s %q", p.Tier, p.TierReason)
	}
	if p.Deploy == nil || p.Deploy.Source != "docker" || p.Deploy.Where != "web-01" || p.Deploy.Version != "v2.3.1" ||
		p.Deploy.State != "running" || p.Deploy.Latest != "v2.4.0" || !p.Deploy.Lagging || p.Deploy.Status != "Up 4 days" {
		t.Errorf("deploy = %+v", p.Deploy)
	}
	if p.Headline != "v2.4.0 is ready; production runs v2.3.1" || p.HeadlineLevel != model.LevelWarn {
		t.Errorf("headline = %q %s", p.Headline, p.HeadlineLevel)
	}
	r := p.Repo
	if r == nil || r.LatestTag != "v2.4.0" || r.CommitsSinceTag != 3 || r.OpenIssues != 4 || r.OpenPRs != 2 || r.HTMLURL != "https://git.example.com/acme/beacon" || r.Private {
		t.Errorf("repo = %+v", r)
	}
	if len(p.Releases) != 6 || p.Releases[0].Tag != "v2.4.0" || p.Releases[0].URL != "https://git.example.com/acme/beacon/releases/tag/v2.4.0" ||
		p.Releases[0].Title != "v2.4.0: checks by region" {
		t.Errorf("releases = %+v", p.Releases)
	}
	if len(p.Commits) != 5 || p.Commits[0].Short != "c433254" || p.Commits[1].Author != "Priya" {
		t.Errorf("commits = %+v", p.Commits)
	}
	// The failed run is on a branch: main is passing.
	if len(p.CI) != 3 || p.CI[0].Workflow != "ci.yml" || p.CI[0].Conclusion != "success" || p.CI[2].Branch != "dark-theme" || p.CI[2].Conclusion != "failure" {
		t.Errorf("ci = %+v", p.CI)
	}
	if len(p.Issues) != 4 || p.Issues[0].Source != "gitea" || p.Issues[0].Labels[0].Color != "e11d48" || len(p.PRs) != 2 {
		t.Errorf("issues = %+v prs = %+v", p.Issues, p.PRs)
	}
	if len(p.Activity) != 12 || p.Activity[11] != 6 {
		t.Errorf("activity = %v (six commits fall in the week from Mon 26 Oct)", p.Activity)
	}
	if !p.LastActivity.Equal(time.Date(2026, 10, 31, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("last activity = %v", p.LastActivity)
	}
	if s := signal(p, "deploy"); s == nil || s.Text != "v2.3.1 running" || s.Level != model.LevelGood {
		t.Errorf("deploy signal = %+v", s)
	}
	if s := signal(p, "lagging"); s == nil || s.Text != "v2.4.0 ready" || s.Level != model.LevelWarn {
		t.Errorf("lagging signal = %+v", s)
	}
	if s := signal(p, "ci"); s == nil || s.Text != "CI passing" || s.Level != model.LevelGood {
		t.Errorf("ci signal = %+v", s)
	}
	if s := signal(p, "issues"); s == nil || s.Text != "4 issues" {
		t.Errorf("issues signal = %+v", s)
	}
	if s := signal(p, "probe"); s == nil || s.Text != "up 23 ms" || s.Level != model.LevelGood {
		t.Errorf("probe signal = %+v", s)
	}
	if s := signal(p, "since-tag"); s == nil || s.Text != "3 commits since v2.4.0" {
		t.Errorf("since-tag signal = %+v (three commits after the tag)", s)
	}
	var deploy, review, decide []model.Action
	for _, a := range p.Actions {
		switch a.Verb {
		case "Deploy":
			deploy = append(deploy, a)
		case "Review":
			review = append(review, a)
		case "Decide":
			decide = append(decide, a)
		}
	}
	if len(deploy) != 1 || deploy[0].Text != "v2.4.0 to production, running v2.3.1" || deploy[0].Level != model.LevelWarn {
		t.Errorf("deploy actions = %+v", deploy)
	}
	// The draft PR waits on no one; the other has had no update for six days.
	if len(review) != 1 || review[0].Text != "#22 Dark theme for status pages (no update for 6 days)" {
		t.Errorf("review actions = %+v", review)
	}
	// Both of the note's waiting clauses are about this deploy, which the
	// Deploy action already names, so neither becomes a Decide action.
	if len(decide) != 0 {
		t.Errorf("decide actions = %+v, want none beside the Deploy action", decide)
	}
	if len(p.Notes) != 1 || p.Notes[0].Stale || !strings.HasPrefix(p.Notes[0].IndexLine, "**v2.3.1 prod;") {
		t.Errorf("notes = %+v", p.Notes)
	}
	if p.Actions[0].Verb != "Deploy" {
		t.Errorf("warn sorts before info, newest first: %+v", p.Actions)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(p.Hash) {
		t.Errorf("hash = %q", p.Hash)
	}
	if fp := b.FP; fp.LatestTag == nil || *fp.LatestTag != "v2.4.0" || fp.DeployVersion == nil || *fp.DeployVersion != "v2.3.1" ||
		fp.ContainerState == nil || *fp.ContainerState != "running" || fp.OpenIssues == nil || *fp.OpenIssues != 4 || len(fp.CI) != 3 {
		t.Errorf("fingerprint = %+v", fp)
	}
	if len(p.Links) < 2 || p.Links[0].Kind != "repo" || p.Links[1].Kind != "live" {
		t.Errorf("links = %+v", p.Links)
	}
}

// synthetic builds a small project around one repo for rule tables.
type synth struct {
	lastCommit   time.Duration // age of the newest commit
	commits      int           // commits in the window, spread from lastCommit back an hour each
	tag          string
	tagAge       time.Duration
	container    string // image; "" means no container
	state        string
	status       string // docker's status; "" picks one that suits the state
	declared     bool
	dockerKnown  bool
	probeUp      *bool
	probeCode    int
	ciConclusion string
	ciBranch     string
	hold         *registry.Hold
	retired      bool
	prUpdated    time.Duration
	prDraft      bool
	noteAge      time.Duration
	fdroidVer    string
}

func (s synth) input() Input {
	repo := forge.Repo{Name: "app", FullName: "acme/app", HTMLURL: "https://git.example.com/acme/app", DefaultBranch: "main", HasActions: true}
	d := &forge.Detail{Repo: repo, Since: testNow.Add(-84 * 24 * time.Hour), FetchedAt: testNow}
	for i := 0; i < s.commits; i++ {
		var c forge.Commit
		c.SHA = fmt.Sprintf("%040x", i+1)
		c.Commit.Message = "change"
		c.Commit.Committer.Date = forge.Time{Time: testNow.Add(-s.lastCommit - time.Duration(i)*time.Hour)}
		d.Commits = append(d.Commits, c)
	}
	if len(d.Commits) > 0 {
		d.LastCommit = &d.Commits[0]
	} else if s.lastCommit > 0 {
		var c forge.Commit
		c.Commit.Committer.Date = forge.Time{Time: testNow.Add(-s.lastCommit)}
		d.LastCommit = &c
	}
	if s.tag != "" {
		d.Tags = []forge.Tag{tag(s.tag, testNow.Add(-s.tagAge).Format(time.RFC3339))}
	}
	if s.ciConclusion != "" {
		br := s.ciBranch
		if br == "" {
			br = "main"
		}
		d.Runs = []forge.Run{{ID: 7, DisplayTitle: "Fix the thing", HeadBranch: br, Status: "completed", Conclusion: s.ciConclusion, Path: "ci.yml@refs/heads/" + br}}
	}
	if s.prUpdated > 0 {
		d.PRs = []forge.Issue{{Number: 42, Title: "Cap the rebuild job", HTMLURL: "https://git.example.com/acme/app/pulls/42", UpdatedAt: forge.Time{Time: testNow.Add(-s.prUpdated)}, Draft: s.prDraft}}
		repo.OpenPRs = 1
		d.Repo = repo
	}
	p := registry.Project{Slug: "app", Name: "App", Category: "media", Icon: "tv", Repos: []registry.RepoRef{{Forge: "gitea", FullName: "acme/app"}}, Hold: s.hold, Retired: s.retired}
	in := Input{Project: p, Repos: []RepoData{{Repo: repo, Detail: d}}, GiteaKnown: true, DockerKnown: s.dockerKnown, FeedAt: testNow}
	if s.container != "" || s.declared {
		ref := ContainerRef{Name: "app", Declared: s.declared}
		if s.container != "" {
			status := s.status
			if status == "" {
				status = map[string]string{"running": "Up 3 days", "exited": "Exited (1) 2 days ago", "restarting": "Restarting (1) 5 seconds ago"}[s.state]
			}
			ref.C = &docker.Container{Name: "app", Image: s.container, State: s.state, Status: status}
		}
		in.Containers = []ContainerRef{ref}
	}
	if s.probeUp != nil {
		in.Probes = []ProbeData{{Label: "Live", URL: "https://app.example", ProbeURL: "http://10.0.0.1/", Last: &probe.Result{Up: *s.probeUp, Code: s.probeCode, LatencyMS: 12, At: testNow}}}
	}
	if s.noteAge > 0 {
		in.Notes = []notes.Note{{Project: "app", File: "app.md", Name: "app", IndexLine: "**v1 live**; rename awaits Mark", Clauses: []string{"rename awaits Mark"}, Modified: testNow.Add(-s.noteAge)}}
	}
	if s.fdroidVer != "" {
		in.Project.FDroid = &registry.FDroid{Index: "http://x/index-v1.json", Package: "uk.app"}
		in.FDroid = &fdroid.Package{Name: "uk.app", VersionName: s.fdroidVer, VersionCode: 3}
		in.FDroidAt = testNow
	}
	return in
}

func ptr[T any](v T) *T { return &v }

func TestHeadlinePrecedence(t *testing.T) {
	day := 24 * time.Hour
	parked := &registry.Hold{Kind: "parked", Since: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), Reason: "Waiting on a decision"}
	frozen := &registry.Hold{Kind: "frozen", Since: time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC), Reason: "Production is frozen"}
	cases := []struct {
		name  string
		s     synth
		want  string
		level model.Level
		verb  string // the first action's verb, "" for none
	}{
		{"stopped container", synth{lastCommit: 2 * day, commits: 1, container: "app:1.0.0", state: "exited", declared: true, dockerKnown: true, probeUp: ptr(false), probeCode: 502},
			"Container app is stopped (exit code 1)", model.LevelAlert, "Fix"},
		{"missing container", synth{lastCommit: 2 * day, commits: 1, declared: true, dockerKnown: true},
			"Container app is missing on web-01", model.LevelAlert, "Fix"},
		{"feed unknown: no container claim", synth{lastCommit: 2 * day, commits: 1, declared: true, dockerKnown: false},
			"Last commit 2 days ago", model.LevelGood, ""},
		{"probe down", synth{lastCommit: 2 * day, commits: 1, container: "app:1.0.0", state: "running", declared: true, dockerKnown: true, probeUp: ptr(false), probeCode: 502},
			"Live is not answering (HTTP 502)", model.LevelAlert, "Fix"},
		{"ci failing on main", synth{lastCommit: 2 * day, commits: 1, ciConclusion: "failure"},
			"CI is failing on main: Fix the thing", model.LevelAlert, ""},
		{"ci failing elsewhere is not an alert", synth{lastCommit: 2 * day, commits: 1, ciConclusion: "failure", ciBranch: "feat/x"},
			"Last commit 2 days ago", model.LevelGood, ""},
		{"lagging", synth{lastCommit: 2 * day, commits: 1, tag: "v1.1.0", tagAge: 3 * day, container: "app:1.0.0", state: "running", declared: true, dockerKnown: true},
			"v1.1.0 is ready; production runs 1.0.0", model.LevelWarn, "Deploy"},
		{"lagging on F-Droid", synth{lastCommit: 2 * day, commits: 1, tag: "v0.35.0", tagAge: 3 * day, fdroidVer: "0.34.0"},
			"v0.35.0 is ready; F-Droid has 0.34.0", model.LevelWarn, "Deploy"},
		{"hold contradicted", synth{lastCommit: 2 * day, commits: 12, hold: parked},
			"Parked since 7 Sep, but 12 commits since", model.LevelWarn, "Confirm"},
		{"hold older than the commit window is a lower bound", synth{lastCommit: 2 * day, commits: 12, hold: &registry.Hold{Kind: "parked", Since: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}},
			"Parked since 1 Aug, but 12+ commits since", model.LevelWarn, "Confirm"},
		{"any commit after the hold contradicts it", synth{lastCommit: 2 * day, commits: 2, hold: frozen},
			"Frozen since 29 May, but 2+ commits since", model.LevelWarn, "Confirm"},
		{"hold with no commits since", synth{lastCommit: 160 * day, hold: frozen},
			"Frozen since 29 May: Production is frozen", model.LevelInfo, ""},
		{"commits on the day of the hold are not after it", synth{lastCommit: 55 * time.Hour, commits: 3, hold: &registry.Hold{Kind: "parked", Since: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)}},
			"Parked since 29 Oct", model.LevelInfo, ""},
		{"one commit the next day", synth{lastCommit: 31 * time.Hour, commits: 1, hold: &registry.Hold{Kind: "parked", Since: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)}},
			"Parked since 29 Oct, but 1 commit since", model.LevelWarn, "Confirm"},
		{"active with commits since tag", synth{lastCommit: 2 * day, commits: 12, tag: "v1.25.0", tagAge: 30 * day},
			"12 commits since v1.25.0", model.LevelGood, ""},
		{"active", synth{lastCommit: 2 * day, commits: 1},
			"Last commit 2 days ago", model.LevelGood, ""},
		{"live", synth{lastCommit: 40 * day, container: "app:v1.24.0", state: "running", declared: true, dockerKnown: true},
			"Running v1.24.0, quiet for 40 days", model.LevelGood, ""},
		{"quiet", synth{lastCommit: 40 * day, commits: 1},
			"Last commit 40 days ago", model.LevelInfo, ""},
		{"dormant", synth{lastCommit: 245 * day},
			"No commits for 8 months", model.LevelInfo, ""},
		{"archived suppresses alerts", synth{lastCommit: 245 * day, retired: true, declared: true, dockerKnown: true},
			"Retired in the registry", model.LevelInfo, ""},
		{"stale PR", synth{lastCommit: 2 * day, commits: 1, prUpdated: 3 * day},
			"Last commit 2 days ago", model.LevelGood, "Review"},
		{"fresh PR", synth{lastCommit: 2 * day, commits: 1, prUpdated: 1 * day},
			"Last commit 2 days ago", model.LevelGood, ""},
		{"stale draft PR is not for review", synth{lastCommit: 2 * day, commits: 1, prUpdated: 3 * day, prDraft: true},
			"Last commit 2 days ago", model.LevelGood, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := BuildProject(env(t), tc.s.input()).Project
			if p.Headline != tc.want || p.HeadlineLevel != tc.level {
				t.Errorf("headline = %q %s, want %q %s", p.Headline, p.HeadlineLevel, tc.want, tc.level)
			}
			verb := ""
			if len(p.Actions) > 0 {
				verb = p.Actions[0].Verb
			}
			if verb != tc.verb {
				t.Errorf("first action = %q, want %q (%+v)", verb, tc.verb, p.Actions)
			}
		})
	}
}

func TestActionTexts(t *testing.T) {
	day := 24 * time.Hour
	parked := &registry.Hold{Kind: "parked", Since: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)}
	p := BuildProject(env(t), synth{lastCommit: 2 * day, commits: 12, hold: parked, prUpdated: 3 * day}.input()).Project
	texts := map[string]string{}
	for _, a := range p.Actions {
		texts[a.Verb] = a.Text
	}
	if texts["Confirm"] != "Still parked? 12 commits since 7 Sep" {
		t.Errorf("confirm = %q", texts["Confirm"])
	}
	if texts["Review"] != "#42 Cap the rebuild job (no update for 3 days)" {
		t.Errorf("review = %q", texts["Review"])
	}
	if p.Hold == nil || !p.Hold.Contradicted || p.Hold.CommitsSince != 12 {
		t.Errorf("hold = %+v", p.Hold)
	}
	fd := BuildProject(env(t), synth{lastCommit: 2 * day, commits: 1, tag: "v0.35.0", fdroidVer: "0.34.0"}.input()).Project
	if fd.Deploy == nil || fd.Deploy.Source != "fdroid" || fd.Deploy.Where != "F-Droid" || fd.Deploy.State != "published" || fd.Actions[0].Text != "v0.35.0 to F-Droid, published 0.34.0" {
		t.Errorf("fdroid deploy = %+v actions = %+v", fd.Deploy, fd.Actions)
	}
}

// An F-Droid project whose repo container runs but whose index has not been
// read (fetch failed, not fetched yet, package not listed) is not an outage.
func TestFDroidRepoRunningWithoutIndex(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		state, chip string
		level       model.Level
		indexNote   bool
	}{
		{"running", "F-Droid repo running", model.LevelGood, true},
		{"exited", "F-Droid repo stopped", model.LevelAlert, false},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			in := synth{lastCommit: 40 * day, container: "fdroid:1.0", state: tc.state, declared: true, dockerKnown: true}.input()
			in.Project.FDroid = &registry.FDroid{Index: "http://x/index-v1.json", Package: "uk.app"}
			p := BuildProject(env(t), in).Project
			if p.Deploy == nil || p.Deploy.Source != "fdroid" || p.Deploy.State != tc.state {
				t.Fatalf("deploy = %+v", p.Deploy)
			}
			if s := signal(p, "deploy"); s == nil || s.Text != tc.chip || s.Level != tc.level {
				t.Errorf("deploy chip = %+v, want %q %s", s, tc.chip, tc.level)
			}
			s := signal(p, "fdroid-index")
			if tc.indexNote && (s == nil || s.Level != model.LevelInfo || s.Text != "F-Droid index not read") {
				t.Errorf("index note = %+v", s)
			}
			if !tc.indexNote && s != nil {
				t.Errorf("a stopped repo needs no index note: %+v", s)
			}
			if tc.state == "running" && (p.HeadlineLevel == model.LevelAlert || p.Headline != "Running, quiet for 40 days") {
				t.Errorf("headline = %q %s", p.Headline, p.HeadlineLevel)
			}
		})
	}
}

func TestNoteStaleAndCap(t *testing.T) {
	day := 24 * time.Hour
	p := BuildProject(env(t), synth{lastCommit: 1 * day, commits: 3, noteAge: 10 * day}.input()).Project
	if len(p.Notes) != 1 || !p.Notes[0].Stale || p.Notes[0].StaleReason != "Written 21 Oct; 3 commits since" {
		t.Errorf("notes = %+v", p.Notes)
	}
	if s := signal(p, "note"); s == nil || s.Level != model.LevelWarn {
		t.Errorf("note signal = %+v", s)
	}
	in := synth{lastCommit: 20 * day, commits: 1, noteAge: 2 * day}.input()
	in.Notes[0] = memoryNote(t, "a awaits Mark; b awaits Mark; c awaits Mark", "d awaits Mark; e awaits Mark", in.Notes[0].Modified)
	p = BuildProject(env(t), in).Project
	n := 0
	for _, a := range p.Actions {
		if a.Verb == "Decide" {
			n++
			if a.Source != "Field notes" || !a.At.Equal(in.Notes[0].Modified) || !a.Heuristic || a.Level != model.LevelInfo {
				t.Errorf("decide = %+v", a)
			}
		}
	}
	if n != 4 {
		t.Errorf("decide actions = %d, want the cap of 4", n)
	}
	if p.Notes[0].Stale {
		t.Error("a note newer than every commit is fresh")
	}
}

// A date-only modified covers the whole day it names in the site's zone: commits made
// earlier that day are not "after its modified date".
func TestNoteDateOnlyModified(t *testing.T) {
	written := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC) // "modified: 2026-10-29"
	cases := []struct {
		name       string
		lastCommit time.Duration // before testNow (31 Oct 22:00 UTC, winter time)
		commits    int
		stale      string
	}{
		{"same-day commits", 55 * time.Hour, 3, ""}, // 29 Oct 13:00-15:00
		{"a commit the next day", 31 * time.Hour, 1, "Written 29 Oct; 1 commit since"},
		{"just before midnight", 46*time.Hour + 30*time.Minute, 1, ""}, // 29 Oct 23:30
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := synth{lastCommit: tc.lastCommit, commits: tc.commits, noteAge: time.Hour}.input()
			in.Notes[0].Modified, in.Notes[0].ModifiedDateOnly = written, true
			n := BuildProject(env(t), in).Project.Notes[0]
			if n.StaleReason != tc.stale || n.Stale != (tc.stale != "") {
				t.Errorf("stale = %v %q, want %q", n.Stale, n.StaleReason, tc.stale)
			}
		})
	}
	// Summer time: a date-only stamp ends at midnight summer time (23:00 UTC).
	in := synth{lastCommit: 91*24*time.Hour - 90*time.Minute, commits: 1, noteAge: time.Hour}.input() // 1 Aug 23:30 UTC = 2 Aug 00:30 summer time
	in.Notes[0].Modified, in.Notes[0].ModifiedDateOnly = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), true
	if n := BuildProject(env(t), in).Project.Notes[0]; !n.Stale {
		t.Errorf("a commit after midnight summer time is the next day: %+v", n)
	}
}

func TestCommitsSinceTagCapped(t *testing.T) {
	day := 24 * time.Hour
	in := synth{lastCommit: 1 * day, commits: 150, tag: "v1.0.0", tagAge: 200 * day}.input()
	in.Repos[0].Detail.CommitsCapped = true
	p := BuildProject(env(t), in).Project
	if p.Repo.CommitsSinceTag != 150 || !p.Repo.CommitsSinceTagCapped || p.Headline != "150+ commits since v1.0.0" {
		t.Errorf("repo = %+v headline = %q", p.Repo, p.Headline)
	}
	if s := signal(p, "since-tag"); s == nil || s.Text != "150+ commits since v1.0.0" {
		t.Errorf("since-tag = %+v", s)
	}
}

func TestHashIgnoresVolatileFields(t *testing.T) {
	base := BuildProject(env(t), beaconInput(t)).Project
	h := Hash(base)
	if base.Hash != h {
		t.Fatalf("stored hash %s != Hash() %s", base.Hash, h)
	}
	vol := cloneProject(base)
	vol.Probes[0].CheckedAt = testNow.Add(time.Hour)
	vol.Probes[0].LatencyMS = 999
	vol.Probes[0].UptimePct = 12.5
	vol.Probes[0].History = []model.ProbePoint{{At: testNow, Up: true}}
	vol.Deploy.CheckedAt = testNow.Add(time.Hour)
	vol.Deploy.Status = "Up 2 days" // was "Up 19 hours": the same container, older
	vol.Sources = []model.SourceStamp{{Name: "x", At: testNow}}
	for i := range vol.Signals {
		if vol.Signals[i].Kind == "probe" {
			vol.Signals[i].Text = "up 999 ms"
		}
	}
	vol.Hash = "different"
	if Hash(vol) != h {
		t.Error("volatile fields changed the hash")
	}
	if base.Probes[0].LatencyMS != 23 || base.Deploy.CheckedAt.Equal(testNow.Add(time.Hour)) {
		t.Error("Hash or cloneProject mutated the original")
	}
	up := cloneProject(base)
	up.Probes[0].Up = false
	if Hash(up) == h {
		t.Error("probe Up must change the hash")
	}
	hl := cloneProject(base)
	hl.Headline = "something else"
	if Hash(hl) == h {
		t.Error("headline must change the hash")
	}
	sick := cloneProject(base)
	sick.Deploy.Status = "Up 19 hours (unhealthy)"
	if Hash(sick) == h {
		t.Error("a health change in Deploy.Status must change the hash")
	}
}

// Before the repo list has been read, a project's repos are unknown, not
// absent: its tier is shown but not fingerprinted, so no tier event follows
// when Gitea answers.
func TestTierBeforeGiteaIsRead(t *testing.T) {
	in := synth{container: "app:1.0.0", state: "running", declared: true, dockerKnown: true}.input()
	in.Repos, in.GiteaKnown = nil, false
	b := BuildProject(env(t), in)
	if b.Project.Tier != model.TierLive || b.Project.TierReason != "Deployed and running, Gitea not read yet" || b.FP.Tier != "" {
		t.Errorf("running, Gitea unread: tier %s %q, fingerprint tier %q", b.Project.Tier, b.Project.TierReason, b.FP.Tier)
	}
	if b.Project.Headline != "Running 1.0.0; Gitea not read yet" {
		t.Errorf("headline = %q", b.Project.Headline)
	}
	in = synth{}.input()
	in.Repos, in.GiteaKnown = nil, false
	if b := BuildProject(env(t), in); b.Project.Tier != model.TierDormant || b.Project.TierReason != "Gitea not read yet" || b.FP.Tier != "" {
		t.Errorf("idle, Gitea unread: %s %q fp %q", b.Project.Tier, b.Project.TierReason, b.FP.Tier)
	}
	// Once the list is read, no repos really means no commits.
	in.GiteaKnown = true
	if b := BuildProject(env(t), in); b.Project.TierReason != "No commits yet" || b.FP.Tier != model.TierDormant {
		t.Errorf("Gitea read, repo gone: %q fp %q", b.Project.TierReason, b.FP.Tier)
	}
	// A project with no Gitea repo, or a retired one, needs no Gitea data.
	in = synth{retired: true}.input()
	in.Repos, in.GiteaKnown = nil, false
	if b := BuildProject(env(t), in); b.FP.Tier != model.TierArchived {
		t.Errorf("retired: fp tier %q", b.FP.Tier)
	}
	in = synth{container: "app:1.0.0", state: "running", declared: true, dockerKnown: true}.input()
	in.Repos, in.Project.Repos, in.GiteaKnown = nil, nil, false
	if b := BuildProject(env(t), in); b.FP.Tier != model.TierLive || b.Project.TierReason != "Deployed and running, no notes yet" {
		t.Errorf("no Gitea repo: fp tier %q reason %q", b.FP.Tier, b.Project.TierReason)
	}
}

// A project that needs fixing keeps its hash across polls: each Fix action
// is dated from when the down state was first seen, not from the poll.
func TestHashStableAcrossPollsWhileDown(t *testing.T) {
	day := 24 * time.Hour
	first := testNow.Add(-10 * time.Minute)
	// Docker's status ages with the clock, as the real feed's does.
	poll := func(at time.Time, state, status string, prev *Fingerprint) Built {
		in := synth{lastCommit: 2 * day, commits: 1, container: "app:1.0.0", state: state, status: status, declared: true, dockerKnown: true, probeUp: ptr(false), probeCode: 502}.input()
		in.FeedAt = at
		in.Probes[0].Last.At = at
		in.Prev = prev
		e := env(t)
		e.Now = at
		return BuildProject(e, in)
	}
	fixAt := func(b Built) map[string]time.Time {
		out := map[string]time.Time{}
		for _, a := range b.Project.Actions {
			if a.Verb == "Fix" {
				out[a.Source] = a.At
			}
		}
		return out
	}
	// The container stopped a minute before the first poll saw it.
	stopped := first.Add(-time.Minute)
	b1 := poll(first, "exited", "Exited (1) About a minute ago", nil)
	if got := fixAt(b1); len(got) != 2 || !got["Docker"].Equal(stopped) || !got["Probe"].Equal(first) {
		t.Fatalf("first poll Fix actions = %v", got)
	}
	m1 := Merge(Fingerprint{}, b1.FP)
	b2 := poll(testNow, "exited", "Exited (1) 11 minutes ago", &m1)
	if b2.Project.Hash != b1.Project.Hash {
		t.Errorf("hash moved between polls of the same outage: %s -> %s\n%+v\n%+v", b1.Project.Hash, b2.Project.Hash, b1.Project.Actions, b2.Project.Actions)
	}
	if got := fixAt(b2); !got["Docker"].Equal(stopped) || !got["Probe"].Equal(first) {
		t.Errorf("second poll Fix actions = %v, want %v and %v", got, stopped, first)
	}
	// The dates survive the round trip through state.json.
	var back Fingerprint
	raw, _ := json.Marshal(Merge(m1, b2.FP))
	if err := json.Unmarshal(raw, &back); err != nil || back.ContainerSince == nil || !back.ContainerSince.Equal(stopped) || !back.ProbeDown["Live"].Equal(first) {
		t.Errorf("persisted since = %+v %v (%v)", back.ContainerSince, back.ProbeDown, err)
	}
	// A new state starts a new date, from docker's own age when it gives one.
	b3 := poll(testNow, "restarting", "Restarting (1) 5 seconds ago", &back)
	if got := fixAt(b3); !got["Docker"].Equal(testNow.Add(-5*time.Second)) || !got["Probe"].Equal(first) {
		t.Errorf("after a state change: %v", got)
	}
	// A container that was already down when Waymark first looked is dated
	// from when docker says it stopped, not from Waymark's first look.
	b4 := poll(testNow, "exited", "Exited (0) 6 days ago", nil)
	if got := fixAt(b4); !got["Docker"].Equal(testNow.Add(-6 * day)) {
		t.Errorf("down before the first look: %v", got)
	}
	// Missing has no docker status: the first look dates it.
	in := synth{lastCommit: 2 * day, commits: 1, declared: true, dockerKnown: true}.input()
	if got := fixAt(BuildProject(env(t), in)); !got["Docker"].Equal(in.FeedAt) {
		t.Errorf("missing: %v", got)
	}
}

// A project's hash moves when what it shows changes, not because the clock
// did. Docker's status ages every minute ("Exited (1) 3 minutes ago", "Up 21
// hours" turning "Up 22 hours"), and a minute count in a headline or an
// action would re-publish the card on every poll for an hour.
func TestHashHoldsAsTheClockMoves(t *testing.T) {
	day := 24 * time.Hour
	t1, t2 := testNow.Add(-50*time.Minute), testNow // 21:10 and 22:00 on Sat 31 Oct (winter time)
	// Commit ages sit clear of a whole-day boundary between t1 and t2, so a
	// day count ("quiet for 40 days") reads the same at both polls.
	stopped := synth{lastCommit: 2*day + 2*time.Hour, commits: 1, container: "app:1.0.0", state: "exited", declared: true, dockerKnown: true}
	running := synth{lastCommit: 40*day + 2*time.Hour, container: "app:v1.24.0", state: "running", declared: true, dockerKnown: true}
	fdroidStopped := synth{lastCommit: 40*day + 2*time.Hour, container: "fdroid:1.0", state: "exited", declared: true, dockerKnown: true, fdroidVer: "0.34.0"}
	undatedTag := func(in *Input) { in.Repos[0].Detail.Tags[0].Commit.Created = forge.Time{} }
	// poll is the project as seen at `at`: every poll stamp is `at`, and the
	// primary container (if any) reports `status`.
	poll := func(s synth, at time.Time, status string, prev *Fingerprint, mut func(*Input)) Built {
		s.status = status
		in := s.input()
		in.FeedAt, in.NotesAt, in.Prev = at, at, prev
		for i := range in.Repos {
			in.Repos[i].Detail.FetchedAt = at
		}
		for i := range in.Probes {
			in.Probes[i].Last.At = at
		}
		if in.FDroid != nil {
			in.FDroidAt = at
		}
		if mut != nil {
			mut(&in)
		}
		e := env(t)
		e.Now = at
		return BuildProject(e, in)
	}
	cases := []struct {
		name             string
		s                synth
		status1, status2 string
		at2              time.Time
		mut              func(*Input)
		same             bool
	}{
		{"stopped a minute, then fifty", stopped, "Exited (1) About a minute ago", "Exited (1) 51 minutes ago", t2, nil, true},
		{"stopped for hours", stopped, "Exited (137) 20 hours ago", "Exited (137) 21 hours ago", t2, nil, true},
		{"running, uptime ticks over an hour", running, "Up 21 hours (healthy)", "Up 22 hours (healthy)", t2, nil, true},
		{"F-Droid repo container stopped", fdroidStopped, "Exited (2) 3 minutes ago", "Exited (2) 53 minutes ago", t2, nil, true},
		{"commit minutes ago", synth{lastCommit: 55 * time.Minute, commits: 1}, "", "", t2, nil, true},
		{"note written minutes ago", synth{lastCommit: 20*day + 2*time.Hour, commits: 1, noteAge: 55 * time.Minute}, "", "", t2, nil, true},
		{"probe down", synth{lastCommit: 2*day + 2*time.Hour, commits: 1, probeUp: ptr(false), probeCode: 502}, "", "", t2, nil, true},
		{"lagging behind an undated tag", synth{lastCommit: 2*day + 2*time.Hour, commits: 1, tag: "v1.1.0", container: "app:1.0.0", state: "running", declared: true, dockerKnown: true}, "Up 3 days", "Up 3 days", t2, undatedTag, true},
		// What the page says did change: these must move the hash.
		{"exit code changed", stopped, "Exited (1) About a minute ago", "Exited (137) 51 minutes ago", t2, nil, false},
		{"health changed", running, "Up 21 hours (healthy)", "Up 22 hours (unhealthy)", t2, nil, false},
		{"a new day in the zone", synth{lastCommit: 55 * time.Minute, commits: 1}, "", "", t2.Add(3 * time.Hour), nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b1 := poll(tc.s, t1, tc.status1, nil, tc.mut)
			m1 := Merge(Fingerprint{}, b1.FP)
			b2 := poll(tc.s, tc.at2, tc.status2, &m1, tc.mut)
			if same := b1.Project.Hash == b2.Project.Hash; same != tc.same {
				a, _ := json.Marshal(b1.Project)
				b, _ := json.Marshal(b2.Project)
				t.Errorf("hash same = %v, want %v\nfirst:  %s\nsecond: %s", same, tc.same, a, b)
			}
		})
	}
}

// The texts that used to carry a minute count now say what holds.
func TestClockFreeTexts(t *testing.T) {
	day := 24 * time.Hour
	p := BuildProject(env(t), synth{lastCommit: 55 * time.Minute, commits: 1}.input()).Project
	if p.Headline != "Last commit today" {
		t.Errorf("active headline = %q", p.Headline)
	}
	p = BuildProject(env(t), synth{lastCommit: 22*time.Hour + 30*time.Minute, commits: 1}.input()).Project // 30 Oct 23:30 winter time
	if p.Headline != "Last commit yesterday" {
		t.Errorf("headline after midnight = %q", p.Headline)
	}
	p = BuildProject(env(t), synth{lastCommit: 2 * day, commits: 1, container: "app:1.0.0", state: "exited", status: "Exited (137) 20 hours ago", declared: true, dockerKnown: true}.input()).Project
	if p.Headline != "Container app is stopped (exit code 137)" || p.Actions[0].Text != p.Headline {
		t.Errorf("stopped: headline %q, Fix %q", p.Headline, p.Actions[0].Text)
	}
	if p.Deploy.Status != "Exited (137) 20 hours ago" {
		t.Errorf("Deploy.Status keeps docker's text for the page: %q", p.Deploy.Status)
	}
	paused := synth{lastCommit: 2 * day, commits: 1, container: "app:1.0.0", state: "paused", status: "Up 3 days (Paused)", declared: true, dockerKnown: true}.input()
	p = BuildProject(env(t), paused).Project
	if p.Headline != "Container app is paused" || !p.Actions[0].At.Equal(paused.FeedAt) {
		t.Errorf("paused: %q, Fix at %v (docker's 3 days is uptime, not how long it has been paused)", p.Headline, p.Actions[0].At)
	}
	in := synth{lastCommit: 2 * day, commits: 1, tag: "v1.1.0", container: "app:1.0.0", state: "running", declared: true, dockerKnown: true}.input()
	in.Repos[0].Detail.Tags[0].Commit.Created = forge.Time{}
	p = BuildProject(env(t), in).Project
	if p.Actions[0].Verb != "Deploy" || !p.Actions[0].At.IsZero() {
		t.Errorf("a Deploy action for an undated tag has no date, not the clock: %+v", p.Actions[0])
	}
}

// cloneProject deep-copies a project through JSON (what Hash sees anyway).
func cloneProject(p model.Project) model.Project {
	var out model.Project
	b, _ := json.Marshal(p)
	_ = json.Unmarshal(b, &out)
	return out
}

func TestCommitsSinceTagBySHA(t *testing.T) {
	day := 24 * time.Hour
	in := synth{lastCommit: 1 * day, commits: 3, tag: "v0.3.0"}.input()
	d := in.Repos[0].Detail
	// The tag sits on the newest commit, but its created stamp is a few
	// seconds earlier than the commit date: still zero commits since.
	d.Tags[0].Commit.SHA = d.Commits[0].SHA
	d.Tags[0].Commit.Created = forge.Time{Time: d.Commits[0].When().Add(-5 * time.Second)}
	p := BuildProject(env(t), in).Project
	if p.Repo.CommitsSinceTag != 0 || p.Repo.CommitsSinceTagCapped || signal(p, "since-tag") != nil {
		t.Errorf("tag on HEAD: %+v", p.Repo)
	}
	// Tag on the third commit of a capped window: exact, not a lower bound.
	in = synth{lastCommit: 1 * day, commits: 150, tag: "v1.0.0"}.input()
	d = in.Repos[0].Detail
	d.CommitsCapped = true
	d.Tags[0].Commit.SHA = d.Commits[2].SHA
	d.Tags[0].Commit.Created = forge.Time{Time: d.Commits[2].When()}
	p = BuildProject(env(t), in).Project
	if p.Repo.CommitsSinceTag != 2 || p.Repo.CommitsSinceTagCapped {
		t.Errorf("tag in a capped window: %+v", p.Repo)
	}
	// Tag on an old commit outside the window, with no newer window commits:
	// the newest commit is the tag's, so zero.
	in = synth{lastCommit: 100 * day, tag: "v2.0.0", tagAge: 100 * day}.input()
	d = in.Repos[0].Detail
	d.LastCommit.SHA = "feedface"
	d.Tags[0].Commit.SHA = "feedface"
	p = BuildProject(env(t), in).Project
	if p.Repo.CommitsSinceTag != 0 {
		t.Errorf("old tag on HEAD: %+v", p.Repo)
	}
}

func TestCappedWindowCountsAreExactAfterTheOldestRead(t *testing.T) {
	day := 24 * time.Hour
	// 150 commits an hour apart from 1 day back: the oldest read is ~7 days back.
	in := synth{lastCommit: 1 * day, commits: 150, noteAge: 2 * day}.input()
	in.Repos[0].Detail.CommitsCapped = true
	p := BuildProject(env(t), in).Project
	if !p.Notes[0].Stale || p.Notes[0].StaleReason != "Written 29 Oct; 24 commits since" {
		t.Errorf("note after the oldest read commit: %+v", p.Notes[0])
	}
	in = synth{lastCommit: 1 * day, commits: 150, noteAge: 30 * day}.input()
	in.Repos[0].Detail.CommitsCapped = true
	p = BuildProject(env(t), in).Project
	if p.Notes[0].StaleReason != "Written 1 Oct; 150+ commits since" {
		t.Errorf("note before the oldest read commit: %+v", p.Notes[0])
	}
}

func acmeMirror() *forge.Mirror {
	return &forge.Mirror{
		Forge: "github", FullName: "acme/app", HTMLURL: "https://github.example.com/acme/app", Stars: 7, OpenIssues: 1,
		Issues: []forge.Issue{{
			Number: 3, Title: "Crash on start", HTMLURL: "https://github.example.com/acme/app/issues/3", User: forge.User{Login: "mark"},
			Labels: []forge.Label{{Name: "bug", Color: "d73a4a"}}, UpdatedAt: forge.Time{Time: testNow.Add(-time.Hour)},
		}},
		LatestRelease: "v1.2.0", FetchedAt: testNow,
	}
}

func hasLink(p model.Project, label, url string) bool {
	for _, l := range p.Links {
		if l.Label == label && l.URL == url {
			return true
		}
	}
	return false
}

// A project's first mirror (v0.1: its GitHub repo) gives stars, its open
// issues after the primary's, a signal, a link and a source stamp.
func TestMirrorFromInput(t *testing.T) {
	in := synth{}.input()
	in.Mirror, in.MirrorURL = acmeMirror(), "https://github.example.com/acme/app"
	p := BuildProject(env(t), in).Project
	want := model.GitHubInfo{FullName: "acme/app", HTMLURL: "https://github.example.com/acme/app", Stars: 7, OpenIssues: 1, LatestRelease: "v1.2.0"}
	if p.GitHub == nil || *p.GitHub != want {
		t.Errorf("mirror = %+v, want %+v", p.GitHub, want)
	}
	if n := len(p.Issues); n != 1 || p.Issues[0].Source != "github" || p.Issues[0].Number != 3 || p.Issues[0].Author != "mark" ||
		len(p.Issues[0].Labels) != 1 || p.Issues[0].Labels[0].Color != "d73a4a" || !p.Issues[0].UpdatedAt.Equal(testNow.Add(-time.Hour)) {
		t.Errorf("issues = %+v", p.Issues)
	}
	if s := signal(p, "gh-issues"); s == nil || s.Text != "1 GitHub issue" || s.Href != "https://github.example.com/acme/app/issues" {
		t.Errorf("gh-issues signal = %+v", s)
	}
	if !hasLink(p, "GitHub", "https://github.example.com/acme/app") {
		t.Errorf("links = %+v", p.Links)
	}
	stamped := false
	for _, s := range p.Sources {
		stamped = stamped || (s.Name == "github" && s.At.Equal(testNow))
	}
	if !stamped {
		t.Errorf("sources = %+v", p.Sources)
	}
}

// Before the mirror's first read its page is still linked.
func TestMirrorLinkBeforeFirstRead(t *testing.T) {
	in := synth{}.input()
	in.MirrorURL, in.MirrorForge = "https://github.example.com/acme/app", "github"
	p := BuildProject(env(t), in).Project
	if p.GitHub != nil || !hasLink(p, "GitHub", "https://github.example.com/acme/app") {
		t.Errorf("github %+v links %+v", p.GitHub, p.Links)
	}
}

// GitHub without a token gives no detail, so a GitHub primary is read like a
// mirror: the project shows as v0.1 showed one with no Gitea repo, dated from
// its notes and with its tier known before any repo list is read.
func TestNoDetailPrimaryIsDatedFromNotes(t *testing.T) {
	note := notes.Note{File: "tool.md", Name: "tool", IndexLine: "**v1 live**", Modified: testNow.Add(-3 * 24 * time.Hour)}
	proj := registry.Project{Slug: "tool", Name: "Tool", Category: registry.UnsortedKey, Repos: []registry.RepoRef{{Forge: "github", FullName: "acme/app"}}}
	in := Input{Project: proj, NoDetail: true, Mirror: acmeMirror(), MirrorURL: "https://github.example.com/acme/app", Notes: []notes.Note{note}, NotesAt: testNow}
	b := BuildProject(env(t), in)
	p := b.Project
	if !p.ActivityFromNotes || !p.LastActivity.Equal(note.Modified) || p.Repo != nil || p.GitHub == nil || p.GitHub.Stars != 7 || b.FP.Tier == "" {
		t.Errorf("no-detail primary: notes %v at %v repo %+v github %+v fp tier %q", p.ActivityFromNotes, p.LastActivity, p.Repo, p.GitHub, b.FP.Tier)
	}
	// The same as a project with no repo at all (the v0.1 path).
	bare := in
	bare.Project.Repos, bare.NoDetail = nil, false
	q := BuildProject(env(t), bare).Project
	if p.Tier != q.Tier || p.TierReason != q.TierReason || p.Headline != q.Headline {
		t.Errorf("tier %s %q %q, want %s %q %q", p.Tier, p.TierReason, p.Headline, q.Tier, q.TierReason, q.Headline)
	}
	// With a token the primary is read in full: commits, not notes, date it.
	in.NoDetail = false
	if p := BuildProject(env(t), in).Project; p.ActivityFromNotes {
		t.Error("a primary read in full must not be dated from its notes")
	}
}

// A GitHub listing counts PRs as issues; the detail splits them, and the
// signals and site totals use the split.
func TestOpenCountsFromDetail(t *testing.T) {
	in := synth{}.input()
	in.Repos[0].Repo.OpenIssues, in.Repos[0].Repo.OpenPRs = 3, 0
	in.Repos[0].Detail.Repo.OpenIssues, in.Repos[0].Detail.Repo.OpenPRs = 2, 1
	s := Assemble(SiteInput{Env: env(t), Registry: registry.Empty(), Projects: []Input{in}}).Site
	p := s.Projects[0]
	if p.Repo.OpenIssues != 2 || p.Repo.OpenPRs != 1 || s.Stats.OpenIssues != 2 || s.Stats.OpenPRs != 1 {
		t.Errorf("repo %d issues %d PRs; site %d issues %d PRs", p.Repo.OpenIssues, p.Repo.OpenPRs, s.Stats.OpenIssues, s.Stats.OpenPRs)
	}
	if sg := signal(p, "prs"); sg == nil || sg.Text != "1 PR" {
		t.Errorf("PR signal = %+v", sg)
	}
	// Before the first detail the listing is all there is.
	in.Repos[0].Detail = nil
	if p := BuildProject(env(t), in).Project; p.Repo.OpenIssues != 3 || p.Repo.OpenPRs != 0 {
		t.Errorf("without a detail: %d issues %d PRs", p.Repo.OpenIssues, p.Repo.OpenPRs)
	}
}
