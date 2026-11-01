package notes

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/registry"
)

const mdDir = "../../../testdata/notes-markdown"

var markers = []string{"WAITING:", "@me"}

func acmeProjects() []registry.Project {
	return []registry.Project{
		{Slug: "acme-web"},
		{Slug: "acme-api"},
		{Slug: "acme-docs"},
		{Slug: "acme-cli", Notes: []string{"cli-notes.md"}},
		{Slug: "acme-idle"},
	}
}

func files(ns []Note) []string {
	var out []string
	for _, n := range ns {
		out = append(out, n.File)
	}
	return out
}

func TestMarkdownMatchesByFileAndFrontMatter(t *testing.T) {
	ad := NewMarkdown(mdDir, markers)
	if ad.Kind() != "markdown" || ad.Label() != "Notes" {
		t.Errorf("kind %q label %q", ad.Kind(), ad.Label())
	}
	got, err := ad.Load(acmeProjects())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		// By file name, and by front matter project: acme-web.
		"acme-web": {"acme-web.md", "meeting-2026-10-28.md"},
		// The v0.1 underscore form of the slug.
		"acme-api": {"acme_api.md"},
		// A subfolder counts; .trash (hidden) is never read.
		"acme-docs": {"archive/acme-docs.md"},
		// The projects file's notes: list adds to the file-name match.
		"acme-cli": {"acme-cli.md", "cli-notes.md"},
	}
	for slug, w := range want {
		if f := files(got[slug]); !reflect.DeepEqual(f, w) {
			t.Errorf("%s: files %q, want %q", slug, f, w)
		}
		for _, n := range got[slug] {
			if n.Project != slug {
				t.Errorf("%s: %s has Project %q", slug, n.File, n.Project)
			}
		}
	}
	if len(got) != len(want) {
		t.Errorf("projects with notes = %d, want %d (ideas.md is nobody's, acme-idle has none): %v", len(got), len(want), got)
	}
	web := got["acme-web"][0]
	if web.Name != "Acme web" || web.Description != "The Acme shop front, what is live and what waits." || !strings.HasPrefix(web.Body, "# Acme web\n\nv1.4.0 is live") {
		t.Errorf("acme-web.md = name %q description %q body %q", web.Name, web.Description, web.Body)
	}
	if api := got["acme-api"][0]; api.Name != "acme_api" || api.IndexLine != "" {
		t.Errorf("acme_api.md: name %q index line %q (no title: the file name; no index)", api.Name, api.IndexLine)
	}

	// A note the projects file names that is not there is reported, and the
	// notes that were read still come back.
	ns, err := ad.Load([]registry.Project{{Slug: "acme-web"}, {Slug: "acme-gone", Notes: []string{"gone.md"}}})
	if err == nil || !strings.Contains(err.Error(), "gone.md") || len(ns["acme-web"]) != 2 {
		t.Errorf("missing listed note: err %v, acme-web %q", err, files(ns["acme-web"]))
	}

	// No folder: nothing read.
	if ns, err := NewMarkdown(filepath.Join(t.TempDir(), "none"), markers).Load(acmeProjects()); err == nil || ns != nil {
		t.Errorf("missing folder: %v %v", ns, err)
	}
}

func TestMarkdownMarkersBecomeClauses(t *testing.T) {
	got, err := NewMarkdown(mdDir, markers).Load(acmeProjects())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		// Unchecked and checked boxes, a paragraph line, a code block, a
		// marker inside a word (@meeting, bob@me.com) and a marker in the
		// wrong case are not needs-you; a repeated item counts once.
		"acme-web.md": {
			"Mark to pick the checkout colours",
			"Pick the logo",
			"sign off the privacy page",
			"Mark's call on the beta flag",
			"numbered items count too",
		},
		"meeting-2026-10-28.md": {"book the photo shoot"},
		"acme_api.md":           {"Mark to rotate the signing key"},
		"cli-notes.md":          {"cut the v2 branch"},
		"acme-cli.md":           nil,
		"archive/acme-docs.md":  nil,
	}
	byFile := map[string]Note{}
	for _, ns := range got {
		for _, n := range ns {
			byFile[n.File] = n
		}
	}
	for file, w := range want {
		n, ok := byFile[file]
		if !ok {
			t.Errorf("%s: not loaded", file)
			continue
		}
		if !reflect.DeepEqual(n.Clauses, w) {
			t.Errorf("%s: clauses %q, want %q", file, n.Clauses, w)
		}
	}
	if len(byFile) != len(want) {
		t.Errorf("loaded %d files, want %d", len(byFile), len(want))
	}

	// Markers are matched as written, and only the configured ones.
	other, err := NewMarkdown(mdDir, []string{"waiting:"}).Load([]registry.Project{{Slug: "acme-web"}})
	if err != nil {
		t.Fatal(err)
	}
	if c := other["acme-web"][0].Clauses; !reflect.DeepEqual(c, []string{"lower case is not the marker"}) {
		t.Errorf("marker waiting: gives %q", c)
	}
	none, err := NewMarkdown(mdDir, nil).Load([]registry.Project{{Slug: "acme-web"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range none["acme-web"] {
		if len(n.Clauses) != 0 {
			t.Errorf("no markers: %s has clauses %q", n.File, n.Clauses)
		}
	}
}

func TestMarkdownUpdatedFrontMatterBeatsMtime(t *testing.T) {
	d := t.TempDir()
	mtime := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	write := func(name, body string) {
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write("stamp.md", "---\nupdated: 2026-10-30T09:15:00Z\n---\n- note\n")
	write("date.md", "---\nupdated: '2026-10-29'\n---\n")
	write("local.md", "---\nupdated: 2026-10-28 14:30\n---\n")
	write("plain.md", "# no front matter\n")
	write("bad.md", "---\nupdated: last Tuesday\n---\n")
	var ps []registry.Project
	for _, s := range []string{"stamp", "date", "local", "plain", "bad"} {
		ps = append(ps, registry.Project{Slug: s})
	}
	got, err := NewMarkdown(d, markers).Load(ps)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		slug     string
		mod      time.Time
		dateOnly bool
	}{
		{"stamp", time.Date(2026, 10, 30, 9, 15, 0, 0, time.UTC), false},
		{"date", time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC), true},
		{"local", time.Date(2026, 10, 28, 14, 30, 0, 0, time.UTC), false},
		{"plain", mtime, false},
		{"bad", mtime, false},
	}
	for _, tc := range cases {
		ns := got[tc.slug]
		if len(ns) != 1 {
			t.Errorf("%s: %d notes", tc.slug, len(ns))
			continue
		}
		if n := ns[0]; !n.Modified.Equal(tc.mod) || n.ModifiedDateOnly != tc.dateOnly {
			t.Errorf("%s: modified %v (date only %v), want %v (%v)", tc.slug, n.Modified, n.ModifiedDateOnly, tc.mod, tc.dateOnly)
		}
	}
}

func TestMarkdownSignatureChanges(t *testing.T) {
	d := t.TempDir()
	ad := NewMarkdown(d, markers)
	write := func(name, s string) {
		p := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "x")
	s1 := ad.Signature()
	write("sub/b.md", "y")
	s2 := ad.Signature()
	if s1 == s2 {
		t.Error("a new note in a subfolder did not change the signature")
	}
	write("a.md", "longer")
	s3 := ad.Signature()
	if s3 == s2 {
		t.Error("an edited note did not change the signature")
	}
	write("readme.txt", "not a note")
	write(".obsidian/workspace.md", "editor state")
	if s4 := ad.Signature(); s4 != s3 {
		t.Errorf("a non-note or a hidden folder changed the signature:\n%s\n%s", s3, s4)
	}
}
