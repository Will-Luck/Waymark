package notes

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Will-Luck/waymark/internal/registry"
)

// markdown reads a folder of .md files (subfolders too, hidden ones skipped).
type markdown struct {
	folder
	markers *regexp.Regexp // nil: no markers, so no clauses
}

// NewMarkdown reads the notes in dir. A list item carrying one of markers
// (matched as written) is a clause, with the marker removed.
func NewMarkdown(dir string, markers []string) Adapter {
	return &markdown{folder: folder{dir: dir}, markers: markerRe(markers)}
}

func (m *markdown) Kind() string      { return "markdown" }
func (m *markdown) Label() string     { return "Notes" }
func (m *markdown) Signature() string { return m.signature(true) }

// Load gives a file to a project by its name (<slug>.md, or the v0.1 form
// with - as _), by its frontmatter (project: <slug>), or by the projects
// file's notes: list; any of these, so one file may be several projects'.
func (m *markdown) Load(projects []registry.Project) (map[string][]Note, error) {
	root, err := m.open()
	if err != nil {
		return nil, fmt.Errorf("notes folder: %w", err)
	}
	defer root.Close()
	paths, err := mdFiles(root, true)
	if err != nil {
		return nil, fmt.Errorf("notes folder: %w", err)
	}
	byName := map[string][]string{} // lower-case file name -> slugs
	bySlug := map[string]string{}   // lower-case slug -> slug
	listed := map[string]bool{}     // file names the projects file lists
	claim := func(name, slug string) {
		key := strings.ToLower(name)
		if !slices.Contains(byName[key], slug) {
			byName[key] = append(byName[key], slug)
		}
	}
	for _, p := range projects {
		bySlug[strings.ToLower(p.Slug)] = p.Slug
		claim(p.Slug+".md", p.Slug)
		claim(strings.ReplaceAll(p.Slug, "-", "_")+".md", p.Slug)
		for _, f := range p.Notes {
			claim(filepath.Base(f), p.Slug)
			listed[filepath.Base(f)] = true
		}
	}
	found := map[string]bool{}
	out := map[string][]Note{}
	for _, rel := range paths {
		name := strings.ToLower(path.Base(rel))
		b, mod, err := m.read(root, rel)
		if errors.Is(err, errSkipped) {
			found[name] = true // there, so not missing
		}
		if err != nil {
			continue
		}
		found[name] = true
		body, top, _ := frontmatter(string(b))
		owners := slices.Clone(byName[name])
		if s, ok := bySlug[strings.ToLower(top["project"])]; ok && !slices.Contains(owners, s) {
			owners = append(owners, s)
		}
		if len(owners) == 0 {
			continue
		}
		n := m.note(rel, body, top, mod)
		for _, slug := range owners {
			one := n
			one.Project = slug
			out[slug] = append(out[slug], one)
		}
	}
	var lost []string
	for name := range listed {
		if !found[strings.ToLower(name)] {
			lost = append(lost, name)
		}
	}
	sort.Strings(lost)
	return out, missingError(m.dir, lost)
}

func (m *markdown) note(rel, body string, top map[string]string, mtime time.Time) Note {
	n := Note{File: rel, Name: top["name"], Modified: mtime.UTC(), Body: capBody(strings.TrimSpace(body))}
	if n.Name == "" {
		n.Name = top["title"]
	}
	if n.Name == "" {
		n.Name = strings.TrimSuffix(path.Base(rel), ".md")
	}
	n.Description = Truncate(top["description"], DescriptionRunes)
	if t, dateOnly, ok := parseUpdated(top["updated"]); ok {
		n.Modified, n.ModifiedDateOnly = t, dateOnly
	}
	n.Clauses = markerClauses(body, m.markers)
	return n
}

// parseUpdated reads an updated: stamp: the claude-memory forms, plus a date
// and time with a space or without seconds (taken as UTC).
func parseUpdated(s string) (t time.Time, dateOnly, ok bool) {
	if t, dateOnly, ok := parseTime(s); ok {
		return t, dateOnly, true
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), false, true
		}
	}
	return time.Time{}, false, false
}

// markerRe matches any of markers as written, never inside a word: a marker
// starting with a letter or digit starts at a word edge, one starting with
// anything else (@me) at the start of the item or after a space, so neither
// @meeting nor bob@me.com matches; a marker ending with a letter or digit
// ends at a word edge.
func markerRe(markers []string) *regexp.Regexp {
	var alts []string
	for _, mk := range markers {
		mk = strings.TrimSpace(mk)
		if mk == "" {
			continue
		}
		q := regexp.QuoteMeta(mk)
		if isWordByte(mk[0]) {
			q = `\b` + q
		} else {
			q = `(?:^|\s)` + q
		}
		if isWordByte(mk[len(mk)-1]) {
			q += `\b`
		}
		alts = append(alts, q)
	}
	if len(alts) == 0 {
		return nil
	}
	return regexp.MustCompile(strings.Join(alts, "|"))
}

var (
	listItemRe = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,9}[.)])\s+(.*)$`)
	taskBoxRe  = regexp.MustCompile(`^\[[ xX]\](?:\s|$)`)
)

// markerClauses turns the list items carrying a marker into clauses, the
// marker removed. A task item ([ ] or [x]) is a to-do, not a wait, and
// fenced code is skipped.
func markerClauses(body string, re *regexp.Regexp) []string {
	if re == nil {
		return nil
	}
	var cs clauseSet
	fence := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
			continue
		}
		m := listItemRe.FindStringSubmatch(line)
		if m == nil || taskBoxRe.MatchString(m[1]) || !re.MatchString(m[1]) {
			continue
		}
		cs.add(cleanClause(re.ReplaceAllString(m[1], " ")))
	}
	return cs.out
}
