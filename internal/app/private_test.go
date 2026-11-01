package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
)

// Events stored while include_private was true do not come back after a
// restart with it false: none of a project not shown, and none linking into
// a private repo, whatever its project. Restarted with it true, they all do.
func TestStoredEventsHiddenWithoutIncludePrivate(t *testing.T) {
	h := newAcmeHost(t)
	dir := t.TempDir()
	secret := &acmeRepo{Owner: "acme", Name: "secret", Private: true, Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day)}
	tool := &acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v0.1.0"}, Updated: fixedNow.Add(-day)}
	// A public repo whose name starts like the private one's.
	secretTool := &acmeRepo{Owner: "acme", Name: "secret-tool", Updated: fixedNow.Add(-day)}
	g := h.forge("gitea", "gitea", "gitea-token", []string{"acme"}, tool, secret, secretTool)
	a := h.app(func(c *config.Config) { c.DataDir = dir }, g)
	runOnce(t, a)
	h.edit(func() { secret.Tags, secret.Updated = []string{"v1.1.0", "v1.0.0"}, fixedNow.Add(-time.Hour) })
	runOnce(t, a)
	page := "https://gitea.example.com/acme/"
	a.store.AddEvents([]model.Event{
		{ID: "t:secret-no-link", At: fixedNow, Project: "secret", Text: "secret was updated"},
		{ID: "t:tool-into-secret", At: fixedNow, Project: "tool", Href: page + "secret/issues/1"},
		{ID: "t:none-into-secret", At: fixedNow, Href: page + "Secret"},
		{ID: "t:none-into-tool", At: fixedNow, Href: page + "tool/releases"},
		{ID: "t:tool-like-secret", At: fixedNow, Project: "tool", Href: page + "secret-tool"},
	}, fixedNow)
	if err := a.store.Save(fixedNow); err != nil {
		t.Fatal(err)
	}
	shown := func(include bool) (secretEvents int, ids map[string]bool) {
		s := runOnce(t, h.app(func(c *config.Config) { c.DataDir, c.IncludePrivate = dir, include }, g))
		ids = map[string]bool{}
		for _, e := range s.Events {
			ids[e.ID] = true
			if e.Project == "secret" {
				secretEvents++
			}
		}
		return secretEvents, ids
	}
	n, ids := shown(true)
	if n == 0 || len(ids) != n+4 {
		t.Fatalf("include_private true: %d secret events of %v, want them all shown", n, ids)
	}
	n, ids = shown(false)
	if n != 0 || ids["t:tool-into-secret"] || ids["t:none-into-secret"] {
		t.Errorf("include_private false: %d secret events; shown %v", n, ids)
	}
	if !ids["t:none-into-tool"] || !ids["t:tool-like-secret"] || len(ids) != 2 {
		t.Errorf("include_private false: shown %v, want the two events of public repos", ids)
	}
	// Restarted while the forge does not answer: no repo is known to be
	// public yet, so no stored event links into one.
	h.edit(func() { h.down["gitea"] = true })
	if _, ids = shown(false); len(ids) != 0 {
		t.Errorf("include_private false, forge not listed yet: shown %v", ids)
	}
}

// activityIDs drains the published changes up to the next tick and returns
// the ids of the events published live.
func activityIDs(t *testing.T, ch <-chan model.Change) []string {
	t.Helper()
	var ids []string
	timeout := time.After(2 * time.Second)
	for {
		select {
		case c := <-ch:
			switch {
			case c.Kind == "tick":
				return ids
			case c.Kind == "activity" && c.Event != nil:
				ids = append(ids, c.Event.ID)
			}
		case <-timeout:
			t.Fatalf("no tick; got %v", ids)
		}
	}
}

// A private repo on one forge with the same owner/name as a public one on
// another (here paired as one project) hides nothing of the public one: its
// new tag shows on the page and goes out live. Events linking the private
// twin itself stay hidden.
func TestPrivateTwinKeepsPublicEvents(t *testing.T) {
	h := newAcmeHost(t)
	tool := &acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day)}
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, tool)
	gh := h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Private: true, Updated: fixedNow.Add(-day)})
	a := h.app(func(c *config.Config) { c.IncludePrivate = false }, g, gh)
	runOnce(t, a)
	twin := "https://github.example.com/acme/tool"
	a.store.AddEvents([]model.Event{
		{ID: "t:tool-into-twin", At: fixedNow, Project: "tool", Href: twin + "/releases/tag/v9.0.0"},
		{ID: "t:none-into-twin", At: fixedNow, Href: twin},
	}, fixedNow)
	ch, cancel := a.Subscribe()
	defer cancel()
	h.edit(func() { tool.Tags, tool.Updated = []string{"v1.1.0", "v1.0.0"}, fixedNow.Add(-time.Hour) })
	s := runOnce(t, a)
	const tag = "tool:release:v1.1.0"
	shown := map[string]bool{}
	for _, e := range s.Events {
		shown[e.ID] = true
	}
	if !shown[tag] {
		t.Errorf("the public repo's new tag is not on the page: %v", shown)
	}
	if live := activityIDs(t, ch); !slices.Contains(live, tag) {
		t.Errorf("the public repo's new tag did not go out live: %v", live)
	}
	if shown["t:tool-into-twin"] || shown["t:none-into-twin"] {
		t.Errorf("an event linking the private twin is shown: %v", shown)
	}
}

// byNameSwitch fails every by-name read while down is set.
type byNameSwitch struct {
	forge.Forge
	down *atomic.Bool
}

func (b byNameSwitch) Repo(ctx context.Context, full string) (forge.Repo, error) {
	if b.down.Load() {
		return forge.Repo{}, errors.New("by-name reads are down")
	}
	return b.Forge.Repo(ctx, full)
}

// Without include_private, a projects-file project shows only once its
// primary repo is known to be public: not while the forge's list fails at
// start, nor while the first by-name read of it fails. Then it shows if the
// primary is public and stays hidden if it is private. With include_private
// it shows throughout, as in v0.1.
func TestPrivacyUnknownHidesFileProject(t *testing.T) {
	for _, byName := range []bool{false, true} {
		for _, private := range []bool{false, true} {
			for _, include := range []bool{true, false} {
				h := newAcmeHost(t)
				owner := "acme"
				if byName {
					owner = "other" // outside owners: read by name
				}
				g := h.forge("gitea", "gitea", "t1", []string{"acme"},
					&acmeRepo{Owner: owner, Name: "vault", Private: private, Updated: fixedNow.Add(-day)})
				file := filepath.Join(t.TempDir(), "projects.yaml")
				if err := os.WriteFile(file, []byte("projects:\n  - slug: vault\n    name: Vault\n    repos:\n      - gitea: "+owner+"/vault\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				cfg := h.config(func(c *config.Config) { c.Projects, c.IncludePrivate = config.FileRef{File: file}, include }, g)
				clients := NewForges(cfg)
				var down atomic.Bool
				down.Store(true)
				clients[0] = byNameSwitch{clients[0], &down}
				if !byName {
					h.edit(func() { h.down["gitea"] = true })
				}
				a := h.start(cfg, clients)
				at := func(when string, want bool) {
					t.Helper()
					s := runOnce(t, a)
					if got := s.ProjectBySlug("vault") != nil; got != want {
						t.Errorf("by name %v, private %v, include_private %v, %s: shown %v, want %v", byName, private, include, when, got, want)
					}
				}
				at("privacy unknown", include)
				down.Store(false)
				h.edit(func() { h.down["gitea"] = false })
				at("primary read", include || !private)
			}
		}
	}
}

// writeProjects writes a projects file and returns its path.
func writeProjects(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "projects.yaml")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// mirrorShown is what a project shows of its GitHub mirror: its view and
// every link to it.
func mirrorShown(p *model.Project) []string {
	var out []string
	if p.GitHub != nil {
		out = append(out, "view "+p.GitHub.HTMLURL)
	}
	for _, l := range p.Links {
		if l.Kind == "github" {
			out = append(out, "link "+l.URL)
		}
	}
	return out
}

// Without include_private a mirror is linked only once it is known to be
// public. GitHub down at a cold start: the projects file names the mirror,
// nothing says whether it is private, so nothing links it. Once GitHub
// answers, a private mirror stays unlinked and its forge's status does not
// name it; a public one is linked.
func TestMirrorUnlinkedUntilKnownPublic(t *testing.T) {
	for _, private := range []bool{true, false} {
		h := newAcmeHost(t)
		g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
		gh := h.forge("github", "github", "t2", []string{"acme"},
			&acmeRepo{Owner: "acme", Name: "tool-private", Private: private, Stars: 7, Updated: fixedNow.Add(-day)})
		file := writeProjects(t, "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n      - github: acme/tool-private\n")
		h.edit(func() { h.down["github"] = true })
		a := h.app(func(c *config.Config) { c.Projects, c.IncludePrivate = config.FileRef{File: file}, false }, g, gh)
		s := runOnce(t, a)
		p := s.ProjectBySlug("tool")
		if p == nil {
			t.Fatalf("private %v: projects = %v", private, slugsOf(s))
		}
		if got := mirrorShown(p); len(got) != 0 {
			t.Errorf("private %v, GitHub not answering: the mirror shows %v", private, got)
		}
		if got := h.requests("github", "/repos/acme/tool-private"); len(got) == 0 {
			t.Fatal("the mirror read never failed, so the status check below proves nothing")
		}

		// GitHub answers its list: the mirror's privacy is known before the
		// next mirror pass, whose failed read is still on record.
		h.edit(func() { h.down["github"] = false })
		a.collectRepos(context.Background(), false)
		a.assemble()
		s = a.Snapshot()
		if st, _ := sourceStatus(s, "github"); private && strings.Contains(st.Error, "tool-private") {
			t.Errorf("github status names the private mirror: %q", st.Error)
		}
		s = runOnce(t, a)
		got := mirrorShown(s.ProjectBySlug("tool"))
		if private && len(got) != 0 || !private && len(got) != 2 {
			t.Errorf("private %v, GitHub answering: the mirror shows %v", private, got)
		}
	}
}
