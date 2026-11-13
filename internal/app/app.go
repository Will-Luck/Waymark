// Package app runs the collectors on their cadences, keeps the last good data
// from each source, assembles the model.Site, turns changes into events and
// publishes them. App implements model.Source for the web layer.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/derive"
	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/hub"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/docker"
	"github.com/Will-Luck/waymark/internal/sources/fdroid"
	"github.com/Will-Luck/waymark/internal/sources/gitea"
	"github.com/Will-Luck/waymark/internal/sources/github"
	"github.com/Will-Luck/waymark/internal/sources/notes"
	"github.com/Will-Luck/waymark/internal/sources/probe"
	"github.com/Will-Luck/waymark/internal/store"
)

// Cadences from the spec.
var (
	RepoListEvery   = 60 * time.Second
	DetailMaxAge    = 30 * time.Minute
	FilesMaxAge     = 6 * time.Hour
	HeatmapEvery    = 10 * time.Minute
	FiledEvery      = 10 * time.Minute
	DockerEvery     = 30 * time.Second
	ProbeEvery      = 2 * time.Minute
	NotesEvery      = 60 * time.Second
	FDroidEvery     = 10 * time.Minute
	MirrorEvery     = 30 * time.Minute
	Debounce        = time.Second
	SaveEvery       = 10 * time.Second
	RefreshInterval = 20 * time.Second
	RegistryRetry   = 10 * time.Minute
	FeedStaleAfter  = 5 * time.Minute
	EventsShown     = 14 * 24 * time.Hour
	CommitWindow    = 84 * 24 * time.Hour
	CommitPages     = 10 // 500 commits in the 84-day window, enough for a busy repo
	ForgeWorkers    = 4  // detail reads at once, per forge
	ProbeWorkers    = 8
	// A rate-limited forge is asked nothing until the reset it names, at most
	// RateLimitMaxWait away; RateLimitRetry when it names none.
	RateLimitMaxWait = time.Hour
	RateLimitRetry   = time.Minute
	// A mirror pass that a rate limit cut short is read again at the first
	// check after the limit ends, one every MirrorRetryEvery (retryMirrors).
	MirrorRetryEvery = time.Minute
	// ProbeSaveEvery: probe points alone are written at most this often.
	// Events and fingerprints are written at the next SaveEvery tick, which
	// carries the probe points too, so a crash loses at most this much of
	// the 24 h uptime history and nothing else.
	ProbeSaveEvery = 10 * time.Minute
)

// EventsGrace is how long after start events wait for a forge that has not
// listed its repos yet (ruling 50): after it, they go on without that forge.
const EventsGrace = 10 * time.Minute

type repoState struct {
	detail       *forge.Detail
	files        *forge.Files
	seenUpdated  time.Time
	seenIssues   int
	seenPRs      int
	filesUpdated time.Time
	detailErr    error
	filesErr     error
}

// forgeState is one configured forge and the last good data read from it.
// Every field but f and cfg is guarded by App.mu.
type forgeState struct {
	f   forge.Forge
	cfg config.Forge

	listing  []forge.Repo    // last good list, by full name; private ones dropped unless include_private
	dropped  map[string]bool // repo keys the last list dropped as private
	listedAt time.Time
	listErr  error
	// named are the projects-file repos the list lacks (an owner outside
	// owners), last read by name; namedDropped the private ones left out, by
	// that read or (when it fails) by their mirror view.
	named        map[string]forge.Repo
	namedDropped map[string]bool
	namedFailed  []repoFailure // the last pass's failed by-name reads, in file order
	repos        []forge.Repo  // listing plus named, by full name: what the app reads
	// noToken: the forge gives nothing in full without a token (GitHub). It
	// is not listed again, and a primary on it is read like a mirror.
	noToken      bool
	noTokenAt    time.Time
	me           string        // login of the user the token belongs to, once read
	detailErr    error         // the repos whose detail or files could not be read
	mirrorFailed []repoFailure // the last mirror pass's failed reads
	mirrorAt     time.Time
	heat         []forge.HeatPoint
	filed        []forge.Issue
	// mirrorLimited: a rate limit kept the last mirror pass from some of
	// this forge's mirrors; they are read again once it ends (retryMirrors).
	mirrorLimited bool
	// limitUntil: the forge answered that its rate limit is spent (limitErr,
	// which its status shows); nothing is asked of it before then.
	limitUntil time.Time
	limitErr   error
}

// known reports whether the forge has answered a list once, either way.
func (fs *forgeState) known() bool { return !fs.listedAt.IsZero() || fs.listErr != nil || fs.noToken }

// isDropped reports whether a repo was left out as private.
func (fs *forgeState) isDropped(key string) bool { return fs.dropped[key] || fs.namedDropped[key] }

// repoFailure is one failed read of a repo: its key and what the status says
// about it.
type repoFailure struct{ key, text string }

// shownErr joins failed reads for a status, each text once (a rate limit
// hits every repo alike), leaving out the repos known to be private: a
// status never names one.
func (fs *forgeState) shownErr(fails []repoFailure) error {
	var parts []string
	for _, f := range fails {
		if !fs.isDropped(f.key) && !slices.Contains(parts, f.text) {
			parts = append(parts, f.text)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return errors.New(strings.Join(parts, "; "))
}

// namedErr is the failed by-name reads a status may name.
func (fs *forgeState) namedErr() error { return fs.shownErr(fs.namedFailed) }

// mirrorErr is the failed mirror reads a status may name.
func (fs *forgeState) mirrorErr() error { return fs.shownErr(fs.mirrorFailed) }

// withNamed is a forge's list plus the repos read by name, by full name.
func withNamed(listing []forge.Repo, named map[string]forge.Repo) []forge.Repo {
	out := append([]forge.Repo(nil), listing...)
	for _, r := range named {
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].FullName) < strings.ToLower(out[j].FullName) })
	return out
}

type fdroidData struct {
	pkgs map[string]fdroid.Package
	at   time.Time
}

type App struct {
	cfg     config.Config
	loc     *time.Location
	now     func() time.Time
	sources []string // the order of Site.Sources

	forges []*forgeState // config order
	byID   map[string]*forgeState
	httpc  *http.Client
	probeC *http.Client
	// homeC probes repo homepages: public addresses only (spec ruling 33).
	homeC *http.Client
	// forgeNames is each forge's name on the page, by id (forgeNames()).
	forgeNames map[string]string

	store *store.Store
	hub   *hub.Hub
	site  atomic.Pointer[model.Site]

	mu          sync.Mutex
	reg         *registry.Registry
	ideas       *registry.Ideas
	regGood     bool   // the last load read every configured file
	projRead    bool   // the projects file has been read once this run
	regSeen     string // the files' marks at the last load
	regRetry    time.Time
	repoSt      map[string]*repoState    // by repoKey
	mirrors     map[string]*forge.Mirror // by repoKey
	heat        []forge.HeatPoint
	filed       map[string]forge.Filed
	feed        *docker.Feed
	feedMod     time.Time
	probes      map[string]*probe.Result
	notesAd     notes.Adapter           // nil: notes type none
	notes       map[string][]notes.Note // by project slug
	notesSig    string
	notesAt     time.Time // last good check, changed or not
	notesErr    error     // what the last load found missing
	fdroid      map[string]fdroidData
	status      map[string]model.SourceStatus
	warned      map[string]bool
	lastRefresh time.Time
	started     time.Time // the first cycle's start, by a.now (EventsGrace)
	graceLogged bool      // the grace ended with a forge unlisted, and was logged

	kick      chan struct{}
	refreshCh chan struct{}

	assembleMu  sync.Mutex
	lastSummary string // guarded by assembleMu
	detailMu    sync.Mutex
	reposMu     sync.Mutex
	cycles      atomic.Int64

	// running holds one TryLock per collector: a forced cycle and the
	// collector's own ticker never run it twice at once.
	running struct{ heatmap, filed, docker, probes, notes, fdroid, mirrors sync.Mutex }
}

// NewForges builds a client for each configured forge, in config order. Each
// sends User-Agent waymark/<version>.
func NewForges(cfg config.Config) []forge.Forge {
	ua := "waymark/" + buildVersion(cfg)
	out := make([]forge.Forge, 0, len(cfg.Forges))
	for _, f := range cfg.Forges {
		if f.Type == "github" {
			c := github.New(f.ID, f.URL, f.Token)
			c.UserAgent = ua
			out = append(out, c)
		} else {
			c := gitea.New(f.ID, f.URL+"/api/v1", f.Token)
			c.UserAgent = ua
			out = append(out, c)
		}
	}
	return out
}

// buildVersion is the version User-Agent headers name: the config's, or dev.
func buildVersion(cfg config.Config) string {
	if cfg.Version == "" {
		return "dev"
	}
	return cfg.Version
}

// New validates cfg, opens the store and starts from the cached projects and
// ideas (or none: with no cache yet, or no source configured). forges are the
// clients of cfg.Forges, in the same order (NewForges builds them).
func New(cfg config.Config, forges []forge.Forge) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(forges) != len(cfg.Forges) {
		return nil, fmt.Errorf("%d forge clients for %d configured forges", len(forges), len(cfg.Forges))
	}
	a := &App{
		cfg:       cfg,
		now:       func() time.Time { return time.Now().UTC() },
		byID:      map[string]*forgeState{},
		httpc:     &http.Client{Timeout: 20 * time.Second},
		probeC:    probe.NewClient(),
		homeC:     probe.NewPublicClient(),
		hub:       hub.New(64),
		repoSt:    map[string]*repoState{},
		mirrors:   map[string]*forge.Mirror{},
		probes:    map[string]*probe.Result{},
		notesAd:   notesAdapter(cfg),
		notes:     map[string][]notes.Note{},
		fdroid:    map[string]fdroidData{},
		status:    map[string]model.SourceStatus{},
		warned:    map[string]bool{},
		kick:      make(chan struct{}, 1),
		refreshCh: make(chan struct{}, 1),
	}
	for i, f := range forges {
		if f.ID() != cfg.Forges[i].ID {
			return nil, fmt.Errorf("forge client %d is %q, but forges[%d] is %q", i, f.ID(), i, cfg.Forges[i].ID)
		}
		fs := &forgeState{f: f, cfg: cfg.Forges[i], dropped: map[string]bool{}, named: map[string]forge.Repo{}, namedDropped: map[string]bool{}}
		a.forges = append(a.forges, fs)
		a.byID[fs.cfg.ID] = fs
	}
	a.forgeNames = forgeNames(cfg.Forges)
	loc, err := time.LoadLocation(cfg.Site.Timezone)
	if err != nil {
		return nil, fmt.Errorf("site.timezone %s: %w", cfg.Site.Timezone, err)
	}
	a.loc = loc
	a.sources = a.sourceNames()
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if w := st.Warning(); w != "" {
		log.Printf("store: %s", w)
	}
	a.store = st
	a.reg, a.ideas = readCaches(cfg.DataDir, isSet(cfg.Projects), isSet(cfg.Ideas.File), a.forgeIDs())
	a.site.Store(a.emptySite(a.now()))
	return a, nil
}

func isSet(ref config.FileRef) bool { return ref != config.FileRef{} }

// forgeNames is how the page names each forge, by config id: by kind and URL
// (derive.ForgeName), with the id added when two forges share a name, so
// "Gitea (work)" and "Gitea (home)" stay apart.
func forgeNames(forges []config.Forge) map[string]string {
	names := make(map[string]string, len(forges))
	count := map[string]int{}
	for _, f := range forges {
		names[f.ID] = derive.ForgeName(f.Type, f.URL)
		count[names[f.ID]]++
	}
	for id, n := range names {
		if count[n] > 1 {
			names[id] = n + " (" + id + ")"
		}
	}
	return names
}

// notesAdapter is the reader for cfg's notes type; nil for none.
func notesAdapter(cfg config.Config) notes.Adapter {
	switch cfg.Notes.Type {
	case "markdown":
		return notes.NewMarkdown(cfg.Notes.Dir, cfg.Notes.Markers)
	case "claude-memory":
		return notes.NewClaudeMemory(cfg.Notes.Dir, cfg.Site.You)
	}
	return nil
}

// forgeIDs are the configured forge ids in config order: the keys a projects
// file may use, and the order that picks a pair's primary.
func (a *App) forgeIDs() []string {
	ids := make([]string, len(a.forges))
	for i, fs := range a.forges {
		ids[i] = fs.cfg.ID
	}
	return ids
}

func (a *App) heatmapOn() bool {
	for _, fs := range a.forges {
		if fs.cfg.Heatmap {
			return true
		}
	}
	return false
}

// sourceNames is the order of Site.Sources: the first forge, then the other
// sources that are configured (config.SourceNames order), then the other
// forges. The registry source reads the projects and ideas files, so it
// needs one of them; the ideas source is the filed-idea search, so it needs
// an ideas file.
func (a *App) sourceNames() []string {
	names := []string{a.forges[0].cfg.ID}
	for _, name := range config.SourceNames() {
		switch {
		case name == "registry" && !isSet(a.cfg.Projects) && !isSet(a.cfg.Ideas.File),
			name == "heatmap" && !a.heatmapOn(),
			name == "ideas" && a.filedQuery() == "",
			name == "docker" && a.cfg.Docker.Feed == "" && a.cfg.Docker.Host == "",
			name == "notes" && a.notesAd == nil:
			continue
		}
		names = append(names, name)
	}
	for _, fs := range a.forges[1:] {
		names = append(names, fs.cfg.ID)
	}
	return names
}

// emptySite is the site before the first cycle finishes: no projects, and
// every source marked as not read yet, so it never looks quiet and healthy.
func (a *App) emptySite(now time.Time) *model.Site {
	pending := make([]model.SourceStatus, 0, len(a.sources))
	for _, name := range a.sources {
		pending = append(pending, model.SourceStatus{Name: name, OK: false, Error: "first read in progress"})
	}
	meta := a.metaLocked()
	for _, p := range a.reg.Projects {
		meta.FDroid = meta.FDroid || p.FDroid != nil
	}
	return &model.Site{
		GeneratedAt: now, Sources: pending, Categories: []model.Category{}, Projects: []model.Project{},
		NeedsYou: []model.Action{}, Events: []model.Event{}, Releases: []model.Release{}, Ideas: []model.Idea{},
		Heatmap: model.Heatmap{Days: []model.HeatDay{}}, Graph: model.Graph{Nodes: []model.GraphNode{}, Edges: []model.GraphEdge{}},
		Meta: meta,
	}
}

// SetClock replaces the clock the app reads. Call it before Run or RunOnce:
// fixture runs pin it, so a world built for a fixed now reads as current.
func (a *App) SetClock(now func() time.Time) { a.now = now }

// Snapshot is the current site; never nil, never mutated after publishing.
func (a *App) Snapshot() *model.Site { return a.site.Load() }

func (a *App) Subscribe() (<-chan model.Change, func()) { return a.hub.Subscribe() }

// Refresh queues a full cycle, at most one per RefreshInterval; extra calls
// inside the interval are absorbed (nil, nothing queued).
func (a *App) Refresh(ctx context.Context) error {
	a.mu.Lock()
	now := a.now()
	if !a.lastRefresh.IsZero() && now.Sub(a.lastRefresh) < RefreshInterval {
		a.mu.Unlock()
		return nil
	}
	a.lastRefresh = now
	a.mu.Unlock()
	select {
	case a.refreshCh <- struct{}{}:
	default:
	}
	return nil
}

// Cycles is how many assembles have run.
func (a *App) Cycles() int64 { return a.cycles.Load() }

func (a *App) kickAssemble() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// begin marks when the app started, by its own clock (so after SetClock),
// the first time only: EventsGrace counts from it.
func (a *App) begin() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started.IsZero() {
		a.started = a.now()
	}
}

// RunOnce does one full collection, assembles, saves and returns the site.
func (a *App) RunOnce(ctx context.Context) (*model.Site, error) {
	a.begin()
	a.fullCycle(ctx, true)
	a.assemble()
	if err := a.store.Save(a.now()); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}
	return a.Snapshot(), nil
}

// Run collects once in full, then keeps every collector on its cadence until
// ctx ends, then saves.
func (a *App) Run(ctx context.Context) error {
	a.begin()
	a.fullCycle(ctx, false)
	a.assemble()
	var wg sync.WaitGroup
	loop := func(every time.Duration, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					fn(ctx)
				}
			}
		}()
	}
	loop(RepoListEvery, func(ctx context.Context) { a.collectForges(ctx, false) })
	loop(HeatmapEvery, a.collectHeatmap)
	loop(FiledEvery, a.collectFiled)
	loop(DockerEvery, a.collectDocker)
	loop(ProbeEvery, a.collectProbes)
	loop(NotesEvery, a.collectNotes)
	loop(FDroidEvery, a.collectFDroid)
	loop(MirrorEvery, a.collectMirrors)
	loop(MirrorRetryEvery, a.retryMirrors)

	wg.Add(3)
	go func() { defer wg.Done(); a.assembler(ctx) }()
	go func() {
		defer wg.Done()
		t := time.NewTicker(SaveEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.save()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.refreshCh:
				a.fullCycle(ctx, true)
				a.kickAssemble()
			}
		}
	}()
	wg.Wait()
	a.flush()
	log.Printf("collectors stopped after %d assembles; state saved", a.cycles.Load())
	return nil
}

// save is the SaveEvery tick: it writes state.json when events or
// fingerprints changed, or when probe points alone changed and the last
// write is ProbeSaveEvery old.
func (a *App) save() {
	if !a.store.Due(a.now(), ProbeSaveEvery) {
		return
	}
	if err := a.store.Save(a.now()); err != nil {
		log.Printf("store: save: %v", err)
	}
}

// flush is the write at shutdown: whatever changed, probe points included.
func (a *App) flush() {
	if !a.store.Dirty() && !a.store.ProbesDirty() {
		return
	}
	if err := a.store.Save(a.now()); err != nil {
		log.Printf("store: save: %v", err)
	}
}

func (a *App) assembler(ctx context.Context) {
	var tc <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.kick:
			if tc == nil {
				tc = time.After(Debounce)
			}
		case <-tc:
			tc = nil
			a.assemble()
		}
	}
}

// fullCycle runs the repo lists (and the projects files) first, then
// everything else in parallel, and returns when all are done.
func (a *App) fullCycle(ctx context.Context, force bool) {
	a.collectRepos(ctx, force)
	var wg sync.WaitGroup
	for _, fn := range []func(context.Context){
		func(ctx context.Context) { a.collectDetails(ctx, force) },
		a.collectHeatmap, a.collectFiled, a.collectDocker, a.collectProbes, a.collectNotes, a.collectFDroid, a.collectMirrors,
	} {
		wg.Add(1)
		go func(fn func(context.Context)) {
			defer wg.Done()
			fn(ctx)
		}(fn)
	}
	wg.Wait()
}

// collectForges is the repo-list cadence: every forge's list, then the
// detail it calls for.
func (a *App) collectForges(ctx context.Context, force bool) {
	a.collectRepos(ctx, force)
	a.collectDetails(ctx, force)
}

// setStatus records a source result; err == nil marks it fresh at refreshed.
func (a *App) setStatus(name string, err error, refreshed time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.setStatusLocked(name, err, refreshed)
}

func (a *App) setStatusLocked(name string, err error, refreshed time.Time) {
	a.setStatusNoteLocked(name, err, "", refreshed)
}

// setStatusNoteLocked is setStatusLocked with the status's note.
func (a *App) setStatusNoteLocked(name string, err error, note string, refreshed time.Time) {
	st := a.status[name]
	before := st
	st.Name, st.Note = name, note
	if err == nil {
		st.OK, st.Error = true, ""
		if !refreshed.IsZero() {
			st.RefreshedAt = refreshed.UTC()
		}
	} else {
		st.OK, st.Error = false, err.Error()
		if !refreshed.IsZero() {
			st.RefreshedAt = refreshed.UTC()
		}
		if before.Error != st.Error {
			log.Printf("source %s: %s", name, st.Error)
		}
	}
	a.status[name] = st
	if before != st {
		a.kickAssemble()
	}
}

// forgeStatusLocked sets a forge's source status from its parts: a failed
// list says only that; otherwise the failed by-name, repo and mirror reads
// are joined.
func (a *App) forgeStatusLocked(fs *forgeState) {
	refreshed := fs.listedAt
	for _, t := range []time.Time{fs.mirrorAt, fs.noTokenAt} {
		if t.After(refreshed) {
			refreshed = t
		}
	}
	var err error
	var parts []string
	namedErr, mirrorErr := fs.namedErr(), fs.mirrorErr()
	for _, e := range []error{namedErr, fs.detailErr, mirrorErr} {
		if e != nil {
			parts = append(parts, e.Error())
		}
	}
	switch {
	case a.limitedLocked(fs):
		err, refreshed = limitShown(fs), time.Time{}
	case fs.listErr != nil:
		err, refreshed = fmt.Errorf("repo list: %w (showing the last good data)", fs.listErr), time.Time{}
	case namedErr != nil || fs.detailErr != nil:
		err, refreshed = errors.New(strings.Join(parts, "; ")), fs.listedAt
	case mirrorErr != nil:
		err, refreshed = mirrorErr, time.Time{}
	}
	a.setStatusNoteLocked(fs.cfg.ID, err, a.noTokenNoteLocked(fs), refreshed)
}

// needsTokenLocked reports whether err is forge.ErrNeedsToken. The first time
// for a forge it stops listing it; its source stays OK (noTokenNoteLocked
// says what that leaves).
func (a *App) needsTokenLocked(fs *forgeState, err error) bool {
	if !errors.Is(err, forge.ErrNeedsToken) {
		return false
	}
	if !fs.noToken {
		fs.noToken, fs.noTokenAt = true, a.now()
	}
	return true
}

// noTokenNoteLocked is what a forge that lists nothing without a token says
// in its source row, and logs once: why its owners show nothing, or that
// nothing at all is read from it when no projects-file repo is on it. With
// no owners and named repos (read like mirrors) there is no note, only the
// usual log line. Whether a repo is named waits for the projects file's
// first read this run.
func (a *App) noTokenNoteLocked(fs *forgeState) string {
	if !fs.noToken {
		return ""
	}
	id, known := fs.cfg.ID, !isSet(a.cfg.Projects) || a.projRead
	named := false
	for _, p := range a.reg.Projects {
		for _, ref := range p.Repos {
			named = named || ref.Forge == id
		}
	}
	switch {
	case known && !named:
		a.noteOnce("forge " + id + ": no token, so nothing is listed, and no repo the projects file names is on it: nothing is read from it")
		return "listing is off without a token, and no repo the projects file names is on this forge: nothing is read"
	case len(fs.cfg.Owners) > 0:
		a.noteOnce("forge " + id + ": no token, so owners " + strings.Join(fs.cfg.Owners, ", ") + " are not listed; only the repos the projects file names are read")
		return "listing is off without a token: owners are not listed, only the repos the projects file names"
	case known:
		a.noteOnce("forge " + id + ": GitHub token needed for full detail; only public repo views are read")
	}
	return ""
}

// noteOnce logs msg the first time only.
func (a *App) noteOnce(msg string) {
	if !a.warned[msg] {
		a.warned[msg] = true
		log.Print(msg)
	}
}

// limitedLocked reports whether fs is waiting for its rate limit to reset.
func (a *App) limitedLocked(fs *forgeState) bool { return a.now().Before(fs.limitUntil) }

// limitWaitLocked is limitedLocked, with what a read the wait stops reports
// meanwhile: the forge's status words (limitShown).
func (a *App) limitWaitLocked(fs *forgeState) (limited bool, err error) {
	if !a.limitedLocked(fs) {
		return false, nil
	}
	return true, limitShown(fs)
}

// limitState is limitWaitLocked for a caller that does not hold a.mu.
func (a *App) limitState(fs *forgeState) (limited bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.limitWaitLocked(fs)
}

// limitHitLocked is rateLimitLocked for a read that reports err: when err is
// the spent limit, what the read reports is limitShown, so it names the
// retry like the forge's status. Any other err is returned as it is.
func (a *App) limitHitLocked(fs *forgeState, err error) error {
	if a.rateLimitLocked(fs, err) {
		return limitShown(fs)
	}
	return err
}

// rateLimitLocked reports whether err says fs's rate limit is spent. If so,
// nothing more is asked of fs until the reset it names, at most
// RateLimitMaxWait away (RateLimitRetry when it names none, or one already
// past); a shorter limit met meanwhile does not shorten the wait. Its status
// shows err and that time meanwhile (limitShown).
func (a *App) rateLimitLocked(fs *forgeState, err error) bool {
	reset, ok := forge.RateLimited(err)
	if !ok {
		return false
	}
	now := a.now()
	switch {
	case !reset.After(now):
		reset = now.Add(RateLimitRetry)
	case reset.After(now.Add(RateLimitMaxWait)):
		reset = now.Add(RateLimitMaxWait)
	}
	if reset.After(fs.limitUntil) {
		fs.limitUntil = reset
	}
	fs.limitErr = err
	if fs.known() {
		a.forgeStatusLocked(fs)
	}
	return true
}

// limitShown is what a rate-limited forge's status says: the forge's answer
// and when the app will ask it again, which is the wait itself (limitUntil),
// not the reset the forge named. The time is the only one shown anywhere;
// a client's error names none. It is the wait's end with the seconds cut
// off, so "after" it is true during the wait and stays true once the wait is
// over, until the read that met the limit runs again on its next tick.
func limitShown(fs *forgeState) error {
	return fmt.Errorf("%w; trying again after %s", fs.limitErr, fs.limitUntil.UTC().Format("15:04 UTC"))
}

func (a *App) warnOnce(msg string) {
	if !a.warned[msg] {
		a.warned[msg] = true
		log.Printf("warning: %s", msg)
	}
}

// reposLocked is every forge's last good list, in config order.
func (a *App) reposLocked() []forge.Repo {
	var out []forge.Repo
	for _, fs := range a.forges {
		out = append(out, fs.repos...)
	}
	return out
}

// projectsLocked is the file's projects (with unclaimed twins attached) plus
// an auto project for every other listed repo. Without include_private it
// fails closed: a project with a primary repo shows only once that repo is
// known to be public (in its forge's list or read by name), or when its
// forge lists nothing without a token and has not seen it as private.
// Otherwise it is left out whole (no card, needs-you, events, map node or
// ship log) until then: while the list fails, and for good when private.
func (a *App) projectsLocked() []registry.Project {
	all := a.reg.AllProjects(a.reposLocked(), a.forgeIDs())
	if a.cfg.IncludePrivate {
		return all
	}
	index := a.repoIndexLocked()
	kept := all[:0]
	for _, p := range all {
		prim := p.Primary()
		key := repoKey(prim.Forge, prim.FullName)
		_, public := index[key]
		fs := a.byID[prim.Forge]
		if prim == (registry.RepoRef{}) || public || fs != nil && fs.noToken && !fs.isDropped(key) {
			kept = append(kept, p)
		}
	}
	return kept
}

// listedLocked reports whether every forge has given its list once (or said
// it needs a token); before that some auto projects are not built yet.
func (a *App) listedLocked() bool {
	for _, fs := range a.forges {
		if fs.listedAt.IsZero() && !fs.noToken {
			return false
		}
	}
	return true
}

// eventsReadyLocked reports whether assemble may diff events: once every
// forge has listed (listed), or once EventsGrace has passed since start with
// some forge still unlisted (a wrong token, or offline), which it logs once,
// naming those forges.
func (a *App) eventsReadyLocked(now time.Time) (ready, listed bool) {
	if a.listedLocked() {
		return true, true
	}
	if a.started.IsZero() || now.Sub(a.started) < EventsGrace {
		return false, false
	}
	if !a.graceLogged {
		a.graceLogged = true
		var ids []string
		for _, fs := range a.forges {
			if fs.listedAt.IsZero() && !fs.noToken {
				ids = append(ids, fs.cfg.ID)
			}
		}
		who := "forge " + ids[0] + " has"
		if len(ids) > 1 {
			who = "forges " + strings.Join(ids, ", ") + " have"
		}
		log.Printf("events: %d minutes after start, %s not listed its repos; events go on without it, and its projects keep their last state",
			int(EventsGrace.Minutes()), who)
	}
	return true, false
}

func repoKey(forgeID, full string) string { return forgeID + "|" + strings.ToLower(full) }

func (a *App) repoIndexLocked() map[string]forge.Repo {
	index := map[string]forge.Repo{}
	for _, fs := range a.forges {
		for _, r := range fs.repos {
			index[repoKey(fs.cfg.ID, r.FullName)] = r
		}
	}
	return index
}

// repoPage is a repo's web page, linked before its first read.
func repoPage(fc config.Forge, full string) string {
	if fc.Type != "github" {
		return fc.URL + "/" + full
	}
	if web, ok := strings.CutSuffix(fc.URL, "/api/v3"); ok { // GitHub Enterprise
		return web + "/" + full
	}
	return "https://github.com/" + full
}

// mirrorRefLocked is the repo a project's mirror view comes from: the
// primary itself when its forge gives no detail without a token, else the
// first mirror not left out as private. ok is false when there is none.
func (a *App) mirrorRefLocked(p registry.Project) (ref registry.RepoRef, isPrimary, ok bool) {
	prim := p.Primary()
	if fs := a.byID[prim.Forge]; fs != nil && fs.noToken {
		return prim, true, true
	}
	for _, m := range p.Mirrors() {
		if fs := a.byID[m.Forge]; fs != nil && !fs.isDropped(repoKey(m.Forge, m.FullName)) {
			return m, false, true
		}
	}
	return registry.RepoRef{}, false, false
}

func (a *App) collectRepos(ctx context.Context, force bool) {
	// Serialised rather than skipped: a forced call must still force the
	// projects reload after a ticker call in flight.
	a.reposMu.Lock()
	defer a.reposMu.Unlock()
	failed := map[string]bool{}
	var failedMu sync.Mutex
	var wg sync.WaitGroup
	for _, fs := range a.forges {
		a.mu.Lock()
		skip, limited := fs.noToken, a.limitedLocked(fs)
		a.mu.Unlock()
		if limited {
			// As if down: its files and named repos wait for the reset too.
			failed[fs.cfg.ID] = true
			continue
		}
		if skip {
			continue
		}
		wg.Add(1)
		go func(fs *forgeState) {
			defer wg.Done()
			if !a.listForge(ctx, fs) {
				failedMu.Lock()
				failed[fs.cfg.ID] = true
				failedMu.Unlock()
			}
		}(fs)
	}
	wg.Wait()
	a.kickAssemble()
	a.reloadRegistry(ctx, force, failed)
	a.readNamed(ctx, failed)
}

// reloadRegistry reads the projects and ideas files again when they may have
// changed. A forge whose list just failed is down: the files it holds are not
// read this time (the cached or current copy stays).
func (a *App) reloadRegistry(ctx context.Context, force bool, failed map[string]bool) {
	for _, ref := range []config.FileRef{a.cfg.Projects, a.cfg.Ideas.File} {
		if ref.Forge != "" && failed[ref.Forge] {
			return
		}
	}
	pm, pk := a.fileMark(a.cfg.Projects)
	im, ik := a.fileMark(a.cfg.Ideas.File)
	mark, known := pm+"|"+im, pk && ik
	now := a.now()
	a.mu.Lock()
	reload := force || mark != a.regSeen || (now.After(a.regRetry) && (!a.regGood || !known))
	a.mu.Unlock()
	if reload {
		a.loadRegistry(ctx, mark, known)
	}
}

// listForge reads one forge's repo list, and the token's user until known.
// It reports false when the list failed.
func (a *App) listForge(ctx context.Context, fs *forgeState) bool {
	repos, err := fs.f.ListRepos(ctx, fs.cfg.Owners)
	a.mu.Lock()
	needMe := fs.me == ""
	a.mu.Unlock()
	var me forge.User
	var meErr error
	if err == nil && needMe {
		me, meErr = fs.f.Me(ctx)
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.needsTokenLocked(fs, err):
	case err != nil:
		fs.listErr = a.limitHitLocked(fs, err)
	default:
		// Before the private filter: an owner whose repos are all private
		// is not a typo.
		for _, o := range fs.cfg.Owners {
			if !slices.ContainsFunc(repos, func(r forge.Repo) bool {
				owner, _, _ := strings.Cut(r.FullName, "/")
				return strings.EqualFold(owner, o)
			}) {
				a.warnOnce("forge " + fs.cfg.ID + ": owner " + o + " has no repos the token can see (is the name right?)")
			}
		}
		sort.SliceStable(repos, func(i, j int) bool { return strings.ToLower(repos[i].FullName) < strings.ToLower(repos[j].FullName) })
		dropped := map[string]bool{}
		kept := repos[:0]
		for _, r := range repos {
			if r.Private && !a.cfg.IncludePrivate {
				dropped[repoKey(fs.cfg.ID, r.FullName)] = true
				continue
			}
			kept = append(kept, r)
		}
		fs.listing, fs.dropped, fs.listedAt, fs.listErr = kept, dropped, now, nil
		fs.repos = withNamed(kept, fs.named)
		switch {
		case a.rateLimitLocked(fs, meErr):
		case meErr != nil:
			a.warnOnce("forge " + fs.cfg.ID + ": the token's user could not be read: " + meErr.Error())
		case me.Login != "":
			fs.me = me.Login
		}
	}
	a.forgeStatusLocked(fs)
	return err == nil || fs.noToken
}

// readNamed reads by name every repo the projects file names that its
// forge's list lacks (an owner outside owners), as v0.1 read any repo the
// file named; include_private still applies. A failed read keeps the last
// good copy and is named in the forge's status. A forge that needs a token,
// or whose list just failed, is left as it is.
func (a *App) readNamed(ctx context.Context, failed map[string]bool) {
	a.mu.Lock()
	want := map[string][]string{} // forge id -> full names; present = read this pass
	for _, fs := range a.forges {
		id := fs.cfg.ID
		if fs.noToken || failed[id] || fs.listedAt.IsZero() {
			continue
		}
		listed := map[string]bool{}
		for _, r := range fs.listing {
			listed[repoKey(id, r.FullName)] = true
		}
		want[id] = nil
		seen := map[string]bool{}
		for _, p := range a.reg.Projects {
			for _, ref := range p.Repos {
				key := repoKey(ref.Forge, ref.FullName)
				if ref.Forge == id && !listed[key] && !fs.dropped[key] && !seen[key] {
					seen[key] = true
					want[id] = append(want[id], ref.FullName)
				}
			}
		}
	}
	a.mu.Unlock()
	for _, fs := range a.forges {
		names, ok := want[fs.cfg.ID]
		if !ok {
			continue
		}
		named, dropped := map[string]forge.Repo{}, map[string]bool{}
		var failed []repoFailure
		var limit error // once the forge says its rate limit is spent, nothing more is asked
		for _, full := range names {
			key := repoKey(fs.cfg.ID, full)
			var r forge.Repo
			err := limit
			if err == nil {
				if r, err = fs.f.Repo(ctx, full); err != nil {
					a.mu.Lock()
					if a.rateLimitLocked(fs, err) {
						err = limitShown(fs)
						limit = err
					}
					a.mu.Unlock()
				}
			}
			if err != nil {
				failed = append(failed, repoFailure{key, namedProblem(fs.f, full, err)})
				continue
			}
			if r.Private && !a.cfg.IncludePrivate {
				dropped[key] = true
				continue
			}
			named[key] = r
		}
		a.mu.Lock()
		// A failed read keeps what was known, as of now: the last good copy,
		// or that the repo is private (which a mirror view may have learnt
		// during this pass), and private wins.
		for _, nf := range failed {
			if dropped[nf.key] = fs.namedDropped[nf.key]; !dropped[nf.key] {
				if last, ok := fs.named[nf.key]; ok {
					named[nf.key] = last
				}
			}
		}
		fs.named, fs.namedDropped, fs.namedFailed = named, dropped, failed
		fs.repos = withNamed(fs.listing, named)
		a.forgeStatusLocked(fs)
		a.mu.Unlock()
		if len(names) > 0 {
			a.kickAssemble()
		}
	}
}

// namedProblem is what a forge's status says about a projects-file repo that
// could not be read by name.
func namedProblem(f forge.Forge, full string, err error) string {
	if nf, ok := f.(interface{ IsNotFound(error) bool }); ok && nf.IsNotFound(err) {
		return full + " (named in the projects file) was not found"
	}
	return full + " (named in the projects file): " + err.Error()
}

// fileMark tells whether a projects or ideas file may have changed: a local
// file's mtime, or the updated_at of the repo holding it in its forge's last
// list. known is false when neither can be seen; such a file is read again
// every RegistryRetry.
func (a *App) fileMark(ref config.FileRef) (mark string, known bool) {
	switch {
	case !isSet(ref):
		return "", true
	case ref.File != "":
		st, err := os.Stat(ref.File)
		if err != nil {
			return "", false
		}
		return st.ModTime().UTC().Format(time.RFC3339Nano), true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if fs := a.byID[ref.Forge]; fs != nil {
		for _, r := range fs.repos {
			if strings.EqualFold(r.FullName, ref.Repo) {
				return r.UpdatedAt.UTC().Format(time.RFC3339Nano), true
			}
		}
	}
	return "", false
}

// readFile reads a projects or ideas file from disk, or through its forge's
// raw-file read. notFound is true when the file is not there.
func (a *App) readFile(ctx context.Context, ref config.FileRef) (b []byte, notFound bool, err error) {
	if ref.File != "" {
		b, err = os.ReadFile(ref.File)
		return b, os.IsNotExist(err), err
	}
	fs := a.byID[ref.Forge]
	src, ok := fs.f.(forge.FileSource)
	if !ok {
		return nil, false, fmt.Errorf("forge %s cannot read files", ref.Forge)
	}
	if limited, limitErr := a.limitState(fs); limited {
		return nil, false, limitErr
	}
	b, err = src.Raw(ctx, ref.Repo, ref.Path, "")
	if err != nil {
		notFound = src.IsNotFound(err)
		a.mu.Lock()
		err = a.limitHitLocked(fs, err)
		a.mu.Unlock()
	}
	return b, notFound, err
}

// fileProblem is what the registry source says about a file that could not
// be read.
func fileProblem(ref config.FileRef, notFound bool, err error) string {
	switch {
	case ref.File != "" && notFound:
		return ref.File + " does not exist yet"
	case ref.File != "":
		return err.Error()
	case notFound:
		return ref.Path + " is not in " + ref.Repo + " yet"
	}
	return ref.Path + ": " + err.Error()
}

// loadRegistry reads the projects file and the ideas file. A good read
// replaces the current copy and is cached in DataDir; on any failure the
// current copy stays (the cached one after a restart, or none).
func (a *App) loadRegistry(ctx context.Context, mark string, known bool) {
	projRef, ideasRef := a.cfg.Projects, a.cfg.Ideas.File
	var problems []string
	var reg *registry.Registry
	if isSet(projRef) {
		if b, notFound, err := a.readFile(ctx, projRef); err != nil {
			problems = append(problems, fileProblem(projRef, notFound, err))
		} else if r, err := registry.Parse(b, a.forgeIDs()); err != nil {
			problems = append(problems, err.Error()+" (keeping the last good copy)")
		} else {
			reg = r
			a.cacheWrite(projectsCache, b)
		}
	}
	var ideas *registry.Ideas
	if isSet(ideasRef) {
		if b, notFound, err := a.readFile(ctx, ideasRef); err != nil {
			problems = append(problems, fileProblem(ideasRef, notFound, err))
		} else if i, err := registry.ParseIdeas(b); err != nil {
			problems = append(problems, err.Error()+" (keeping the last good copy)")
		} else {
			ideas = i
			a.cacheWrite(ideasCache, b)
		}
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.regSeen = mark
	if reg != nil {
		a.reg, a.projRead = reg, true
		// Which repos the file names on a forge without a token decides
		// that forge's note.
		for _, fs := range a.forges {
			if fs.noToken {
				a.forgeStatusLocked(fs)
			}
		}
	}
	if ideas != nil {
		a.ideas = ideas
	}
	a.regGood = (!isSet(projRef) || reg != nil) && (!isSet(ideasRef) || ideas != nil)
	if !a.regGood || !known {
		a.regRetry = now.Add(RegistryRetry)
	}
	for _, w := range a.reg.Warnings {
		a.warnOnce("registry: " + w)
	}
	if len(problems) > 0 {
		a.setStatusLocked("registry", fmt.Errorf("%s", strings.Join(problems, "; ")), time.Time{})
		return
	}
	a.setStatusLocked("registry", nil, now)
}

// cacheWrite stores the last good bytes of a source; a failure is logged once
// and never fails the load.
func (a *App) cacheWrite(name string, b []byte) {
	if err := writeFileAtomic(filepath.Join(a.cfg.DataDir, name), b); err != nil {
		a.mu.Lock()
		a.warnOnce("cache " + name + ": " + err.Error())
		a.mu.Unlock()
	}
}

// fullRepo is a listed repo read in full: one on a project's primary forge.
type fullRepo struct {
	fs       *forgeState
	repo     forge.Repo
	key      string
	planDirs []string
}

// fullReposLocked lists the repos read in full: every listed repo on a
// project's primary forge, with the plan directories to read (the project's
// own plan_dirs, else the configured ones).
func (a *App) fullReposLocked() []fullRepo {
	index := a.repoIndexLocked()
	seen := map[string]bool{}
	var out []fullRepo
	for _, p := range a.projectsLocked() {
		dirs := p.PlanDirs
		if len(dirs) == 0 {
			dirs = a.cfg.Plans.Dirs
		}
		for _, ref := range p.FullRepos() {
			fs := a.byID[ref.Forge]
			key := repoKey(ref.Forge, ref.FullName)
			r, listed := index[key]
			if fs == nil || fs.noToken || !listed || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, fullRepo{fs: fs, repo: r, key: key, planDirs: dirs})
		}
	}
	return out
}

type detailJob struct {
	fullRepo
	detail, files bool
}

// collectDetails refreshes repo detail when the listing shows a change, a run
// is in progress, or it is 30 min old; files when updated_at moved or 6 h.
// Each forge reads its own repos. force (a forced refresh) reads every
// detail again on a Gitea forge, as 0.1.0 did; on GitHub, whose rate limit
// is shared and punitive, it adds nothing: the listing it re-reads already
// shows what moved (pushed_at or updated_at), and a failed read is retried.
func (a *App) collectDetails(ctx context.Context, force bool) {
	a.detailMu.Lock()
	defer a.detailMu.Unlock()
	now := a.now()
	a.mu.Lock()
	jobs := map[*forgeState][]detailJob{}
	for _, fr := range a.fullReposLocked() {
		if a.limitedLocked(fr.fs) {
			continue
		}
		st := a.repoSt[fr.key]
		if st == nil {
			st = &repoState{}
			a.repoSt[fr.key] = st
		}
		r, d := fr.repo, st.detail
		j := detailJob{fullRepo: fr}
		forced := force && fr.fs.cfg.Type != "github"
		j.detail = forced || d == nil || st.detailErr != nil || !r.UpdatedAt.Equal(st.seenUpdated) ||
			r.OpenIssues != st.seenIssues || r.OpenPRs != st.seenPRs || d.InProgress() || now.Sub(d.FetchedAt) >= DetailMaxAge
		j.files = st.files == nil || st.filesErr != nil || !r.UpdatedAt.Equal(st.filesUpdated) || now.Sub(st.files.FetchedAt) >= FilesMaxAge
		if j.detail || j.files {
			jobs[fr.fs] = append(jobs[fr.fs], j)
		}
	}
	a.mu.Unlock()

	changed := atomic.Bool{}
	var wg sync.WaitGroup
	for _, js := range jobs {
		wg.Add(1)
		go func(js []detailJob) {
			defer wg.Done()
			a.runDetailJobs(ctx, js, now, &changed)
		}(js)
	}
	wg.Wait()

	a.mu.Lock()
	type tally struct {
		failed, wanted int
		first          error
	}
	tallies := map[*forgeState]*tally{}
	for _, fr := range a.fullReposLocked() {
		t := tallies[fr.fs]
		if t == nil {
			t = &tally{}
			tallies[fr.fs] = t
		}
		t.wanted++
		st := a.repoSt[fr.key]
		if st == nil {
			continue
		}
		for _, e := range []error{st.detailErr, st.filesErr} {
			if e != nil {
				t.failed++
				if t.first == nil {
					t.first = e
				}
				break
			}
		}
	}
	for _, fs := range a.forges {
		fs.detailErr = nil
		if t := tallies[fs]; t != nil && t.first != nil {
			fs.detailErr = fmt.Errorf("%d of %d repos could not be read (showing their last good data); first: %v", t.failed, t.wanted, t.first)
		}
		if fs.known() {
			a.forgeStatusLocked(fs)
		}
	}
	a.mu.Unlock()
	if changed.Load() {
		a.kickAssemble()
	}
}

// runDetailJobs runs one forge's jobs, ForgeWorkers at a time.
func (a *App) runDetailJobs(ctx context.Context, jobs []detailJob, now time.Time, changed *atomic.Bool) {
	ch := make(chan detailJob)
	var wg sync.WaitGroup
	for w := 0; w < ForgeWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				a.runDetailJob(ctx, j, now, changed)
			}
		}()
	}
	for _, j := range jobs {
		select {
		case ch <- j:
		case <-ctx.Done():
		}
	}
	close(ch)
	wg.Wait()
}

// A job the forge's rate limit stops (met by another job) is not run: the
// repo keeps its last good detail and files.
func (a *App) runDetailJob(ctx context.Context, j detailJob, now time.Time, changed *atomic.Bool) {
	if limited, _ := a.limitState(j.fs); j.detail && !limited {
		d, err := j.fs.f.FetchDetail(ctx, j.repo, now.Add(-CommitWindow), CommitPages, now)
		a.mu.Lock()
		st := a.repoSt[j.key]
		if a.needsTokenLocked(j.fs, err) {
			err = nil
		}
		st.detailErr = a.limitHitLocked(j.fs, err)
		if err == nil && d != nil {
			st.detail = d
			st.seenUpdated, st.seenIssues, st.seenPRs = j.repo.UpdatedAt.Time, j.repo.OpenIssues, j.repo.OpenPRs
			changed.Store(true)
		}
		a.mu.Unlock()
	}
	if limited, _ := a.limitState(j.fs); j.files && !limited {
		f, err := j.fs.f.FetchFiles(ctx, j.repo, j.planDirs, now)
		a.mu.Lock()
		st := a.repoSt[j.key]
		if a.needsTokenLocked(j.fs, err) {
			err = nil
		}
		st.filesErr = a.limitHitLocked(j.fs, err)
		if err == nil && f != nil {
			st.files = f
			st.filesUpdated = j.repo.UpdatedAt.Time
			changed.Store(true)
		}
		a.mu.Unlock()
	}
}
