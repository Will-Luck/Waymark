package web

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

// "Since you last looked". The server renders 14 days of events, each
// project's runs folded into rows. Its first render (also a first visit and
// the no-JS view) shows the last 7 days: at most sincePerProject rows per
// project and sinceCap in all, shared so that every project with a change
// gets its first row before any project gets a second. web/static/js/since.js
// counts again from the visitor's last look and applies the same caps: keep
// the numbers and the rules in step.
const (
	sinceShown      = 7 * 24 * time.Hour
	sinceKept       = 14 * 24 * time.Hour
	sinceCap        = 12
	sincePerProject = 3
)

// eventItem is one row: one event, or a run of one project's events of one
// kind and one subject folded together. The embedded event is the newest
// member, with Text set to what the row says on the first render.
type eventItem struct {
	model.Event
	// Old: no member is in the last 7 days, so the row stays hidden until
	// since.js shows it to a visitor whose last look is older.
	Old bool
	// Folded rows only: the members' times (unix ms) and weights, newest
	// first and space-separated; the newest member's own words (Single);
	// the folded words (Fold), with {n} for the number, {times} for "once"
	// or "N times" and {subj} for Subj, what the row folds by (a probe's
	// label, a container's or a note's name). Subj comes from outside, so
	// it is kept out of Fold until the tokens are filled.
	Ats, Weights, Single, Fold, Subj string
}

// eventGroup is one project's rows: Top on the first render, the others
// under "N more" (More of them from the last 7 days, then the older ones).
type eventGroup struct {
	Slug, Name, Icon, Cat string
	Top, Extra            []eventItem
	More                  int
}

// sinceGroups folds each project's events of the last 14 days (newest
// first, as Site.Events is) into rows and shares out the first render's
// rows. shown are the groups that got a row; rest are the others, in the
// same order, under "N more projects": restN of them have rows from the
// last 7 days, the others only older ones. recent counts the events of the
// last 7 days.
func sinceGroups(evs []model.Event, ref time.Time, notes bool, cat func(string) string) (shown, rest []eventGroup, restN, recent int) {
	var groups []eventGroup
	var members [][]model.Event
	idx := map[string]int{}
	for _, e := range evs {
		age := ref.Sub(e.At)
		if age > sinceKept || e.Kind == "note" && !notes {
			continue
		}
		if age <= sinceShown {
			recent++
		}
		i, ok := idx[e.Project]
		if !ok {
			i = len(groups)
			idx[e.Project] = i
			groups = append(groups, eventGroup{Slug: e.Project, Name: e.ProjectName, Icon: e.Icon, Cat: cat(e.Project)})
			members = append(members, nil)
		}
		members[i] = append(members[i], e)
	}
	rows := make([][]eventItem, len(groups))
	counts := make([]int, len(groups))
	for i := range groups {
		rows[i] = foldRuns(members[i], ref)
		for _, r := range rows[i] {
			if !r.Old {
				counts[i]++
			}
		}
	}
	picks := pickRows(counts)
	for i, g := range groups {
		n := picks[i]
		if n == 0 {
			n = min(counts[i], sincePerProject)
		}
		g.Top, g.Extra, g.More = rows[i][:n], rows[i][n:], counts[i]-n
		if picks[i] > 0 {
			shown = append(shown, g)
			continue
		}
		rest = append(rest, g)
		if counts[i] > 0 {
			restN++
		}
	}
	return shown, rest, restN, recent
}

// pickRows shares sinceCap rows among the groups, counts[i] being group
// i's rows to show: round by round, every group with another row gets one,
// up to sincePerProject rounds. The result is each group's rows.
func pickRows(counts []int) []int {
	picks := make([]int, len(counts))
	total := 0
	for round := 0; round < sincePerProject; round++ {
		for i, n := range counts {
			if total == sinceCap {
				return picks
			}
			if n > round {
				picks[i]++
				total++
			}
		}
	}
	return picks
}

// foldRuns turns one project's events (newest first) into rows. A run is a
// stretch of consecutive events of one foldable kind (commits, probe,
// container, note); inside it each subject's events make one row, in the
// order the subjects first appear. Every other kind (release, deploy, tier,
// CI, issues) is one row per event.
func foldRuns(evs []model.Event, ref time.Time) []eventItem {
	var out []eventItem
	for i := 0; i < len(evs); {
		j := i + 1
		if _, ok := subject(evs[i]); ok {
			for j < len(evs) && evs[j].Kind == evs[i].Kind {
				if _, ok := subject(evs[j]); !ok {
					break
				}
				j++
			}
		}
		var order []string
		by := map[string][]model.Event{}
		for _, e := range evs[i:j] {
			s, _ := subject(e)
			if _, seen := by[s]; !seen {
				order = append(order, s)
			}
			by[s] = append(by[s], e)
		}
		for _, s := range order {
			out = append(out, foldRow(by[s], ref))
		}
		i = j
	}
	return out
}

// subject is what a run of a foldable kind folds by, read from the words
// derive writes (internal/derive/events.go, Diff). ok is false for a kind
// that never folds, or for words it does not know.
func subject(e model.Event) (string, bool) {
	switch e.Kind {
	case "commits":
		return "", true
	case "probe":
		for _, suf := range []string{" is back up", " is down"} {
			if l, ok := strings.CutSuffix(e.Text, suf); ok {
				return l, true
			}
		}
	case "container":
		if rest, ok := strings.CutPrefix(e.Text, "Container "); ok {
			for _, suf := range []string{" is running again", " stopped", " is missing"} {
				if name, ok := strings.CutSuffix(rest, suf); ok {
					return name, true
				}
			}
		}
	case "note":
		return e.Text, true
	}
	return "", false
}

// weight is what member k of a row (newest first) adds to its number: its
// commits, one update of a note, or one outage. An outage starts where a
// probe goes down or a container stops or goes missing after being up (or
// at the oldest member): a container that stops and is then removed is one
// outage, though derive writes "stopped" and then "is missing" for it.
func weight(ms []model.Event, k int) int {
	e := ms[k]
	switch e.Kind {
	case "commits":
		if f := strings.Fields(e.Text); len(f) > 0 {
			if n, err := strconv.Atoi(f[0]); err == nil && n > 0 {
				return n
			}
		}
		return 1
	case "probe", "container":
		if isUp(e) || k+1 < len(ms) && !isUp(ms[k+1]) {
			return 0
		}
		return 1
	}
	return 1
}

// isUp is true for a probe back up or a container running again.
func isUp(e model.Event) bool {
	return strings.HasSuffix(e.Text, " is back up") || strings.HasSuffix(e.Text, " is running again")
}

// foldWords are a folded row's words, {n}, {times} and {subj} still in,
// named after its newest member.
func foldWords(newest model.Event) string {
	switch newest.Kind {
	case "commits":
		return "{n} new commits"
	case "probe":
		if strings.HasSuffix(newest.Text, " is back up") {
			return "{subj} went down and came back {times}"
		}
		return "{subj} went down {times}, still down"
	case "container":
		switch {
		case strings.HasSuffix(newest.Text, " is running again"):
			return "Container {subj} stopped and came back {times}"
		case strings.HasSuffix(newest.Text, " is missing"):
			return "Container {subj} stopped {times}, now missing"
		}
		return "Container {subj} stopped {times}, still stopped"
	}
	return "{subj} {times}"
}

// fill is a folded row's words for its newest keep members, whose weights
// add up to n: the newest member's own words when it stands alone or the
// weights add up to nothing. The tokens are filled in one pass, so braces
// in Subj stay as written. since.js has the same rule.
func fill(it eventItem, keep, n int) string {
	if keep <= 1 || n == 0 {
		return it.Single
	}
	times := strconv.Itoa(n) + " times"
	if n == 1 {
		times = "once"
	}
	return strings.NewReplacer("{n}", strconv.Itoa(n), "{times}", times, "{subj}", it.Subj).Replace(it.Fold)
}

// foldRow makes one row of one subject's members.
func foldRow(ms []model.Event, ref time.Time) eventItem {
	sort.SliceStable(ms, func(a, b int) bool { return ms[a].At.After(ms[b].At) })
	it := eventItem{Event: ms[0], Old: ref.Sub(ms[0].At) > sinceShown}
	if len(ms) == 1 {
		return it
	}
	subj, _ := subject(ms[0])
	ats := make([]string, len(ms))
	ws := make([]string, len(ms))
	keep, n := 0, 0
	for k, e := range ms {
		w := weight(ms, k)
		ats[k] = strconv.FormatInt(e.At.UnixMilli(), 10)
		ws[k] = strconv.Itoa(w)
		if ref.Sub(e.At) <= sinceShown {
			keep++
			n += w
		}
	}
	it.Ats, it.Weights = strings.Join(ats, " "), strings.Join(ws, " ")
	it.Single, it.Fold, it.Subj = ms[0].Text, foldWords(ms[0]), subj
	if !it.Old {
		it.Text = fill(it, keep, n)
	}
	return it
}
