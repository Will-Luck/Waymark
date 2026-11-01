package app

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/docker"
)

func ctr(name, state, source string) docker.Container {
	c := docker.Container{Name: name, State: state, Image: "ghcr.io/acme/tool:1.2.0", Labels: map[string]string{}}
	if source != "" {
		c.Labels[ociSourceLabel] = source
	}
	return c
}

func names(cs []docker.Container) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func TestOCISourceMatches(t *testing.T) {
	const page = "https://github.com/acme/tool"
	tool := registry.Project{Slug: "tool"}
	urls := func(p registry.Project) []string {
		if p.Slug == "tool" {
			return []string{page, "https://git.example.net/acme/tool"}
		}
		return []string{"https://github.com/acme/other"}
	}
	for _, src := range []string{page, "HTTPS://GitHub.com/Acme/Tool", page + ".git", page + "/", page + ".git/", "  " + page} {
		feed := &docker.Feed{Containers: []docker.Container{ctr("web", "running", src)}}
		if got := names(assignContainers([]registry.Project{tool}, feed, urls)["tool"]); !slices.Equal(got, []string{"web"}) {
			t.Errorf("source %q: containers = %v", src, got)
		}
	}
	// A mirror's page matches too.
	feed := &docker.Feed{Containers: []docker.Container{ctr("web", "running", "https://git.example.net/acme/tool.git")}}
	if got := names(assignContainers([]registry.Project{tool}, feed, urls)["tool"]); !slices.Equal(got, []string{"web"}) {
		t.Errorf("mirror: containers = %v", got)
	}
	// Near misses and unlabelled containers are not matches.
	feed = &docker.Feed{Containers: []docker.Container{
		ctr("a", "running", "https://github.com/acme/tool-extra"),
		ctr("b", "running", page+"/tree/release"),
		ctr("c", "running", "https://github.com/acme"),
		ctr("d", "running", ""),
		ctr("e", "running", "https://github.com/acme/other"),
	}}
	got := assignContainers([]registry.Project{tool, {Slug: "other"}}, feed, urls)
	if len(got["tool"]) != 0 || !slices.Equal(names(got["other"]), []string{"e"}) {
		t.Errorf("assigned = %v", got)
	}
	// One project only: the first claimer keeps it. The slug container goes
	// to its slug project even when its label points at another.
	feed = &docker.Feed{Containers: []docker.Container{ctr("shared", "running", page), ctr("other", "running", page)}}
	two := []registry.Project{tool, {Slug: "tool2"}, {Slug: "other"}}
	urls2 := func(p registry.Project) []string { return []string{page} }
	got = assignContainers(two, feed, urls2)
	if !slices.Equal(names(got["other"]), []string{"other"}) || !slices.Equal(names(got["tool"]), []string{"shared"}) || len(got["tool2"]) != 0 {
		t.Errorf("exclusive = %v", got)
	}
	// A running container is the primary ahead of a stopped one; the slug one ahead of both.
	feed = &docker.Feed{Containers: []docker.Container{ctr("tool-prev", "exited", page), ctr("tool-new", "running", page), ctr("tool", "exited", "")}}
	got = assignContainers([]registry.Project{tool}, feed, urls)
	if !slices.Equal(names(got["tool"]), []string{"tool", "tool-new", "tool-prev"}) {
		t.Errorf("order = %v", names(got["tool"]))
	}
}

func TestDeclaredBeatsOCI(t *testing.T) {
	const page = "https://github.com/acme/tool"
	urls := func(registry.Project) []string { return []string{page} }
	feed := &docker.Feed{Containers: []docker.Container{ctr("web", "running", page), ctr("worker", "running", page)}}
	// "web" is named by another project, so the label does not pull it to tool.
	projects := []registry.Project{{Slug: "tool"}, {Slug: "site", Containers: []string{"web"}}}
	got := assignContainers(projects, feed, urls)
	if !slices.Equal(names(got["tool"]), []string{"worker"}) {
		t.Errorf("tool = %v, want only worker", names(got["tool"]))
	}
	if len(got["site"]) != 0 {
		t.Errorf("a project with a list is read from the list, not assigned: %v", got["site"])
	}
	// A project with its own list never takes label matches either.
	projects = []registry.Project{{Slug: "tool", Containers: []string{"gone"}}}
	if got := assignContainers(projects, feed, urls); len(got["tool"]) != 0 {
		t.Errorf("listed project gained %v", names(got["tool"]))
	}
	// A slug container that another project names is that project's.
	projects = []registry.Project{{Slug: "web"}, {Slug: "site", Containers: []string{"web"}}}
	if got := assignContainers(projects, feed, func(registry.Project) []string { return nil }); len(got["web"]) != 0 {
		t.Errorf("web project took a declared container: %v", names(got["web"]))
	}
}

// dockerAPI serves the Engine API's container list over a unix socket.
type dockerAPI struct {
	mu    sync.Mutex
	list  []map[string]any
	paths []string
	sock  string
}

func newDockerAPI(t *testing.T) *dockerAPI {
	t.Helper()
	d := &dockerAPI{sock: filepath.Join(t.TempDir(), "docker.sock")}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.paths = append(d.paths, r.Method+" "+r.URL.RequestURI())
		_ = json.NewEncoder(w).Encode(d.list)
	}))
	l, err := net.Listen("unix", d.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return d
}

func apiCtr(name, state, source string) map[string]any {
	labels := map[string]string{}
	if source != "" {
		labels[ociSourceLabel] = source
	}
	return map[string]any{"Id": name + "-id", "Names": []string{"/" + name}, "Image": "ghcr.io/acme/tool:1.2.0", "State": state, "Status": "Up 3 hours", "Created": 1793000000, "Labels": labels}
}

func projectsFile(t *testing.T, body string) config.FileRef {
	t.Helper()
	f := filepath.Join(t.TempDir(), "projects.yaml")
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return config.FileRef{File: f}
}

func TestDockerAPIMatchesBySourceLabel(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "tok", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.2.0"}, Updated: fixedNow.Add(-day)},
		&acmeRepo{Owner: "acme", Name: "box", Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day)},
		&acmeRepo{Owner: "acme", Name: "lib", Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "tok2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "lib", Stars: 3, Updated: fixedNow.Add(-day)})
	page := "https://gitea.example.com/acme/tool"
	d := newDockerAPI(t)
	d.list = []map[string]any{apiCtr("tool-app", "running", page+".git/"), apiCtr("unrelated", "running", "https://github.com/acme/unrelated"), apiCtr("lib-app", "running", "https://GITHUB.example.com/acme/lib"), apiCtr("box-elsewhere", "running", "https://gitea.example.com/acme/lib")}
	a := h.app(func(c *config.Config) {
		c.Docker = config.Docker{Host: "unix://" + d.sock, Label: "Docker"}
		c.Projects = projectsFile(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n  - slug: box\n    name: Box\n    repos:\n      - gitea: acme/box\n  - slug: lib\n    name: Lib\n    repos:\n      - gitea: acme/lib\n      - github: acme/lib\n")
	}, g, gh)
	s := runOnce(t, a)
	p := s.ProjectBySlug("tool")
	if p == nil || p.Deploy == nil || p.Deploy.Container != "tool-app" || p.Deploy.Version != "1.2.0" || p.Deploy.State != "running" {
		t.Fatalf("tool deploy = %+v", p)
	}
	if l := s.ProjectBySlug("lib"); l == nil || l.Deploy == nil || l.Deploy.Container != "lib-app" || l.Deploy.State != "running" {
		t.Errorf("a container matched through the mirror's page: %+v", l)
	}
	if b := s.ProjectBySlug("box"); b == nil || b.Deploy != nil {
		t.Errorf("box must not gain a container (one labelled for lib, one for nobody): %+v", b)
	}
	if !slices.ContainsFunc(s.Sources, func(st model.SourceStatus) bool { return st.Name == "docker" && st.OK }) {
		t.Errorf("sources = %+v", s.Sources)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.paths {
		if p != "GET /containers/json?all=1" {
			t.Errorf("unexpected engine request %q", p)
		}
	}
}

func TestDockerAPIDownKeepsLastReadAndSaysSo(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "tok", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	d := newDockerAPI(t)
	d.list = []map[string]any{apiCtr("tool", "running", "")}
	a := h.app(func(c *config.Config) {
		c.Docker = config.Docker{Host: "unix://" + d.sock, Label: "Docker"}
		c.Projects = projectsFile(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n")
	}, g)
	runOnce(t, a)
	if err := os.Remove(d.sock); err != nil {
		t.Fatal(err)
	}
	a.collectDocker(t.Context())
	a.mu.Lock()
	st, kept := a.status["docker"], a.feed != nil && a.feed.Get("tool") != nil
	a.mu.Unlock()
	if st.OK || st.Error == "" || !kept {
		t.Errorf("status = %+v, kept = %v", st, kept)
	}
}

func TestNoDockerConfigured(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "tok", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	a := h.app(nil, g)
	s := runOnce(t, a)
	for _, st := range s.Sources {
		if st.Name == "docker" {
			t.Errorf("docker source present: %+v", st)
		}
	}
	if p := s.ProjectBySlug("tool"); p == nil || p.Deploy != nil {
		t.Errorf("tool = %+v", p)
	}
	a.mu.Lock()
	known := a.feed != nil
	a.mu.Unlock()
	if known {
		t.Error("a feed was read with no docker configured")
	}
}

func TestDockerHostCredentialsNotInStatus(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "tok", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	a := h.app(func(c *config.Config) { c.Docker = config.Docker{Host: "tcp://127.0.0.1:1", Label: "Docker"} }, g)
	a.cfg.Docker.Host = "tcp://u:secret@127.0.0.1:1" // past Validate, as a config written by hand to the struct
	s, err := a.RunOnce(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "secret") {
		t.Error("site.json carries the host password")
	}
	if !slices.ContainsFunc(s.Sources, func(st model.SourceStatus) bool { return st.Name == "docker" && !st.OK && st.Error != "" }) {
		t.Errorf("sources = %+v", s.Sources)
	}
}
