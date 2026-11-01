// Package notes reads project notes (read-only) through one adapter per notes
// type: claude-memory (Claude's memory folder, v0.1's reader) and markdown
// (any folder of .md files: an Obsidian vault, plain notes).
package notes

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/waymark/internal/mdlite"
	"github.com/Will-Luck/waymark/internal/registry"
)

// Adapter reads one kind of notes folder (config notes.type). Type none has
// no adapter.
type Adapter interface {
	Kind() string  // "markdown" | "claude-memory"
	Label() string // panel title: "Notes" | "Claude's notes"
	// Load reads each project's notes, by project slug. A non-nil map comes
	// back with an error when the notes were read but a note the projects
	// file names is missing; a nil map means nothing could be read.
	Load(projects []registry.Project) (map[string][]Note, error)
	// Signature changes when any input file changes, so the caller can skip
	// a reload. It does not cover the projects passed to Load.
	Signature() string
}

type Note struct {
	// Project is the slug of the project the note was loaded for.
	Project string
	// File is the note's path under the notes folder, with forward slashes.
	File string
	Name string
	// Description is the frontmatter description, truncated for display. It
	// is often an out-of-date log, so it is never used to find actions.
	Description string
	// IndexLine is the curated MEMORY.md line for the note (claude-memory).
	IndexLine string
	Modified  time.Time
	// ModifiedDateOnly is set when the stamp was a bare YYYY-MM-DD: Modified
	// is then midnight UTC of that date, and the note covers the whole day.
	ModifiedDateOnly bool
	// Heading is the newest "## YYYY-MM-DD ..." heading's text, without the
	// date (claude-memory).
	Heading     string
	HeadingDate time.Time
	// Clauses are the note's needs-you items, already extracted by the
	// adapter: what derive turns into Decide actions.
	Clauses []string
	// Body is the note's markdown after the frontmatter, for the panel
	// (markdown; Claude's notes show their index line instead).
	Body string
}

// cleanClause makes a clause plain text on one line.
func cleanClause(c string) string {
	c = strings.ReplaceAll(c, "**", "")
	c = mdlite.Plain(c)
	c = strings.Join(strings.Fields(c), " ")
	return strings.TrimSpace(strings.TrimLeft(c, ",:- "))
}

// clauseSet keeps clauses in order, dropping repeats case-insensitively.
type clauseSet struct {
	out  []string
	seen map[string]bool
}

func (s *clauseSet) add(c string) {
	key := strings.ToLower(c)
	if c == "" || s.seen[key] {
		return
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.seen[key] = true
	s.out = append(s.out, c)
}

// Read limits: a note file over maxNoteBytes is skipped (and logged once);
// Note.Body keeps at most maxBodyBytes.
const (
	maxNoteBytes = 1 << 20
	maxBodyBytes = 64 << 10
	readTries    = 3
)

var (
	// errSkipped: the file is there, but was not read this time.
	errSkipped    = errors.New("skipped")
	errTooBig     = fmt.Errorf("larger than 1 MiB: %w", errSkipped)
	errChanging   = fmt.Errorf("changed while it was read: %w", errSkipped)
	errNotRegular = errors.New("not a regular file")
)

// afterRead, when set, runs between reading a file and checking it did not
// change meanwhile (tests).
var afterRead func(name string)

// folder is the notes folder both adapters read, and the only way they read
// it. Every path is relative to the folder and stays inside it (os.Root: a
// symlink may not leave it, so /proc/self/environ or ../secret never reach a
// note); only regular files are read, a FIFO never blocks, and a file is
// read whole or not at all.
type folder struct {
	dir    string
	mu     sync.Mutex
	warned map[string]bool // files already logged as too big
}

// open resolves the folder (which may be a symlink) and opens it as a root.
func (f *folder) open() (*os.Root, error) {
	dir, err := filepath.EvalSymlinks(f.dir)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

// mdFiles lists the .md files in root, as slash paths, sorted. With recursive
// set it reads subfolders too, except hidden ones (.obsidian, .trash, .git);
// an unreadable subfolder is skipped, and a linked folder is not followed.
func mdFiles(root *os.Root, recursive bool) ([]string, error) {
	var out []string
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == "." {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != "." && (!recursive || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".md") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// signature is every regular .md file mdFiles lists, with its size and mtime.
func (f *folder) signature(recursive bool) string {
	root, err := f.open()
	if err != nil {
		return "-"
	}
	defer root.Close()
	names, err := mdFiles(root, recursive)
	if err != nil {
		return "-"
	}
	var b strings.Builder
	for _, name := range names {
		st, err := root.Stat(filepath.FromSlash(name))
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d;", name, st.Size(), st.ModTime().UnixNano())
	}
	return b.String()
}

// read reads the file name (a slash path) in root, with its mtime. An error
// wrapping errSkipped means the file is there but was not read: too big
// (logged once), or still changing after readTries reads. Any other error
// means there is no regular file by that name inside the folder.
func (f *folder) read(root *os.Root, name string) ([]byte, time.Time, error) {
	for try := 1; ; try++ {
		b, mod, err := readOnce(root, filepath.FromSlash(name))
		switch {
		case errors.Is(err, errTooBig):
			f.mu.Lock()
			if !f.warned[name] {
				if f.warned == nil {
					f.warned = map[string]bool{}
				}
				f.warned[name] = true
				log.Printf("notes: skipped %s in %s: %v", name, f.dir, errTooBig)
			}
			f.mu.Unlock()
		case errors.Is(err, errChanging) && try < readTries:
			continue
		}
		return b, mod, err
	}
}

func readOnce(root *os.Root, name string) ([]byte, time.Time, error) {
	st, err := root.Stat(name)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !st.Mode().IsRegular() {
		return nil, time.Time{}, errNotRegular
	}
	if st.Size() > maxNoteBytes {
		return nil, time.Time{}, errTooBig
	}
	// Non-blocking: a file swapped for a FIFO since the Stat cannot hold
	// the open waiting for a writer.
	fh, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer fh.Close()
	before, err := fh.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if !before.Mode().IsRegular() {
		return nil, time.Time{}, errNotRegular
	}
	b, err := io.ReadAll(io.LimitReader(fh, maxNoteBytes+1))
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(b) > maxNoteBytes {
		return nil, time.Time{}, errTooBig
	}
	if afterRead != nil {
		afterRead(name)
	}
	after, err := fh.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, time.Time{}, errChanging
	}
	return b, before.ModTime(), nil
}

// capBody keeps the first maxBodyBytes of s, cut on a rune boundary.
func capBody(s string) string {
	if len(s) <= maxBodyBytes {
		return s
	}
	cut := maxBodyBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isWordByte(b byte) bool {
	return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

// missingError names the notes the projects file lists that are not in dir.
func missingError(dir string, lost []string) error {
	if len(lost) == 0 {
		return nil
	}
	return fmt.Errorf("registry names notes that are not in %s: %s", dir, strings.Join(lost, ", "))
}
