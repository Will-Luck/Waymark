package notes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/waymark/internal/registry"
)

// DescriptionRunes is the display cap for the frontmatter description.
const DescriptionRunes = 280

// claudeMemory reads Claude's memory folder: MEMORY.md (the index), and for
// each note its frontmatter, its index line and its newest dated heading.
type claudeMemory struct {
	folder
	re *regexp.Regexp // nil: no names, so no clauses
}

// NewClaudeMemory reads Claude's memory notes in dir. Clauses are the parts
// of a note's index line and newest heading that wait on one of you by name.
func NewClaudeMemory(dir string, you []string) Adapter {
	return &claudeMemory{folder: folder{dir: dir}, re: NeedsYouRe(you)}
}

func (c *claudeMemory) Kind() string      { return "claude-memory" }
func (c *claudeMemory) Label() string     { return "Claude's notes" }
func (c *claudeMemory) Signature() string { return c.signature(false) }

// Load gives each project the notes its projects-file entry lists, or else
// the note named like its slug with - as _ (acme-web: acme_web.md).
// Only a listed note that is missing is reported; with MEMORY.md unreadable
// nothing is read.
func (c *claudeMemory) Load(projects []registry.Project) (map[string][]Note, error) {
	root, err := c.open()
	if err != nil {
		return nil, fmt.Errorf("notes folder: %w", err)
	}
	defer root.Close()
	idx, _, err := c.read(root, "MEMORY.md")
	if err != nil {
		return nil, fmt.Errorf("notes index: %w", err)
	}
	index := ParseIndex(string(idx))
	read := map[string]*Note{} // by file name; nil: not read
	readErr := map[string]error{}
	out := map[string][]Note{}
	var lost []string
	for _, p := range projects {
		names, listed := p.Notes, true
		if len(names) == 0 {
			names, listed = []string{strings.ReplaceAll(p.Slug, "-", "_") + ".md"}, false
		}
		got := map[string]bool{}
		for _, f := range names {
			name := filepath.Base(f)
			if got[name] {
				continue
			}
			got[name] = true
			n, done := read[name]
			if !done {
				n, readErr[name] = c.note(root, name, index)
				read[name] = n
			}
			if n == nil {
				// A note too big or still changing is there: not missing.
				if listed && !errors.Is(readErr[name], errSkipped) && !slices.Contains(lost, name) {
					lost = append(lost, name)
				}
				continue
			}
			one := *n
			one.Project = p.Slug
			out[p.Slug] = append(out[p.Slug], one)
		}
	}
	sort.Strings(lost)
	return out, missingError(c.dir, lost)
}

// note reads and parses one note file; nil, with the read error, when it is
// not there or was skipped.
func (c *claudeMemory) note(root *os.Root, name string, index map[string]string) (*Note, error) {
	b, mod, err := c.read(root, name)
	if err != nil {
		return nil, err
	}
	n := ParseNote(name, b, mod, index)
	n.Clauses = noteClauses(c.re, n.IndexLine, n.Heading)
	return &n, nil
}

var (
	indexLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+\.md)\)`)
	datedHeadRe = regexp.MustCompile(`^#{2,6}\s+(\d{4}-\d{2}-\d{2})(?:/\d{1,2})?(.*)$`)
	fmKeyRe     = regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_-]*):(?:\s+(.*)|\s*)$`)
	leadParenRe = regexp.MustCompile(`^\([^)]*\)`)
)

// ParseIndex maps each linked note file to its MEMORY.md line text. The first
// line linking a file wins.
func ParseIndex(md string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, m := range indexLinkRe.FindAllStringSubmatchIndex(line, -1) {
			file := line[m[2]:m[3]]
			if _, ok := out[file]; ok {
				continue
			}
			out[file] = indexText(line, m[0], m[1])
		}
	}
	return out
}

// indexText removes the list marker and the [title](file.md) link (with bold
// markers wrapped round it) and a leading ": ".
func indexText(line string, start, end int) string {
	if start >= 2 && end+2 <= len(line) && line[start-2:start] == "**" && line[end:end+2] == "**" {
		start, end = start-2, end+2
	}
	s := line[:start] + line[end:]
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "- ")
	s = strings.TrimPrefix(s, "* ")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, ":")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// frontmatter is a forgiving reader for the notes' YAML-ish header: plain
// "key: value" lines (values may contain ": ", which strict YAML rejects),
// one level of nesting (metadata:), and wrapped continuation lines.
func frontmatter(b string) (body string, top, meta map[string]string) {
	top, meta = map[string]string{}, map[string]string{}
	lines := strings.Split(b, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return b, top, meta
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return b, top, meta
	}
	parent, lastMap, lastKey := "", top, ""
	for _, raw := range lines[1:end] {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if m := fmKeyRe.FindStringSubmatch(line); m != nil {
			indent, key, val := m[1], m[2], m[3]
			if indent == "" {
				parent = ""
				if strings.TrimSpace(val) == "" {
					parent = key
					lastKey = ""
					continue
				}
				switch strings.TrimSpace(val) {
				case ">", "|", ">-", "|-", ">+", "|+":
					top[key] = ""
				default:
					top[key] = unquote(val)
				}
				lastMap, lastKey = top, key
				continue
			}
			if parent == "metadata" {
				meta[key] = unquote(val)
				lastMap, lastKey = meta, key
				continue
			}
		}
		if lastKey != "" {
			lastMap[lastKey] = strings.TrimSpace(lastMap[lastKey] + " " + unquote(strings.TrimSpace(line)))
		}
	}
	return strings.Join(lines[end+1:], "\n"), top, meta
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
		return v[1 : len(v)-1]
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

// parseTime reads a modified stamp; dateOnly is set for a bare YYYY-MM-DD.
func parseTime(s string) (t time.Time, dateOnly, ok bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), layout == "2006-01-02", true
		}
	}
	return time.Time{}, false, false
}

// ParseNote reads one note file. mtime is the fallback for modified.
func ParseNote(file string, b []byte, mtime time.Time, index map[string]string) Note {
	body, top, meta := frontmatter(string(b))
	n := Note{File: file, Name: top["name"], IndexLine: index[file], Modified: mtime.UTC()}
	if n.Name == "" {
		n.Name = strings.TrimSuffix(file, ".md")
	}
	n.Description = Truncate(top["description"], DescriptionRunes)
	mod := top["modified"]
	if mod == "" {
		mod = meta["modified"]
	}
	if t, dateOnly, ok := parseTime(mod); ok {
		n.Modified, n.ModifiedDateOnly = t, dateOnly
	}
	for _, line := range strings.Split(body, "\n") {
		m := datedHeadRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		d, err := time.Parse("2006-01-02", m[1])
		if err != nil || !d.After(n.HeadingDate) {
			continue
		}
		n.HeadingDate = d
		n.Heading = headingText(m[2])
	}
	return n
}

func headingText(rest string) string {
	s := strings.TrimSpace(rest)
	s = strings.TrimSpace(leadParenRe.ReplaceAllString(s, ""))
	s = strings.TrimLeft(s, ":-–— \t")
	return strings.TrimSpace(s)
}

// Truncate cuts s to at most n runes on a word boundary and adds an ellipsis.
// A bold or code marker left open by the cut is dropped.
func Truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)[:n]
	cut := string(r)
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	cut = strings.TrimRight(cut, " ,;:.-([")
	if strings.Count(cut, "**")%2 == 1 {
		i := strings.LastIndex(cut, "**")
		cut = cut[:i] + cut[i+2:]
	}
	if strings.Count(cut, "`")%2 == 1 {
		i := strings.LastIndex(cut, "`")
		cut = cut[:i] + cut[i+1:]
	}
	return strings.TrimRight(cut, " ") + "…"
}

// NeedsYouRe matches a clause that waits on one of names. A name matches as
// written or in capitals (Mark or MARK), never in lower case, so a name that
// is also a word ("we will mark it") does not; an "await" counts only
// alongside a name: "awaiting reply" waits on someone else. It is nil when
// names is empty.
func NeedsYouRe(names []string) *regexp.Regexp {
	var alts []string
	seen := map[string]bool{}
	ascii := true // every name starts and ends with an ASCII letter or digit
	for _, name := range names {
		name = strings.TrimSpace(name)
		for _, v := range []string{name, strings.ToUpper(name)} {
			if v != "" && !seen[v] {
				seen[v] = true
				alts = append(alts, regexp.QuoteMeta(v))
				ascii = ascii && isWordByte(v[0]) && isWordByte(v[len(v)-1])
			}
		}
	}
	if len(alts) == 0 {
		return nil
	}
	n := "(" + strings.Join(alts, "|") + ")"
	// v0.1's pattern. Its \b knows only ASCII, so a name with another edge
	// (José, Zoë, A.J.) is bounded by anything that is not a letter or digit.
	pre, post := `\b`, `\b`
	if !ascii {
		pre, post = `(?:^|[^\pL\pN_])`, `(?:[^\pL\pN_]|$)`
	}
	return regexp.MustCompile(`(?i:\bawait(s|ing|ed)?\b).*` + pre + n + post + `|` + pre + n + post + `.*(?i:\bawait)|(?i:\bneeds?) ` + n + post + `|` + pre + n + `'?s\b[^;.]{0,30}?\b(ruling|decision|call|tests?|approval|hands-on|sign-off|go-ahead)\b|(?i:\b(waits? )?for) ` + n + post)
}

var trailingAsideRe = regexp.MustCompile(`\s*\([^()]*\)[.!?]?$`)

// noteClauses splits texts on ";" and sentence ends (outside parentheses) and
// keeps the clauses re matches, deduplicated. A nil re matches nothing.
func noteClauses(re *regexp.Regexp, texts ...string) []string {
	if re == nil {
		return nil
	}
	var cs clauseSet
	for _, text := range texts {
		for _, c := range splitClauses(text) {
			c = cleanClause(c)
			if c == "" || !re.MatchString(c) {
				continue
			}
			if short := strings.TrimSpace(trailingAsideRe.ReplaceAllString(c, "")); short != c {
				if re.MatchString(short) {
					c = short
				} else if m := trailingAsideRe.FindString(c); m != "" {
					// The wait is only in the aside. An aside that says what
					// waits ("build-10, build-11 still listed, removal awaits Mark") is
					// the clause; a bare "(waits for Mark)" needs the rest.
					a := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(m), ".!?"))
					a = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(a, "("), ")"))
					if len(strings.Fields(a)) >= 4 {
						c = a
					}
				}
			}
			cs.add(c)
		}
	}
	return cs.out
}

func splitClauses(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	rs := []rune(s)
	for i, r := range rs {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && r == ';' {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
		if depth == 0 && (r == '.' || r == '!' || r == '?') && (i+1 == len(rs) || rs[i+1] == ' ' || rs[i+1] == '\n') {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	out = append(out, cur.String())
	return out
}
