package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/sources/probe"
	"github.com/Will-Luck/waymark/internal/web"
)

// homepages serves websites: /down answers 503, anything else 200. It
// records the paths asked for.
type homepages struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	uas   []string
}

func newHomepages(t *testing.T) *homepages {
	h := &homepages{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.paths = append(h.paths, r.URL.Path)
		h.uas = append(h.uas, r.UserAgent())
		h.mu.Unlock()
		if r.URL.Path == "/down" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *homepages) asked(path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.paths {
		if p == path {
			return true
		}
	}
	return false
}

// Repo websites are probed when probes.homepages is on: once per URL, not
// when a projects-file url already probes it, and never for a repo left out
// as private (listed, or named in the projects file).
func TestHomepageProbed(t *testing.T) {
	hp := newHomepages(t)
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "tok", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day), Website: hp.srv.URL + "/tool"},
		&acmeRepo{Owner: "acme", Name: "box", Updated: fixedNow.Add(-day), Website: hp.srv.URL + "/down"},
		&acmeRepo{Owner: "acme", Name: "site", Updated: fixedNow.Add(-day), Website: hp.srv.URL + "/site"},
		&acmeRepo{Owner: "acme", Name: "notes", Updated: fixedNow.Add(-day), Website: "docs.example.com"},
		&acmeRepo{Owner: "acme", Name: "secret", Private: true, Updated: fixedNow.Add(-day), Website: hp.srv.URL + "/secret"},
		// Outside owners, so read by name because the projects file names it.
		&acmeRepo{Owner: "other", Name: "vault", Private: true, Updated: fixedNow.Add(-day), Website: hp.srv.URL + "/vault"})
	file := projectsFile(t, "projects:\n  - slug: site\n    name: Site\n    repos:\n      - gitea: acme/site\n      - gitea: other/vault\n    urls:\n      - {label: Live, url: \""+hp.srv.URL+"/site\", probe: \""+hp.srv.URL+"/site\"}\n")
	app := func(private bool) *App {
		a := h.app(func(c *config.Config) {
			c.Probes.Homepages, c.IncludePrivate, c.Projects = true, private, file
		}, g)
		// httptest serves on loopback, which the real homepage client refuses
		// (TestHomepageNotPublicNotProbed); this test is about the rest.
		a.homeC = probe.NewClient()
		return a
	}
	s := runOnce(t, app(false))

	tool := s.ProjectBySlug("tool")
	if tool == nil || len(tool.Probes) != 1 {
		t.Fatalf("tool probes = %+v", tool)
	}
	if pr := tool.Probes[0]; !pr.Homepage || pr.Label != "Homepage" || pr.URL != hp.srv.URL+"/tool" || !pr.Up || pr.CheckedAt.IsZero() || len(pr.History) != 1 {
		t.Errorf("tool homepage probe = %+v", pr)
	}
	if box := s.ProjectBySlug("box"); box == nil || len(box.Probes) != 1 || box.Probes[0].Up || box.Probes[0].Code != 503 {
		t.Errorf("box probes = %+v", box)
	}
	// A down homepage raises nothing.
	for _, n := range s.NeedsYou {
		if n.Verb == "Fix" {
			t.Errorf("a homepage raised %+v", n)
		}
	}
	site := s.ProjectBySlug("site")
	if site == nil || len(site.Probes) != 1 || site.Probes[0].Homepage || site.Probes[0].Label != "Live" {
		t.Errorf("site probes = %+v (the url probe, not a second homepage one)", site)
	}
	if n := s.ProjectBySlug("notes"); n == nil || len(n.Probes) != 0 {
		t.Errorf("a website that is not an http(s) URL was probed: %+v", n)
	}
	for _, path := range []string{"/secret", "/vault"} {
		if hp.asked(path) {
			t.Errorf("a private repo's homepage %s was probed with include_private false", path)
		}
	}

	// Control: with private repos shown, the same homepages are probed.
	s = runOnce(t, app(true))
	for _, path := range []string{"/secret", "/vault"} {
		if !hp.asked(path) {
			t.Errorf("include_private true: %s was not probed", path)
		}
	}
	if site := s.ProjectBySlug("site"); site == nil || len(site.Probes) != 2 || !site.Probes[1].Homepage || site.Probes[1].URL != hp.srv.URL+"/vault" {
		t.Errorf("include_private true: site probes = %+v", site)
	}

	// Off, nothing is probed.
	hp2 := newHomepages(t)
	h2 := newAcmeHost(t)
	g2 := h2.forge("gitea", "gitea", "tok", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day), Website: hp2.srv.URL + "/tool"})
	a := h2.app(nil, g2)
	a.homeC = probe.NewClient()
	s = runOnce(t, a)
	if p := s.ProjectBySlug("tool"); p == nil || len(p.Probes) != 0 || hp2.asked("/tool") {
		t.Errorf("homepages off: probes %+v", p)
	}
}

// Spec ruling 33: a homepage comes from repo metadata, so it must not make
// the server reach a local or private address. The check is on the resolved
// address; the homepage shows as not probed, never as down. A projects-file
// url to the same server is the operator's choice and is probed.
func TestHomepageNotPublicNotProbed(t *testing.T) {
	hp := newHomepages(t)
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "tok", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day), Website: hp.srv.URL + "/tool"},
		&acmeRepo{Owner: "acme", Name: "lan", Updated: fixedNow.Add(-day)})
	file := projectsFile(t, "projects:\n  - slug: lan\n    name: Lan\n    repos:\n      - gitea: acme/lan\n    urls:\n      - {label: Live, url: \""+hp.srv.URL+"/lan\", probe: \""+hp.srv.URL+"/lan\"}\n")
	a := h.app(func(c *config.Config) { c.Probes.Homepages, c.Projects = true, file }, g)
	s := runOnce(t, a)
	if hp.asked("/tool") {
		t.Error("a homepage on a loopback address was requested")
	}
	tool := s.ProjectBySlug("tool")
	if tool == nil || len(tool.Probes) != 1 {
		t.Fatalf("tool = %+v", tool)
	}
	if pr := tool.Probes[0]; pr.NotProbed == "" || pr.Up || !pr.CheckedAt.IsZero() || len(pr.History) != 0 || pr.Code != 0 {
		t.Errorf("refused homepage = %+v, want not probed with a reason", pr)
	}
	for _, n := range s.NeedsYou {
		if n.Verb == "Fix" {
			t.Errorf("a refused homepage raised %+v", n)
		}
	}
	if !hp.asked("/lan") {
		t.Error("a projects-file url on the LAN was not probed")
	}
	if lan := s.ProjectBySlug("lan"); lan == nil || len(lan.Probes) != 1 || !lan.Probes[0].Up || lan.Probes[0].NotProbed != "" {
		t.Errorf("lan = %+v", lan)
	}
}

// The site's Meta comes from the config, the notes adapter and the data.
func TestSiteMeta(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "tok", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	a := h.app(func(c *config.Config) { c.Site.Title = "Acme status" }, g)
	if m := a.Snapshot().Meta; m.Title != "Acme status" {
		t.Errorf("before the first cycle: meta %+v", m)
	}
	s := runOnce(t, a)
	if m := s.Meta; m.Title != "Acme status" || m.Notes || m.Docker || m.Ideas || m.FDroid || m.NotesLabel != "" || len(m.Forges) != 1 || m.Forges["gitea"] != "Gitea" {
		t.Errorf("nothing configured: meta %+v", m)
	}

	h = newAcmeHost(t)
	g = h.forge("gitea", "gitea", "tok", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day),
		Issues: []acmeIssue{{N: 4, Title: "Dark mode", Updated: fixedNow.Add(-day), Labels: []string{"idea"}}}})
	index := newHomepages(t).srv.URL + "/repo/index-v1.json" // read, not parsed: the flag is from the projects file
	a = h.app(func(c *config.Config) {
		c.Notes = config.Notes{Type: "markdown", Dir: t.TempDir(), Markers: []string{"WAITING:"}}
		c.Docker.Feed = t.TempDir() + "/docker.json"
		c.Ideas.Labels = []string{"idea", "enhancement"}
		c.Projects = projectsFile(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n    fdroid: {index: \""+index+"\", package: com.example.tool}\n")
	}, g)
	s = runOnce(t, a)
	if m := s.Meta; !m.Notes || m.NotesLabel != "Notes" || !m.Docker || !m.Ideas || !m.FDroid || strings.Join(m.IdeaLabels, ",") != "idea,enhancement" {
		t.Errorf("everything configured: meta %+v", m)
	}
}

// A GitHub-only site never says Gitea, on the page or in its data.
func TestGitHubOnlySiteSaysGitHub(t *testing.T) {
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "tok", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.0.0"}, CI: "success", Updated: fixedNow.Add(-day),
			Issues: []acmeIssue{{N: 2, Title: "Crash", Updated: fixedNow.Add(-day)}}})
	s := runOnce(t, h.app(nil, gh))
	p := s.ProjectBySlug("tool")
	if p == nil || p.Forge != "GitHub" || len(p.Links) == 0 || p.Links[0].Label != "GitHub" {
		t.Fatalf("tool = %+v", p)
	}
	srv := web.New(model.NewStaticSource(s), web.Options{Version: "test"})
	for _, path := range []string{"/", "/p/tool", "/api/site.json", "/api/search.json"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK || strings.Contains(string(body), "Gitea") {
			t.Errorf("%s: %d, says Gitea %v", path, rec.Code, strings.Contains(string(body), "Gitea"))
		}
	}
}

// Every request names Waymark and its version: waymark/<version> to the
// forges (GitHub asks every API client for a User-Agent), the F-Droid index
// and the Docker Engine API, waymark-probe/<version> for probes. A config
// with no version (tests) says dev.
func TestUserAgents(t *testing.T) {
	for _, c := range []struct{ version, client, probe string }{
		{"1.2.3", "waymark/1.2.3", "waymark-probe/1.2.3"},
		{"", "waymark/dev", "waymark-probe/dev"},
	} {
		hp := newHomepages(t)
		h := newAcmeHost(t)
		g := h.forge("gitea", "gitea", "tok", []string{"acme"},
			&acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.0.0"}, CI: "success", Updated: fixedNow.Add(-day), Website: hp.srv.URL + "/tool"})
		gh := h.forge("github", "github", "tok", []string{"acme"},
			&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
		file := projectsFile(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n    fdroid: {index: \""+hp.srv.URL+"/fdroid/index-v1.json\", package: com.example.tool}\n")
		a := h.app(func(cfg *config.Config) {
			cfg.Version, cfg.Probes.Homepages, cfg.Projects = c.version, true, file
			cfg.Docker.Host = "tcp://" + strings.TrimPrefix(hp.srv.URL, "http://")
		}, g, gh)
		a.homeC = probe.NewClient()
		runOnce(t, a)
		h.edit(func() {
			if len(h.ua) != 1 || !h.ua[c.client] {
				t.Errorf("version %q: forge User-Agents = %v, want only %q", c.version, h.ua, c.client)
			}
		})
		hp.mu.Lock()
		want := map[string]string{"/tool": c.probe, "/fdroid/index-v1.json": c.client, "/containers/json": c.client}
		for i, path := range hp.paths {
			if hp.uas[i] != want[path] {
				t.Errorf("version %q: %s User-Agent = %q, want %q", c.version, path, hp.uas[i], want[path])
			}
			delete(want, path)
		}
		if len(want) > 0 {
			t.Errorf("version %q: never asked for %v", c.version, want)
		}
		hp.mu.Unlock()
	}
}

// Each forge's page name, once: by kind and URL, with the id added only when
// two forges share a name.
func TestForgeNames(t *testing.T) {
	for _, c := range []struct {
		forges []config.Forge
		want   map[string]string
	}{
		{[]config.Forge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com"}, {ID: "github", Type: "github", URL: "https://api.github.com"}},
			map[string]string{"gitea": "Gitea", "github": "GitHub"}},
		{[]config.Forge{{ID: "cb", Type: "gitea", URL: "https://codeberg.org"}, {ID: "gh", Type: "github", URL: "https://api.github.com"}},
			map[string]string{"cb": "Codeberg", "gh": "GitHub"}},
		{[]config.Forge{{ID: "work", Type: "gitea", URL: "https://git.example.com"}, {ID: "home", Type: "gitea", URL: "https://git.example.net"},
			{ID: "gh", Type: "github", URL: "https://api.github.com"}},
			map[string]string{"work": "Gitea (work)", "home": "Gitea (home)", "gh": "GitHub"}},
	} {
		got := forgeNames(c.forges)
		if len(got) != len(c.want) {
			t.Errorf("forgeNames = %v, want %v", got, c.want)
		}
		for id, name := range c.want {
			if got[id] != name {
				t.Errorf("forgeNames[%s] = %q, want %q", id, got[id], name)
			}
		}
	}
}
