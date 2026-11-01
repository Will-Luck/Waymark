//go:build unix

package notes

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/registry"
)

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// leaked reports whether any note carries text from outside the folder.
func leaked(got map[string][]Note, secrets ...string) string {
	for _, ns := range got {
		for _, n := range ns {
			all := n.Name + n.Description + n.IndexLine + n.Heading + n.Body + strings.Join(n.Clauses, " ")
			for _, s := range secrets {
				if strings.Contains(all, s) {
					return n.Project + " " + n.File
				}
			}
		}
	}
	return ""
}

// A symlink never reads outside the notes folder: not to a file elsewhere
// (absolute or relative), not to /proc, not through a linked subfolder.
func TestReadsStayInTheFolder(t *testing.T) {
	outside := t.TempDir()
	mkfile(t, filepath.Join(outside, "secret.md"), "---\ndescription: TOKEN-XYZ\n---\n- WAITING: TOKEN-XYZ\n")
	mkfile(t, filepath.Join(outside, "web.md"), "- WAITING: TOKEN-XYZ\n")
	d := t.TempDir()
	symlink(t, filepath.Join(outside, "secret.md"), filepath.Join(d, "app.md"))
	symlink(t, filepath.Join("..", filepath.Base(outside), "secret.md"), filepath.Join(d, "rel.md"))
	symlink(t, "/proc/self/environ", filepath.Join(d, "env.md"))
	symlink(t, outside, filepath.Join(d, "linked"))
	mkfile(t, filepath.Join(d, "real.md"), "- WAITING: Mark to sign off\n")
	symlink(t, "real.md", filepath.Join(d, "alias.md")) // inside the folder: fine
	secrets := []string{"TOKEN-XYZ", "PATH=", "HOME="}

	ps := []registry.Project{{Slug: "app"}, {Slug: "rel"}, {Slug: "env"}, {Slug: "web"}, {Slug: "alias"}}
	got, err := NewMarkdown(d, markers).Load(ps)
	if err != nil {
		t.Fatal(err)
	}
	if where := leaked(got, secrets...); where != "" {
		t.Errorf("markdown: outside text in %s", where)
	}
	for _, slug := range []string{"app", "rel", "env", "web"} {
		if len(got[slug]) != 0 {
			t.Errorf("markdown: %s read %q", slug, files(got[slug]))
		}
	}
	if a := got["alias"]; len(a) != 1 || a[0].Clauses[0] != "Mark to sign off" {
		t.Errorf("markdown: a link inside the folder must be read: %+v", a)
	}

	mkfile(t, filepath.Join(d, "MEMORY.md"), "- [App](app.md): v1 live\n- [Env](env.md): env\n")
	got, err = NewClaudeMemory(d, []string{"Mark"}).Load([]registry.Project{{Slug: "app"}, {Slug: "env", Notes: []string{"env.md"}}})
	if where := leaked(got, secrets...); where != "" {
		t.Errorf("claude-memory: outside text in %s", where)
	}
	if len(got["app"]) != 0 || len(got["env"]) != 0 || err == nil || !strings.HasSuffix(err.Error(), ": env.md") {
		t.Errorf("claude-memory: app %q env %q err %v (a listed note that leaves the folder is missing)", files(got["app"]), files(got["env"]), err)
	}
}

// A FIFO named like a note is not a note, and never blocks a load.
func TestFIFONoteDoesNotBlock(t *testing.T) {
	d := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(d, "app.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(d, "MEMORY.md"), "- [App](app.md): v1 live\n")
	mkfile(t, filepath.Join(d, "tool.md"), "- WAITING: Mark to pick a name\n")
	ps := []registry.Project{{Slug: "app"}, {Slug: "tool"}}
	for _, ad := range []Adapter{NewMarkdown(d, markers), NewClaudeMemory(d, []string{"Mark"})} {
		done := make(chan map[string][]Note, 1)
		go func() {
			got, _ := ad.Load(ps)
			ad.Signature()
			done <- got
		}()
		select {
		case got := <-done:
			if len(got["app"]) != 0 || len(got["tool"]) != 1 {
				t.Errorf("%s: app %q tool %q", ad.Kind(), files(got["app"]), files(got["tool"]))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Load blocked on a FIFO", ad.Kind())
		}
	}
}

// A notes folder that is a symlink is read, and its signature follows edits.
func TestSymlinkedFolder(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "notes")
	symlink(t, real, link)
	mkfile(t, filepath.Join(real, "MEMORY.md"), "- [App](app.md): v1 live; rename awaits Mark\n")
	mkfile(t, filepath.Join(real, "app.md"), "- WAITING: Mark to pick a name\n")
	ps := []registry.Project{{Slug: "app"}}
	cases := []struct {
		ad   Adapter
		edit string // the file the edit changes
		want func(Note) string
	}{
		{NewMarkdown(link, markers), "app.md", func(n Note) string { return strings.Join(n.Clauses, "; ") }},
		{NewClaudeMemory(link, []string{"Mark"}), "MEMORY.md", func(n Note) string { return n.IndexLine }},
	}
	for _, tc := range cases {
		got, err := tc.ad.Load(ps)
		if err != nil || len(got["app"]) != 1 {
			t.Fatalf("%s: %v %+v", tc.ad.Kind(), err, got)
		}
		before, s1 := tc.want(got["app"][0]), tc.ad.Signature()
		if s1 == "" || s1 == "-" {
			t.Errorf("%s: signature %q lists no files", tc.ad.Kind(), s1)
		}
		p := filepath.Join(real, tc.edit)
		b, _ := os.ReadFile(p)
		mkfile(t, p, strings.Replace(string(b), "Mark", "Mark and Sam", 1))
		if s2 := tc.ad.Signature(); s2 == s1 {
			t.Errorf("%s: the signature did not follow an edit", tc.ad.Kind())
		}
		got, _ = tc.ad.Load(ps)
		if after := tc.want(got["app"][0]); after == before || !strings.Contains(after, "Mark and Sam") {
			t.Errorf("%s: after the edit %q (before %q)", tc.ad.Kind(), after, before)
		}
	}
}
