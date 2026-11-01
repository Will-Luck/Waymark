package app

import (
	"slices"
	"strings"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/docker"
)

// ociSourceLabel names the repo an image was built from.
const ociSourceLabel = "org.opencontainers.image.source"

// normRepoURL is a repo URL as compared: lower case, no trailing slash, no
// .git.
func normRepoURL(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")
	return strings.TrimRight(u, "/")
}

// assignContainers decides which containers each project shows, by slug.
// A project that names containers shows exactly those (named ones that are
// not there show as missing). The others get the container named like their
// slug (v0.1), then every container whose OCI source label is the page of one
// of their repos (urls). A container belongs to one project: a named one to
// the project that names it, any other to the first project, in order, that
// claims it. The first container of a project is its primary: the slug one,
// then running before stopped, then the feed's order.
func assignContainers(projects []registry.Project, feed *docker.Feed, urls func(registry.Project) []string) map[string][]docker.Container {
	out := map[string][]docker.Container{}
	named := map[string]bool{}
	for _, p := range projects {
		for _, n := range p.Containers {
			named[n] = true
		}
	}
	taken := map[string]bool{}
	for _, p := range projects {
		if len(p.Containers) > 0 {
			continue
		}
		if c := feed.Get(p.Slug); c != nil && !named[c.Name] && !taken[c.Name] {
			taken[c.Name] = true
			out[p.Slug] = append(out[p.Slug], *c)
		}
	}
	for _, p := range projects {
		if len(p.Containers) > 0 {
			continue
		}
		want := map[string]bool{}
		for _, u := range urls(p) {
			if u = normRepoURL(u); u != "" {
				want[u] = true
			}
		}
		if len(want) == 0 {
			continue
		}
		var found []docker.Container
		for _, c := range feed.Containers {
			if named[c.Name] || taken[c.Name] {
				continue
			}
			if src := normRepoURL(c.Labels[ociSourceLabel]); src != "" && want[src] {
				found = append(found, c)
			}
		}
		slices.SortStableFunc(found, func(a, b docker.Container) int {
			ar, br := a.State == "running", b.State == "running"
			switch {
			case ar && !br:
				return -1
			case br && !ar:
				return 1
			}
			return 0
		})
		for _, c := range found {
			taken[c.Name] = true
		}
		out[p.Slug] = append(out[p.Slug], found...)
	}
	return out
}

// repoURLsLocked are the web pages of a project's repos as the forges
// returned them: listed repos by their HTMLURL, mirrors by their view's. A
// repo neither has read yet falls back to the page its forge would give.
func (a *App) repoURLsLocked(index map[string]forge.Repo) func(registry.Project) []string {
	return func(p registry.Project) []string {
		var out []string
		for _, ref := range p.Repos {
			key := repoKey(ref.Forge, ref.FullName)
			switch {
			case index[key].HTMLURL != "":
				out = append(out, index[key].HTMLURL)
			case a.mirrors[key] != nil && a.mirrors[key].HTMLURL != "":
				out = append(out, a.mirrors[key].HTMLURL)
			case a.byID[ref.Forge] != nil:
				out = append(out, repoPage(a.byID[ref.Forge].cfg, ref.FullName))
			}
		}
		return out
	}
}
