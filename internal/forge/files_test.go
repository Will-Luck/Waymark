package forge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var errMissing = errors.New("missing")

// memSource is a FileSource over maps; it records every read.
type memSource struct {
	files map[string]string
	dirs  map[string][]Entry
	fail  map[string]error
	reads []string
}

func (s *memSource) Raw(ctx context.Context, full, path, ref string) ([]byte, error) {
	s.reads = append(s.reads, "raw "+full+" "+path+"@"+ref)
	if err := s.fail[path]; err != nil {
		return nil, err
	}
	b, ok := s.files[path]
	if !ok {
		return nil, errMissing
	}
	return []byte(b), nil
}

func (s *memSource) Dir(ctx context.Context, full, path, ref string) ([]Entry, error) {
	s.reads = append(s.reads, "dir "+full+" "+path+"@"+ref)
	if err := s.fail[path]; err != nil {
		return nil, err
	}
	es, ok := s.dirs[path]
	if !ok {
		return nil, errMissing
	}
	return es, nil
}

func (s *memSource) IsNotFound(err error) bool { return errors.Is(err, errMissing) }

func TestReadFiles(t *testing.T) {
	src := &memSource{
		files: map[string]string{
			"CHANGELOG.md":                 "# Changelog\n\n## [Unreleased]\n\n- One thing\n\n## [0.3.0] - 2026-10-30\n",
			"plans/2026-10-20-sync.md":     "# Sync\n\n- [x] a\n- [ ] b\n",
			"plans/2026-10-25-no-boxes.md": "# Nothing to tick\n",
			"plans/2026-10-28-untitled.md": "- [x] only\n",
		},
		dirs: map[string][]Entry{"plans": {
			{Name: "2026-10-20-sync.md", Path: "plans/2026-10-20-sync.md", Type: "file", HTMLURL: "https://git.example.com/acme/app/f/2026-10-20-sync.md"},
			{Name: "2026-10-25-no-boxes.md", Path: "plans/2026-10-25-no-boxes.md", Type: "file"},
			{Name: "2026-10-28-untitled.md", Path: "plans/2026-10-28-untitled.md", Type: "file"},
			{Name: "2026-10-29-gone.md", Path: "plans/2026-10-29-gone.md", Type: "file"},
			{Name: "2025-01-01-ancient.md", Path: "plans/2025-01-01-ancient.md", Type: "file"},
		}},
	}
	repo := Repo{FullName: "acme/app", DefaultBranch: "main"}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	f, err := ReadFiles(context.Background(), src, repo, []string{"plans", "nowhere"}, now, "https://git.example.com/acme/app/CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if f.Changelog == nil || f.Changelog.LastVersion != "0.3.0" || len(f.Changelog.Unreleased) != 1 || f.ChangelogURL != "https://git.example.com/acme/app/CHANGELOG.md" || !f.FetchedAt.Equal(now) {
		t.Errorf("changelog = %+v %q", f.Changelog, f.ChangelogURL)
	}
	// Newest first; no checkboxes and missing files are skipped; a missing
	// H1 falls back to the name.
	if len(f.Plans) != 2 || f.Plans[0].Title != "Untitled" || f.Plans[1].Title != "Sync" || f.Plans[1].Done != 1 || f.Plans[1].Total != 2 ||
		f.Plans[1].URL != "https://git.example.com/acme/app/f/2026-10-20-sync.md" {
		t.Errorf("plans = %+v", f.Plans)
	}
	for _, r := range src.reads {
		if strings.Contains(r, "ancient") {
			t.Errorf("a plan older than PlanMaxAge was read: %s", r)
		}
		if !strings.HasSuffix(r, "@main") {
			t.Errorf("read off the default branch: %s", r)
		}
	}

	// A missing CHANGELOG.md is no error and no URL.
	f, err = ReadFiles(context.Background(), &memSource{}, repo, nil, now, "https://git.example.com/acme/app/CHANGELOG.md")
	if err != nil || f.Changelog != nil || f.ChangelogURL != "" {
		t.Errorf("no changelog = %+v %v", f, err)
	}

	// An empty repo reads nothing.
	src = &memSource{}
	if f, err := ReadFiles(context.Background(), src, Repo{FullName: "acme/empty", Empty: true}, []string{"plans"}, now, "x"); err != nil || f.Changelog != nil || len(src.reads) != 0 {
		t.Errorf("empty repo = %+v %v, reads %v", f, err, src.reads)
	}

	// Any other failure fails the read and names the file.
	boom := errors.New("HTTP 500")
	for path, want := range map[string]string{"CHANGELOG.md": "acme/app CHANGELOG.md: HTTP 500", "plans": "acme/app plans: HTTP 500"} {
		src := &memSource{files: map[string]string{"CHANGELOG.md": "# Changelog\n"}, fail: map[string]error{path: boom}}
		if _, err := ReadFiles(context.Background(), src, repo, []string{"plans"}, now, ""); err == nil || err.Error() != want || !errors.Is(err, boom) {
			t.Errorf("%s failure = %v, want %q", path, err, want)
		}
	}
}

func TestSortByUpdated(t *testing.T) {
	var is []Issue
	for i := 1; i <= 12; i++ {
		var it Issue
		it.Number = i
		it.UpdatedAt.Time = time.Date(2026, 10, i, 0, 0, 0, 0, time.UTC)
		is = append(is, it)
	}
	got := SortByUpdated(is)
	if len(got) != 10 || got[0].Number != 12 || got[9].Number != 3 {
		t.Errorf("sorted = %d, first %d, last %d", len(got), got[0].Number, got[9].Number)
	}
	if SortByUpdated(nil) != nil {
		t.Error("nil stays nil")
	}
}
