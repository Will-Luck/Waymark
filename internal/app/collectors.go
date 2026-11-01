package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/Will-Luck/waymark/internal/derive"
	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/docker"
	"github.com/Will-Luck/waymark/internal/sources/fdroid"
	"github.com/Will-Luck/waymark/internal/sources/probe"
)

// passErrors gathers one pass's failures over the forges; the forge ids show
// only when more than one forge was asked.
type passErrors struct {
	asked int
	ids   []string
	errs  []error
}

func (p *passErrors) add(id string, err error) {
	p.ids, p.errs = append(p.ids, id), append(p.errs, err)
}

func (p *passErrors) err() error {
	switch {
	case len(p.errs) == 0:
		return nil
	case p.asked == 1:
		return p.errs[0]
	}
	parts := make([]string, len(p.errs))
	for i, err := range p.errs {
		parts[i] = p.ids[i] + ": " + err.Error()
	}
	return errors.New(strings.Join(parts, "; "))
}

// collectHeatmap reads the token user's days from every forge with heatmap
// on, keeping each forge's last good points; the days are summed.
func (a *App) collectHeatmap(ctx context.Context) {
	if !a.running.heatmap.TryLock() {
		return
	}
	defer a.running.heatmap.Unlock()
	if !a.heatmapOn() {
		return
	}
	var pass passErrors
	read := false // some forge's points were read this pass
	for _, fs := range a.forges {
		if !fs.cfg.Heatmap {
			continue
		}
		pts, skip, err := a.forgeHeatmap(ctx, fs)
		if skip {
			continue
		}
		pass.asked++
		if err != nil {
			pass.add(fs.cfg.ID, err)
			continue
		}
		read = true
		a.mu.Lock()
		fs.heat = pts
		a.mu.Unlock()
	}
	a.mu.Lock()
	var all []forge.HeatPoint
	for _, fs := range a.forges {
		all = append(all, fs.heat...)
	}
	a.heat = all
	a.mu.Unlock()
	if err := pass.err(); err != nil {
		a.setStatus("heatmap", err, nilTime)
		if read {
			a.kickAssemble()
		}
		return
	}
	a.setStatus("heatmap", nil, a.now())
	a.kickAssemble()
}

// forgeHeatmap is one forge's heatmap for the user its token belongs to;
// skip is true when the forge needs a token for it. A forge waiting for its
// rate limit is not asked: its error stands.
func (a *App) forgeHeatmap(ctx context.Context, fs *forgeState) (pts []forge.HeatPoint, skip bool, err error) {
	a.mu.Lock()
	login, skip := fs.me, fs.noToken
	limited, limitErr := a.limitedLocked(fs), fs.limitErr
	a.mu.Unlock()
	switch {
	case skip:
		return nil, true, nil
	case limited:
		return nil, false, limitErr
	}
	if login == "" {
		me, err := fs.f.Me(ctx)
		a.mu.Lock()
		skip = a.needsTokenLocked(fs, err)
		a.rateLimitLocked(fs, err)
		if err == nil {
			fs.me = me.Login
		}
		a.mu.Unlock()
		switch {
		case skip:
			return nil, true, nil
		case err != nil:
			return nil, false, fmt.Errorf("the token's user: %w", err)
		}
		login = me.Login
	}
	pts, err = fs.f.Heatmap(ctx, login)
	a.mu.Lock()
	skip = a.needsTokenLocked(fs, err)
	a.rateLimitLocked(fs, err)
	a.mu.Unlock()
	if skip {
		return nil, true, nil
	}
	return pts, false, err
}

// filedQuery is what the filed-idea search looks for: the marker up to its
// last letter or digit ("waymark-idea:" gives "waymark-idea"). Only file
// ideas are filed, so with no ideas file it is "" and there is no search.
func (a *App) filedQuery() string {
	if !isSet(a.cfg.Ideas.File) {
		return ""
	}
	return strings.TrimRightFunc(a.cfg.Ideas.Marker, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// linkableLocked keeps the filed-idea search results the site may link.
// Without include_private that is only those in a repo the forge's list or a
// by-name read gave as public: the search sees every repo the token can and
// does not say which are private, so a repo the app does not know is left out.
func (a *App) linkableLocked(fs *forgeState, issues []forge.Issue) []forge.Issue {
	if a.cfg.IncludePrivate {
		return issues
	}
	public := map[string]bool{}
	for _, r := range fs.repos {
		public[repoKey(fs.cfg.ID, r.FullName)] = true
	}
	kept := issues[:0]
	for _, is := range issues {
		if is.Repository != nil && public[repoKey(fs.cfg.ID, is.Repository.FullName)] {
			kept = append(kept, is)
		}
	}
	return kept
}

// collectFiled finds the issues filed from file ideas on every forge.
func (a *App) collectFiled(ctx context.Context) {
	query := a.filedQuery()
	if query == "" || !a.running.filed.TryLock() {
		return
	}
	defer a.running.filed.Unlock()
	var pass passErrors
	read := false // some forge's issues were read this pass
	for _, fs := range a.forges {
		a.mu.Lock()
		skip := fs.noToken
		limited, limitErr := a.limitedLocked(fs), fs.limitErr
		a.mu.Unlock()
		if skip {
			continue
		}
		if limited { // its last results stand
			pass.asked++
			pass.add(fs.cfg.ID, limitErr)
			continue
		}
		issues, err := fs.f.SearchIssues(ctx, query)
		a.mu.Lock()
		skip = a.needsTokenLocked(fs, err)
		a.rateLimitLocked(fs, err)
		if err == nil {
			fs.filed, read = a.linkableLocked(fs, issues), true
		}
		a.mu.Unlock()
		if skip {
			continue
		}
		pass.asked++
		if err != nil {
			pass.add(fs.cfg.ID, err)
		}
	}
	a.mu.Lock()
	var all []forge.Issue
	for _, fs := range a.forges {
		all = append(all, fs.filed...)
	}
	a.filed = forge.FiledIdeas(all, a.cfg.Ideas.Marker)
	a.mu.Unlock()
	if err := pass.err(); err != nil {
		a.setStatus("ideas", fmt.Errorf("filed-idea search: %w", err), nilTime)
		if read {
			a.kickAssemble()
		}
		return
	}
	a.setStatus("ideas", nil, a.now())
	a.kickAssemble()
}

// collectDocker re-reads the feed when its mtime moves, and flags it stale
// when the host cron has stopped writing it.
func (a *App) collectDocker(ctx context.Context) {
	if a.cfg.Docker.Host != "" {
		a.collectDockerAPI(ctx)
		return
	}
	p := a.cfg.Docker.Feed
	if p == "" {
		return
	}
	if !a.running.docker.TryLock() {
		return
	}
	defer a.running.docker.Unlock()
	st, err := os.Stat(p)
	if err != nil {
		a.setStatus("docker", fmt.Errorf("no feed at %s (is the host cron running?)", p), nilTime)
		return
	}
	mod := st.ModTime().UTC()
	a.mu.Lock()
	same := a.feed != nil && mod.Equal(a.feedMod)
	a.mu.Unlock()
	if !same {
		feed, err := docker.ReadFeed(p)
		if err != nil {
			a.setStatus("docker", err, nilTime)
			return
		}
		a.mu.Lock()
		a.feed, a.feedMod = feed, mod
		a.mu.Unlock()
		a.kickAssemble()
	}
	if age := a.now().Sub(mod); age > FeedStaleAfter {
		a.setStatus("docker", fmt.Errorf("the feed is %s old; container states may be out of date", derive.For(mod, a.now())), mod)
		return
	}
	a.mu.Lock()
	skipped := a.feed.Skipped
	a.mu.Unlock()
	if skipped > 0 {
		lines := "1 line of the feed could not be read"
		if skipped > 1 {
			lines = fmt.Sprintf("%d lines of the feed could not be read", skipped)
		}
		a.setStatus("docker", fmt.Errorf("%s; containers on them show as missing", lines), mod)
		return
	}
	a.setStatus("docker", nil, mod)
}

// collectDockerAPI reads the containers from the Docker Engine API. There is
// no mtime to watch, so it compares what came back with the last read and only
// rebuilds the site when it differs.
func (a *App) collectDockerAPI(ctx context.Context) {
	if !a.running.docker.TryLock() {
		return
	}
	defer a.running.docker.Unlock()
	feed, err := docker.FetchAPI(ctx, a.cfg.Docker.Host, "waymark/"+buildVersion(a.cfg))
	if err != nil {
		a.setStatus("docker", err, nilTime)
		return
	}
	a.mu.Lock()
	same := a.feed != nil && slices.EqualFunc(a.feed.Containers, feed.Containers, func(x, y docker.Container) bool {
		return x.ID == y.ID && x.Name == y.Name && x.Image == y.Image && x.State == y.State &&
			x.Status == y.Status && x.CreatedAt.Equal(y.CreatedAt) && maps.Equal(x.Labels, y.Labels)
	})
	if same {
		a.feed.ModTime = feed.ModTime
	} else {
		a.feed = feed
	}
	a.feedMod = feed.ModTime
	a.mu.Unlock()
	if !same {
		a.kickAssemble()
	}
	a.setStatus("docker", nil, feed.ModTime)
}

// probeTarget is one URL to probe. Homepage: only repo homepages name it, so
// it is probed with the public-only client (spec ruling 33); a projects-file
// url is the operator's choice and is probed wherever it points.
type probeTarget struct {
	URL      string
	Homepage bool
}

func (a *App) probeTargetsLocked() []probeTarget {
	urls := map[string]bool{}
	homes := map[string]bool{}
	index := a.repoIndexLocked()
	for _, p := range a.projectsLocked() {
		for _, u := range p.URLs {
			if u.Probe != "" {
				urls[u.Probe] = true
			}
		}
		for _, u := range a.homepagesLocked(p, index) {
			homes[u] = true
		}
	}
	var out []probeTarget
	for u := range urls {
		out = append(out, probeTarget{URL: u})
	}
	for u := range homes {
		if !urls[u] {
			out = append(out, probeTarget{URL: u, Homepage: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// homepagesLocked are the websites of p's repos that probes.homepages adds,
// in repo order: http(s) URLs only, each once, and none that a projects-file
// url already links or probes. A repo left out as private is never probed:
// it is not in index (the repos the site may show), and its forge says so.
func (a *App) homepagesLocked(p registry.Project, index map[string]forge.Repo) []string {
	if !a.cfg.Probes.Homepages {
		return nil
	}
	key := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/")) }
	seen := map[string]bool{}
	for _, u := range p.URLs {
		seen[key(u.URL)], seen[key(u.Probe)] = true, true
	}
	var out []string
	for _, ref := range p.Repos {
		k := repoKey(ref.Forge, ref.FullName)
		if fs := a.byID[ref.Forge]; fs == nil || fs.isDropped(k) {
			continue
		}
		w := strings.TrimSpace(index[k].Website)
		u, err := url.Parse(w)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || seen[key(w)] {
			continue
		}
		seen[key(w)] = true
		out = append(out, w)
	}
	return out
}

func (a *App) collectProbes(ctx context.Context) {
	if !a.running.probes.TryLock() {
		return
	}
	defer a.running.probes.Unlock()
	a.mu.Lock()
	targets := a.probeTargetsLocked()
	a.mu.Unlock()
	sem := make(chan struct{}, ProbeWorkers)
	var wg sync.WaitGroup
	results := make([]probe.Result, len(targets))
	version := buildVersion(a.cfg)
	for i, tg := range targets {
		c := a.probeC
		if tg.Homepage {
			c = a.homeC
		}
		wg.Add(1)
		go func(i int, c *http.Client, u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = probe.Check(ctx, c, u, version)
		}(i, c, tg.URL)
	}
	wg.Wait()
	if ctx.Err() != nil {
		// Cut short (shutdown): the results say "context canceled", not down.
		return
	}
	keep := map[string]bool{}
	a.mu.Lock()
	for i, tg := range targets {
		r := results[i]
		a.probes[tg.URL] = &r
		keep[tg.URL] = true
	}
	for u := range a.probes {
		if !keep[u] {
			delete(a.probes, u)
		}
	}
	a.mu.Unlock()
	for _, r := range results {
		if r.Refused {
			continue // not checked: no point in the history, up or down
		}
		a.store.AddProbe(r.URL, model.ProbePoint{At: r.At, Up: r.Up, LatencyMS: r.LatencyMS}, a.now())
	}
	a.store.PruneProbes(keep)
	a.setStatus("probes", nil, a.now())
	a.kickAssemble()
}

// notesKey is what of the projects a notes load depends on: their slugs and
// the notes the projects file lists.
func notesKey(ps []registry.Project) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString(p.Slug)
		for _, f := range p.Notes {
			b.WriteString("," + f)
		}
		b.WriteString(";")
	}
	return b.String()
}

// collectNotes reads the notes through the configured adapter (type none:
// nothing). It reloads only when a file or the projects changed.
func (a *App) collectNotes(ctx context.Context) {
	if a.notesAd == nil || !a.running.notes.TryLock() {
		return
	}
	defer a.running.notes.Unlock()
	a.mu.Lock()
	projects := a.projectsLocked()
	prevSig := a.notesSig
	a.mu.Unlock()
	sig := a.notesAd.Signature() + "\n" + notesKey(projects)
	if sig == prevSig {
		// Nothing changed: the notes are current as of this check.
		now := a.now()
		a.mu.Lock()
		a.notesAt = now
		a.setStatusLocked("notes", a.notesErr, now)
		a.mu.Unlock()
		return
	}
	// A map with an error: read, but a listed note is missing (a problem the
	// status shows); no map: nothing could be read.
	ns, problem := a.notesAd.Load(projects)
	if ns == nil && problem != nil {
		a.setStatus("notes", problem, nilTime)
		return
	}
	now := a.now()
	a.mu.Lock()
	a.notes, a.notesSig, a.notesAt, a.notesErr = ns, sig, now, problem
	a.setStatusLocked("notes", problem, now)
	a.mu.Unlock()
	a.kickAssemble()
}

func (a *App) collectFDroid(ctx context.Context) {
	if !a.running.fdroid.TryLock() {
		return
	}
	defer a.running.fdroid.Unlock()
	a.mu.Lock()
	var indexes []string
	seen := map[string]bool{}
	for _, p := range a.reg.Projects {
		if p.FDroid != nil && p.FDroid.Index != "" && !seen[p.FDroid.Index] {
			seen[p.FDroid.Index] = true
			indexes = append(indexes, p.FDroid.Index)
		}
	}
	a.mu.Unlock()
	if len(indexes) == 0 {
		return
	}
	var errs []string
	for _, u := range indexes {
		pkgs, err := fdroid.Fetch(ctx, a.httpc, u, "waymark/"+buildVersion(a.cfg))
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		a.mu.Lock()
		a.fdroid[u] = fdroidData{pkgs: pkgs, at: a.now()}
		a.mu.Unlock()
	}
	if len(errs) > 0 {
		a.setStatus("fdroid", fmt.Errorf("%s", strings.Join(errs, "; ")), nilTime)
	} else {
		a.setStatus("fdroid", nil, a.now())
	}
	a.kickAssemble()
}

// collectMirrors reads every mirror view with FetchMirror, the light read:
// each project's first mirror, or its primary when that forge gives no
// detail without a token.
func (a *App) collectMirrors(ctx context.Context) {
	if !a.running.mirrors.TryLock() {
		return
	}
	defer a.running.mirrors.Unlock()
	a.mu.Lock()
	var targets []registry.RepoRef
	asked := map[string]bool{}
	seen := map[string]bool{}
	for _, p := range a.projectsLocked() {
		ref, _, ok := a.mirrorRefLocked(p)
		if key := repoKey(ref.Forge, ref.FullName); ok && !seen[key] {
			seen[key] = true
			asked[ref.Forge] = true
			targets = append(targets, ref)
		}
	}
	// A forge with no mirrors left has no mirror error left either.
	for _, fs := range a.forges {
		if !asked[fs.cfg.ID] && len(fs.mirrorFailed) > 0 {
			fs.mirrorFailed = nil
			a.forgeStatusLocked(fs)
		}
	}
	a.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	errs := map[string][]repoFailure{}
	for _, ref := range targets {
		key := repoKey(ref.Forge, ref.FullName)
		fs := a.byID[ref.Forge]
		if limited, limitErr := a.limitState(fs); limited { // the last good view stands
			errs[ref.Forge] = append(errs[ref.Forge], repoFailure{key, limitErr.Error()})
			continue
		}
		m, err := fs.f.FetchMirror(ctx, ref.FullName, a.now())
		if err != nil {
			a.mu.Lock()
			a.rateLimitLocked(fs, err)
			a.mu.Unlock()
			errs[ref.Forge] = append(errs[ref.Forge], repoFailure{key, err.Error()})
			continue
		}
		a.mu.Lock()
		if m.Private && !a.cfg.IncludePrivate {
			// The view is the first to say so (no list or by-name read did):
			// left out from now on as if read by name, so it is neither
			// linked nor read again; a by-name read can still clear it.
			delete(a.mirrors, key)
			a.byID[ref.Forge].namedDropped[key] = true
		} else {
			a.mirrors[key] = m
		}
		a.mu.Unlock()
	}
	now := a.now()
	a.mu.Lock()
	for _, fs := range a.forges {
		if !asked[fs.cfg.ID] {
			continue
		}
		fs.mirrorFailed = errs[fs.cfg.ID]
		if len(fs.mirrorFailed) == 0 {
			fs.mirrorAt = now
		}
		a.forgeStatusLocked(fs)
	}
	a.mu.Unlock()
	a.kickAssemble()
}
