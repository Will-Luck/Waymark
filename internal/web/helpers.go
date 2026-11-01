package web

import (
	"fmt"
	"html/template"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/waymark/internal/mdlite"
	"github.com/Will-Luck/waymark/internal/model"
)

const fallbackIcon = "circle-dashed"

var (
	identRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	hexRe    = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)
	anchorRe = regexp.MustCompile(`</?a\b[^>]*>`)
)

// mdText is md for text inside a link-wrapped card: bold and code stay,
// links become their text, because an <a> inside an <a> makes the parser
// close the card early. mdlite escapes every "<" in the input, so the only
// <a> tags in its output are the ones it wrote.
func mdText(s string) template.HTML {
	return template.HTML(anchorRe.ReplaceAllString(string(mdlite.Render(s)), ""))
}

var zones sync.Map // IANA name -> *time.Location

// zone is the location the page writes its times in: the site's configured
// time zone, UTC when it is unset or unknown.
func zone(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, ok := zones.Load(name); ok {
		return loc.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.UTC
	}
	zones.Store(name, loc)
	return loc
}

// loc is the current snapshot's time zone.
func (s *server) loc() *time.Location { return zone(s.src.Snapshot().Meta.Timezone) }

func (s *server) funcs() template.FuncMap {
	return template.FuncMap{
		"icon":        s.icon,
		"ago":         s.agoHTML,
		"agoText":     func(t time.Time) string { return agoText(t, s.now()) },
		"date":        func(t time.Time) string { return fmtDate(t, s.loc()) },
		"iso":         func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"unixMilli":   func(t time.Time) int64 { return t.UnixMilli() },
		"md":          mdlite.Render,
		"mdText":      mdText,
		"tierLabel":   tierLabel,
		"tierIcon":    tierIcon,
		"tierClass":   tierClass,
		"levelIcon":   levelIcon,
		"levelWord":   levelWord,
		"loud":        func(l model.Level) bool { return l == model.LevelWarn || l == model.LevelAlert },
		"sparkline":   sparkline,
		"uptimeCells": func(h []model.ProbePoint) []uptimeCell { return uptimeCells(h, s.loc()) },
		"pct":         pct,
		"pctf":        func(f float64) int { return clampPct(int(math.Round(f))) },
		"ident":       ident,
		"labelColor":  labelColor,
		"ciIcon":      ciIcon,
		"ciWord":      ciWord,
		"stateClass":  stateClass,
		"kindIcon":    ideaKindIcon,
		"kindLabel":   ideaKindLabel,
		"sizeLabel":   sizeLabel,
		"relWord":     relWord,
		"linkIcon":    linkIcon,
		"eventIcon":   eventIcon,
		"signals":     func(sig []model.Signal, n int) []model.Signal { return sig[:min(n, len(sig))] },
		"moreThan":    func(n, max int) int { return max0(n - max) },
		"plural":      plural,
		"num":         func(n int) string { return thousands(n) },
		"dict":        dict,
		"contains":    strings.Contains,
		"stateWord":   stateWord,
		"tiers":       func() []model.Tier { return model.TierOrder },
		"iconName": func(n string) string {
			if s.icons[n] {
				return n
			}
			return fallbackIcon
		},
	}
}

// siteTitle is the page's title (config site.title), Waymark when a site
// does not say.
func siteTitle(site *model.Site) string {
	if site.Meta.Title != "" {
		return site.Meta.Title
	}
	return "Waymark"
}

// notesLabel is the notes panel's title, from the notes adapter.
func notesLabel(m model.Meta) string {
	if m.NotesLabel != "" {
		return m.NotesLabel
	}
	return "Notes"
}

// sourceShown reports whether the page mentions a source status or stamp:
// not an optional source the site does not have.
func sourceShown(m model.Meta, name string) bool {
	switch name {
	case "notes":
		return m.Notes
	case "docker":
		return m.Docker
	case "ideas":
		return m.Ideas
	case "fdroid":
		return m.FDroid
	}
	return true
}

// shownSources are the site's source statuses the page mentions.
func shownSources(site *model.Site) []model.SourceStatus {
	var out []model.SourceStatus
	for _, s := range site.Sources {
		if sourceShown(site.Meta, s.Name) {
			out = append(out, s)
		}
	}
	return out
}

// sourceLabel names a source status or a project's source stamp. A forge's
// is its config id ("gitea", "gh files"), named as Meta.Forges says; notes
// take the adapter's label. Live updates carry the same label (sse.go).
func sourceLabel(name string, m model.Meta) string {
	if id, ok := strings.CutSuffix(name, " files"); ok && id != "" {
		return sourceLabel(id, m) + " files"
	}
	if n := m.Forges[name]; n != "" {
		return n
	}
	switch name {
	case "gitea":
		return "Gitea"
	case "docker":
		return "Docker"
	case "probes":
		return "Probes"
	case "notes":
		return notesLabel(m)
	case "fdroid":
		return "F-Droid"
	case "github":
		return "GitHub"
	case "registry":
		return filesLabel(m)
	case "heatmap":
		return "Heatmap"
	}
	if name == "" {
		return "Unknown"
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

// filesLabel names the registry source's row after the files it reads.
func filesLabel(m model.Meta) string {
	switch {
	case m.ProjectsFile && m.IdeasFile:
		return "Projects and ideas files"
	case m.IdeasFile:
		return "Ideas file"
	}
	return "Projects file"
}

func stateWord(state string) string {
	switch state {
	case "running":
		return "Running"
	case "exited":
		return "Stopped"
	case "missing":
		return "Missing"
	case "published":
		return "Published"
	case "":
		return "Unknown"
	}
	return strings.ToUpper(state[:1]) + state[1:]
}

// icon renders a sprite reference. Unknown names fall back to circle-dashed,
// so a registry typo shows a neutral shape instead of nothing.
func (s *server) icon(name string, class ...string) template.HTML {
	if !s.icons[name] {
		name = fallbackIcon
	}
	c := "i"
	if len(class) > 0 && identRe.MatchString(strings.ReplaceAll(class[0], " ", "-")) {
		c += " " + class[0]
	}
	return template.HTML(`<svg class="` + c + `" aria-hidden="true" focusable="false"><use href="#i-` + name + `"></use></svg>`)
}

func (s *server) agoHTML(t time.Time) template.HTML {
	if t.IsZero() {
		return "never"
	}
	return template.HTML(`<time datetime="` + t.UTC().Format(time.RFC3339) + `" data-ago title="` +
		template.HTMLEscapeString(t.In(s.loc()).Format("2 Jan 2006, 15:04")) + `">` +
		template.HTMLEscapeString(agoText(t, s.now())) + `</time>`)
}

// agoText is the relative time; web/static/js/util.js has the same rules.
func agoText(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < 10*time.Second:
		return "just now"
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + " s ago"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute", "minutes") + " ago"
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour", "hours") + " ago"
	case d < 48*time.Hour:
		return "yesterday"
	}
	days := int(d / (24 * time.Hour))
	switch {
	case days < 60:
		return plural(days, "day", "days") + " ago"
	case days < 730:
		return plural(days/30, "month", "months") + " ago"
	}
	return plural(days/365, "year", "years") + " ago"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return thousands(n) + " " + many
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 10000 && n > -10000 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 && s[i-1] != '-' {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func fmtDate(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format("2 Jan 2006")
}

// tierLabel takes a Tier or a project (whose hold kind names the word).
func tierLabel(v any) string {
	t, h := tierOf(v)
	switch t {
	case model.TierActive:
		return "Active"
	case model.TierLive:
		return "Live"
	case model.TierQuiet:
		return "Quiet"
	case model.TierDormant:
		return "Dormant"
	case model.TierArchived:
		return "Archived"
	case model.TierHold:
		if h != nil && h.Kind == "frozen" {
			return "Frozen"
		}
		if h != nil && h.Kind == "parked" {
			return "Parked"
		}
		return "On hold"
	}
	return "Unknown"
}

func tierIcon(v any) string {
	t, h := tierOf(v)
	switch t {
	case model.TierActive:
		return "zap"
	case model.TierLive:
		return "radio"
	case model.TierQuiet:
		return "moon"
	case model.TierDormant:
		return "hourglass"
	case model.TierArchived:
		return "archive"
	case model.TierHold:
		if h != nil && h.Kind == "frozen" {
			return "snowflake"
		}
		return "pause"
	}
	return fallbackIcon
}

func tierClass(v any) string {
	t, _ := tierOf(v)
	switch t {
	case model.TierActive, model.TierLive, model.TierQuiet, model.TierDormant, model.TierHold, model.TierArchived:
		return "t-" + string(t)
	}
	return "t-dormant"
}

func tierOf(v any) (model.Tier, *model.Hold) {
	switch x := v.(type) {
	case model.Tier:
		return x, nil
	case string:
		return model.Tier(x), nil
	case *model.Project:
		if x == nil {
			return "", nil
		}
		return x.Tier, x.Hold
	case model.Project:
		return x.Tier, x.Hold
	}
	return "", nil
}

var tierExplain = map[model.Tier]string{
	model.TierActive:   "Commits in the last 3 weeks",
	model.TierLive:     "Running and answering, no recent commits",
	model.TierHold:     "Parked or frozen on purpose",
	model.TierQuiet:    "Some commits in the last 4 months, nothing running",
	model.TierDormant:  "No commits for 4 months or more",
	model.TierArchived: "Retired or archived",
}

func levelIcon(l model.Level) string {
	switch l {
	case model.LevelGood:
		return "check"
	case model.LevelWarn:
		return "triangle-alert"
	case model.LevelAlert:
		return "octagon-alert"
	}
	return "info"
}

func levelWord(l model.Level) string {
	switch l {
	case model.LevelGood:
		return "Good"
	case model.LevelWarn:
		return "Warning"
	case model.LevelAlert:
		return "Problem"
	}
	return "Note"
}

// sparkline draws 12 weekly counts as a polyline over a soft area. The SVG is
// built from numbers only, so it is safe to return as HTML.
func sparkline(vals []int) template.HTML {
	const w, h, pad = 120.0, 30.0, 2.0
	if len(vals) == 0 {
		return ""
	}
	maxV := 0
	total := 0
	for _, v := range vals {
		maxV = max(maxV, v)
		total += v
	}
	step := w / float64(max(len(vals)-1, 1))
	pts := make([]string, len(vals))
	for i, v := range vals {
		y := h - pad
		if maxV > 0 {
			y = h - pad - (h-2*pad)*float64(v)/float64(maxV)
		}
		pts[i] = fmt.Sprintf("%.1f,%.1f", float64(i)*step, y)
	}
	line := strings.Join(pts, " ")
	cls := "spark"
	if maxV == 0 {
		cls += " flat"
	}
	label := plural(total, "commit", "commits") + " in 12 weeks"
	return template.HTML(fmt.Sprintf(
		`<svg class="%s" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" role="img" aria-label="%s"><path class="spark-area" d="M0,%.0f L%s L%.0f,%.0f Z"></path><polyline class="spark-line" points="%s" vector-effect="non-scaling-stroke"></polyline></svg>`,
		cls, w, h, label, h, line, w, h, line))
}

type uptimeCell struct {
	Class string
	Tip   string
}

// uptimeCells lays the probe history into 144 ten-minute cells ending at the
// newest point, their tips in loc; slots with no sample are "none".
func uptimeCells(hist []model.ProbePoint, loc *time.Location) []uptimeCell {
	const n = 144
	cells := make([]uptimeCell, n)
	if len(hist) == 0 {
		for i := range cells {
			cells[i] = uptimeCell{Class: "none", Tip: "No data"}
		}
		return cells
	}
	end := hist[len(hist)-1].At
	for i := range cells {
		at := end.Add(-time.Duration(n-1-i) * 10 * time.Minute)
		cells[i] = uptimeCell{Class: "none", Tip: at.In(loc).Format("2 Jan 15:04") + ", no data"}
	}
	for _, p := range hist {
		slot := n - 1 - int(math.Round(float64(end.Sub(p.At))/float64(10*time.Minute)))
		if slot < 0 || slot >= n {
			continue
		}
		c := uptimeCell{Class: "up", Tip: p.At.In(loc).Format("2 Jan 15:04") + ", up, " + strconv.Itoa(p.LatencyMS) + " ms"}
		if !p.Up {
			c = uptimeCell{Class: "down", Tip: p.At.In(loc).Format("2 Jan 15:04") + ", down"}
		}
		cells[slot] = c
	}
	return cells
}

func pct(a, b int) int {
	if b <= 0 {
		return 0
	}
	return clampPct(int(math.Round(float64(a) * 100 / float64(b))))
}

func clampPct(p int) int { return min(max(p, 0), 100) }

func max0(n int) int { return max(n, 0) }

// ident makes a safe CSS ident / attribute token from a slug or key.
func ident(s string) string {
	s = strings.ToLower(s)
	if identRe.MatchString(s) {
		return s
	}
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	return out
}

func labelColor(c string) string {
	c = strings.TrimPrefix(c, "#")
	if !hexRe.MatchString(c) {
		return "#888888"
	}
	return "#" + strings.ToLower(c)
}

func ciIcon(r model.CIRun) string {
	switch {
	case r.Status == "queued":
		return "clock"
	case r.Status != "completed":
		return "loader-circle"
	case r.Conclusion == "success":
		return "circle-check"
	case r.Conclusion == "failure":
		return "circle-x"
	case r.Conclusion == "cancelled":
		return "circle-slash"
	}
	return "circle-minus"
}

func ciWord(r model.CIRun) string {
	switch {
	case r.Status == "queued":
		return "Queued"
	case r.Status != "completed":
		return "Running"
	case r.Conclusion == "success":
		return "Passed"
	case r.Conclusion == "failure":
		return "Failed"
	case r.Conclusion == "cancelled":
		return "Cancelled"
	case r.Conclusion == "skipped":
		return "Skipped"
	}
	return "Finished"
}

func stateClass(state string) string {
	switch state {
	case "running":
		return "good"
	case "published":
		return "live"
	case "exited", "missing":
		return "alert"
	}
	return "info"
}

var ideaKinds = []struct{ Key, Label, Icon string }{
	{"quick-win", "Quick win", "zap"},
	{"bold", "Bold", "rocket"},
	{"connective", "Connective", "link"},
	{"hardening", "Hardening", "shield"},
}

func ideaKindIcon(k string) string {
	for _, x := range ideaKinds {
		if x.Key == k {
			return x.Icon
		}
	}
	return "lightbulb"
}

func ideaKindLabel(k string) string {
	for _, x := range ideaKinds {
		if x.Key == k {
			return x.Label
		}
	}
	return "Idea"
}

func sizeLabel(s string) string {
	switch s {
	case "S":
		return "Small"
	case "M":
		return "Medium"
	case "L":
		return "Large"
	}
	return "Unsized"
}

func relWord(kind string, outgoing bool) string {
	type pair struct{ out, in string }
	words := map[string]pair{
		"depends-on": {"depends on", "needed by"},
		"client-of":  {"client of", "serves"},
		"fork-of":    {"fork of", "forked as"},
		"deploys":    {"deploys", "deployed by"},
		"sibling":    {"sibling of", "sibling of"},
		"monitors":   {"monitors", "monitored by"},
		"supersedes": {"supersedes", "superseded by"},
	}
	p, ok := words[kind]
	if !ok {
		return strings.ReplaceAll(kind, "-", " ")
	}
	if outgoing {
		return p.out
	}
	return p.in
}

func linkIcon(kind string) string {
	switch kind {
	case "repo":
		return "git-branch"
	case "github", "mirror":
		return "github"
	case "live":
		return "globe"
	case "docs":
		return "file-text"
	}
	return "external-link"
}

func eventIcon(kind string) string {
	switch kind {
	case "release":
		return "tag"
	case "deploy":
		return "package-check"
	case "container":
		return "container"
	case "ci":
		return "workflow"
	case "commits":
		return "git-commit-horizontal"
	case "issues":
		return "circle-dot"
	case "probe":
		return "activity"
	case "note":
		return "notebook-pen"
	case "tier":
		return "layers"
	}
	return "info"
}

// shortName trims a long registry name at its first bracket for tight spots
// (map labels). The full name is always available alongside.
func shortName(n string) string {
	if i := strings.Index(n, " ("); i > 0 {
		n = n[:i]
	}
	r := []rune(n)
	if len(r) > 26 {
		return strings.TrimSpace(string(r[:25])) + "…"
	}
	return n
}

func dict(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		k, _ := kv[i].(string)
		m[k] = kv[i+1]
	}
	return m
}
