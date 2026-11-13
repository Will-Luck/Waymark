package app

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Will-Luck/waymark/internal/derive"
	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/docker"
)

var nilTime time.Time

// inputsLocked builds derive inputs from the last good data of every source.
func (a *App) inputsLocked(now time.Time) derive.SiteInput {
	index := a.repoIndexLocked()
	stored := a.store.Fingerprints()
	me := map[string]string{}
	for _, fs := range a.forges {
		if fs.me != "" {
			me[fs.cfg.ID] = fs.me
		}
	}
	var projects []derive.Input
	all := a.projectsLocked()
	var owned map[string][]docker.Container
	if a.feed != nil {
		owned = assignContainers(all, a.feed, a.repoURLsLocked(index))
	}
	for _, p := range all {
		in := derive.Input{Project: p, DockerKnown: a.feed != nil, NotesAt: a.notesAt, Me: me}
		if fp, ok := stored[p.Slug]; ok {
			in.Prev = &fp
		}
		if fs := a.byID[p.Primary().Forge]; fs != nil {
			in.GiteaKnown = !fs.listedAt.IsZero()
		}
		if ref, isPrimary, ok := a.mirrorRefLocked(p); ok {
			in.Mirror = a.mirrors[repoKey(ref.Forge, ref.FullName)]
			in.MirrorForge, in.NoDetail = ref.Forge, isPrimary
			if a.mirrorLinkableLocked(ref, index) {
				in.MirrorURL = repoPage(a.byID[ref.Forge].cfg, ref.FullName)
			}
		}
		var full []registry.RepoRef
		if !in.NoDetail {
			full = p.FullRepos()
		}
		// A named repo that is not there is in its forge's status (readNamed).
		for _, ref := range full {
			key := repoKey(ref.Forge, ref.FullName)
			r, ok := index[key]
			if !ok {
				continue
			}
			rd := derive.RepoData{Repo: r}
			if st := a.repoSt[key]; st != nil {
				rd.Detail, rd.Files = st.detail, st.files
			}
			in.Repos = append(in.Repos, rd)
		}
		if a.feed != nil {
			in.FeedAt = a.feed.ModTime
			if len(p.Containers) > 0 {
				for _, name := range p.Containers {
					in.Containers = append(in.Containers, derive.ContainerRef{Name: name, Declared: true, C: a.feed.Get(name)})
				}
			} else {
				for _, c := range owned[p.Slug] {
					in.Containers = append(in.Containers, derive.ContainerRef{Name: c.Name, C: a.feed.Get(c.Name)})
				}
			}
		}
		if p.FDroid != nil {
			if fd, ok := a.fdroid[p.FDroid.Index]; ok {
				in.FDroidIndexRead = true
				if pkg, ok := fd.pkgs[p.FDroid.Package]; ok {
					in.FDroid, in.FDroidAt = &pkg, fd.at
				} else {
					a.warnOnce("project " + p.Slug + ": package " + p.FDroid.Package + " is not in " + p.FDroid.Index)
				}
			}
		}
		for _, u := range p.URLs {
			if u.Probe == "" {
				continue
			}
			label := u.Label
			if label == "" {
				label = "Live"
			}
			in.Probes = append(in.Probes, derive.ProbeData{
				Label: label, URL: u.URL, ProbeURL: u.Probe, Last: a.probes[u.Probe], History: a.store.ProbeHistory(u.Probe, now),
			})
		}
		for _, u := range a.homepagesLocked(p, index) {
			in.Probes = append(in.Probes, derive.ProbeData{
				Label: "Homepage", URL: u, ProbeURL: u, Last: a.probes[u], History: a.store.ProbeHistory(u, now), Homepage: true,
			})
		}
		in.Notes = a.notes[p.Slug]
		projects = append(projects, in)
	}
	var sources []model.SourceStatus
	for _, name := range a.sources {
		if st, ok := a.status[name]; ok {
			sources = append(sources, st)
		}
	}
	kinds := map[string]string{}
	for _, fs := range a.forges {
		kinds[fs.cfg.ID] = fs.cfg.Type
	}
	meta := a.metaLocked()
	return derive.SiteInput{
		Env: derive.Env{Now: now, Loc: a.loc, HostLabel: a.cfg.Docker.Label, NeedsYou: derive.NeedsYou(a.cfg.NeedsYou),
			ForgeKinds: kinds, ForgeNames: a.forgeNames, HomeForge: a.forges[0].cfg.ID, NotesLabel: meta.NotesLabel},
		Meta:       meta,
		Registry:   a.reg,
		Projects:   projects,
		Ideas:      a.ideas,
		IdeaLabels: a.cfg.Ideas.Labels,
		IdeaMarker: a.cfg.Ideas.Marker,
		Filed:      a.filed,
		Heat:       a.heat,
		Sources:    sources,
	}
}

// mirrorLinkableLocked reports whether the page may link a mirror view's repo
// before (or without) a view of it. Without include_private only one known to
// be public: in its forge's list or read by name, or a view of it that says
// so (a view that says private is never kept), or on a forge with no token,
// which nothing can say is private (spec ruling 28). Unknown is not linked.
func (a *App) mirrorLinkableLocked(ref registry.RepoRef, index map[string]forge.Repo) bool {
	if a.cfg.IncludePrivate {
		return true
	}
	key := repoKey(ref.Forge, ref.FullName)
	if _, listed := index[key]; listed {
		return true
	}
	if m := a.mirrors[key]; m != nil && !m.Private {
		return true
	}
	fs := a.byID[ref.Forge]
	return fs != nil && fs.noToken && !fs.isDropped(key)
}

// metaLocked is the site's Meta as configured. Assemble adds what only the
// data shows: ideas from labels, and F-Droid packages in the projects file.
func (a *App) metaLocked() model.Meta {
	m := model.Meta{
		Title:      a.cfg.Site.Title,
		Notes:      a.notesAd != nil,
		Docker:     a.cfg.Docker.Feed != "" || a.cfg.Docker.Host != "",
		Ideas:      isSet(a.cfg.Ideas.File),
		IdeaLabels: a.cfg.Ideas.Labels,
		Forges:     a.forgeNames,
		Timezone:   a.cfg.Site.Timezone,
		Demo:       a.cfg.Demo,

		ProjectsFile: isSet(a.cfg.Projects),
		IdeasFile:    isSet(a.cfg.Ideas.File),
	}
	if a.notesAd != nil {
		m.NotesLabel = a.notesAd.Label()
	}
	return m
}

// publicPagesLocked are the web pages of the repos known to be public: every
// forge's list and by-name reads, which hold no private repo without
// include_private. Keys are pageKey's.
func (a *App) publicPagesLocked() map[string]bool {
	pages := map[string]bool{}
	for _, fs := range a.forges {
		for _, r := range fs.repos {
			if k := pageKey(r.HTMLURL); k != "" {
				pages[k] = true
			}
		}
	}
	return pages
}

// pageKey is a link without query, fragment or trailing slash, in lower case
// (forges match owner and name in any case).
func pageKey(u string) string {
	k := strings.ToLower(strings.TrimSpace(u))
	if i := strings.IndexAny(k, "?#"); i >= 0 {
		k = k[:i]
	}
	return strings.TrimRight(k, "/")
}

// underPage reports whether href is one of pages or anything under one.
func underPage(href string, pages map[string]bool) bool {
	for k := pageKey(href); k != ""; {
		if pages[k] {
			return true
		}
		i := strings.LastIndex(k, "/")
		if i < 0 {
			return false
		}
		k = k[:i]
	}
	return false
}

// shownEvents keeps the events a site without private repos may show: those
// of a shown project, or of none, whose link (if any) is into a repo known to
// be public, by its host-qualified page. A link into a repo not known, or not
// known yet, is not shown: a same-named private repo on another forge hides
// nothing, and a stored event of a private repo stays out after a restart
// while its forge has not listed.
func shownEvents(evs []model.Event, projects []model.Project, public map[string]bool) []model.Event {
	shown := make(map[string]bool, len(projects))
	for _, p := range projects {
		shown[p.Slug] = true
	}
	out := make([]model.Event, 0, len(evs))
	for _, e := range evs {
		if e.Project != "" && !shown[e.Project] || e.Href != "" && !underPage(e.Href, public) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// assemble builds a new site, swaps it in, records events and publishes the
// changes (project hashes, new events, one tick).
func (a *App) assemble() {
	a.assembleMu.Lock()
	defer a.assembleMu.Unlock()
	now := a.now()
	a.mu.Lock()
	in := a.inputsLocked(now)
	ready, listed := a.eventsReadyLocked(now)
	public := a.publicPagesLocked()
	a.mu.Unlock()
	out := derive.Assemble(in)
	a.mu.Lock()
	for _, w := range out.Warnings {
		a.warnOnce(w)
	}
	a.mu.Unlock()

	// Before every forge's first repo list since start, some projects are not
	// built yet and some are built from the wrong repo (a pair's mirror while
	// its primary's forge has not answered), and the registry may still be
	// the cached copy or none. For up to EventsGrace nothing is diffed and the
	// stored state is kept as it is (but for when a container was first seen
	// not running, so a stop is confirmed 2 minutes after that, not after the
	// grace), and the first full list is compared with the last good one.
	// After that a forge still unlisted is taken as down: events go on, and
	// its projects not built yet keep their stored state (neither forgotten
	// nor diffed).
	var evs []model.Event
	if !ready {
		stored := a.store.Fingerprints()
		for slug, fp := range out.Fingerprints {
			if p, ok := stored[slug]; ok {
				stored[slug] = derive.KeepSighting(p, fp)
			}
		}
		a.store.SetFingerprints(stored)
	} else {
		prev := a.store.Fingerprints()
		if !a.store.Empty() {
			evs = derive.Diff(prev, out.Fingerprints, now)
		}
		merged := make(map[string]derive.Fingerprint, len(out.Fingerprints))
		for slug, fp := range out.Fingerprints {
			merged[slug] = derive.Merge(prev[slug], fp)
		}
		if !listed {
			for slug, fp := range prev {
				if _, ok := merged[slug]; !ok {
					merged[slug] = fp
				}
			}
		}
		a.store.SetFingerprints(merged)
	}
	added := a.store.AddEvents(evs, now)
	out.Site.Events = a.store.Events(now.Add(-EventsShown), now)
	if !a.cfg.IncludePrivate {
		// The store keeps what an earlier run with include_private saw.
		out.Site.Events = shownEvents(out.Site.Events, out.Site.Projects, public)
		added = shownEvents(added, out.Site.Projects, public)
	}

	old := a.site.Swap(out.Site)
	a.cycles.Add(1)
	oldHash := map[string]string{}
	if old != nil {
		for _, p := range old.Projects {
			oldHash[p.Slug] = p.Hash
		}
	}
	for _, p := range out.Site.Projects {
		if oldHash[p.Slug] != p.Hash {
			a.hub.Publish(model.Change{Kind: "project", Slug: p.Slug, Hash: p.Hash, At: now})
		}
	}
	for i := range added {
		e := added[i]
		a.hub.Publish(model.Change{Kind: "activity", Slug: e.Project, Event: &e, At: now})
	}
	a.hub.Publish(model.Change{Kind: "tick", At: now})
	a.logSummary(out.Site, len(added))
}

// logSummary logs one line when the headline numbers or failing sources
// change, so the log shows cycles without a line per assemble.
func (a *App) logSummary(s *model.Site, newEvents int) {
	var failing []string
	for _, st := range s.Sources {
		if !st.OK {
			failing = append(failing, st.Name)
		}
	}
	f := "none"
	if len(failing) > 0 {
		f = strings.Join(failing, ",")
	}
	sum := fmt.Sprintf("%d projects (%d active, %d live), %d needs you, sources failing: %s", len(s.Projects), s.Stats.Active, s.Stats.Live, s.Stats.NeedsYou, f)
	if sum != a.lastSummary || newEvents > 0 {
		log.Printf("assemble %d: %s; %d new events", a.cycles.Load(), sum, newEvents)
		a.lastSummary = sum
	}
}

var _ model.Source = (*App)(nil)
