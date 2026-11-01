package derive

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
)

func siteInput(t *testing.T) SiteInput {
	r := reg(t)
	em := beaconInput(t)
	// In this test Beacon also supersedes the old site.
	em.Project.Relations = append(em.Project.Relations, registry.Relation{To: "old-site", Kind: "supersedes"})

	// old-site: retired, Beacon points at it.
	as := Input{Project: *r.Project("old-site"), GiteaKnown: true}
	asRepo := forge.Repo{Name: "old-site", FullName: "acme/old-site", HTMLURL: "https://g/acme/old-site", DefaultBranch: "main"}
	asDetail := &forge.Detail{Repo: asRepo, FetchedAt: testNow}
	var c forge.Commit
	c.Commit.Committer.Date = forge.Time{Time: testNow.Add(-150 * 24 * time.Hour)}
	asDetail.LastCommit = &c
	asDetail.Tags = []forge.Tag{tag("v0.9.0", "2025-01-10T00:00:00Z"), tag("v1.0.0", "2026-03-01T00:00:00Z")}
	as.Repos = []RepoData{{Repo: asRepo, Detail: asDetail}}

	// beacon-app points at beacon (client-of) and also, in this test, at a
	// project that does not exist.
	ea := Input{Project: *r.Project("beacon-app"), GiteaKnown: true}
	ea.Project.Relations = append(ea.Project.Relations, registry.Relation{To: "ghost", Kind: "sibling"})

	// An auto project from an unregistered repo.
	auto := r.AutoProjects([]forge.Repo{{Forge: "gitea", FullName: "acme/sandbox", Name: "sandbox", Description: "Scratch space"}}, []string{"gitea", "github"})
	pr := Input{Project: auto[0], GiteaKnown: true}
	pRepo := forge.Repo{Name: "sandbox", FullName: "acme/sandbox", HTMLURL: "https://g/acme/sandbox", DefaultBranch: "main"}
	pr.Repos = []RepoData{{Repo: pRepo}} // detail never fetched

	ideas := &registry.Ideas{RoundupDate: "2026-10-31", Ideas: []registry.Idea{
		{ID: "beacon-01", Project: "beacon", Title: "Status badge", Pitch: "An SVG badge with the status.", Why: "Asked for twice.", Size: "S", Kind: "quick-win"},
		{ID: "beacon-02", Project: "beacon", Title: "Filed one", Size: "M", Kind: "bold"},
		{ID: "beacon-03", Project: "beacon", Title: "Already there", Size: "S", Kind: "hardening", ExistingIssue: 17},
		{ID: "ghost-01", Project: "ghost", Title: "Lost", Size: "S", Kind: "bold"},
		{ID: "sandbox-01", Project: "sandbox", Title: "Notebook export", Size: "L", Kind: "connective"},
	}}
	return SiteInput{
		Env:        env(t),
		Registry:   r,
		Projects:   []Input{as, pr, ea, em},
		Ideas:      ideas,
		IdeaMarker: "roadmap-idea:", // v0.1's marker: sites with issues filed under it keep it
		Filed:      map[string]forge.Filed{"beacon-02": {Number: 205, URL: "https://g/acme/beacon/issues/205"}},
		Heat:       []forge.HeatPoint{{Timestamp: testNow.Add(-time.Hour).Unix(), Contributions: 4}},
		Sources:    []model.SourceStatus{{Name: "gitea", OK: true, RefreshedAt: testNow}},
	}
}

func TestAssemble(t *testing.T) {
	out := Assemble(siteInput(t))
	s := out.Site
	if !s.GeneratedAt.Equal(testNow) || s.RoundupDate != "2026-10-31" || len(s.Sources) != 1 {
		t.Errorf("site header = %v %q %v", s.GeneratedAt, s.RoundupDate, s.Sources)
	}
	// Order: TierOrder, then last activity newest first.
	var order []string
	for _, p := range s.Projects {
		order = append(order, p.Slug+":"+string(p.Tier))
	}
	if got := strings.Join(order, " "); got != "beacon:active beacon-app:dormant sandbox:dormant old-site:archived" {
		t.Errorf("order = %s", got)
	}
	sandbox := s.ProjectBySlug("sandbox")
	if sandbox == nil || !sandbox.Auto || sandbox.Category != registry.UnsortedKey || sandbox.Summary != "Scratch space" {
		t.Errorf("sandbox = %+v", sandbox)
	}
	if sandbox.Tier != model.TierDormant || sandbox.Repo == nil {
		t.Errorf("sandbox without detail = %s repo %+v", sandbox.Tier, sandbox.Repo)
	}

	// Categories: registry order, counts, empty omitted, unsorted last.
	var cats []string
	for _, c := range s.Categories {
		cats = append(cats, c.Key)
		if c.Count == 0 {
			t.Errorf("empty category %s kept", c.Key)
		}
	}
	if got := strings.Join(cats, ","); got != "services,apps,mobile,unsorted" {
		t.Errorf("categories = %s", got)
	}
	if s.Categories[0].Count != 1 || s.Categories[0].Hue != 150 || s.Categories[2].Key != "mobile" || s.Categories[2].Hue != 300 {
		t.Errorf("services = %+v, mobile = %+v", s.Categories[0], s.Categories[2])
	}

	// Relations on both ends; unknown targets dropped.
	as := s.ProjectBySlug("old-site")
	if len(as.Relations) != 1 || as.Relations[0].To != "beacon" || as.Relations[0].Outgoing || as.Relations[0].Kind != "supersedes" || as.Relations[0].ToName != "Beacon" {
		t.Errorf("old-site relations = %+v", as.Relations)
	}
	em := s.ProjectBySlug("beacon")
	if len(em.Relations) != 2 {
		t.Errorf("beacon relations = %+v (ledger, atlas and harbour are not on this site)", em.Relations)
	}
	ea := s.ProjectBySlug("beacon-app")
	if len(ea.Relations) != 1 || ea.Relations[0].To != "beacon" || !ea.Relations[0].Outgoing {
		t.Errorf("beacon-app relations = %+v", ea.Relations)
	}
	if !hasWarning(out.Warnings, "ghost") {
		t.Errorf("warnings = %v", out.Warnings)
	}

	// Graph: every project a node, edges deduped, weights 0..1.
	if len(s.Graph.Nodes) != 4 || len(s.Graph.Edges) != 2 {
		t.Errorf("graph = %+v", s.Graph)
	}
	for _, n := range s.Graph.Nodes {
		if n.ID == "beacon" && n.Weight != 1 {
			t.Errorf("busiest node weight = %v", n.Weight)
		}
		if n.ID == "sandbox" && n.Weight != 0 {
			t.Errorf("idle node weight = %v", n.Weight)
		}
	}

	// Ideas: unknown project dropped, filed detected, file link prefilled.
	if len(s.Ideas) != 4 || s.Stats.Ideas != 4 || !hasWarning(out.Warnings, "ghost-01") {
		t.Errorf("ideas = %+v warnings %v", s.Ideas, out.Warnings)
	}
	byID := map[string]model.Idea{}
	for _, i := range s.Ideas {
		byID[i.ID] = i
		if i.Source != "file" || i.URL != "" {
			t.Errorf("file idea %s: source %q url %q", i.ID, i.Source, i.URL)
		}
	}
	i1 := byID["beacon-01"]
	u, err := url.Parse(i1.FileURL)
	if err != nil || !strings.HasPrefix(i1.FileURL, "https://git.example.com/acme/beacon/issues/new?") {
		t.Fatalf("file url = %q", i1.FileURL)
	}
	// The body names no product: the marker line, last, is what finds the
	// filed issue again.
	if want := "An SVG badge with the status.\n\nWhy: Asked for twice.\n\nFrom the ideas list.\nroadmap-idea:beacon-01"; u.Query().Get("title") != "Status badge" || u.Query().Get("body") != want {
		t.Errorf("file url query = %q, want body %q", u.Query(), want)
	}
	if i1.ProjectName != "Beacon" || i1.Icon != "bell-ring" {
		t.Errorf("idea project = %+v", i1)
	}
	if f := byID["beacon-02"]; f.FiledIssue != 205 || f.FiledURL == "" || f.FileURL != "" {
		t.Errorf("filed idea = %+v", f)
	}
	if e := byID["beacon-03"]; e.ExistingIssue != 17 || e.FileURL != "" {
		t.Errorf("existing-issue idea = %+v", e)
	}
	if p := byID["sandbox-01"]; !strings.HasPrefix(p.FileURL, "https://g/acme/sandbox/issues/new?") {
		t.Errorf("sandbox idea file url = %q", p.FileURL)
	}
	if len(em.Ideas) != 3 {
		t.Errorf("beacon ideas = %d", len(em.Ideas))
	}

	// Releases: all projects, newest first, last 18 months, deployed marked.
	if len(s.Releases) != 7 {
		t.Fatalf("releases = %+v", s.Releases)
	}
	if s.Releases[0].Tag != "v2.4.0" || s.Releases[0].Project != "beacon" {
		t.Errorf("newest release = %+v", s.Releases[0])
	}
	var deployed []string
	for i, r := range s.Releases {
		if i > 0 && r.Date.After(s.Releases[i-1].Date) {
			t.Errorf("releases not newest first at %d", i)
		}
		if r.Deployed {
			deployed = append(deployed, r.Project+" "+r.Tag)
		}
		if r.Tag == "v0.9.0" {
			t.Error("a tag older than 18 months was kept")
		}
	}
	if strings.Join(deployed, ",") != "beacon v2.3.1" {
		t.Errorf("deployed = %v", deployed)
	}

	// Stats and needs-you.
	st := s.Stats
	if st.Total != 4 || st.Active != 1 || st.Dormant != 2 || st.Archived != 1 || st.OpenIssues != 4 || st.OpenPRs != 2 || st.ReleasesLast30d != 2 {
		t.Errorf("stats = %+v", st)
	}
	if st.NeedsYou != len(s.NeedsYou) || len(s.NeedsYou) != 2 || s.NeedsYou[0].Verb != "Deploy" || s.NeedsYou[1].Verb != "Review" {
		t.Errorf("needs you = %+v", s.NeedsYou)
	}
	if len(s.Heatmap.Days) != 371 || s.Heatmap.Total != 4 {
		t.Errorf("heatmap = %d days total %d", len(s.Heatmap.Days), s.Heatmap.Total)
	}
	if fp, ok := out.Fingerprints["beacon"]; !ok || fp.Name != "Beacon" {
		t.Errorf("fingerprints = %+v", out.Fingerprints)
	}
	// JSON consumers get arrays, never null.
	if sandbox.Signals == nil || sandbox.Actions == nil || sandbox.Issues == nil || sandbox.Activity == nil || s.Events == nil {
		t.Error("nil slices left in the site")
	}
	// Relations and ideas are part of what a project shows, so of its hash.
	if em.Hash != Hash(*em) {
		t.Error("hash computed before relations/ideas were attached")
	}
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
