// Package derive turns collected source data into the model.Site the pages
// show. Everything here is pure: no I/O, no clocks (now is passed in).
package derive

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
)

// Thresholds from the spec, plus the hold-contradiction floor.
const (
	ActiveDays = 21
	QuietDays  = 120
	// HoldContradictFloor is how many commits after a hold's date (after the
	// end of that day in the configured time zone) contradict it. The spec sets no threshold, so
	// any commit does; a lower-bound count ("1+") is at least one.
	HoldContradictFloor = 1
	StalePRDays         = 2
	MaxNoteActions      = 4
	// A plan shows while it is in progress (some boxes ticked, not all) and
	// dated within PlanDays. Shipped plans often have no ticks at all.
	PlanDays = 120
	MaxPlans = 3
)

// PlanInProgress says whether a plan belongs on the project page.
func PlanInProgress(done, total int, date, now time.Time) bool {
	return done > 0 && done < total && !date.IsZero() && now.Sub(date) <= PlanDays*24*time.Hour
}

var deployWordRe = regexp.MustCompile(`(?i)\bdeploy`)
var versionRe = regexp.MustCompile(`\bv?\d+\.\d+\.\d+\b`)

// SameDeploy says whether a note clause is about the deploy a Deploy action
// already names: it mentions deploying and either names latest or no version
// at all. "v1.9.3 release + deploy await Mark" next to a v1.9.2 Deploy action
// is a different decision and stays.
func SameDeploy(clause, latest string) bool {
	if !deployWordRe.MatchString(clause) {
		return false
	}
	vs := versionRe.FindAllString(clause, -1)
	if len(vs) == 0 {
		return true
	}
	for _, v := range vs {
		if SameVersion(v, latest) {
			return true
		}
	}
	return false
}

// Version is a parsed semantic version. Pre is kept for display only.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

// ParseSemver reads v?MAJOR.MINOR.PATCH with optional -pre and +build.
func ParseSemver(s string) (Version, bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Version{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	pat, _ := strconv.Atoi(m[3])
	return Version{Major: maj, Minor: min, Patch: pat, Pre: m[4]}, true
}

// compareCore compares MAJOR.MINOR.PATCH only (the -suffix is ignored).
func compareCore(a, b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Lagging: the latest tag and the deployed version both parse and the tag is
// greater. Unparsable means unknown, never lagging.
func Lagging(latest, deployed string) bool {
	l, ok1 := ParseSemver(latest)
	d, ok2 := ParseSemver(deployed)
	return ok1 && ok2 && compareCore(l, d) > 0
}

// SameVersion compares two version strings ignoring a leading v.
func SameVersion(a, b string) bool {
	a, b = strings.TrimPrefix(strings.TrimSpace(a), "v"), strings.TrimPrefix(strings.TrimSpace(b), "v")
	return a != "" && a == b
}

// PickLatestTag looks at the newest tag by commit date (ties: the first the
// forge lists). When that tag is semver, the highest semver tag wins (a release
// beats a prerelease with the same core), so a late backport tag does not
// hide a newer version. When it is not (calendar versions such as
// v2026.10.07.1), the newest tag wins.
func PickLatestTag(tags []forge.Tag) *forge.Tag {
	if len(tags) == 0 {
		return nil
	}
	newest := 0
	for i := range tags {
		if tags[i].Commit.Created.After(tags[newest].Commit.Created.Time) {
			newest = i
		}
	}
	if _, ok := ParseSemver(tags[newest].Name); !ok {
		return &tags[newest]
	}
	var best *forge.Tag
	var bestV Version
	for i := range tags {
		v, ok := ParseSemver(tags[i].Name)
		if !ok {
			continue
		}
		if best == nil {
			best, bestV = &tags[i], v
			continue
		}
		c := compareCore(v, bestV)
		if c > 0 || (c == 0 && bestV.Pre != "" && v.Pre == "") {
			best, bestV = &tags[i], v
		}
	}
	return best
}

var bareIDRe = regexp.MustCompile(`^(sha256:)?[0-9a-f]{12,64}$`)

const versionLabel = "org.opencontainers.image.version"

// DeployedVersion reads the image tag (after the last /, then after :).
// latest, no tag or a bare image ID fall back to the OCI version label.
func DeployedVersion(image string, labels map[string]string) string {
	img := strings.TrimSpace(image)
	if i := strings.Index(img, "@"); i >= 0 {
		img = img[:i]
	}
	tag := ""
	if !bareIDRe.MatchString(img) {
		last := img
		if i := strings.LastIndex(img, "/"); i >= 0 {
			last = img[i+1:]
		}
		if _, t, ok := strings.Cut(last, ":"); ok {
			tag = t
		}
	}
	// A tag with no digit (latest, stable, alpine, edge) floats: it names a
	// channel, not a version, so the image's version label says more.
	if strings.ContainsAny(tag, "0123456789") {
		return tag
	}
	if v := strings.TrimSpace(labels[versionLabel]); v != "" && v != "latest" {
		return v
	}
	return ""
}

// CountText renders a count that may be a lower bound ("150+").
func CountText(n int, lowerBound bool) string {
	if lowerBound {
		return strconv.Itoa(n) + "+"
	}
	return strconv.Itoa(n)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// DaysAgo is a relative date in loc's calendar days: "today", "yesterday",
// "5 days ago", "3 months ago". It reads the same all day, so text built
// from it moves a project's hash at midnight, not on every poll the way a
// minute count would. The page ages exact times itself (from Action.At,
// LastActivity and the like).
func DaysAgo(t, now time.Time, loc *time.Location) string {
	days := calendarDays(t, now, loc)
	switch {
	case days < 1:
		return "today"
	case days == 1:
		return "yesterday"
	case days < 60:
		return plural(days, "day", "days") + " ago"
	case days < 730:
		return plural(days/30, "month", "months") + " ago"
	}
	return plural(days/365, "year", "years") + " ago"
}

// calendarDays is how many dates in loc lie between t's and now's.
func calendarDays(t, now time.Time, loc *time.Location) int {
	if loc == nil {
		loc = time.UTC
	}
	y1, m1, d1 := t.In(loc).Date()
	y2, m2, d2 := now.In(loc).Date()
	return int(time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)) / (24 * time.Hour))
}

// For is a span length ("40 days", "8 months").
func For(t, now time.Time) string {
	days := int(now.Sub(t) / (24 * time.Hour))
	switch {
	case days < 1:
		return "less than a day"
	case days < 60:
		return plural(days, "day", "days")
	case days < 730:
		return plural(days/30, "month", "months")
	}
	return plural(days/365, "year", "years")
}

// ShortDate is "7 Aug", with the year when it is not the current year.
func ShortDate(t, now time.Time, loc *time.Location) string {
	lt := t.In(loc)
	if lt.Year() != now.In(loc).Year() {
		return lt.Format("2 Jan 2006")
	}
	return lt.Format("2 Jan")
}

// endOfDay is the end, in loc, of the calendar date a date-only stamp names
// (it was parsed as midnight UTC of that date): "after 3 Oct" means from
// midnight in the configured time zone at the end of 3 Oct.
func endOfDay(date time.Time, loc *time.Location) time.Time {
	y, m, d := date.UTC().Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

// WeekStart is Monday 00:00 of t's week in loc.
func WeekStart(t time.Time, loc *time.Location) time.Time {
	lt := t.In(loc)
	back := (int(lt.Weekday()) + 6) % 7
	return time.Date(lt.Year(), lt.Month(), lt.Day()-back, 0, 0, 0, 0, loc)
}

// Activity is 12 weekly counts (Monday weeks in loc), oldest first, the
// current week last.
func Activity(times []time.Time, now time.Time, loc *time.Location) []int {
	out := make([]int, 12)
	cur := WeekStart(now, loc)
	for _, t := range times {
		if t.After(now) {
			continue
		}
		ws := WeekStart(t, loc)
		// Calendar-day difference, safe across clock changes.
		days := int(cur.Sub(ws).Hours()+12) / 24
		idx := 11 - days/7
		if idx >= 0 && idx <= 11 {
			out[idx]++
		}
	}
	return out
}

// HeatDays is how many days the heatmap covers (53 weeks).
const HeatDays = 371

// Heatmap buckets the forges' heatmap points into days in loc ending today.
func Heatmap(pts []forge.HeatPoint, now time.Time, loc *time.Location) model.Heatmap {
	ln := now.In(loc)
	today := time.Date(ln.Year(), ln.Month(), ln.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -(HeatDays - 1))
	h := model.Heatmap{Days: make([]model.HeatDay, HeatDays)}
	index := map[string]int{}
	for i := 0; i < HeatDays; i++ {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		h.Days[i] = model.HeatDay{Date: key, Weekday: (int(d.Weekday()) + 6) % 7}
		index[key] = i
	}
	for _, p := range pts {
		key := time.Unix(p.Timestamp, 0).In(loc).Format("2006-01-02")
		if i, ok := index[key]; ok {
			h.Days[i].Count += p.Contributions
		}
	}
	for _, d := range h.Days {
		h.Total += d.Count
		if d.Count > h.Max {
			h.Max = d.Count
		}
	}
	return h
}

// TierInput is what the tier rule looks at.
type TierInput struct {
	Retired      bool
	RepoArchived bool
	Hold         *registry.Hold
	LastCommit   time.Time // zero: no commits known
	Running      bool      // the primary container is running
	ProbeUp      bool      // any probe is up
	// CommitsUnknown: the primary's forge has not listed its repos yet, so a
	// zero LastCommit means unknown rather than "no commits".
	CommitsUnknown bool
	// FromNotes: the project has no repo read in full, so LastCommit is when
	// its newest note was written.
	FromNotes bool
	Loc       *time.Location
	// Forge names the primary's forge ("Gitea", "GitHub") in the reason.
	Forge string
}

// notReadYet is the reason before the primary's forge has listed its repos.
func notReadYet(forgeName string) string { return forgeName + " not read yet" }

// ForgeName is how the page names a forge: GitHub for kind github; for kind
// gitea or forgejo, Codeberg when its URL is on codeberg.org, else Forgejo
// or Gitea as configured.
func ForgeName(kind, rawURL string) string {
	if kind == "github" {
		return "GitHub"
	}
	if u, err := url.Parse(rawURL); err == nil && strings.EqualFold(u.Hostname(), "codeberg.org") {
		return "Codeberg"
	}
	if kind == "forgejo" {
		return "Forgejo"
	}
	return "Gitea"
}

// Tier applies the spec's first-match rules and says why in one sentence.
func Tier(in TierInput, now time.Time) (model.Tier, string) {
	loc := in.Loc
	if loc == nil {
		loc = time.UTC
	}
	switch {
	case in.Retired:
		return model.TierArchived, "Retired in the registry"
	case in.RepoArchived:
		return model.TierArchived, "Archived on " + in.Forge
	case in.Hold != nil:
		r := holdWord(in.Hold.Kind) + " since " + ShortDate(in.Hold.Since, now, loc)
		if in.Hold.Reason != "" {
			r += ": " + in.Hold.Reason
		}
		return model.TierHold, r
	}
	age := now.Sub(in.LastCommit)
	has := !in.LastCommit.IsZero()
	quietFor, none := "no commits yet", "No commits yet"
	what, recent := "commits", "Commits in the last 3 weeks"
	if in.FromNotes {
		quietFor, none = "no notes yet", "No repo and no notes yet"
		what, recent = "note updates", "Notes updated in the last 3 weeks"
	}
	if in.CommitsUnknown && !has {
		quietFor = notReadYet(in.Forge)
		none = quietFor
	}
	if has {
		quietFor = "no " + what + " for " + For(in.LastCommit, now)
	}
	switch {
	case has && age <= ActiveDays*24*time.Hour:
		return model.TierActive, recent
	case in.Running && in.ProbeUp:
		return model.TierLive, "Deployed and answering, " + quietFor
	case in.Running:
		return model.TierLive, "Deployed and running, " + quietFor
	case in.ProbeUp:
		return model.TierLive, "Answering, " + quietFor
	case has && age <= QuietDays*24*time.Hour:
		return model.TierQuiet, "No " + what + " for " + For(in.LastCommit, now)
	case has:
		return model.TierDormant, "No " + what + " for " + For(in.LastCommit, now)
	}
	return model.TierDormant, none
}

func holdWord(kind string) string {
	if kind == "frozen" {
		return "Frozen"
	}
	return "Parked"
}

// repoWindow is one repo's fetched commits, newest first. start is where the
// fetched history is complete from: the since date, or the oldest commit read
// when the page cap cut the window short (the cap drops the oldest commits).
type repoWindow struct {
	times []time.Time
	shas  []string
	start time.Time
	last  time.Time
}

func newRepoWindow(d *forge.Detail) repoWindow {
	w := repoWindow{start: d.Since}
	for _, c := range d.Commits {
		w.times = append(w.times, c.When())
		w.shas = append(w.shas, c.SHA)
	}
	if d.CommitsCapped && len(w.times) > 0 {
		oldest := w.times[0]
		for _, t := range w.times {
			if t.Before(oldest) {
				oldest = t
			}
		}
		w.start = oldest
	}
	if d.LastCommit != nil {
		w.last = d.LastCommit.When()
	}
	return w
}

// countAfter counts commits after t; lowerBound is true when t is before the
// complete part of the window, so older commits may be missing.
func (w repoWindow) countAfter(t time.Time) (n int, lowerBound bool) {
	for _, c := range w.times {
		if c.After(t) {
			n++
		}
	}
	if t.Before(w.start) {
		lowerBound = true
		if n == 0 && w.last.After(t) {
			// The newest commit is after t but outside the window.
			n = 1
		}
	}
	return n, lowerBound
}

// sinceSHA is how many commits are newer than sha on the branch; ok is false
// when sha is not in the window.
func (w repoWindow) sinceSHA(sha string) (int, bool) {
	if sha == "" {
		return 0, false
	}
	for i, s := range w.shas {
		if s == sha {
			return i, true
		}
	}
	return 0, false
}

// commitWindow is every repo of a project.
type commitWindow []repoWindow

func (cw commitWindow) countAfter(t time.Time) (n int, lowerBound bool) {
	for _, w := range cw {
		c, lb := w.countAfter(t)
		n += c
		lowerBound = lowerBound || lb
	}
	return n, lowerBound
}

func fmtCommits(n int, lower bool) string {
	if n == 1 && !lower {
		return "1 commit"
	}
	return CountText(n, lower) + " commits"
}
