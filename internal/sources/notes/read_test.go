package notes

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Will-Luck/waymark/internal/registry"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &b
}

// A file over 1 MiB is skipped, logged once, and is not a missing note.
func TestOversizedNoteSkipped(t *testing.T) {
	logged := captureLog(t)
	d := t.TempDir()
	big := "- WAITING: Mark big\n" + strings.Repeat("x", maxNoteBytes)
	mkfile(t, filepath.Join(d, "big.md"), big)
	mkfile(t, filepath.Join(d, "small.md"), "- WAITING: Mark small\n")
	mkfile(t, filepath.Join(d, "MEMORY.md"), "- [Big](big.md): awaits Mark\n- [Small](small.md): awaits Mark\n")
	ps := []registry.Project{{Slug: "big", Notes: []string{"big.md"}}, {Slug: "small"}}
	for _, ad := range []Adapter{NewMarkdown(d, markers), NewClaudeMemory(d, []string{"Mark"})} {
		for i := 0; i < 2; i++ {
			got, err := ad.Load(ps)
			if err != nil || len(got["big"]) != 0 || len(got["small"]) != 1 {
				t.Errorf("%s: err %v, big %q, small %q", ad.Kind(), err, files(got["big"]), files(got["small"]))
			}
		}
	}
	if n := strings.Count(logged.String(), "big.md"); n != 2 {
		t.Errorf("big.md logged %d times, want once per adapter:\n%s", n, logged.String())
	}
}

func TestBodyCappedOnARuneBoundary(t *testing.T) {
	d := t.TempDir()
	mkfile(t, filepath.Join(d, "app.md"), "a"+strings.Repeat("é", 40000)) // 80001 bytes; é starts at odd offsets
	got, err := NewMarkdown(d, markers).Load([]registry.Project{{Slug: "app"}})
	if err != nil || len(got["app"]) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	b := got["app"][0].Body
	if len(b) > maxBodyBytes || len(b) < maxBodyBytes-utf8.UTFMax || !utf8.ValidString(b) {
		t.Errorf("body is %d bytes (cap %d), valid UTF-8 %v", len(b), maxBodyBytes, utf8.ValidString(b))
	}
}

// A file written to while it is read is read again; one that never settles
// is skipped (and is not a missing note).
func TestNoteChangingDuringReadIsReadAgain(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "app.md")
	mkfile(t, p, "- WAITING: Mark old\n")
	writes := 0
	afterRead = func(name string) {
		if writes < 1 {
			writes++
			mkfile(t, p, "- WAITING: Mark new, longer\n")
		}
	}
	t.Cleanup(func() { afterRead = nil })
	ps := []registry.Project{{Slug: "app", Notes: []string{"app.md"}}}
	got, err := NewMarkdown(d, markers).Load(ps)
	if err != nil || len(got["app"]) != 1 || got["app"][0].Clauses[0] != "Mark new, longer" {
		t.Errorf("after one write: %v %+v", err, got["app"])
	}

	afterRead = func(name string) {
		writes++
		mkfile(t, p, "- WAITING: Mark "+strings.Repeat("!", writes)+"\n")
	}
	got, err = NewMarkdown(d, markers).Load(ps)
	if err != nil || len(got["app"]) != 0 {
		t.Errorf("never settles: %v %+v", err, got["app"])
	}
}

func mkfile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
