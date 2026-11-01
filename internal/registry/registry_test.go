package registry

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
)

var forgeIDs = []string{"gitea", "github"}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The seed registry and ideas (testdata/registry, the demo world's files) pin
// the parser's semantics; the repo-root files change whenever they are edited.
const (
	seedRegistry = "../../testdata/registry/seed.yaml"
	seedIdeas    = "../../testdata/registry/seed_ideas.json"
)

func TestParseSeedRegistry(t *testing.T) {
	r, err := Parse(readFile(t, seedRegistry), forgeIDs)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if r.Owner != "acme" {
		t.Errorf("owner = %q", r.Owner)
	}
	if len(r.Categories) != 6 || r.Categories[0].Key != "services" || r.Categories[0].Hue != 150 {
		t.Errorf("categories = %+v", r.Categories)
	}
	if last := r.Categories[len(r.Categories)-1]; last.Key != UnsortedKey || last.Icon != "folder-git-2" || last.Hue != 265 {
		t.Errorf("unsorted category not appended last: %+v", last)
	}
	if !r.Hidden("waymark-config") || !r.Hidden("Waymark-Config") || r.Hidden("beacon") {
		t.Error("hide_repos match is wrong")
	}
	b := r.Project("beacon")
	if b == nil {
		t.Fatal("beacon missing")
	}
	if b.Primary() != (RepoRef{Forge: "gitea", FullName: "acme/beacon"}) {
		t.Errorf("primary = %+v", b.Primary())
	}
	if len(b.URLs) != 1 || b.URLs[0].Probe != "http://sites.example.net/probe/beacon" || b.URLs[0].URL != "https://status.example.com" {
		t.Errorf("urls = %+v", b.URLs)
	}
	if len(b.Relations) != 3 || b.Relations[0].To != "ledger" || b.Relations[0].Kind != "depends-on" {
		t.Errorf("relations = %+v", b.Relations)
	}
	ba := r.Project("beacon-app")
	if ba.FDroid == nil || ba.FDroid.Package != "com.example.beacon" || len(ba.Notes) != 1 || ba.Notes[0] != "beacon_app.md" {
		t.Errorf("fdroid = %+v notes %v", ba.FDroid, ba.Notes)
	}
	h := r.Project("harbour")
	if m := h.Mirrors(); len(m) != 1 || m[0] != (RepoRef{Forge: "github", FullName: "acme/harbour"}) {
		t.Errorf("mirrors = %+v", m)
	}
	if !r.Project("old-site").Retired {
		t.Error("old-site should be retired")
	}
	if ik := r.Project("inkwell"); ik.Hold == nil || ik.Hold.Kind != "parked" || !ik.Hold.Since.Equal(time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("inkwell hold = %+v", ik.Hold)
	}
}

func TestParseDefaultsAndHold(t *testing.T) {
	src := `
owner: acme
categories:
  - {key: media, label: Media, icon: clapperboard, hue: 20}
projects:
  - slug: brochure
    category: media
    repos:
      - gitea: other/brochure-site
    hold: {kind: frozen, since: 2026-05-29, reason: Production is frozen}
  - slug: lost
    category: nope
`
	r, err := Parse([]byte(src), forgeIDs)
	if err != nil {
		t.Fatal(err)
	}
	j := r.Project("brochure")
	if j.Name != "brochure" {
		t.Errorf("name default = %q", j.Name)
	}
	if j.Icon != "clapperboard" {
		t.Errorf("icon default = %q, want the category icon", j.Icon)
	}
	if j.Primary().FullName != "other/brochure-site" {
		t.Errorf("owner/name repo = %q", j.Primary().FullName)
	}
	if j.Hold == nil || j.Hold.Kind != "frozen" || !j.Hold.Since.Equal(time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("hold = %+v", j.Hold)
	}
	if l := r.Project("lost"); l.Category != UnsortedKey {
		t.Errorf("unknown category kept: %q", l.Category)
	}
	if len(r.Warnings) == 0 {
		t.Error("unknown category should warn")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"not yaml":       "projects: [",
		"duplicate slug": "projects:\n  - {slug: a}\n  - {slug: a}\n",
		"missing slug":   "projects:\n  - {name: A}\n",
		"bad slug":       "projects:\n  - {slug: 'Has Space'}\n",
		"bad hold kind":  "projects:\n  - {slug: a, hold: {kind: maybe, since: 2026-01-01}}\n",
		"bad hold date":  "projects:\n  - {slug: a, hold: {kind: parked, since: soon}}\n",
	}
	cases["unknown forge key"] = "projects:\n  - {slug: a, repos: [{svn: acme/a}]}\n"
	cases["bare repo, no owner"] = "projects:\n  - {slug: a, repos: [{gitea: a}]}\n"
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			// The file has whatever name the config gives it: the error says
			// "projects file", never the 0.1.0 name.
			_, err := Parse([]byte(src), forgeIDs)
			if err == nil || !strings.HasPrefix(err.Error(), "projects file: ") || strings.Contains(err.Error(), "registry.yaml") {
				t.Errorf("err = %v, want one starting \"projects file: \"", err)
			}
		})
	}
}

func TestAutoProjects(t *testing.T) {
	r, err := Parse(readFile(t, seedRegistry), forgeIDs)
	if err != nil {
		t.Fatal(err)
	}
	repos := []forge.Repo{
		{Forge: "gitea", FullName: "acme/beacon", Name: "beacon", Description: "x"},
		{Forge: "gitea", FullName: "acme/waymark-config", Name: "waymark-config"},
		{Forge: "gitea", FullName: "acme/Ledger", Name: "Ledger", Description: "Billing and invoices API."},
		{Forge: "gitea", FullName: "acme/sandbox", Name: "sandbox"},
		{Forge: "gitea", FullName: "acme/harbour", Name: "harbour"},
	}
	auto := r.AutoProjects(repos, forgeIDs)
	if len(auto) != 2 {
		t.Fatalf("auto = %+v", auto)
	}
	p := auto[0]
	if p.Slug != "ledger" || p.Name != "Ledger" || p.Category != UnsortedKey || p.Icon != "folder-git-2" || !p.Auto {
		t.Errorf("ledger auto = %+v", p)
	}
	if p.Summary != "Billing and invoices API." || p.Primary() != (RepoRef{Forge: "gitea", FullName: "acme/Ledger"}) {
		t.Errorf("ledger summary/repo = %q %+v", p.Summary, p.Primary())
	}
	if auto[1].Summary != "No description yet." {
		t.Errorf("empty description summary = %q", auto[1].Summary)
	}
}

func TestAutoProjectSlugCollision(t *testing.T) {
	r, err := Parse([]byte("projects:\n  - {slug: ledger, name: Ledger docs}\n"), forgeIDs)
	if err != nil {
		t.Fatal(err)
	}
	auto := r.AutoProjects([]forge.Repo{{Forge: "gitea", FullName: "acme/Ledger", Name: "Ledger"}}, forgeIDs)
	if len(auto) != 1 || auto[0].Slug != "ledger-repo" {
		t.Errorf("collision slug = %+v", auto)
	}
}

// v01Shape is a registry in the v0.1 shape: an owner, bare gitea names, a
// github mirror and a repo with an explicit owner.
const v01Shape = `
owner: acme
categories:
  - {key: tools, label: Tools, icon: wrench, hue: 40}
hide_repos: [scratch]
projects:
  - slug: tool
    name: Tool
    category: tools
    repos:
      - gitea: tool
      - github: acme-mirror/tool
  - slug: site
    category: tools
    repos:
      - gitea: other/site
    plan_dirs: [docs/plans]
  - slug: server
    category: tools
`

func TestParseV01RegistryUnchanged(t *testing.T) {
	r, err := Parse([]byte(v01Shape), forgeIDs)
	if err != nil {
		t.Fatal(err)
	}
	if r.Owner != "acme" || len(r.Projects) != 3 {
		t.Fatalf("owner/projects = %q %d", r.Owner, len(r.Projects))
	}
	tool := r.Project("tool")
	if tool.Primary() != (RepoRef{Forge: "gitea", FullName: "acme/tool"}) {
		t.Errorf("bare name not qualified by owner: %+v", tool.Primary())
	}
	if m := tool.Mirrors(); len(m) != 1 || m[0] != (RepoRef{Forge: "github", FullName: "acme-mirror/tool"}) {
		t.Errorf("mirrors = %+v", m)
	}
	if got := r.Project("site").Primary().FullName; got != "other/site" {
		t.Errorf("explicit owner = %q", got)
	}
	if pd := r.Project("site").PlanDirs; len(pd) != 1 || pd[0] != "docs/plans" {
		t.Errorf("plan_dirs = %v", pd)
	}
	if r.Project("server").Primary() != (RepoRef{}) || len(r.Project("server").Mirrors()) != 0 {
		t.Error("a project with no repos has the zero primary and no mirrors")
	}
	if !r.Hidden("Scratch") {
		t.Error("hide_repos lost")
	}
}

func TestBareRepoNeedsAnOwner(t *testing.T) {
	_, err := Parse([]byte("projects:\n  - slug: a\n    repos:\n      - gitea: tool\n"), forgeIDs)
	if err == nil || !strings.Contains(err.Error(), "tool") {
		t.Errorf("bare repo without owner: err = %v", err)
	}
}

func TestUnknownForgeKey(t *testing.T) {
	src := "projects:\n  - slug: a\n    repos:\n      - gitea: acme/a\n      - forgejo: acme/a\n"
	_, err := Parse([]byte(src), forgeIDs)
	if err == nil || !strings.Contains(err.Error(), "forgejo") {
		t.Fatalf("err = %v, want one naming forgejo", err)
	}
	if _, err := Parse([]byte(src), []string{"gitea", "forgejo"}); err != nil {
		t.Errorf("a configured forge id must be accepted: %v", err)
	}
	for name, bad := range map[string]string{
		"two keys":  "projects:\n  - slug: a\n    repos:\n      - {gitea: acme/a, github: acme/a}\n",
		"scalar":    "projects:\n  - slug: a\n    repos:\n      - acme/a\n",
		"empty val": "projects:\n  - slug: a\n    repos:\n      - gitea: \"\"\n",
	} {
		if _, err := Parse([]byte(bad), forgeIDs); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func repo(forgeID, full string, mirror bool) forge.Repo {
	_, name, _ := strings.Cut(full, "/")
	owner, _, _ := strings.Cut(full, "/")
	return forge.Repo{Forge: forgeID, FullName: full, Name: name, Owner: forge.User{Login: owner}, Mirror: mirror}
}

func TestAutoProjectsPairsMirrors(t *testing.T) {
	r := Empty()
	auto := r.AutoProjects([]forge.Repo{
		repo("github", "acme/Tool", false),
		repo("gitea", "acme/tool", false),
		repo("github", "acme/solo", false),
	}, forgeIDs)
	if len(auto) != 2 {
		t.Fatalf("auto = %+v", auto)
	}
	tool := auto[0]
	if tool.Primary() != (RepoRef{Forge: "gitea", FullName: "acme/tool"}) {
		t.Errorf("primary = %+v, want the gitea repo (earliest in forge order)", tool.Primary())
	}
	if m := tool.Mirrors(); len(m) != 1 || m[0] != (RepoRef{Forge: "github", FullName: "acme/Tool"}) {
		t.Errorf("mirrors = %+v", m)
	}
	if auto[1].Primary() != (RepoRef{Forge: "github", FullName: "acme/solo"}) {
		t.Errorf("solo = %+v", auto[1].Repos)
	}
}

func TestPullMirrorNeverPrimary(t *testing.T) {
	auto := Empty().AutoProjects([]forge.Repo{
		repo("gitea", "acme/tool", true),
		repo("github", "acme/tool", false),
	}, forgeIDs)
	if len(auto) != 1 {
		t.Fatalf("auto = %+v", auto)
	}
	if auto[0].Primary() != (RepoRef{Forge: "github", FullName: "acme/tool"}) {
		t.Errorf("primary = %+v, want github: the gitea twin is a pull mirror", auto[0].Primary())
	}
	if m := auto[0].Mirrors(); len(m) != 1 || m[0].Forge != "gitea" {
		t.Errorf("mirrors = %+v", m)
	}
	// Both mirrors: the flag decides nothing, the order does.
	auto = Empty().AutoProjects([]forge.Repo{
		repo("github", "acme/tool", true),
		repo("gitea", "acme/tool", true),
	}, forgeIDs)
	if len(auto) != 1 || auto[0].Primary().Forge != "gitea" {
		t.Errorf("both mirrors: %+v", auto)
	}
}

func TestAutoProjectsSkipsClaimedAndHidden(t *testing.T) {
	r, err := Parse([]byte(v01Shape), forgeIDs)
	if err != nil {
		t.Fatal(err)
	}
	auto := r.AutoProjects([]forge.Repo{
		repo("gitea", "acme/tool", false),         // claimed (bare name + owner)
		repo("github", "acme-mirror/tool", false), // claimed on github
		repo("gitea", "other/site", false),        // claimed with an explicit owner
		repo("gitea", "acme/Scratch", false),      // hidden, any case
		repo("gitea", "acme/fresh", false),        // new
		repo("github", "acme-mirror/site", false), // twin of a file project's repo: attached, not auto
	}, forgeIDs)
	if len(auto) != 1 || auto[0].Primary().FullName != "acme/fresh" {
		t.Errorf("auto = %+v, want only acme/fresh", auto)
	}
}

func TestUnclaimedTwinAttachesToFileProject(t *testing.T) {
	r, err := Parse([]byte(v01Shape), forgeIDs)
	if err != nil {
		t.Fatal(err)
	}
	repos := []forge.Repo{
		repo("github", "acme-mirror/Site", false),   // name matches other/site, project has no github repo
		repo("github", "acme-mirror/tool2", false),  // no such name
		repo("github", "acme/server", false),        // project server has no repos to match
		repo("github", "acme-mirror/tool-x", false), // no match
		repo("gitea", "acme/other-tool", false),     // no match
		repo("github", "second/tool", false),        // tool already has a github repo: stays separate
	}
	all := r.AllProjects(repos, forgeIDs)
	site := all[1]
	if site.Slug != "site" || len(site.Repos) != 2 || site.Repos[1] != (RepoRef{Forge: "github", FullName: "acme-mirror/Site"}) {
		t.Fatalf("site = %+v", site.Repos)
	}
	if m := site.Mirrors(); len(m) != 1 || m[0].Forge != "github" {
		t.Errorf("mirrors = %+v", m)
	}
	if len(all[0].Repos) != 2 {
		t.Errorf("tool gained a repo although it already has one on github: %+v", all[0].Repos)
	}
	for _, p := range all[3:] {
		if !p.Auto {
			t.Errorf("%s should be auto", p.Slug)
		}
		if p.Primary().FullName == "acme-mirror/Site" {
			t.Error("the attached repo also became an auto project")
		}
	}
	if len(all) != 3+5 {
		t.Errorf("projects = %d, want 3 file + 5 auto", len(all))
	}
	// The stored registry is untouched.
	if n := len(r.Project("site").Repos); n != 1 {
		t.Errorf("stored project mutated: %d repos", n)
	}
}

func TestFullReposAndMirrors(t *testing.T) {
	src := "projects:\n  - slug: p\n    repos:\n      - gitea: acme/one\n      - github: acme/one\n      - gitea: acme/two\n"
	r, err := Parse([]byte(src), forgeIDs)
	if err != nil {
		t.Fatal(err)
	}
	p := r.Project("p")
	full := p.FullRepos()
	if len(full) != 2 || full[0].FullName != "acme/one" || full[1].FullName != "acme/two" {
		t.Errorf("FullRepos = %+v", full)
	}
	if m := p.Mirrors(); len(m) != 1 || m[0] != (RepoRef{Forge: "github", FullName: "acme/one"}) {
		t.Errorf("Mirrors = %+v", m)
	}
	var none Project
	if none.FullRepos() != nil || none.Mirrors() != nil || none.FirstOn("gitea") != (RepoRef{}) {
		t.Error("a project with no repos has no full repos, mirrors or first")
	}
}

func TestAutoPairingPrefersSameOwner(t *testing.T) {
	auto := Empty().AutoProjects([]forge.Repo{
		repo("gitea", "other/tool", false),
		repo("gitea", "acme/tool", false),
		repo("github", "acme/tool", false),
	}, forgeIDs)
	if len(auto) != 2 {
		t.Fatalf("auto = %+v", auto)
	}
	if auto[0].Primary().FullName != "other/tool" || len(auto[0].Repos) != 1 {
		t.Errorf("other/tool should stand alone: %+v", auto[0].Repos)
	}
	if len(auto[1].Repos) != 2 || auto[1].Mirrors()[0] != (RepoRef{Forge: "github", FullName: "acme/tool"}) {
		t.Errorf("acme/tool should pair with its github twin: %+v", auto[1].Repos)
	}
}

func TestSameNameOnOneForgeStaysTwoProjects(t *testing.T) {
	auto := Empty().AutoProjects([]forge.Repo{repo("gitea", "acme/tool", false), repo("gitea", "other/tool", false)}, forgeIDs)
	if len(auto) != 2 || auto[0].Slug != "tool" || auto[1].Slug != "tool-repo" {
		t.Errorf("auto = %+v", auto)
	}
}

func TestEmptyRegistry(t *testing.T) {
	r := Empty()
	if len(r.Projects) != 0 || len(r.Categories) != 1 || r.Categories[0].Key != UnsortedKey {
		t.Errorf("Empty = %+v", r)
	}
	if r.Hidden("x") || r.Project("x") != nil {
		t.Error("Empty hides and holds nothing")
	}
}

func TestParseIdeas(t *testing.T) {
	ideas, err := ParseIdeas(readFile(t, seedIdeas))
	if err != nil {
		t.Fatal(err)
	}
	if ideas.RoundupDate != "2026-10-30" || len(ideas.Ideas) != 7 {
		t.Fatalf("ideas = %+v", ideas)
	}
	i := ideas.Ideas[0]
	if i.ID != "beacon-01" || i.Project != "beacon" || i.Size != "S" || i.Kind != "quick-win" || i.Why != "Asked for twice this month." {
		t.Errorf("idea = %+v", i)
	}
	if _, err := ParseIdeas([]byte(`{"ideas": [{"id": ""}]}`)); err == nil {
		t.Error("idea without id should fail")
	}
	if _, err := ParseIdeas([]byte(`{"ideas": [{"id": "a"}, {"id": "a"}]}`)); err == nil {
		t.Error("duplicate idea id should fail")
	}
}
