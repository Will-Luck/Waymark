package forge

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// FileSource is how a forge client reads a repo's files for ReadFiles.
type FileSource interface {
	// Raw returns a file's bytes at ref.
	Raw(ctx context.Context, full, path, ref string) ([]byte, error)
	// Dir lists a directory at ref.
	Dir(ctx context.Context, full, path, ref string) ([]Entry, error)
	// IsNotFound reports a missing file or directory.
	IsNotFound(err error) bool
}

// ReadFiles reads CHANGELOG.md and the newest plans in planDirs on the
// default branch; changelogURL is where the forge shows CHANGELOG.md. A
// missing file or directory is skipped; any other failure fails the read, so
// the caller keeps the previous good one.
func ReadFiles(ctx context.Context, src FileSource, repo Repo, planDirs []string, now time.Time, changelogURL string) (*Files, error) {
	f := &Files{FetchedAt: now.UTC()}
	if repo.Empty {
		return f, nil
	}
	full, ref := repo.FullName, repo.DefaultBranch
	b, err := src.Raw(ctx, full, "CHANGELOG.md", ref)
	switch {
	case err == nil:
		cl := ParseChangelog(string(b))
		f.Changelog = &cl
		f.ChangelogURL = changelogURL
	case src.IsNotFound(err):
	default:
		return nil, fmt.Errorf("%s CHANGELOG.md: %w", full, err)
	}

	var entries []Entry
	for _, dir := range planDirs {
		es, err := src.Dir(ctx, full, dir, ref)
		if err != nil {
			if src.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("%s %s: %w", full, dir, err)
		}
		entries = append(entries, es...)
	}
	for _, pc := range PlanCandidates(entries, PlanCandidateMax) {
		if !pc.Date.IsZero() && now.Sub(pc.Date) > PlanMaxAge {
			continue
		}
		b, err := src.Raw(ctx, full, pc.Path, ref)
		if err != nil {
			if src.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("%s %s: %w", full, pc.Path, err)
		}
		title, done, total := ParsePlan(string(b))
		if total == 0 {
			continue
		}
		if title == "" {
			title = PlanTitleFromName(pc.Name)
		}
		f.Plans = append(f.Plans, Plan{Path: pc.Path, Title: title, Date: pc.Date, Done: done, Total: total, URL: pc.URL})
	}
	return f, nil
}

// Changelog is what Waymark reads from a Keep a Changelog file.
type Changelog struct {
	// Unreleased holds the top-level bullets under [Unreleased], in order.
	Unreleased  []string
	LastVersion string
	LastDate    string
}

var (
	versionHeadRe = regexp.MustCompile(`^##\s+\[?v?([0-9][^\]\s]*)\]?(?:\s*[-:]\s*(\d{4}-\d{2}-\d{2}))?`)
	bulletRe      = regexp.MustCompile(`^[-*+]\s+(.*)$`)
	checkboxRe    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+\[([ xX])\]\s`)
	datePrefixRe  = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-`)
)

func isUnreleasedHead(line string) bool {
	l := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(l, "## [unreleased]") || l == "## unreleased" || strings.HasPrefix(l, "## unreleased ")
}

// ParseChangelog reads the Unreleased bullets and the newest released version.
func ParseChangelog(s string) Changelog {
	var cl Changelog
	inUnreleased, seenRelease := false, false
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if strings.HasPrefix(line, "## ") {
			inUnreleased = false
			if isUnreleasedHead(line) {
				inUnreleased = true
				continue
			}
			if m := versionHeadRe.FindStringSubmatch(line); m != nil && !seenRelease {
				cl.LastVersion, cl.LastDate = m[1], m[2]
				seenRelease = true
			}
			continue
		}
		if !inUnreleased {
			continue
		}
		if m := bulletRe.FindStringSubmatch(line); m != nil {
			cl.Unreleased = append(cl.Unreleased, strings.TrimSpace(m[1]))
			continue
		}
		// An indented, non-bullet line continues the previous item.
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && line != trimmed && len(cl.Unreleased) > 0 && bulletRe.FindString(trimmed) == "" {
			cl.Unreleased[len(cl.Unreleased)-1] += " " + trimmed
		}
	}
	return cl
}

// Plan is one plan file's progress.
type Plan struct {
	Path  string
	Title string
	Date  time.Time
	Done  int
	Total int
	URL   string
}

// ParsePlan returns the first H1 and the checkbox counts, ignoring fenced code.
func ParsePlan(s string) (title string, done, total int) {
	inFence := false
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if title == "" && strings.HasPrefix(t, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(t, "# "))
			continue
		}
		if m := checkboxRe.FindStringSubmatch(line); m != nil {
			total++
			if m[1] != " " {
				done++
			}
		}
	}
	return title, done, total
}

// PlanCandidate is a plan file found in a directory listing.
type PlanCandidate struct {
	Path string // file path in the repo
	URL  string // html URL of the file
	Name string
	Date time.Time
}

// PlanCandidates picks date-prefixed plans from listings, newest first, at
// most n. Files must be .md; a date-prefixed directory means <dir>/plan.md.
func PlanCandidates(entries []Entry, n int) []PlanCandidate {
	var out []PlanCandidate
	for _, e := range entries {
		m := datePrefixRe.FindStringSubmatch(e.Name)
		if m == nil {
			continue
		}
		d, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			continue
		}
		switch {
		case e.Type == "file" && strings.HasSuffix(strings.ToLower(e.Name), ".md"):
			out = append(out, PlanCandidate{Path: e.Path, URL: e.HTMLURL, Name: e.Name, Date: d})
		case e.Type == "dir":
			out = append(out, PlanCandidate{Path: e.Path + "/plan.md", URL: e.HTMLURL + "/plan.md", Name: e.Name, Date: d})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.After(out[j].Date)
		}
		return out[i].Name > out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// PlanTitleFromName is the fallback title: the name without date and
// extension, dashes as spaces.
func PlanTitleFromName(name string) string {
	s := datePrefixRe.ReplaceAllString(name, "")
	s = strings.TrimSuffix(s, ".md")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return name
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Filed is the issue an idea was filed as.
type Filed struct {
	Number int
	URL    string
}

// FiledIdeas maps idea ids to the issue whose body carries marker<id> (the
// marker taken literally, such as "waymark-idea:"). When several issues carry
// the same id the oldest wins. An empty marker finds nothing.
func FiledIdeas(issues []Issue, marker string) map[string]Filed {
	out := map[string]Filed{}
	if marker == "" {
		return out
	}
	ideaRe := regexp.MustCompile(regexp.QuoteMeta(marker) + `([A-Za-z0-9_-]+)`)
	created := map[string]time.Time{}
	for _, is := range issues {
		for _, m := range ideaRe.FindAllStringSubmatch(is.Body, -1) {
			id := m[1]
			if prev, ok := created[id]; ok && !is.CreatedAt.Before(prev) {
				continue
			}
			created[id] = is.CreatedAt.Time
			out[id] = Filed{Number: is.Number, URL: is.HTMLURL}
		}
	}
	return out
}
