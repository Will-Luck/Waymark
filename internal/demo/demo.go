package demo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/store"
)

// FeedEvery is how often -demo touches its Docker feed, so the feed never
// reads as stale (a real host's cron rewrites it each minute).
const FeedEvery = 30 * time.Second

// Demo is a running demo: the config that reads the fake forges, and the
// servers and files behind it.
type Demo struct {
	Config config.Config
	World  *World
	// GiteaURL, GitHubURL and SitesURL are where the fake Gitea, the fake
	// GitHub and the team's sites (probes, F-Droid index) listen.
	GiteaURL, GitHubURL, SitesURL string

	servers []*http.Server
	stop    chan struct{}
	stopped chan struct{}
}

// Run starts demo mode for w under dir: a fake Gitea, a fake GitHub and the
// team's sites on three loopback listeners, and in dir the markdown notes,
// the Docker feed and a data dir seeded with w's events and a day of probe
// history. The feed's mtime moves on with the clock every feedEvery; 0 keeps
// the time it was written with, so a run reads the same feed whenever it
// happens (fixtures). Nothing it starts reaches the network. The returned
// Config reads it all; Close stops it.
func Run(w *World, dir string, feedEvery time.Duration) (*Demo, error) {
	d := &Demo{World: w, stop: make(chan struct{}), stopped: make(chan struct{})}
	var urls []string
	for range 3 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			d.closeServers()
			return nil, fmt.Errorf("demo: %w", err)
		}
		srv := &http.Server{Handler: Handler(w), ReadHeaderTimeout: 5 * time.Second}
		d.servers = append(d.servers, srv)
		go func() { _ = srv.Serve(ln) }()
		urls = append(urls, "http://"+ln.Addr().String())
	}
	d.GiteaURL, d.GitHubURL, d.SitesURL = urls[0], urls[1], urls[2]
	w.SetBase(d.SitesURL)

	notesDir, feedDir, dataDir := filepath.Join(dir, "notes"), filepath.Join(dir, "feed"), filepath.Join(dir, "data")
	feed := filepath.Join(feedDir, "docker.json")
	if err := errors.Join(w.WriteNotes(notesDir), w.WriteFeed(feed, w.Now.Add(-30*time.Second)), w.seedData(dataDir)); err != nil {
		d.closeServers()
		return nil, fmt.Errorf("demo: %w", err)
	}
	if feedEvery > 0 {
		go d.keepFeedFresh(feed, feedEvery)
	} else {
		close(d.stopped)
	}

	repo := Owner + "/" + ConfigRepo
	d.Config = config.Config{
		Listen: ":8080", DataDir: dataDir,
		Site:           config.Site{Title: "Acme projects", Timezone: "UTC"},
		IncludePrivate: true,
		Forges: []config.Forge{
			{ID: "gitea", Type: "gitea", URL: d.GiteaURL, Token: "demo-token", Owners: []string{Owner}, Heatmap: true},
			{ID: "github", Type: "github", URL: d.GitHubURL, Token: "demo-token", Owners: []string{Owner}, Heatmap: true},
		},
		Projects: config.FileRef{Forge: "gitea", Repo: repo, Path: "projects.yaml"},
		Ideas: config.Ideas{Labels: []string{"idea", "enhancement"}, Marker: "waymark-idea:",
			File: config.FileRef{Forge: "gitea", Repo: repo, Path: "ideas.json"}},
		NeedsYou: config.NeedsYou{Labels: []string{"needs-you", "decision"}, Assigned: true, ReviewRequests: true},
		Notes:    config.Notes{Type: "markdown", Dir: notesDir, Markers: []string{"WAITING:", "@me"}},
		Plans:    config.Plans{Dirs: []string{"docs/plans"}},
		Docker:   config.Docker{Feed: feed, Label: "web-01"},
		// Repo homepages are left out: the probe client refuses loopback, and
		// demo mode reaches nothing beyond it.
		Probes: config.Probes{Homepages: false},
		Demo:   true,
	}
	return d, nil
}

// Close stops the servers and the feed refresher.
func (d *Demo) Close() {
	select {
	case <-d.stop:
	default:
		close(d.stop)
		<-d.stopped
	}
	d.closeServers()
}

func (d *Demo) closeServers() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, s := range d.servers {
		_ = s.Shutdown(ctx)
	}
}

// keepFeedFresh moves the feed's mtime on with the world's clock.
func (d *Demo) keepFeedFresh(path string, every time.Duration) {
	defer close(d.stopped)
	start := time.Now()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			mt := d.World.Now.Add(time.Since(start))
			_ = os.Chtimes(path, mt, mt)
		}
	}
}

// WriteNotes writes the markdown notes into dir, each with its Updated time
// as its mtime.
func (w *World) WriteNotes(dir string) error {
	w.mu.Lock()
	notes := append([]Note(nil), w.Notes...)
	w.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, n := range notes {
		p := filepath.Join(dir, n.File)
		if err := os.WriteFile(p, []byte(n.Body), 0o644); err != nil {
			return err
		}
		if err := os.Chtimes(p, n.Updated, n.Updated); err != nil {
			return err
		}
	}
	return nil
}

// WriteFeed writes the Docker feed to path with mtime as its mtime.
func (w *World) WriteFeed(path string, mtime time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, w.FeedJSONL(), 0o644); err != nil {
		return err
	}
	return os.Chtimes(path, mtime, mtime)
}

// ProbeHistory is a day of checks every ten minutes for each probe: up, or
// down for the last six hours when it answers an error now.
func (w *World) ProbeHistory() map[string][]model.ProbePoint {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string][]model.ProbePoint{}
	names := make([]string, 0, len(w.Probes))
	for n := range w.Probes {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, name := range names {
		code := w.Probes[name]
		var pts []model.ProbePoint
		for m := 24 * 60; m >= 10; m -= 10 {
			at := w.Now.Add(-time.Duration(m) * time.Minute)
			up := code < 400 || at.Before(w.Now.Add(-6*time.Hour))
			pt := model.ProbePoint{At: at, Up: up}
			if up {
				pt.LatencyMS = 18 + (m/10+i*7)%23
			}
			pts = append(pts, pt)
		}
		out[w.Base+"/probe/"+name] = pts
	}
	return out
}

// seedData gives a new data dir w's events and probe history, through the
// store, so the first assemble has a history to show. A data dir that holds
// state already is left as it is.
func (w *World) seedData(dir string) error {
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	w.mu.Lock()
	evs := append([]model.Event(nil), w.Events...)
	now := w.Now
	w.mu.Unlock()
	if !st.Empty() || len(st.Events(time.Time{}, now)) > 0 {
		return nil
	}
	st.AddEvents(evs, now)
	hist := w.ProbeHistory()
	keys := make([]string, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, pt := range hist[k] {
			st.AddProbe(k, pt, now)
		}
	}
	return st.Save(now)
}
