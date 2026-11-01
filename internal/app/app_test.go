package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/demo"
	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
)

var fixedNow = time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)

const td = "../../testdata/"

func readT(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// world is the demo world (internal/demo) behind one fake server: Gitea's
// API, GitHub's, the probes and the F-Droid index. It records every request
// and can make some of them fail.
type world struct {
	t        *testing.T
	srv      *httptest.Server
	demo     *demo.World
	app      *App
	feedDir  string
	dataDir  string
	listFail atomic.Bool
	rawFail  atomic.Bool // every /raw/ read answers 500
	mu       sync.Mutex
	leaks    []string
	giteaHit atomic.Int32
	hits     map[string]int    // guarded by mu: requests per path
	body     map[string][]byte // guarded by mu: what an edited path serves instead
	// probeGate, when set, holds /probe/beacon until closed, after a send
	// on probeArrived (guarded by mu).
	probeGate, probeArrived chan struct{}
}

// count is how many requests a path has had.
func (w *world) count(path string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hits[path]
}

// edit rewrites what a path serves, starting from what it serves now.
func (w *world) edit(path string, fn func([]byte) []byte) {
	w.mu.Lock()
	b, ok := w.body[path]
	w.mu.Unlock()
	if !ok {
		rec := httptest.NewRecorder()
		demo.Handler(w.demo).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		b = rec.Body.Bytes()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.body[path] = fn(b)
}

func newWorld(t *testing.T) *world {
	w := &world{t: t, demo: demo.Dataset(fixedNow), feedDir: t.TempDir(), dataDir: t.TempDir(), hits: map[string]int{}, body: map[string][]byte{}}
	fake := demo.Handler(w.demo)
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.mu.Lock()
		w.hits[p]++
		b, edited := w.body[p]
		gate, arrived := w.probeGate, w.probeArrived
		w.mu.Unlock()
		isGitea := strings.HasPrefix(p, "/api/v1/")
		if auth := r.Header.Get("Authorization"); isGitea {
			w.giteaHit.Add(1)
			if auth != "token test-token" {
				w.leak("gitea request without the token: " + p)
			}
		} else if auth != "" {
			w.leak("credential sent outside Gitea: " + p)
		}
		if r.Method != http.MethodGet {
			w.leak("non-GET " + r.Method + " " + p)
		}
		switch {
		case p == "/api/v1/user/repos" && w.listFail.Load(),
			strings.Contains(p, "/raw/") && w.rawFail.Load():
			http.Error(rw, "boom", http.StatusInternalServerError)
		case edited:
			_, _ = rw.Write(b)
		case p == "/probe/beacon" && gate != nil:
			arrived <- struct{}{}
			<-gate
			fake.ServeHTTP(rw, r)
		default:
			fake.ServeHTTP(rw, r)
		}
	}))
	t.Cleanup(w.srv.Close)
	w.demo.SetBase(w.srv.URL)
	w.writeFeed(nil, "", 0)

	// The v0.1 setup: Gitea with a token and the projects files in a repo,
	// GitHub for mirrors only (no token, no heatmap), the claude-memory notes
	// adapter (the invented notes in testdata/notes, waiting on Mark), the feed and
	// the default ideas labels and marker.
	cfg := config.Config{
		DataDir:        w.dataDir,
		Site:           config.Site{Title: "Roadmap", Timezone: "Europe/Lisbon", You: []string{"Mark"}},
		IncludePrivate: true,
		Forges: []config.Forge{
			{ID: "gitea", Type: "gitea", URL: w.srv.URL, Token: "test-token", Owners: []string{"acme"}, Heatmap: true},
			{ID: "github", Type: "github", URL: w.srv.URL},
		},
		Projects: config.FileRef{Forge: "gitea", Repo: "acme/waymark-config", Path: "projects.yaml"},
		Ideas: config.Ideas{Labels: []string{"idea", "enhancement"}, Marker: "waymark-idea:",
			File: config.FileRef{Forge: "gitea", Repo: "acme/waymark-config", Path: "ideas.json"}},
		Notes:  config.Notes{Type: "claude-memory", Dir: td + "notes"},
		Plans:  config.Plans{Dirs: []string{"docs/plans"}},
		Docker: config.Docker{Feed: filepath.Join(w.feedDir, "docker.json"), Label: "web-01"},
	}
	w.app = w.newApp(cfg)
	return w
}

// newApp is New with the clock fixed and the registry swapped for the
// world's, as the projects file in the forge gives it.
func (w *world) newApp(cfg config.Config) *App {
	w.t.Helper()
	a, err := New(cfg, NewForges(cfg))
	if err != nil {
		w.t.Fatal(err)
	}
	a.now = func() time.Time { return fixedNow }
	reg, err := registry.Parse(w.registry(), a.forgeIDs())
	if err != nil {
		w.t.Fatal(err)
	}
	ideas, err := registry.ParseIdeas(w.demo.IdeasJSON())
	if err != nil {
		w.t.Fatal(err)
	}
	a.reg, a.ideas = reg, ideas
	return a
}

func (w *world) leak(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.leaks = append(w.leaks, s)
}

// registry is the projects file as the forge serves it.
func (w *world) registry() []byte { return w.demo.ProjectsYAML() }

var (
	statusRe  = regexp.MustCompile(`"Status":"[^"]*"`)
	upHoursRe = regexp.MustCompile(`"Status":"Up (\d+) hours`)
)

// writeFeed writes the world's feed, applying edits (container -> state).
// Docker's text moves on between two writes of a real feed, so the edited
// containers read "Exited (1) <stoppedFor> ago" and every "Up N hours" reads
// upHours more.
func (w *world) writeFeed(edits map[string]string, stoppedFor string, upHours int) {
	lines := strings.Split(strings.TrimSpace(string(w.demo.FeedJSONL())), "\n")
	for i, l := range lines {
		for name, state := range edits {
			if strings.Contains(l, `"Names":"`+name+`"`) {
				l = strings.Replace(l, `"State":"running"`, `"State":"`+state+`"`, 1)
				l = statusRe.ReplaceAllLiteralString(l, `"Status":"Exited (1) `+stoppedFor+` ago"`)
			}
		}
		if upHours > 0 {
			l = upHoursRe.ReplaceAllStringFunc(l, func(m string) string {
				n, _ := strconv.Atoi(upHoursRe.FindStringSubmatch(m)[1])
				return `"Status":"Up ` + strconv.Itoa(n+upHours) + ` hours`
			})
		}
		lines[i] = l
	}
	p := filepath.Join(w.feedDir, "docker.json")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		w.t.Fatal(err)
	}
	mt := fixedNow.Add(-30 * time.Second)
	if edits != nil {
		mt = fixedNow.Add(-10 * time.Second)
	}
	if err := os.Chtimes(p, mt, mt); err != nil {
		w.t.Fatal(err)
	}
}

func find(s *model.Site, slug string) *model.Project {
	p := s.ProjectBySlug(slug)
	if p == nil {
		panic("no project " + slug)
	}
	return p
}

func TestRunOnceAgainstFixtures(t *testing.T) {
	w := newWorld(t)
	site, err := w.app.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(site.Projects); n != 11 {
		t.Errorf("projects = %d, want 11 (10 Gitea repos, one hidden; a GitHub repo; one with no repo)", n)
	}
	for _, st := range site.Sources {
		// The notes dir has no beacon_app.md, which the projects file
		// names: that must show as a notes failure, not silence.
		if st.Name == "notes" {
			if st.OK || !strings.Contains(st.Error, "beacon_app.md") || st.RefreshedAt.IsZero() {
				t.Errorf("notes status = %+v", st)
			}
			continue
		}
		if !st.OK {
			t.Errorf("source %s failed: %s", st.Name, st.Error)
		}
	}
	if len(site.Sources) != len(w.app.sources) {
		t.Errorf("sources = %+v", site.Sources)
	}
	b := find(site, "beacon")
	if b.Tier != model.TierActive || b.Deploy == nil || b.Deploy.Version != "v2.3.1" || b.Deploy.Latest != "v2.4.0" || !b.Deploy.Lagging {
		t.Errorf("beacon = %s %+v", b.Tier, b.Deploy)
	}
	if len(b.Probes) != 1 || !b.Probes[0].Up || len(b.Probes[0].History) != 1 {
		t.Errorf("beacon probes = %+v", b.Probes)
	}
	if b.Unreleased == nil || b.Unreleased.LastVersion != "2.4.0" || b.Unreleased.Count != 3 || len(b.Plans) != 1 || b.Plans[0].Done != 3 {
		t.Errorf("beacon files = %+v %+v", b.Unreleased, b.Plans)
	}
	l := find(site, "ledger")
	if !l.Auto || l.Deploy == nil || l.Deploy.Version != "1.8.0" || l.Deploy.State != "running" || l.Tier != model.TierActive {
		t.Errorf("ledger = auto %v tier %s deploy %+v", l.Auto, l.Tier, l.Deploy)
	}
	at := find(site, "atlas")
	if at.Deploy == nil || at.Deploy.Version != "v0.7.2" || at.HeadlineLevel != model.LevelAlert || at.Headline != "Live is not answering (HTTP 503)" {
		t.Errorf("atlas = %+v %q", at.Deploy, at.Headline)
	}
	// GitHub without a token still gives the mirror's public view.
	if h := find(site, "harbour"); h.GitHub == nil || h.GitHub.Stars != 128 || h.GitHub.LatestRelease != "v3.1.0" {
		t.Errorf("harbour github = %+v", h.GitHub)
	}
	verbs := map[string]bool{}
	for _, a := range append(b.Actions, l.Actions...) {
		verbs[a.Verb] = true
	}
	if !verbs["Deploy"] || !verbs["Review"] || !verbs["Decide"] || len(at.Actions) == 0 || at.Actions[0].Verb != "Fix" {
		t.Errorf("beacon and ledger actions = %+v %+v, atlas %+v", b.Actions, l.Actions, at.Actions)
	}
	ba := find(site, "beacon-app")
	if ba.Deploy == nil || ba.Deploy.Source != "fdroid" || ba.Deploy.Version != "1.6.0" || ba.Deploy.State != "published" || ba.Deploy.Container != "fdroid-repo" {
		t.Errorf("beacon-app deploy = %+v", ba.Deploy)
	}
	on := find(site, "office-network")
	if len(on.Notes) != 1 || on.Notes[0].File != "office_network.md" {
		t.Errorf("office-network should pick up office_network.md by name: %+v", on.Notes)
	}
	if find(site, "old-site").Tier != model.TierArchived {
		t.Error("old-site is retired")
	}
	if site.ProjectBySlug("waymark-config") != nil {
		t.Error("hidden repo shown")
	}
	if len(site.Heatmap.Days) != 371 {
		t.Errorf("heatmap days = %d", len(site.Heatmap.Days))
	}
	heuristic := 0
	for _, a := range site.NeedsYou {
		if a.Heuristic {
			heuristic++
		}
	}
	if heuristic == 0 || site.Stats.NeedsYou != len(site.NeedsYou) {
		t.Errorf("needs you = %+v", site.NeedsYou)
	}
	if len(site.Events) != 0 {
		t.Errorf("first assemble emitted %+v", site.Events)
	}
	if _, err := os.Stat(filepath.Join(w.dataDir, "state.json")); err != nil {
		t.Errorf("state.json: %v", err)
	}
	if len(w.leaks) > 0 {
		t.Errorf("request problems: %v", w.leaks)
	}
	if w.giteaHit.Load() < 50 {
		t.Errorf("gitea requests = %d", w.giteaHit.Load())
	}
}

func TestChangesBecomeEventsAndPublish(t *testing.T) {
	w := newWorld(t)
	if _, err := w.app.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ch, cancel := w.app.Subscribe()
	defer cancel()
	w.writeFeed(map[string]string{"beacon": "exited"}, "About a minute", 0)
	w.app.collectDocker(context.Background())
	w.app.assemble()

	var kinds []string
	var activity *model.Event
	timeout := time.After(2 * time.Second)
	for done := false; !done; {
		select {
		case c := <-ch:
			kinds = append(kinds, c.Kind+":"+c.Slug)
			if c.Kind == "activity" && c.Event != nil && c.Event.Kind == "container" {
				activity = c.Event
			}
			if c.Kind == "tick" {
				done = true
			}
		case <-timeout:
			t.Fatalf("no tick; got %v", kinds)
		}
	}
	joined := strings.Join(kinds, " ")
	if !strings.Contains(joined, "project:beacon") || activity == nil || activity.Text != "Container beacon stopped" {
		t.Errorf("changes = %v activity = %+v", kinds, activity)
	}
	s := w.app.Snapshot()
	bc := find(s, "beacon")
	if bc.HeadlineLevel != model.LevelAlert || !strings.HasPrefix(bc.Headline, "Container beacon is stopped") {
		t.Errorf("headline = %q", bc.Headline)
	}
	if len(s.Events) == 0 || s.Events[0].Kind != "container" {
		t.Errorf("site events = %+v", s.Events)
	}

	// The same state again is no new event (IDs are deterministic).
	w.app.assemble()
	if got := w.app.Snapshot().Events; len(got) != len(s.Events) {
		t.Errorf("re-assemble duplicated events: %d -> %d", len(s.Events), len(got))
	}

	// A restart reads the saved state and does not replay anything.
	w.app.save()
	b := w.newApp(w.app.cfg)
	if b.store.Empty() || len(b.store.Events(time.Time{}, fixedNow)) != len(s.Events) {
		t.Error("restart lost the state")
	}
}

// drainUntilTick collects the changes published up to the next tick.
func drainUntilTick(t *testing.T, ch <-chan model.Change) []string {
	t.Helper()
	var got []string
	timeout := time.After(2 * time.Second)
	for {
		select {
		case c := <-ch:
			if c.Kind == "tick" {
				return got
			}
			got = append(got, c.Kind+":"+c.Slug)
		case <-timeout:
			t.Fatalf("no tick; got %v", got)
		}
	}
}

// An outage that lasts is not re-published on every poll: the Fix actions
// keep the time the down state began, and docker's ageing text ("Exited (1)
// 3 minutes ago", "Up 20 hours") is not part of what the hash covers.
func TestOutageDoesNotRepublishEachPoll(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	w.writeFeed(map[string]string{"beacon": "exited"}, "About a minute", 0)
	// The feed mtime writeFeed sets, less docker's "About a minute".
	stoppedAt := fixedNow.Add(-10*time.Second - time.Minute)
	w.app.collectDocker(ctx)
	w.app.assemble()
	ch, cancel := w.app.Subscribe()
	defer cancel()

	// Two minutes on: the feed is rewritten with the same states but
	// docker's text has moved on (beacon "Exited (1) 3 minutes ago", and
	// the uptimes past an hour mark, harbour's "Up 19 hours" now "Up 20
	// hours"), and the probes run again (atlas is still answering 503).
	later := fixedNow.Add(2 * time.Minute)
	w.app.now = func() time.Time { return later }
	w.writeFeed(map[string]string{"beacon": "exited"}, "3 minutes", 1)
	p := filepath.Join(w.feedDir, "docker.json")
	if err := os.Chtimes(p, later.Add(-time.Second), later.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	w.app.collectDocker(ctx)
	w.app.collectProbes(ctx)
	w.app.assemble()
	for _, c := range drainUntilTick(t, ch) {
		if strings.HasPrefix(c, "project:") {
			t.Errorf("%s re-published, but nothing it shows has changed", c)
		}
	}
	if st := find(w.app.Snapshot(), "beacon").Deploy.Status; st != "Exited (1) 3 minutes ago" {
		t.Errorf("the second feed was not read: beacon status %q", st)
	}
	if st := find(w.app.Snapshot(), "harbour").Deploy.Status; st != "Up 20 hours" {
		t.Errorf("the second feed was not read: harbour status %q", st)
	}
	s := w.app.Snapshot()
	for _, a := range find(s, "beacon").Actions {
		if a.Verb == "Fix" && !a.At.Equal(stoppedAt) {
			t.Errorf("beacon Fix at %v, want %v (first seen stopped)", a.At, stoppedAt)
		}
	}
}

// Gitea unreachable when Waymark starts (a host reboot, Gitea still coming
// up): tiers worked out without commit data must not become events, either
// when the store is empty or after a good run.
func TestGiteaDownAtStartEmitsNoTierEvents(t *testing.T) {
	ctx := context.Background()
	t.Run("first start", func(t *testing.T) {
		w := newWorld(t)
		w.listFail.Store(true)
		if _, err := w.app.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		w.listFail.Store(false)
		s, err := w.app.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Events) != 0 {
			t.Errorf("Gitea answering after a first start without it emitted %+v", s.Events)
		}
	})
	t.Run("restart after a good run", func(t *testing.T) {
		w := newWorld(t)
		if _, err := w.app.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		w.listFail.Store(true)
		b := w.newApp(w.app.cfg)
		s, err := b.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Events) != 0 {
			t.Errorf("restart with Gitea down emitted %+v", s.Events)
		}
		if bc := find(s, "beacon"); bc.TierReason != "Deployed and answering, Gitea not read yet" {
			t.Errorf("beacon tier reason = %q", bc.TierReason)
		}
		fps := b.store.Fingerprints()
		if fp, ok := fps["ledger"]; !ok || fp.Tier != model.TierActive {
			t.Errorf("auto project fingerprint lost while Gitea was down: %+v", fp)
		}
		if fps["beacon"].Tier != model.TierActive {
			t.Errorf("stored beacon tier = %q, want active kept", fps["beacon"].Tier)
		}
		w.listFail.Store(false)
		s, err = b.RunOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Events) != 0 {
			t.Errorf("Gitea coming back emitted %+v", s.Events)
		}
	})
}

func TestSourceFailureKeepsLastGoodData(t *testing.T) {
	w := newWorld(t)
	if _, err := w.app.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.listFail.Store(true)
	w.app.collectForges(context.Background(), false)
	if err := os.Remove(filepath.Join(w.feedDir, "docker.json")); err != nil {
		t.Fatal(err)
	}
	w.app.collectDocker(context.Background())
	w.app.assemble()
	s := w.app.Snapshot()
	if len(s.Projects) != 11 {
		t.Errorf("projects after a failed list = %d", len(s.Projects))
	}
	failed := map[string]string{}
	for _, st := range s.Sources {
		if !st.OK {
			failed[st.Name] = st.Error
		}
	}
	delete(failed, "notes") // see TestRunOnceAgainstFixtures
	if !strings.Contains(failed["gitea"], "500") || !strings.Contains(failed["docker"], "no feed") || len(failed) != 2 {
		t.Errorf("failed sources = %v", failed)
	}
	if b := find(s, "beacon"); b.Deploy == nil || b.Deploy.Version != "v2.3.1" {
		t.Errorf("last good docker data dropped: %+v", b.Deploy)
	}
}

func TestRefreshIsRateLimited(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := w.app.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(w.app.refreshCh); n != 1 {
		t.Errorf("queued refreshes = %d, want 1", n)
	}
	<-w.app.refreshCh
	w.app.now = func() time.Time { return fixedNow.Add(RefreshInterval) }
	_ = w.app.Refresh(ctx)
	if n := len(w.app.refreshCh); n != 1 {
		t.Errorf("after the interval, queued = %d, want 1", n)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.app.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for w.app.Cycles() == 0 {
		select {
		case <-deadline:
			t.Fatal("no first cycle")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if len(w.app.Snapshot().Projects) != 11 {
		t.Errorf("projects = %d", len(w.app.Snapshot().Projects))
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// A token is optional now (GitHub reads public repos without one), but a
// forge is not.
func TestNewNeedsAForge(t *testing.T) {
	cfg := config.Config{DataDir: t.TempDir(), Site: config.Site{Timezone: "UTC"}, Notes: config.Notes{Type: "none"}}
	if _, err := New(cfg, nil); err == nil {
		t.Error("New without a forge must fail")
	}
	cfg.Forges = []config.Forge{{ID: "gitea", Type: "gitea", URL: "https://git.example.com"}}
	if _, err := New(cfg, nil); err == nil {
		t.Error("New with no client for the configured forge must fail")
	}
}

// A notes check that finds nothing new is still a fresh read: the header and
// the per-project stamp say "now", and a registry-named file that is missing
// stays reported.
func TestNotesCheckWithoutChangesIsFresh(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	later := fixedNow.Add(5 * time.Minute)
	w.app.now = func() time.Time { return later }
	w.app.collectNotes(ctx)
	w.app.assemble()
	s := w.app.Snapshot()
	seen := false
	for _, st := range s.Sources {
		if st.Name == "notes" {
			seen = true
			if !st.RefreshedAt.Equal(later) || st.OK || !strings.Contains(st.Error, "beacon_app.md") {
				t.Errorf("notes status after an unchanged check = %+v", st)
			}
		}
	}
	if !seen {
		t.Error("no notes status")
	}
	stamped := false
	for _, src := range find(s, "beacon").Sources {
		if src.Name == "notes" {
			stamped = true
			if !src.At.Equal(later) {
				t.Errorf("beacon notes stamp = %v, want %v", src.At, later)
			}
		}
	}
	if !stamped {
		t.Error("beacon has no notes stamp")
	}
}

// Before the first cycle finishes the site must not look quiet and healthy.
func TestSnapshotBeforeTheFirstCycleSaysPending(t *testing.T) {
	w := newWorld(t)
	s := w.app.Snapshot()
	want := []string{"gitea", "registry", "heatmap", "ideas", "docker", "probes", "notes", "fdroid", "github"}
	if len(s.Sources) != len(want) {
		t.Fatalf("sources before the first cycle = %+v", s.Sources)
	}
	for i, st := range s.Sources {
		if st.Name != want[i] || st.OK || st.Error != "first read in progress" {
			t.Errorf("source %d = %+v", i, st)
		}
	}
}

func probeKey(w *world) string { return w.srv.URL + "/probe/beacon" }

// A probe cycle cut short by shutdown records nothing: no false outage in
// the saved history, and the last good result stays.
func TestCancelledProbeCycleRecordsNothing(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	before := len(w.app.store.ProbeHistory(probeKey(w), fixedNow))
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	w.app.collectProbes(cctx)
	if n := len(w.app.store.ProbeHistory(probeKey(w), fixedNow)); n != before {
		t.Errorf("history %d -> %d points after a cancelled cycle", before, n)
	}
	w.app.mu.Lock()
	r := w.app.probes[probeKey(w)]
	w.app.mu.Unlock()
	if r == nil || !r.Up {
		t.Errorf("last good probe result replaced: %+v", r)
	}
}

// A forced cycle must not run a second probe pass beside the ticker's: the
// second is skipped rather than adding duplicate history points.
func TestProbeRunsDoNotOverlap(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	before := len(w.app.store.ProbeHistory(probeKey(w), fixedNow))
	gate, arrived := make(chan struct{}), make(chan struct{}, 4)
	w.mu.Lock()
	w.probeGate, w.probeArrived = gate, arrived
	w.mu.Unlock()
	first, second := make(chan struct{}), make(chan struct{})
	go func() { w.app.collectProbes(ctx); close(first) }()
	<-arrived // the first pass is probing
	go func() { w.app.collectProbes(ctx); close(second) }()
	select {
	case <-second:
	case <-arrived:
		t.Error("a second probe pass ran while one was in flight")
	case <-time.After(5 * time.Second):
		t.Error("the second pass neither skipped nor probed")
	}
	close(gate)
	for _, ch := range []chan struct{}{first, second} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("probe pass did not finish")
		}
	}
	if n := len(w.app.store.ProbeHistory(probeKey(w), fixedNow)); n != before+1 {
		t.Errorf("history %d -> %d points, want one new point", before, n)
	}
}

// Feed lines that do not parse would make their containers look missing:
// the docker source says so.
func TestUnreadableFeedLinesAreReported(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(w.feedDir, "docker.json")
	b := append(readT(t, p), []byte("{not json\n")...)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	mt := fixedNow.Add(-5 * time.Second)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	w.app.collectDocker(ctx)
	w.app.mu.Lock()
	st := w.app.status["docker"]
	w.app.mu.Unlock()
	if st.OK || !strings.Contains(st.Error, "1 line of the feed could not be read") {
		t.Errorf("docker status = %+v", st)
	}
}

// The Gitea scheduler: detail is refetched on a listing change (updated_at,
// open issues, open PRs), while a run is in progress, or at 30 min; files on
// an updated_at change or at 6 h.
func TestGiteaRefreshTriggers(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	const (
		list      = "/api/v1/user/repos"
		tags      = "/api/v1/repos/acme/beacon/tags"
		changelog = "/api/v1/repos/acme/beacon/raw/CHANGELOG.md"
		runs      = "/api/v1/repos/acme/beacon/actions/runs"
	)
	at := fixedNow
	step := func(name string, advance time.Duration, wantDetail, wantFiles int) {
		t.Helper()
		at = at.Add(advance)
		now := at
		w.app.now = func() time.Time { return now }
		d0, f0 := w.count(tags), w.count(changelog)
		w.app.collectForges(ctx, false)
		if d, f := w.count(tags)-d0, w.count(changelog)-f0; d != wantDetail || f != wantFiles {
			t.Errorf("%s: beacon detail fetched %d, files %d; want %d, %d", name, d, f, wantDetail, wantFiles)
		}
	}
	editRepo := func(field string, v any) {
		w.edit(list, func(b []byte) []byte {
			var repos []map[string]any
			if err := json.Unmarshal(b, &repos); err != nil {
				t.Fatal(err)
			}
			for _, r := range repos {
				if r["name"] == "beacon" {
					r[field] = v
				}
			}
			out, err := json.Marshal(repos)
			if err != nil {
				t.Fatal(err)
			}
			return out
		})
	}
	setRun := func(from, to string) {
		w.edit(runs, func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"status":"`+from+`"`), []byte(`"status":"`+to+`"`), 1)
		})
	}
	step("nothing changed", time.Minute, 0, 0)
	editRepo("updated_at", "2026-10-31T21:30:00Z")
	step("updated_at moved", time.Minute, 1, 1)
	editRepo("open_issues_count", 19)
	step("open issues changed", time.Minute, 1, 0)
	editRepo("open_pr_counter", 1)
	step("open PRs changed", time.Minute, 1, 0)
	step("settled", time.Minute, 0, 0)
	setRun("completed", "in_progress")
	editRepo("open_issues_count", 20) // the listing change reads the running job
	step("run started", time.Minute, 1, 0)
	step("run in progress", time.Minute, 1, 0)
	setRun("in_progress", "completed")
	step("run finished", time.Minute, 1, 0)
	step("quiet", time.Minute, 0, 0)
	step("30 min old", DetailMaxAge, 1, 0)
	step("6 h old", FilesMaxAge, 1, 1)
}

// acmeHost is a synthetic forge host for the multi-forge tests (no captured
// data): each forge id is a path prefix serving the Gitea API under
// /<id>/api/v1 or the GitHub API under /<id>, built from the repos given.
type acmeHost struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	kind  map[string]string      // forge id -> gitea | github
	repos map[string][]*acmeRepo // forge id -> repos
	down  map[string]bool        // forge id -> every request answers 500
	log   []string               // "<forge id> <method> <path>"
	ua    map[string]bool        // every User-Agent sent
	// limit makes a forge's next request whose "<method> <path>" contains
	// the value answer 403 with its hourly allowance spent, resetting at
	// limitReset; once.
	limit      map[string]string
	limitReset time.Time
	// missing are owners no GitHub forge of the host has: /users/<o>/repos
	// answers 404 (as /orgs/<o>/repos does for any owner here).
	missing map[string]bool
}

type acmeRepo struct {
	Owner, Name string
	Private     bool
	Stars       int
	Issues      []acmeIssue // open issues
	PRs         []acmeIssue // open pull requests
	Tags        []string    // newest first; the newest is on the newest commit
	CI          string      // conclusion of the newest run on main; "" = no runs
	Updated     time.Time   // also the newest commit's date
	// Filed are the marker lines ("waymark-idea:<id>") of the file ideas filed
	// as issues here, #50 onwards; the issue search finds them.
	Filed   []string
	Website string // the repo's website (Gitea) or homepage (GitHub)
}

type acmeIssue struct {
	N       int
	Title   string
	Updated time.Time
	Labels  []string
}

// The token user on every forge of the host.
const acmeLogin = "acme"

func newAcmeHost(t *testing.T) *acmeHost {
	h := &acmeHost{t: t, kind: map[string]string{}, repos: map[string][]*acmeRepo{}, down: map[string]bool{}, ua: map[string]bool{}, limit: map[string]string{}, missing: map[string]bool{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

// forge adds a forge to the host and returns its config entry.
func (h *acmeHost) forge(id, kind, token string, owners []string, repos ...*acmeRepo) config.Forge {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kind[id], h.repos[id] = kind, repos
	return config.Forge{ID: id, Type: kind, URL: h.srv.URL + "/" + id, Token: token, Owners: owners}
}

func (h *acmeHost) edit(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn()
}

// requests lists forge id's requests ("<method> <path>") whose path contains sub.
func (h *acmeHost) requests(id, sub string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, l := range h.log {
		if rest, ok := strings.CutPrefix(l, id+" "); ok && strings.Contains(rest, sub) {
			out = append(out, rest)
		}
	}
	return out
}

// config is a config on the given forges with nothing else configured (no
// ideas labels, no ideas file, the default marker).
func (h *acmeHost) config(mod func(*config.Config), forges ...config.Forge) config.Config {
	cfg := config.Config{
		DataDir: h.t.TempDir(), Site: config.Site{Title: "Waymark", Timezone: "UTC"}, IncludePrivate: true, Forges: forges,
		Ideas: config.Ideas{Marker: "waymark-idea:"}, Notes: config.Notes{Type: "none"}, Plans: config.Plans{Dirs: []string{"docs/plans"}},
		Docker: config.Docker{Label: "Docker"},
	}
	if mod != nil {
		mod(&cfg)
	}
	return cfg
}

func (h *acmeHost) app(mod func(*config.Config), forges ...config.Forge) *App {
	h.t.Helper()
	cfg := h.config(mod, forges...)
	return h.start(cfg, NewForges(cfg))
}

// start is New with the clock fixed.
func (h *acmeHost) start(cfg config.Config, clients []forge.Forge) *App {
	h.t.Helper()
	a, err := New(cfg, clients)
	if err != nil {
		h.t.Fatal(err)
	}
	a.now = func() time.Time { return fixedNow }
	return a
}

// countingForge counts the calls that need a GitHub token.
type countingForge struct {
	forge.Forge
	lists, searches atomic.Int32
}

func (c *countingForge) ListRepos(ctx context.Context, owners []string) ([]forge.Repo, error) {
	c.lists.Add(1)
	return c.Forge.ListRepos(ctx, owners)
}

func (c *countingForge) SearchIssues(ctx context.Context, q string) ([]forge.Issue, error) {
	c.searches.Add(1)
	return c.Forge.SearchIssues(ctx, q)
}

// blindForge cannot read a repo by name, so only the mirror view itself can
// say a repo is private.
type blindForge struct{ forge.Forge }

func (blindForge) Repo(context.Context, string) (forge.Repo, error) {
	return forge.Repo{}, errors.New("by-name reads are off")
}

func (h *acmeHost) serve(w http.ResponseWriter, r *http.Request) {
	id, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	rest = "/" + rest
	h.mu.Lock()
	h.log = append(h.log, id+" "+r.Method+" "+rest)
	h.ua[r.UserAgent()] = true
	kind, down := h.kind[id], h.down[id]
	limited := false
	if sub, ok := h.limit[id]; ok && strings.Contains(r.Method+" "+rest, sub) {
		delete(h.limit, id)
		limited = true
	}
	reset := strconv.FormatInt(h.limitReset.Unix(), 10)
	var body any
	if !down {
		page := "https://" + id + ".example.com/"
		switch kind {
		case "gitea":
			body = giteaAnswer(strings.TrimPrefix(rest, "/api/v1"), h.repos[id], page)
		case "github":
			var q struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if r.Method == http.MethodPost {
				_ = json.NewDecoder(r.Body).Decode(&q)
			}
			body = githubAnswer(r.Method, rest, q.Query, q.Variables, h.repos[id], page)
			if o, ok := strings.CutPrefix(rest, "/users/"); ok && h.missing[strings.TrimSuffix(o, "/repos")] {
				body = nil
			}
		}
	}
	h.mu.Unlock()
	switch {
	case limited:
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", reset)
		w.WriteHeader(http.StatusForbidden)
	case down:
		http.Error(w, "boom", http.StatusInternalServerError)
	case body == nil:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	default:
		_ = json.NewEncoder(w).Encode(body)
	}
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (r *acmeRepo) full() string { return r.Owner + "/" + r.Name }

func (r *acmeRepo) sha() string { return "c0ffee" + r.Name }

// tagAt is when tag i was made: the newest on the newest commit, the others a
// day apart before it.
func (r *acmeRepo) tagAt(i int) time.Time { return r.Updated.Add(-time.Duration(i) * 24 * time.Hour) }

func (r *acmeRepo) tagSHA(i int) string {
	if i == 0 {
		return r.sha()
	}
	return "0ld" + strconv.Itoa(i) + r.Name
}

func findRepo(repos []*acmeRepo, full string) *acmeRepo {
	for _, r := range repos {
		if strings.EqualFold(r.full(), full) {
			return r
		}
	}
	return nil
}

func issueJSON(is acmeIssue, url string, pull bool) map[string]any {
	labels := []any{}
	for _, l := range is.Labels {
		labels = append(labels, map[string]any{"name": l, "color": "00aabb"})
	}
	m := map[string]any{
		"number": is.N, "title": is.Title, "state": "open", "html_url": url, "user": map[string]any{"login": "mark"},
		"labels": labels, "comments": 0, "created_at": stamp(is.Updated), "updated_at": stamp(is.Updated),
	}
	if pull {
		m["pull_request"] = map[string]any{"html_url": url}
	}
	return m
}

func commitJSON(r *acmeRepo, page string) map[string]any {
	who := map[string]any{"name": "Mark", "date": stamp(r.Updated)}
	return map[string]any{"sha": r.sha(), "html_url": page + r.full() + "/commit/" + r.sha(), "created": stamp(r.Updated),
		"commit": map[string]any{"message": "Change things", "author": who, "committer": who}}
}

func releasesJSON(r *acmeRepo, page string) []any {
	out := []any{}
	for i, tg := range r.Tags {
		out = append(out, map[string]any{"tag_name": tg, "name": tg, "html_url": page + r.full() + "/releases/tag/" + tg,
			"created_at": stamp(r.tagAt(i)), "published_at": stamp(r.tagAt(i))})
	}
	return out
}

// giteaAnswer is the Gitea API's answer to a GET of path, nil for a 404.
func giteaAnswer(path string, repos []*acmeRepo, page string) any {
	repoJSON := func(i int, r *acmeRepo) map[string]any {
		return map[string]any{
			"id": i + 1, "name": r.Name, "full_name": r.full(), "owner": map[string]any{"login": r.Owner}, "private": r.Private,
			"html_url": page + r.full(), "default_branch": "main", "stars_count": r.Stars, "open_issues_count": len(r.Issues),
			"open_pr_counter": len(r.PRs), "release_counter": len(r.Tags), "has_actions": r.CI != "", "updated_at": stamp(r.Updated),
			"website": r.Website,
		}
	}
	switch path {
	case "/user":
		return map[string]any{"login": acmeLogin}
	case "/user/repos":
		out := []any{}
		for i, r := range repos {
			out = append(out, repoJSON(i, r))
		}
		return out
	case "/users/" + acmeLogin + "/heatmap":
		return []any{map[string]any{"timestamp": fixedNow.Add(-2 * time.Hour).Unix(), "contributions": 3}}
	case "/repos/issues/search":
		out := []any{}
		for _, r := range repos {
			for i, line := range r.Filed {
				is := issueJSON(acmeIssue{N: 50 + i, Title: "Filed idea", Updated: r.Updated}, page+r.full()+"/issues/"+strconv.Itoa(50+i), false)
				is["body"], is["repository"] = "From the ideas list.\n"+line, map[string]any{"full_name": r.full()}
				out = append(out, is)
			}
		}
		return out
	}
	rest, ok := strings.CutPrefix(path, "/repos/")
	if !ok {
		return nil
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return nil
	}
	i := -1
	for j, r := range repos {
		if strings.EqualFold(r.full(), parts[0]+"/"+parts[1]) {
			i = j
		}
	}
	if i < 0 {
		return nil
	}
	r := repos[i]
	sub := ""
	if len(parts) == 3 {
		sub = parts[2]
	}
	switch sub {
	case "":
		return repoJSON(i, r)
	case "tags":
		out := []any{}
		for j, tg := range r.Tags {
			out = append(out, map[string]any{"name": tg, "id": r.tagSHA(j), "commit": map[string]any{"sha": r.tagSHA(j), "created": stamp(r.tagAt(j))}})
		}
		return out
	case "releases":
		return releasesJSON(r, page)
	case "issues":
		out := []any{}
		for _, is := range r.Issues {
			out = append(out, issueJSON(is, page+r.full()+"/issues/"+strconv.Itoa(is.N), false))
		}
		return out
	case "pulls":
		out := []any{}
		for _, is := range r.PRs {
			out = append(out, issueJSON(is, page+r.full()+"/pulls/"+strconv.Itoa(is.N), false))
		}
		return out
	case "commits":
		return []any{commitJSON(r, page)}
	case "actions/runs":
		if r.CI == "" {
			return map[string]any{"workflow_runs": []any{}, "total_count": 0}
		}
		return map[string]any{"total_count": 1, "workflow_runs": []any{map[string]any{
			"id": 1, "display_title": "Build", "path": "ci.yml@refs/heads/main", "head_branch": "main", "status": "completed",
			"conclusion": r.CI, "html_url": page + r.full() + "/actions/runs/1", "started_at": stamp(r.Updated), "completed_at": stamp(r.Updated),
		}}}
	}
	return nil
}

// githubAnswer is the GitHub API's answer, nil for a 404.
func githubAnswer(method, path, query string, vars map[string]any, repos []*acmeRepo, page string) any {
	repoJSON := func(r *acmeRepo) map[string]any {
		return map[string]any{
			"id": 1, "name": r.Name, "full_name": r.full(), "owner": map[string]any{"login": r.Owner}, "private": r.Private,
			"html_url": page + r.full(), "default_branch": "main", "stargazers_count": r.Stars,
			"open_issues_count": len(r.Issues) + len(r.PRs), "updated_at": stamp(r.Updated), "pushed_at": stamp(r.Updated),
			"homepage": r.Website,
		}
	}
	owned := func(owner string) []any {
		out := []any{}
		for _, r := range repos {
			if strings.EqualFold(r.Owner, owner) {
				out = append(out, repoJSON(r))
			}
		}
		return out
	}
	if method == http.MethodPost && path == "/graphql" {
		if strings.Contains(query, "contributionCalendar") {
			day := map[string]any{"date": fixedNow.Format("2006-01-02"), "contributionCount": 4}
			return map[string]any{"data": map[string]any{"viewer": map[string]any{"contributionsCollection": map[string]any{
				"contributionCalendar": map[string]any{"weeks": []any{map[string]any{"contributionDays": []any{day}}}}}}}}
		}
		owner, _ := vars["owner"].(string)
		name, _ := vars["name"].(string)
		r := findRepo(repos, owner+"/"+name)
		if r == nil {
			return nil
		}
		nodes := []any{}
		for i, tg := range r.Tags {
			nodes = append(nodes, map[string]any{"name": tg, "target": map[string]any{"oid": r.tagSHA(i), "committedDate": stamp(r.tagAt(i))}})
		}
		return map[string]any{"data": map[string]any{"repository": map[string]any{"refs": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}, "nodes": nodes}}}}
	}
	switch {
	case path == "/user":
		return map[string]any{"login": acmeLogin}
	case path == "/user/repos":
		return owned(acmeLogin)
	case strings.HasPrefix(path, "/users/") && strings.HasSuffix(path, "/repos"):
		return owned(strings.TrimSuffix(strings.TrimPrefix(path, "/users/"), "/repos"))
	case path == "/search/issues":
		return map[string]any{"items": []any{}}
	}
	rest, ok := strings.CutPrefix(path, "/repos/")
	if !ok {
		return nil
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return nil
	}
	r := findRepo(repos, parts[0]+"/"+parts[1])
	if r == nil {
		return nil
	}
	sub := ""
	if len(parts) == 3 {
		sub = parts[2]
	}
	switch sub {
	case "":
		return repoJSON(r)
	case "releases":
		return releasesJSON(r, page)
	case "releases/latest":
		if rels := releasesJSON(r, page); len(rels) > 0 {
			return rels[0]
		}
		return nil
	case "issues":
		out := []any{}
		for _, is := range r.Issues {
			out = append(out, issueJSON(is, page+r.full()+"/issues/"+strconv.Itoa(is.N), false))
		}
		for _, is := range r.PRs {
			out = append(out, issueJSON(is, page+r.full()+"/pull/"+strconv.Itoa(is.N), true))
		}
		return out
	case "pulls":
		out := []any{}
		for _, is := range r.PRs {
			out = append(out, issueJSON(is, page+r.full()+"/pull/"+strconv.Itoa(is.N), false))
		}
		return out
	case "commits":
		return []any{commitJSON(r, page)}
	case "actions/runs":
		runs := []any{}
		if r.CI != "" {
			runs = append(runs, map[string]any{"id": 1, "display_title": "Build", "path": ".github/workflows/ci.yml", "head_branch": "main",
				"status": "completed", "conclusion": r.CI, "html_url": page + r.full() + "/actions/runs/1",
				"run_started_at": stamp(r.Updated), "updated_at": stamp(r.Updated)})
		}
		return map[string]any{"workflow_runs": runs}
	}
	return nil
}

func sourceStatus(s *model.Site, name string) (model.SourceStatus, bool) {
	for _, st := range s.Sources {
		if st.Name == name {
			return st, true
		}
	}
	return model.SourceStatus{}, false
}

func sourceNamesOf(s *model.Site) []string {
	var out []string
	for _, st := range s.Sources {
		out = append(out, st.Name)
	}
	return out
}

func runOnce(t *testing.T, a *App) *model.Site {
	t.Helper()
	s, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const day = 24 * time.Hour

// The same repo on Gitea and GitHub is one project: Gitea's data, GitHub's
// stars and issues, and GitHub read light (no tags, commits, PRs or runs).
func TestAssembleMirrorPair(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "gitea-token", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Stars: 2, Tags: []string{"v1.0.0"}, CI: "success", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "github-token", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Stars: 7, Tags: []string{"v1.0.0"}, Updated: fixedNow.Add(-day),
			Issues: []acmeIssue{{N: 3, Title: "Crash on start", Updated: fixedNow.Add(-time.Hour)}}})
	a := h.app(nil, g, gh)
	s := runOnce(t, a)
	if len(s.Projects) != 1 {
		t.Fatalf("projects = %v, want the pair as one", slugsOf(s))
	}
	p := s.Projects[0]
	if p.Slug != "tool" || p.Repo == nil || p.Repo.HTMLURL != "https://gitea.example.com/acme/tool" || p.Repo.Stars != 2 || p.Repo.LatestTag != "v1.0.0" || len(p.CI) != 1 {
		t.Errorf("primary = %+v ci %+v", p.Repo, p.CI)
	}
	if p.GitHub == nil || p.GitHub.Stars != 7 || p.GitHub.HTMLURL != "https://github.example.com/acme/tool" || p.GitHub.LatestRelease != "v1.0.0" {
		t.Errorf("mirror = %+v", p.GitHub)
	}
	if len(p.Issues) != 1 || p.Issues[0].Source != "github" || p.Issues[0].Number != 3 {
		t.Errorf("issues = %+v", p.Issues)
	}
	for _, r := range h.requests("github", "") {
		if strings.HasPrefix(r, "POST /graphql") || strings.HasSuffix(r, "/commits") || strings.HasSuffix(r, "/pulls") ||
			strings.HasSuffix(r, "/actions/runs") || strings.HasSuffix(r, "/tool/releases") {
			t.Errorf("the mirror was read in full: %s", r)
		}
	}
	for _, id := range []string{"gitea", "github"} {
		if st, ok := sourceStatus(s, id); !ok || !st.OK {
			t.Errorf("%s status = %+v", id, st)
		}
	}
	a.mu.Lock()
	in := a.inputsLocked(fixedNow)
	a.mu.Unlock()
	if me := in.Projects[0].Me; me["gitea"] != acmeLogin || me["github"] != acmeLogin {
		t.Errorf("Me = %v, want the token user on each forge", me)
	}

	// A projects-file project naming only the Gitea repo gets the GitHub twin
	// attached as its mirror, which is read too.
	file := filepath.Join(t.TempDir(), "projects.yaml")
	if err := os.WriteFile(file, []byte("projects:\n  - slug: the-tool\n    name: The Tool\n    repos:\n      - gitea: acme/tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s = runOnce(t, h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g, gh))
	if p := s.ProjectBySlug("the-tool"); len(s.Projects) != 1 || p == nil || p.GitHub == nil || p.GitHub.Stars != 7 {
		t.Errorf("projects %v; the-tool = %+v", slugsOf(s), p)
	}
}

// include_private: false drops a private repo at listing time, so it shows
// nowhere. The same fixture with it true shows it everywhere (the check that
// the fixture can show it at all).
func TestIncludePrivateFalseHidesEverywhere(t *testing.T) {
	// run reads a forge with a public repo and a private one twice; projects
	// is a projects file ("" for none).
	run := func(t *testing.T, includePrivate bool, projects string) (*acmeHost, *model.Site) {
		h := newAcmeHost(t)
		secret := &acmeRepo{Owner: "acme", Name: "secret", Private: true, Tags: []string{"v1.0.0"}, CI: "success", Updated: fixedNow.Add(-day),
			Issues: []acmeIssue{{N: 1, Title: "Rotate the keys", Updated: fixedNow.Add(-day), Labels: []string{"idea"}}},
			PRs:    []acmeIssue{{N: 2, Title: "Swap the cipher", Updated: fixedNow.Add(-10 * day)}}}
		tool := &acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v0.1.0"}, Updated: fixedNow.Add(-day)}
		file := ""
		if projects != "" {
			file = filepath.Join(t.TempDir(), "projects.yaml")
			if err := os.WriteFile(file, []byte(projects), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		a := h.app(func(c *config.Config) {
			c.IncludePrivate = includePrivate
			if file != "" {
				c.Projects = config.FileRef{File: file}
			}
		}, h.forge("gitea", "gitea", "gitea-token", []string{"acme"}, tool, secret))
		runOnce(t, a)
		// A new tag: the second cycle makes it an event.
		h.edit(func() { secret.Tags, secret.Updated = []string{"v1.1.0", "v1.0.0"}, fixedNow.Add(-time.Hour) })
		return h, runOnce(t, a)
	}
	shownIn := func(s *model.Site, slug string) []string {
		var out []string
		if s.ProjectBySlug(slug) != nil {
			out = append(out, "projects")
		}
		for _, x := range s.NeedsYou {
			if x.Project == slug {
				out = append(out, "needs-you")
				break
			}
		}
		for _, e := range s.Events {
			if e.Project == slug {
				out = append(out, "events")
				break
			}
		}
		for _, r := range s.Releases {
			if r.Project == slug {
				out = append(out, "releases")
				break
			}
		}
		for _, n := range s.Graph.Nodes {
			if n.ID == slug {
				out = append(out, "graph")
			}
		}
		return out
	}
	t.Run("included", func(t *testing.T) {
		_, s := run(t, true, "")
		if got := shownIn(s, "secret"); len(got) != 5 {
			t.Errorf("with include_private the private repo shows in %v, want all five", got)
		}
	})
	t.Run("left out", func(t *testing.T) {
		h, s := run(t, false, "")
		if got := shownIn(s, "secret"); len(got) != 0 {
			t.Errorf("the private repo shows in %v", got)
		}
		if s.ProjectBySlug("tool") == nil {
			t.Error("the public repo must stay")
		}
		if got := h.requests("gitea", "/repos/acme/secret"); len(got) != 0 {
			t.Errorf("the private repo was read: %v", got)
		}
	})
	// A projects-file project whose primary repo is private: left out, it is
	// hidden entirely, not shown as a card with no repo; included, it shows
	// everywhere.
	t.Run("projects-file project", func(t *testing.T) {
		projects := "projects:\n  - slug: vault\n    name: Vault\n    repos:\n      - gitea: acme/secret\n" +
			"  - slug: box\n    name: Box\n    relations:\n      - to: vault\n        kind: depends-on\n"
		for _, include := range []bool{true, false} {
			h, s := run(t, include, projects)
			got := shownIn(s, "vault")
			if include && len(got) != 5 || !include && len(got) != 0 {
				t.Errorf("include_private %v: the private primary's project shows in %v", include, got)
			}
			box := s.ProjectBySlug("box")
			if box == nil || s.ProjectBySlug("tool") == nil {
				t.Fatalf("include_private %v: projects = %v, want box and tool to stay", include, slugsOf(s))
			}
			if linked := len(box.Relations) == 1; linked != include {
				t.Errorf("include_private %v: box relations = %+v", include, box.Relations)
			}
			if got := h.requests("gitea", "/repos/acme/secret"); !include && len(got) != 0 {
				t.Errorf("the private primary was read: %v", got)
			}
		}
	})
	// A file idea filed as an issue in the private repo: left out, the idea
	// shows no link to it.
	t.Run("filed idea", func(t *testing.T) {
		for _, include := range []bool{true, false} {
			h := newAcmeHost(t)
			g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)},
				&acmeRepo{Owner: "acme", Name: "secret", Private: true, Filed: []string{"waymark-idea:tool-01"}, Updated: fixedNow.Add(-day)})
			file := filepath.Join(t.TempDir(), "ideas.json")
			if err := os.WriteFile(file, []byte(`{"roundup_date":"2026-10-31","ideas":[{"id":"tool-01","project":"tool","title":"Sleep timer"}]}`), 0o644); err != nil {
				t.Fatal(err)
			}
			s := runOnce(t, h.app(func(c *config.Config) { c.Ideas.File, c.IncludePrivate = config.FileRef{File: file}, include }, g))
			if len(s.Ideas) != 1 {
				t.Fatalf("ideas = %+v", s.Ideas)
			}
			if linked := strings.Contains(s.Ideas[0].FiledURL, "acme/secret"); linked != include {
				t.Errorf("include_private %v: filed link %q", include, s.Ideas[0].FiledURL)
			}
		}
	})
	// A private GitHub mirror named in the projects file, with a token: in the
	// GitHub list (owner acme) or read by name (owners elsewhere). Left out,
	// none of it shows (no stars, issues or link); included, it all does.
	t.Run("private mirror", func(t *testing.T) {
		for _, owners := range [][]string{{"acme"}, {"other"}} {
			for _, include := range []bool{true, false} {
				h := newAcmeHost(t)
				g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
				gh := h.forge("github", "github", "t2", owners, &acmeRepo{Owner: "acme", Name: "tool", Private: true, Stars: 7, Tags: []string{"v1.0.0"},
					Issues: []acmeIssue{{N: 4, Title: "Secret plan", Updated: fixedNow.Add(-time.Hour)}}, Updated: fixedNow.Add(-day)})
				file := filepath.Join(t.TempDir(), "projects.yaml")
				body := "projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n      - github: acme/tool\n"
				if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				s := runOnce(t, h.app(func(c *config.Config) { c.Projects, c.IncludePrivate = config.FileRef{File: file}, include }, g, gh))
				p := s.ProjectBySlug("tool")
				if p == nil {
					t.Fatalf("owners %v include %v: projects = %v", owners, include, slugsOf(s))
				}
				var shown []string
				if p.GitHub != nil {
					shown = append(shown, "stars")
				}
				for _, is := range p.Issues {
					if is.Source == "github" {
						shown = append(shown, "issues")
						break
					}
				}
				for _, l := range p.Links {
					if l.Kind == "github" {
						shown = append(shown, "link "+l.URL)
					}
				}
				if include && len(shown) != 3 || !include && len(shown) != 0 {
					t.Errorf("owners %v include_private %v: the private mirror shows %v", owners, include, shown)
				}
				if got := h.requests("github", "/repos/acme/tool/"); !include && len(got) != 0 {
					t.Errorf("owners %v: the private mirror's issues or releases were read: %v", owners, got)
				}
			}
		}
		// The by-name read fails, so the mirror is read; its view says
		// private and is dropped.
		h := newAcmeHost(t)
		g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
		gh := h.forge("github", "github", "t2", []string{"other"}, &acmeRepo{Owner: "acme", Name: "tool", Private: true, Stars: 7, Updated: fixedNow.Add(-day)})
		file := filepath.Join(t.TempDir(), "projects.yaml")
		if err := os.WriteFile(file, []byte("projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n      - github: acme/tool\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := h.config(func(c *config.Config) { c.Projects, c.IncludePrivate = config.FileRef{File: file}, false }, g, gh)
		clients := NewForges(cfg)
		clients[1] = blindForge{clients[1]}
		a := h.start(cfg, clients)
		s := runOnce(t, a)
		p := s.ProjectBySlug("tool")
		if p == nil || p.GitHub != nil {
			t.Fatalf("a mirror view that says private was shown: %+v", p)
		}
		for _, l := range p.Links {
			if l.Kind == "github" {
				t.Errorf("a mirror view that says private is still linked: %s", l.URL)
			}
		}
		if got := h.requests("github", "GET /repos/acme/tool"); len(got) == 0 {
			t.Error("the mirror was never read, so this case proves nothing")
		}
		// Known as private now, the failed by-name read of it is not named in
		// the forge's status, and the mirror is neither read again nor linked.
		if st, _ := sourceStatus(s, "github"); strings.Contains(st.Error, "acme/tool") {
			t.Errorf("github status names the private repo: %q", st.Error)
		}
		before := len(h.requests("github", "/repos/acme/tool"))
		s = runOnce(t, a)
		if got := h.requests("github", "/repos/acme/tool"); len(got) != before {
			t.Errorf("the private mirror was read again: %v", got[before:])
		}
		for _, l := range s.ProjectBySlug("tool").Links {
			if l.Kind == "github" {
				t.Errorf("second cycle: the private mirror is linked: %s", l.URL)
			}
		}
		if st, _ := sourceStatus(s, "github"); strings.Contains(st.Error, "acme/tool") {
			t.Errorf("second cycle: github status names the private repo: %q", st.Error)
		}
	})
}

// A repo the projects file names is read by name when its owner is not in
// the forge's owners (as v0.1 read any repo the file named); owners still
// decide the auto projects. A named repo that is not there is named in the
// forge's status.
func TestNamedRepoOutsideOwnersIsRead(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)},
		&acmeRepo{Owner: "other", Name: "lib", Tags: []string{"v2.1.0"}, CI: "success", Updated: fixedNow.Add(-day)},
		&acmeRepo{Owner: "other", Name: "extra", Updated: fixedNow.Add(-day)})
	file := filepath.Join(t.TempDir(), "projects.yaml")
	body := "projects:\n  - slug: lib\n    name: Lib\n    repos:\n      - gitea: other/lib\n  - slug: gone\n    name: Gone\n    repos:\n      - gitea: other/gone\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g)
	for cycle := 1; cycle <= 2; cycle++ {
		s := runOnce(t, a)
		lib := s.ProjectBySlug("lib")
		if lib == nil || lib.Repo == nil || lib.Repo.HTMLURL != "https://gitea.example.com/other/lib" || lib.Repo.LatestTag != "v2.1.0" || len(lib.CI) != 1 {
			t.Errorf("cycle %d: the named repo was not read in full: %+v", cycle, lib)
		}
		if got := slugsOf(s); !slices.Equal(got, []string{"gone", "lib", "tool"}) {
			t.Errorf("cycle %d: projects = %v (other/extra is not tracked)", cycle, got)
		}
		st, _ := sourceStatus(s, "gitea")
		if st.OK || !strings.Contains(st.Error, "other/gone") || strings.Contains(st.Error, "other/lib") {
			t.Errorf("cycle %d: gitea status = %+v, want the missing named repo named", cycle, st)
		}
	}
	if strings.Contains(logged.String(), "is not on") {
		t.Errorf("log still says a repo is not on the forge:\n%s", logged.String())
	}
}

// An owner that matches no repo (a typo, or one the token cannot see) is
// logged once, not on every poll, naming the forge and the owner.
func TestOwnerWithNoReposLoggedOnce(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme", "acmee"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	a := h.app(nil, g)
	for range 3 {
		runOnce(t, a)
	}
	if n := strings.Count(logged.String(), "owner acmee"); n != 1 {
		t.Errorf("the unmatched owner was logged %d times, want once:\n%s", n, logged.String())
	}
	if !strings.Contains(logged.String(), "forge gitea: owner acmee") || strings.Contains(logged.String(), "owner acme ") {
		t.Errorf("log:\n%s", logged.String())
	}
}

// Two Gitea forges: each lists its own owners only (an owner split inside
// one listing) and has its own source status.
func TestTwoGiteaForges(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "other", Name: "thing", Updated: fixedNow.Add(-day)})
	cb := h.forge("codeberg", "gitea", "t2", []string{"other"},
		&acmeRepo{Owner: "other", Name: "lib", Updated: fixedNow.Add(-day)}, &acmeRepo{Owner: "acme", Name: "site", Updated: fixedNow.Add(-day)})
	a := h.app(nil, g, cb)
	s := runOnce(t, a)
	if got := slugsOf(s); !slices.Equal(got, []string{"lib", "tool"}) {
		t.Errorf("projects = %v, want each forge's own owners only", got)
	}
	if lib := s.ProjectBySlug("lib"); lib == nil || lib.Repo == nil || lib.Repo.HTMLURL != "https://codeberg.example.com/other/lib" {
		t.Errorf("lib = %+v", lib)
	}
	// The first forge leads, the others close the list; sources that are
	// not configured are not listed at all.
	if names := sourceNamesOf(s); len(names) == 0 || names[0] != "gitea" || names[len(names)-1] != "codeberg" ||
		slices.Contains(names, "notes") || slices.Contains(names, "docker") || slices.Contains(names, "heatmap") {
		t.Errorf("sources = %v", names)
	}
	for _, id := range []string{"gitea", "codeberg"} {
		if st, ok := sourceStatus(s, id); !ok || !st.OK {
			t.Errorf("%s status = %+v", id, st)
		}
	}
	h.edit(func() { h.down["codeberg"] = true })
	s = runOnce(t, a)
	if st, _ := sourceStatus(s, "codeberg"); st.OK || !strings.Contains(st.Error, "500") {
		t.Errorf("codeberg status = %+v, want its own failure", st)
	}
	if st, _ := sourceStatus(s, "gitea"); !st.OK {
		t.Errorf("gitea status = %+v, want it untouched", st)
	}
	if s.ProjectBySlug("lib") == nil {
		t.Error("the down forge's projects must stay from the last good list")
	}
}

// A forge that fails on a later cycle keeps its last good detail, shows red,
// and leaves the other forges alone.
func TestForgeFailureKeepsLastGood(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Tags: []string{"v1.0.0"}, CI: "success", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "t2", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "app", Tags: []string{"v2.0.0"}, CI: "failure", Updated: fixedNow.Add(-day)})
	a := h.app(nil, g, gh)
	first := runOnce(t, a)
	if app := first.ProjectBySlug("app"); app == nil || app.Repo == nil || app.Repo.LatestTag != "v2.0.0" || len(app.CI) != 1 {
		t.Fatalf("first cycle app = %+v", app)
	}
	h.edit(func() { h.down["github"] = true })
	s := runOnce(t, a)
	app := s.ProjectBySlug("app")
	if app == nil || app.Repo == nil || app.Repo.LatestTag != "v2.0.0" || len(app.CI) != 1 || app.CI[0].Conclusion != "failure" {
		t.Errorf("the last good detail was dropped: %+v", app)
	}
	if st, _ := sourceStatus(s, "github"); st.OK || !strings.Contains(st.Error, "500") {
		t.Errorf("github status = %+v", st)
	}
	if st, _ := sourceStatus(s, "gitea"); !st.OK {
		t.Errorf("gitea status = %+v", st)
	}
	if tool := s.ProjectBySlug("tool"); tool == nil || tool.Repo == nil || tool.Repo.LatestTag != "v1.0.0" {
		t.Errorf("tool = %+v", tool)
	}
}

// GitHub without a token: one log note, the source stays OK, and the forge is
// never asked for what needs a token (it is not listed again).
func TestNoTokenAppStatus(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)})
	gh.Heatmap = true
	cfg := h.config(nil, g, gh)
	clients := NewForges(cfg)
	counted := &countingForge{Forge: clients[1]}
	clients[1] = counted
	a := h.start(cfg, clients)
	runOnce(t, a)
	s := runOnce(t, a)
	if n, m := counted.lists.Load(), counted.searches.Load(); n != 1 || m != 0 {
		t.Errorf("GitHub listed %d times and searched %d times over two cycles, want once and never", n, m)
	}
	// Owners and no projects file: nothing at all is read (the note's words
	// are in TestTokenlessForgeSaysWhy).
	if n := strings.Count(logged.String(), "forge github: no token, so nothing is listed"); n != 1 {
		t.Errorf("token note logged %d times, want once:\n%s", n, logged.String())
	}
	if st, ok := sourceStatus(s, "github"); !ok || !st.OK {
		t.Errorf("github status = %+v (present %v), want OK", st, ok)
	}
	if st, ok := sourceStatus(s, "heatmap"); !ok || !st.OK {
		t.Errorf("heatmap status = %+v (present %v), want OK without GitHub's", st, ok)
	}
	if got := h.requests("github", ""); len(got) != 0 {
		t.Errorf("a forge that needs a token was asked: %v", got)
	}
	if s.ProjectBySlug("app") != nil || s.ProjectBySlug("tool") == nil {
		t.Errorf("projects = %v", slugsOf(s))
	}
}

// A projects-file project whose only repo is on GitHub, without a token: the
// repo is read like a mirror and the project is dated as one with no repo,
// as v0.1 did.
func TestTokenlessGitHubPrimaryUsesMirrorPath(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "", nil, &acmeRepo{Owner: "acme", Name: "cli", Stars: 5, Tags: []string{"v3.0.0"},
		Issues: []acmeIssue{{N: 9, Title: "Add a flag", Updated: fixedNow.Add(-time.Hour)}}, Updated: fixedNow.Add(-day)})
	file := filepath.Join(t.TempDir(), "projects.yaml")
	body := "projects:\n  - slug: cli\n    name: CLI\n    repos:\n      - github: acme/cli\n  - slug: box\n    name: Box\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := runOnce(t, h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g, gh))
	cli, box := s.ProjectBySlug("cli"), s.ProjectBySlug("box")
	if cli == nil || box == nil {
		t.Fatalf("projects = %v", slugsOf(s))
	}
	if !cli.ActivityFromNotes || cli.Repo != nil || cli.GitHub == nil || cli.GitHub.Stars != 5 || cli.GitHub.LatestRelease != "v3.0.0" {
		t.Errorf("cli: from notes %v repo %+v github %+v", cli.ActivityFromNotes, cli.Repo, cli.GitHub)
	}
	if cli.Tier != box.Tier || cli.TierReason != box.TierReason || cli.Headline != box.Headline {
		t.Errorf("cli tier %s %q %q, want a project with no repo's: %s %q %q", cli.Tier, cli.TierReason, cli.Headline, box.Tier, box.TierReason, box.Headline)
	}
	if len(cli.Issues) != 1 || cli.Issues[0].Source != "github" || cli.Issues[0].Number != 9 {
		t.Errorf("cli issues = %+v", cli.Issues)
	}
	got := h.requests("github", "")
	slices.Sort(got)
	want := []string{"GET /repos/acme/cli", "GET /repos/acme/cli/issues", "GET /repos/acme/cli/releases/latest"}
	if !slices.Equal(got, want) {
		t.Errorf("github requests = %v, want the three mirror reads only", got)
	}
	for _, name := range []string{"registry", "github", "gitea"} {
		if st, ok := sourceStatus(s, name); !ok || !st.OK {
			t.Errorf("%s status = %+v", name, st)
		}
	}
}

// A GitHub primary's listing counts its PR as an issue; the detail splits
// them, so the PR signal and the site's PR total include it.
func TestGitHubPrimaryCountsFromDetail(t *testing.T) {
	h := newAcmeHost(t)
	gh := h.forge("github", "github", "t", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day),
		Issues: []acmeIssue{{N: 1, Title: "Slow start", Updated: fixedNow.Add(-day)}, {N: 2, Title: "Odd colours", Updated: fixedNow.Add(-day)}},
		PRs:    []acmeIssue{{N: 3, Title: "Faster start", Updated: fixedNow.Add(-time.Hour)}}})
	s := runOnce(t, h.app(nil, gh))
	p := s.ProjectBySlug("app")
	if p == nil || p.Repo == nil || p.Repo.OpenIssues != 2 || p.Repo.OpenPRs != 1 || len(p.PRs) != 1 {
		t.Fatalf("app = %+v", p)
	}
	prs := ""
	for _, sg := range p.Signals {
		if sg.Kind == "prs" {
			prs = sg.Text
		}
	}
	if prs != "1 PR" || s.Stats.OpenPRs != 1 || s.Stats.OpenIssues != 2 {
		t.Errorf("PR signal %q, site %d issues %d PRs", prs, s.Stats.OpenIssues, s.Stats.OpenPRs)
	}
}

// Each forge with heatmap on adds the token user's days.
func TestHeatmapSumsForges(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "t2", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day)})
	g.Heatmap, gh.Heatmap = true, true
	s := runOnce(t, h.app(nil, g, gh))
	if today := s.Heatmap.Days[len(s.Heatmap.Days)-1]; today.Count != 7 || s.Heatmap.Total != 7 {
		t.Errorf("today = %+v, total %d; want 3 + 4", today, s.Heatmap.Total)
	}
	if n := len(h.requests("gitea", "/users/"+acmeLogin+"/heatmap")); n != 1 {
		t.Errorf("gitea heatmap of the token user read %d times", n)
	}
	if st, ok := sourceStatus(s, "heatmap"); !ok || !st.OK {
		t.Errorf("heatmap status = %+v", st)
	}
}

// noteDecides are the needs-you items taken from notes.
func noteDecides(s *model.Site) []model.Action {
	var out []model.Action
	for _, a := range s.NeedsYou {
		if a.Verb == "Decide" && a.Heuristic {
			out = append(out, a)
		}
	}
	return out
}

func writeNote(t *testing.T, p, body string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// Markdown notes: a notes source, marker items as needs-you in the
// adapter's words, and an edited note as an event in its words.
func TestMarkdownNotesThroughTheApp(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	dir := t.TempDir()
	note := filepath.Join(dir, "tool.md")
	writeNote(t, note, "# Tool\n\n- WAITING: Mark to pick a name\n- [ ] WAITING: a to-do\n", fixedNow.Add(-2*time.Hour))
	a := h.app(func(c *config.Config) {
		c.Notes = config.Notes{Type: "markdown", Dir: dir, Markers: []string{"WAITING:", "@me"}}
	}, g)
	s := runOnce(t, a)
	if st, ok := sourceStatus(s, "notes"); !ok || !st.OK {
		t.Errorf("notes status = %+v (present %v)", st, ok)
	}
	if ns := find(s, "tool").Notes; len(ns) != 1 || ns[0].File != "tool.md" || !strings.HasPrefix(ns[0].Body, "# Tool") {
		t.Errorf("tool notes = %+v", ns)
	}
	if d := noteDecides(s); len(d) != 1 || d[0].Text != "Mark to pick a name" || d[0].Source != "Notes" || d[0].Project != "tool" {
		t.Errorf("note decides = %+v", d)
	}

	writeNote(t, note, "# Tool\n\n- WAITING: Mark to pick a name\n- @me tag v1.0.0\n", fixedNow.Add(-time.Hour))
	s = runOnce(t, a)
	if d := noteDecides(s); len(d) != 2 {
		t.Errorf("note decides after the edit = %+v", d)
	}
	var texts []string
	for _, e := range s.Events {
		if e.Kind == "note" {
			texts = append(texts, e.Text)
		}
	}
	if len(texts) != 1 || texts[0] != "Note tool.md was updated" {
		t.Errorf("note events = %q", texts)
	}
}

// Claude's notes wait on the names in site.you; with none, the notes still
// show but nothing from them needs you.
func TestClaudeMemoryNamesFromSiteYou(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, filepath.Join(dir, "MEMORY.md"), "- [Tool](tool.md): v1 live; rename awaits Mark\n", fixedNow.Add(-time.Hour))
	writeNote(t, filepath.Join(dir, "tool.md"), "---\nname: tool\n---\n", fixedNow.Add(-2*time.Hour))
	for _, tc := range []struct {
		you  []string
		want int
	}{{nil, 0}, {[]string{"Mark"}, 1}} {
		h := newAcmeHost(t)
		g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
		a := h.app(func(c *config.Config) {
			c.Site.You = tc.you
			c.Notes = config.Notes{Type: "claude-memory", Dir: dir}
		}, g)
		s := runOnce(t, a)
		if st, ok := sourceStatus(s, "notes"); !ok || !st.OK {
			t.Errorf("you %q: notes status = %+v (present %v)", tc.you, st, ok)
		}
		if ns := find(s, "tool").Notes; len(ns) != 1 || ns[0].IndexLine != "v1 live; rename awaits Mark" {
			t.Errorf("you %q: tool notes = %+v", tc.you, ns)
		}
		d := noteDecides(s)
		if len(d) != tc.want || tc.want > 0 && (d[0].Text != "rename awaits Mark" || d[0].Source != "Claude's notes") {
			t.Errorf("you %q: note decides = %+v", tc.you, d)
		}
	}
}

// A project that appears later gets its notes, though no note changed.
func TestNotesFollowNewProjects(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	dir := t.TempDir()
	writeNote(t, filepath.Join(dir, "tool.md"), "- WAITING: Mark to pick a name\n", fixedNow.Add(-2*time.Hour))
	writeNote(t, filepath.Join(dir, "web.md"), "- WAITING: Mark to pick a colour\n", fixedNow.Add(-2*time.Hour))
	a := h.app(func(c *config.Config) {
		c.Notes = config.Notes{Type: "markdown", Dir: dir, Markers: []string{"WAITING:"}}
	}, g)
	if s := runOnce(t, a); len(find(s, "tool").Notes) != 1 || s.ProjectBySlug("web") != nil {
		t.Fatalf("first pass: %v", sourceNamesOf(s))
	}
	h.edit(func() {
		h.repos["gitea"] = append(h.repos["gitea"], &acmeRepo{Owner: "acme", Name: "web", Updated: fixedNow.Add(-day)})
	})
	if ns := find(runOnce(t, a), "web").Notes; len(ns) != 1 || ns[0].File != "web.md" {
		t.Errorf("web notes = %+v, want web.md", ns)
	}
}

// Notes type none: no notes source and no notes, whatever is on disk.
func TestNotesTypeNoneReadsNothing(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	s := runOnce(t, h.app(nil, g))
	if _, ok := sourceStatus(s, "notes"); ok {
		t.Errorf("sources = %v, want no notes source", sourceNamesOf(s))
	}
	if ns := find(s, "tool").Notes; len(ns) != 0 {
		t.Errorf("tool notes = %+v", ns)
	}
}

// A mirror error goes when the projects file stops naming the mirror.
func TestMirrorErrorGoesWithTheMirror(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "", nil) // no repos: every mirror read is a 404
	file := filepath.Join(t.TempDir(), "projects.yaml")
	write := func(body string) {
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n      - github: acme/gone\n")
	a := h.app(func(c *config.Config) { c.Projects = config.FileRef{File: file} }, g, gh)
	if st, _ := sourceStatus(runOnce(t, a), "github"); st.OK || !strings.Contains(st.Error, "acme/gone") {
		t.Fatalf("github status = %+v, want the failed mirror read", st)
	}
	write("projects:\n  - slug: tool\n    name: Tool\n    repos:\n      - gitea: acme/tool\n")
	if st, _ := sourceStatus(runOnce(t, a), "github"); !st.OK {
		t.Errorf("github status = %+v after the mirror left the file, want OK", st)
	}
}

// A pass where one forge fails and another is read still assembles: the
// status does not change, but the data did.
func TestPartialReadsStillAssemble(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	gh := h.forge("github", "github", "t2", []string{"acme"})
	g.Heatmap, gh.Heatmap = true, true
	ideas := filepath.Join(t.TempDir(), "ideas.json") // the filed-idea search runs for a file only
	if err := os.WriteFile(ideas, []byte(`{"ideas":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := h.app(func(c *config.Config) { c.Ideas.File = config.FileRef{File: ideas} }, g, gh)
	h.edit(func() { h.down["github"] = true })
	runOnce(t, a) // the failures are in the statuses already
	ctx := context.Background()
	for name, collect := range map[string]func(context.Context){"heatmap": a.collectHeatmap, "ideas": a.collectFiled} {
		select {
		case <-a.kick:
		default:
		}
		collect(ctx)
		if len(a.kick) != 1 {
			t.Errorf("%s: gitea's data was read, but no assemble was asked for", name)
		}
		if st, _ := sourceStatus(func() *model.Site { a.assemble(); return a.Snapshot() }(), name); st.OK {
			t.Errorf("%s status = %+v, want GitHub's failure", name, st)
		}
	}
}
