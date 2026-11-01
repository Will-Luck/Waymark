package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/model"
)

func slugsOf(s *model.Site) []string {
	var out []string
	for _, p := range s.Projects {
		out = append(out, p.Slug)
	}
	slices.Sort(out)
	return out
}

// fileIdeas counts the ideas that came from the ideas file.
func fileIdeas(s *model.Site) int {
	n := 0
	for _, i := range s.Ideas {
		if i.Source == "file" {
			n++
		}
	}
	return n
}

func registryStatus(t *testing.T, s *model.Site) model.SourceStatus {
	t.Helper()
	for _, st := range s.Sources {
		if st.Name == "registry" {
			return st
		}
	}
	t.Fatal("no registry source status")
	return model.SourceStatus{}
}

// restarted is a second App on the first one's data directory, as after a
// container restart.
func (w *world) restarted() *App {
	w.t.Helper()
	a, err := New(w.app.cfg, NewForges(w.app.cfg))
	if err != nil {
		w.t.Fatal(err)
	}
	a.now = func() time.Time { return fixedNow }
	return a
}

func TestGoodLoadWritesCaches(t *testing.T) {
	w := newWorld(t)
	if _, err := w.app.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{
		projectsCache: w.registry(),
		ideasCache:    w.demo.IdeasJSON(),
	} {
		got, err := os.ReadFile(filepath.Join(w.dataDir, name))
		if err != nil || string(got) != string(want) {
			t.Errorf("%s: err %v, cached bytes differ from the source's", name, err)
		}
	}
	left, _ := filepath.Glob(filepath.Join(w.dataDir, "*.tmp*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// With the source down after a restart, the cached projects and ideas are
// used and the registry source says what failed.
func TestProjectsFallBackToCache(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	first, err := w.app.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if registryStatus(t, first).Error != "" {
		t.Fatalf("source should be healthy: %+v", registryStatus(t, first))
	}

	w.rawFail.Store(true)
	a := w.restarted()
	got, err := a.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want, have := slugsOf(first), slugsOf(got); !slices.Equal(want, have) {
		t.Errorf("projects from the cache = %v, want %v", have, want)
	}
	if len(got.Ideas) != len(first.Ideas) || len(got.Ideas) == 0 {
		t.Errorf("ideas from the cache = %d, want %d", len(got.Ideas), len(first.Ideas))
	}
	st := registryStatus(t, got)
	if st.OK || !strings.Contains(st.Error, "500") {
		t.Errorf("registry status = %+v, want the source error", st)
	}
	cached, err := os.ReadFile(filepath.Join(w.dataDir, projectsCache))
	if err != nil || string(cached) != string(w.registry()) {
		t.Error("a failed read must leave the cache as it was")
	}
}

// A bad file in the source is a failure too: the cached copy stays.
func TestUnparseableSourceKeepsCache(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	first, err := w.app.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w.edit("/api/v1/repos/acme/waymark-config/raw/ideas.json", func([]byte) []byte { return []byte("{not json") })
	a := w.restarted()
	got, err := a.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ideas) != len(first.Ideas) || registryStatus(t, got).OK {
		t.Errorf("ideas %d (want %d), status %+v", len(got.Ideas), len(first.Ideas), registryStatus(t, got))
	}
	b, _ := os.ReadFile(filepath.Join(w.dataDir, ideasCache))
	if !strings.Contains(string(b), "beacon-01") {
		t.Error("a bad read overwrote the ideas cache")
	}
}

// No source and no cache: no file projects, only auto ones, and the error shows.
func TestNoSourceNoCacheMeansAutoProjectsOnly(t *testing.T) {
	w := newWorld(t)
	w.rawFail.Store(true)
	// newWorld's app starts from the world's registry (newApp swaps it in); a
	// fresh App on the empty data directory does not.
	a := w.restarted()
	got, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Projects) == 0 {
		t.Fatal("the repo list should still give auto projects")
	}
	for _, p := range got.Projects {
		if !p.Auto {
			t.Errorf("%s is not an auto project, but there is no registry", p.Slug)
		}
	}
	// Ideas from labelled issues need no file; none from the file shows.
	if n := fileIdeas(got); n != 0 {
		t.Errorf("file ideas = %d, want none", n)
	}
	if st := registryStatus(t, got); st.OK || !strings.Contains(st.Error, "500") {
		t.Errorf("registry status = %+v", st)
	}
}

// Removing the projects source must not leave the old cache on screen.
func TestNoSourceIgnoresCache(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	w.app.cfg.Projects, w.app.cfg.Ideas.File = config.FileRef{}, config.FileRef{}
	a := w.restarted()
	got, err := a.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got.Projects {
		if !p.Auto {
			t.Errorf("%s came from a cache although no source is configured", p.Slug)
		}
	}
	if n := fileIdeas(got); n != 0 {
		t.Errorf("file ideas = %d, want none", n)
	}
	// Nor is there a source row for the files: no file is read.
	if _, ok := sourceStatus(got, "registry"); ok {
		t.Errorf("sources = %v, want no registry source", sourceNamesOf(got))
	}
}

// A project's plan_dirs are the directories asked for, not the default.
func TestProjectPlanDirsReachTheForge(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if _, err := w.app.RunOnce(ctx); err != nil { // loads the source registry
		t.Fatal(err)
	}
	const base = "/api/v1/repos/acme/beacon/contents/"
	before := w.count(base + "docs/plans")
	if before == 0 {
		t.Fatal("the configured plan dir was not read before the override")
	}
	w.app.mu.Lock()
	for i := range w.app.reg.Projects {
		if w.app.reg.Projects[i].Slug == "beacon" {
			w.app.reg.Projects[i].PlanDirs = []string{"notes/plans"}
		}
	}
	w.app.repoSt[repoKey("gitea", "acme/beacon")].files = nil // read the files again
	w.app.mu.Unlock()
	w.app.collectDetails(ctx, false)
	if w.count(base+"notes/plans") == 0 {
		t.Error("the project's plan_dirs were not requested")
	}
	if n := w.count(base + "docs/plans"); n != before {
		t.Errorf("the default plan dir was read again (%d -> %d) despite the override", before, n)
	}
}
