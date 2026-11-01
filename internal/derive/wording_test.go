package derive

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/docker"
	"github.com/Will-Luck/waymark/internal/sources/probe"
)

// wordingEnv knows three forges: a Gitea, GitHub and a Gitea-kind forge on
// codeberg.org, named as the app names them (ForgeName).
func wordingEnv(t *testing.T) Env {
	e := env(t)
	e.ForgeKinds = map[string]string{"gitea": "gitea", "github": "github", "cb": "gitea"}
	e.ForgeNames = map[string]string{"gitea": ForgeName("gitea", "https://git.example.com"), "github": ForgeName("github", "https://api.github.com"),
		"cb": ForgeName("gitea", "https://codeberg.org")}
	e.HomeForge = "gitea"
	return e
}

// acmeApp is acme/app on forge id, read in full: v1.1.0 tagged, v1.0.0
// running, a pull request untouched for a month, and a parked hold that two
// days of commits contradict.
func acmeApp(id string) Input {
	page := "https://" + id + ".example.com/acme/app"
	repo := forge.Repo{Forge: id, Name: "app", FullName: "acme/app", HTMLURL: page, DefaultBranch: "main", OpenPRs: 1}
	d := &forge.Detail{Repo: repo, Since: testNow.Add(-84 * 24 * time.Hour), FetchedAt: testNow}
	for i := 0; i < 3; i++ {
		var c forge.Commit
		c.SHA = strings.Repeat(string(rune('a'+i)), 40)
		c.Commit.Message = "change"
		c.Commit.Committer.Date = forge.Time{Time: testNow.Add(-time.Duration(i+1) * time.Hour)}
		d.Commits = append(d.Commits, c)
	}
	d.LastCommit = &d.Commits[0]
	d.Tags = []forge.Tag{tag("v1.1.0", testNow.Add(-5*24*time.Hour).Format(time.RFC3339))}
	d.PRs = []forge.Issue{{Number: 4, Title: "Old change", State: "open", HTMLURL: page + "/pulls/4", UpdatedAt: forge.Time{Time: testNow.Add(-30 * 24 * time.Hour)}}}
	p := registry.Project{Slug: "app", Name: "App", Category: "tools", Icon: "box", Repos: []registry.RepoRef{{Forge: id, FullName: "acme/app"}},
		Hold: &registry.Hold{Kind: "parked", Since: testNow.Add(-20 * 24 * time.Hour), Reason: "Waiting on a decision"}}
	return Input{
		Project: p, Repos: []RepoData{{Repo: repo, Detail: d, Files: &forge.Files{FetchedAt: testNow}}}, GiteaKnown: true,
		DockerKnown: true, FeedAt: testNow,
		Containers: []ContainerRef{{Name: "app", Declared: true, C: &docker.Container{Name: "app", Image: "acme/app:v1.0.0", State: "running", Status: "Up 3 days"}}},
	}
}

func projectJSON(t *testing.T, p model.Project) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestForgeName(t *testing.T) {
	for _, c := range []struct{ kind, url, want string }{
		{"gitea", "https://git.example.com", "Gitea"},
		{"gitea", "https://codeberg.org", "Codeberg"},
		{"gitea", "https://Codeberg.org/", "Codeberg"},
		{"gitea", "https://codeberg.org.example.com", "Gitea"},
		{"github", "https://api.github.com", "GitHub"},
		{"github", "https://github.example.com/api/v3", "GitHub"},
		{"forgejo", "https://git.example.com", "Forgejo"},
		{"forgejo", "https://codeberg.org", "Codeberg"},
	} {
		if got := ForgeName(c.kind, c.url); got != c.want {
			t.Errorf("ForgeName(%q, %q) = %q, want %q", c.kind, c.url, got, c.want)
		}
	}
}

// A GitHub primary is named GitHub wherever v0.1 said Gitea: its link, the
// deploy and review sources, the hold check and the reason before a read.
func TestGitHubPrimaryWording(t *testing.T) {
	e := wordingEnv(t)
	p := BuildProject(e, acmeApp("github")).Project
	if p.Forge != "GitHub" || !hasLink(p, "GitHub", "https://github.example.com/acme/app") {
		t.Errorf("forge %q links %+v", p.Forge, p.Links)
	}
	sources := map[string]string{}
	for _, a := range p.Actions {
		sources[a.Verb] = a.Source
	}
	want := map[string]string{"Deploy": "GitHub tags + Docker", "Review": "GitHub", "Confirm": "Registry hold vs GitHub commits"}
	for verb, src := range want {
		if sources[verb] != src {
			t.Errorf("%s source %q, want %q (actions %+v)", verb, sources[verb], src, p.Actions)
		}
	}
	for _, s := range p.Sources {
		if s.Name != "github" && s.Name != "github files" && s.Name != "docker" {
			t.Errorf("source stamp %q is not the forge's id", s.Name)
		}
	}
	if j := projectJSON(t, p); strings.Contains(j, "Gitea") || strings.Contains(j, `"gitea`) {
		t.Errorf("a GitHub project says Gitea: %s", j)
	}

	unread := acmeApp("github")
	unread.Project.Hold, unread.Repos, unread.GiteaKnown = nil, nil, false
	q := BuildProject(e, unread).Project
	if q.TierReason != "Deployed and running, GitHub not read yet" || q.Headline != "Running v1.0.0; GitHub not read yet" {
		t.Errorf("before the first read: %q / %q", q.TierReason, q.Headline)
	}
	archived := acmeApp("github")
	archived.Repos[0].Repo.Archived = true
	if r := BuildProject(e, archived).Project.TierReason; r != "Archived on GitHub" {
		t.Errorf("archived: %q", r)
	}
	// The Gitea primary keeps v0.1's words.
	g := BuildProject(e, acmeApp("gitea")).Project
	if g.Forge != "Gitea" || !hasLink(g, "Gitea", "https://gitea.example.com/acme/app") {
		t.Errorf("gitea primary: forge %q links %+v", g.Forge, g.Links)
	}
	for _, a := range g.Actions {
		if a.Verb == "Deploy" && a.Source != "Gitea tags + Docker" {
			t.Errorf("gitea deploy source %q", a.Source)
		}
	}
}

// A mirror on Codeberg is named Codeberg: its link, its issues and their
// signal; its issues are marked as the mirror's, by forge id.
func TestCodebergMirrorWording(t *testing.T) {
	in := acmeApp("gitea")
	in.Project.Hold = nil
	in.Mirror = &forge.Mirror{
		Forge: "cb", FullName: "acme/app", HTMLURL: "https://codeberg.org/acme/app", OpenIssues: 1, FetchedAt: testNow,
		Issues: []forge.Issue{{Number: 9, Title: "Typo", State: "open", HTMLURL: "https://codeberg.org/acme/app/issues/9", UpdatedAt: forge.Time{Time: testNow}}},
	}
	in.MirrorURL, in.MirrorForge = in.Mirror.HTMLURL, "cb"
	p := BuildProject(wordingEnv(t), in).Project
	if !hasLink(p, "Codeberg", "https://codeberg.org/acme/app") {
		t.Errorf("links %+v", p.Links)
	}
	for _, l := range p.Links {
		if l.Label == "Codeberg" && l.Kind != "mirror" {
			t.Errorf("Codeberg link kind %q, want mirror", l.Kind)
		}
	}
	if s := signal(p, "gh-issues"); s == nil || s.Text != "1 Codeberg issue" {
		t.Errorf("mirror issues signal %+v", s)
	}
	if len(p.Issues) != 1 || !p.Issues[0].Mirror || p.Issues[0].Forge != "Codeberg" || p.Issues[0].Source != "cb" {
		t.Errorf("issues %+v", p.Issues)
	}
	stamped := false
	for _, s := range p.Sources {
		stamped = stamped || s.Name == "cb"
	}
	if !stamped {
		t.Errorf("no mirror stamp under its forge id: %+v", p.Sources)
	}
	// Before the first read the link is named from the mirror's forge id.
	in.Mirror = nil
	if p := BuildProject(wordingEnv(t), in).Project; !hasLink(p, "Codeberg", "https://codeberg.org/acme/app") {
		t.Errorf("before the first read: links %+v", p.Links)
	}
}

// Spec ruling 3: a homepage probe shows in "Where it runs" but is its own
// signal: never Live, never Fix, no chip, no event.
func TestHomepageProbeNeverLiveNorFix(t *testing.T) {
	e := wordingEnv(t)
	home := func(up bool) Input {
		in := acmeApp("gitea")
		in.Project.Hold, in.Containers = nil, nil
		for i := range in.Repos[0].Detail.Commits {
			in.Repos[0].Detail.Commits[i].Commit.Committer.Date = forge.Time{Time: testNow.Add(-200 * 24 * time.Hour)}
		}
		code := 200
		if !up {
			code = 503
		}
		in.Probes = []ProbeData{{Label: "Homepage", URL: "https://app.example.com", ProbeURL: "https://app.example.com", Homepage: true,
			Last: &probe.Result{URL: "https://app.example.com", Up: up, Code: code, LatencyMS: 9, At: testNow}}}
		in.Prev = &Fingerprint{Probes: map[string]bool{"Homepage": !up}}
		return in
	}
	for _, up := range []bool{true, false} {
		b := BuildProject(e, home(up))
		p := b.Project
		if p.Tier != model.TierDormant {
			t.Errorf("up=%v: tier %s (%s), want dormant", up, p.Tier, p.TierReason)
		}
		if len(p.Probes) != 1 || !p.Probes[0].Homepage || p.Probes[0].Up != up {
			t.Errorf("up=%v: probes %+v", up, p.Probes)
		}
		for _, a := range p.Actions {
			if a.Verb == "Fix" {
				t.Errorf("up=%v: homepage raised %+v", up, a)
			}
		}
		if signal(p, "probe") != nil {
			t.Errorf("up=%v: homepage gave a signal %+v", up, signal(p, "probe"))
		}
		if p.HeadlineLevel == model.LevelAlert || strings.Contains(p.Headline, "answering") {
			t.Errorf("up=%v: headline %q (%s)", up, p.Headline, p.HeadlineLevel)
		}
		if b.FP.Probes != nil || len(b.FP.ProbeDown) > 0 {
			t.Errorf("up=%v: homepage in the fingerprint %+v %+v", up, b.FP.Probes, b.FP.ProbeDown)
		}
	}
	// A projects-file probe beside it keeps v0.1's rules and words.
	in := home(false)
	in.Probes = append(in.Probes, ProbeData{Label: "Live", URL: "https://app.example.net", ProbeURL: "https://app.example.net",
		Last: &probe.Result{Up: true, Code: 200, LatencyMS: 7, At: testNow}})
	p := BuildProject(e, in).Project
	if p.Tier != model.TierLive || p.TierReason != "Answering, no commits for 6 months" {
		t.Errorf("with a url probe: %s %q", p.Tier, p.TierReason)
	}
	if s := signal(p, "probe"); s == nil || s.Text != "up 7 ms" {
		t.Errorf("url probe chip %+v, want the v0.1 unlabelled one", s)
	}
}

// Label ideas take their kind from kind/<k> and their size from size/<s>,
// mapped to the page's S, M and L; any other size gives none.
func TestLabelIdeaSizes(t *testing.T) {
	in := acmeApp("gitea")
	page := in.Repos[0].Repo.HTMLURL
	var issues []forge.Issue
	for i, size := range []string{"size/xl", "size/S", "size/medium", "size/huge", ""} {
		is := forge.Issue{Number: i + 1, Title: "Idea", State: "open", HTMLURL: page + "/issues/" + itoa(i+1), Labels: []forge.Label{{Name: "idea"}}}
		if size != "" {
			is.Labels = append(is.Labels, forge.Label{Name: size})
		}
		issues = append(issues, is)
	}
	in.Repos[0].Detail.Issues = issues
	var got []string
	for _, idea := range labelIdeas(in, []string{"idea"}) {
		got = append(got, idea.Size+"/"+idea.Kind)
	}
	if want := "L/ S/ M/ / /"; strings.Join(got, " ") != want {
		t.Errorf("sizes %q, want %q", strings.Join(got, " "), want)
	}
}

// Assemble adds the sources it can see to the configured Meta.
func TestAssembleMeta(t *testing.T) {
	in := acmeApp("gitea")
	meta := model.Meta{Title: "Acme", NotesLabel: "Notes", Notes: true, IdeaLabels: []string{"idea"}}
	s := Assemble(SiteInput{Env: wordingEnv(t), Registry: registry.Empty(), Projects: []Input{in}, Meta: meta, IdeaLabels: []string{"idea"}}).Site
	if s.Meta.Title != "Acme" || s.Meta.NotesLabel != "Notes" || !s.Meta.Notes || s.Meta.Docker || s.Meta.Ideas || s.Meta.FDroid {
		t.Errorf("meta %+v", s.Meta)
	}
	in.Repos[0].Detail.Issues = []forge.Issue{{Number: 3, Title: "Dark mode", State: "open", HTMLURL: "https://gitea.example.com/acme/app/issues/3", Labels: []forge.Label{{Name: "idea"}}}}
	in.Project.FDroid = &registry.FDroid{Index: "https://fdroid.example.com/repo/index-v1.json", Package: "com.example.app"}
	s = Assemble(SiteInput{Env: wordingEnv(t), Registry: registry.Empty(), Projects: []Input{in}, Meta: meta, IdeaLabels: []string{"idea"}}).Site
	if !s.Meta.Ideas || !s.Meta.FDroid {
		t.Errorf("with an idea and a package: meta %+v", s.Meta)
	}
}
