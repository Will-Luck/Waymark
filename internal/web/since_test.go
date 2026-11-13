package web

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

// sinceSite is a site whose only content is evs (sorted newest first, as
// Site.Events is), with one project for each slug they name.
func sinceSite(evs []model.Event) *model.Site {
	sort.SliceStable(evs, func(a, b int) bool { return evs[a].At.After(evs[b].At) })
	site := &model.Site{GeneratedAt: fixedNow, Events: evs, Meta: model.Meta{Title: "Acme", Notes: true}}
	seen := map[string]bool{}
	for _, e := range evs {
		if !seen[e.Project] {
			seen[e.Project] = true
			site.Projects = append(site.Projects, model.Project{Slug: e.Project, Name: e.ProjectName, Category: "tools", Icon: "box", Tier: model.TierActive, Hash: "h-" + e.Project})
		}
	}
	return site
}

// sev is one event of project slug (named after the slug, first letter
// upper case), ago before fixedNow.
func sev(slug, kind, text string, ago time.Duration) model.Event {
	at := fixedNow.Add(-ago)
	return model.Event{
		ID: slug + ":" + kind + ":" + strconv.FormatInt(at.UnixMilli(), 10) + ":" + text, At: at,
		Project: slug, ProjectName: strings.ToUpper(slug[:1]) + slug[1:], Icon: "box", Kind: kind, Level: model.LevelInfo, Text: text,
	}
}

// sinceBlock is the overview's "Since you last looked" section.
func sinceBlock(t *testing.T, h http.Handler) string {
	t.Helper()
	body := get(t, h, "/").Body.String()
	start := strings.Index(body, `<section class="block since"`)
	if start < 0 {
		t.Fatal("the overview has no since section")
	}
	end := strings.Index(body[start:], "</section>")
	if end < 0 {
		t.Fatal("the since section does not end")
	}
	return body[start : start+end]
}

// firstSight is what a reader sees of s before opening anything: every
// <details> element taken out, innermost first.
func firstSight(s string) string {
	for {
		end := strings.Index(s, "</details>")
		if end < 0 {
			return s
		}
		start := strings.LastIndex(s[:end], "<details")
		if start < 0 {
			return s
		}
		s = s[:start] + s[end+len("</details>"):]
	}
}

var (
	sinceNameRe = regexp.MustCompile(`<b>([^<]*)</b></a>`)
	sinceRowRe  = regexp.MustCompile(`(?s)<li class="ev [^"]*"([^>]*)>(.*?)<span class="ev-when">`)
	sinceMoreRe = regexp.MustCompile(`(?s)<details class="more since-more" data-since-more( hidden)?>.*?<span data-since-more-word>([^<]*)</span>`)
)

// sinceMore is the "N more" control of project name's group in s: its
// words, and whether it is hidden. ok is false when the group has none.
func sinceMore(s, name string) (word string, hidden, ok bool) {
	for _, part := range strings.Split(s, `<li class="since-group">`)[1:] {
		if m := sinceNameRe.FindStringSubmatch(part); m == nil || html.UnescapeString(m[1]) != name {
			continue
		}
		m := sinceMoreRe.FindStringSubmatch(part)
		if m == nil {
			return "", false, false
		}
		return m[2], m[1] != "", true
	}
	return "", false, false
}

// sinceRows lists the words of each project's rows in s, by project name,
// leaving out the rows hidden as older than 7 days (data-old).
func sinceRows(s string) map[string][]string {
	out := map[string][]string{}
	for _, part := range strings.Split(s, `<li class="since-group">`)[1:] {
		m := sinceNameRe.FindStringSubmatch(part)
		if m == nil {
			continue
		}
		name := html.UnescapeString(m[1])
		rows := []string{}
		for _, r := range sinceRowRe.FindAllStringSubmatch(part, -1) {
			if strings.Contains(r[1], "data-old") {
				continue
			}
			text := strings.TrimSpace(tagRe.ReplaceAllString(r[2], ""))
			_, words, _ := strings.Cut(text, ": ") // after the level word read to screen readers
			rows = append(rows, html.UnescapeString(strings.TrimSpace(words)))
		}
		out[name] = rows
	}
	return out
}

// The first render (a first visit, and the no-JS view): at most 3 rows per
// project and 12 in all, every project with a change getting its first row
// before any project gets a second; each project's other rows under its own
// "N more", and the other projects under "N more projects". Opening them
// (no JS needed) shows every row.
func TestSinceFirstRenderIsShortAndFair(t *testing.T) {
	cases := []struct {
		name     string
		changes  []int // changes per project; the first project's are the newest
		wantRows []int // rows each project shows on the first render
		wantMore []int // rows under each project's "N more"; 0: the control is hidden
		wantRest string
	}{
		{"one busy project", []int{10, 1, 1, 1, 1}, []int{3, 1, 1, 1, 1}, []int{7, 0, 0, 0, 0}, ""},
		{"six busy projects", []int{5, 5, 5, 5, 5, 5}, []int{2, 2, 2, 2, 2, 2}, []int{3, 3, 3, 3, 3, 3}, ""},
		{"thirteen projects", []int{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}, []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0}, []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0}, "1 more project"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var evs []model.Event
			for p, n := range c.changes {
				for j := 0; j < n; j++ {
					// CI runs never fold: each change is one row.
					evs = append(evs, sev(fmt.Sprintf("p%02d", p), "ci", fmt.Sprintf("CI passed on main: change %d", j), time.Duration(p*100+j+1)*time.Minute))
				}
			}
			block := sinceBlock(t, serve(sinceSite(evs)))
			seen := sinceRows(firstSight(block))
			opened := sinceRows(block)
			total := 0
			for p, want := range c.wantRows {
				name := fmt.Sprintf("P%02d", p)
				if got := len(seen[name]); got != want {
					t.Errorf("%s shows %d rows on the first render, want %d", name, got, want)
				}
				total += len(seen[name])
				if !strings.Contains(block, "<b>"+name+"</b>") {
					t.Errorf("%s is not in the block at all", name)
				}
				if got := len(opened[name]); got != c.changes[p] {
					t.Errorf("%s has %d rows with its controls open, want all %d", name, got, c.changes[p])
				}
				word, hidden, ok := sinceMore(block, name)
				switch {
				case !ok:
					t.Errorf("%s has no \"N more\" control", name)
				case c.wantMore[p] > 0 && (hidden || word != fmt.Sprintf("%d more", c.wantMore[p])):
					t.Errorf("%s's control says %q (hidden %v), want %q shown", name, word, hidden, fmt.Sprintf("%d more", c.wantMore[p]))
				case c.wantMore[p] == 0 && !hidden:
					t.Errorf("%s's control %q shows with nothing under it", name, word)
				}
			}
			if total > 12 {
				t.Errorf("%d rows on the first render, want at most 12", total)
			}
			if c.wantRest != "" && !strings.Contains(block, "<span data-since-rest-word>"+c.wantRest+"</span>") {
				t.Errorf("no %q control for the projects past the cap", c.wantRest)
			}
		})
	}
}

// One chatty project folds into one row and leaves room for the others.
func TestSinceFoldsARunIntoOneRow(t *testing.T) {
	var evs []model.Event
	for j := 0; j < 20; j++ {
		evs = append(evs, sev("busy", "commits", "1 new commit", time.Duration(j+1)*time.Hour))
	}
	evs = append(evs,
		sev("ship", "release", "Ship v1.2.0 tagged", 30*time.Hour),
		sev("docs", "note", "Note docs.md was updated", 40*time.Hour),
		sev("site", "probe", "Site is back up", 50*time.Hour),
	)
	seen := sinceRows(firstSight(sinceBlock(t, serve(sinceSite(evs)))))
	shown := 0
	for _, rows := range seen {
		if len(rows) > 0 {
			shown++
		}
	}
	if shown != 4 {
		t.Errorf("%d projects shown, want 4: %q", shown, seen)
	}
	if got := seen["Busy"]; len(got) != 1 || got[0] != "20 new commits" {
		t.Errorf("Busy shows %q, want one row \"20 new commits\"", got)
	}
	for name, want := range map[string]string{"Ship": "Ship v1.2.0 tagged", "Docs": "Note docs.md was updated", "Site": "Site is back up"} {
		if got := seen[name]; len(got) != 1 || got[0] != want {
			t.Errorf("%s shows %q, want one row %q", name, got, want)
		}
	}
}

// A folded row counts only the events in its window, the last 7 days on the
// first render, and carries every member, newest first, so since.js can
// count again from the visitor's own last look. (Review Focus 3)
func TestSinceFoldCountsOnlyTheWindow(t *testing.T) {
	day := 24 * time.Hour
	evs := []model.Event{
		sev("app", "commits", "1 new commit", time.Hour),
		sev("app", "commits", "3 new commits", 2*day),
		sev("app", "commits", "1 new commit", 6*day),
		sev("app", "commits", "2 new commits", 8*day),
		sev("app", "commits", "1 new commit", 10*day),
	}
	block := sinceBlock(t, serve(sinceSite(evs)))
	if got := sinceRows(block)["App"]; len(got) != 1 || got[0] != "5 new commits" {
		t.Errorf("App shows %q, want one row \"5 new commits\" (1 + 3 + 1 from the last 7 days, not the 3 older ones)", got)
	}
	ms := func(d time.Duration) string { return strconv.FormatInt(fixedNow.Add(-d).UnixMilli(), 10) }
	want := `data-ats="` + strings.Join([]string{ms(time.Hour), ms(2 * day), ms(6 * day), ms(8 * day), ms(10 * day)}, " ") +
		`" data-ws="1 3 1 2 1" data-single="1 new commit" data-fold="{n} new commits"`
	if !strings.Contains(block, want) {
		t.Errorf("the row lacks its members for since.js; want %s", want)
	}
}

// What a folded row says, and the kinds that never fold.
func TestSinceFoldWords(t *testing.T) {
	m := time.Minute
	cases := []struct {
		name string
		evs  []model.Event
		want []string // the project's rows, newest first
	}{
		{"a probe that came back", []model.Event{
			sev("app", "probe", "Live is back up", 1*m), sev("app", "probe", "Live is down", 3*m),
			sev("app", "probe", "Live is back up", 5*m), sev("app", "probe", "Live is down", 7*m),
			sev("app", "probe", "Live is back up", 9*m), sev("app", "probe", "Live is down", 11*m),
		}, []string{"Live went down and came back 3 times"}},
		{"a probe that came back once", []model.Event{
			sev("app", "probe", "Live is back up", 1*m), sev("app", "probe", "Live is down", 3*m),
		}, []string{"Live went down and came back once"}},
		{"a probe still down", []model.Event{
			sev("app", "probe", "Live is down", 1*m), sev("app", "probe", "Live is back up", 3*m), sev("app", "probe", "Live is down", 5*m),
		}, []string{"Live went down 2 times, still down"}},
		{"a container that came back", []model.Event{
			sev("app", "container", "Container beacon is running again", 1*m), sev("app", "container", "Container beacon stopped", 3*m),
			sev("app", "container", "Container beacon is running again", 5*m), sev("app", "container", "Container beacon stopped", 7*m),
		}, []string{"Container beacon stopped and came back 2 times"}},
		{"a container now missing", []model.Event{
			sev("app", "container", "Container beacon is missing", 1*m), sev("app", "container", "Container beacon is running again", 3*m),
			sev("app", "container", "Container beacon stopped", 5*m),
		}, []string{"Container beacon stopped 2 times, now missing"}},
		// A stop then a removal is one outage: derive writes "stopped" and
		// then "is missing" for it.
		{"a container stopped, removed and back", []model.Event{
			sev("app", "container", "Container beacon is running again", 1*m), sev("app", "container", "Container beacon is missing", 3*m),
			sev("app", "container", "Container beacon stopped", 5*m),
		}, []string{"Container beacon stopped and came back once"}},
		{"a container stopped and removed", []model.Event{
			sev("app", "container", "Container beacon is missing", 1*m), sev("app", "container", "Container beacon stopped", 3*m),
		}, []string{"Container beacon stopped once, now missing"}},
		{"one note updated again and again", []model.Event{
			sev("app", "note", "Note app.md was updated", 1*m), sev("app", "note", "Note app.md was updated", 3*m),
			sev("app", "note", "Note app.md was updated", 5*m),
		}, []string{"Note app.md was updated 3 times"}},
		{"a reboot wave folds per probe", []model.Event{
			sev("app", "probe", "LAN is back up", 1*m), sev("app", "probe", "Public is back up", 1*m),
			sev("app", "probe", "LAN is down", 3*m), sev("app", "probe", "Public is down", 3*m),
		}, []string{"LAN went down and came back once", "Public went down and came back once"}},
		{"releases never fold", []model.Event{
			sev("app", "release", "App v2.0.0 tagged", 1*m), sev("app", "release", "App v1.0.0 tagged", 3*m),
		}, []string{"App v2.0.0 tagged", "App v1.0.0 tagged"}},
		{"state changes never fold", []model.Event{
			sev("app", "tier", "Now Live (was Quiet)", 1*m), sev("app", "tier", "Now Quiet (was Live)", 3*m),
		}, []string{"Now Live (was Quiet)", "Now Quiet (was Live)"}},
		{"a release breaks a run of commits", []model.Event{
			sev("app", "commits", "1 new commit", 1*m), sev("app", "release", "App v2.0.0 tagged", 3*m), sev("app", "commits", "1 new commit", 5*m),
		}, []string{"1 new commit", "App v2.0.0 tagged", "1 new commit"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sinceRows(sinceBlock(t, serve(sinceSite(c.evs))))["App"]
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("rows %q, want %q", got, c.want)
			}
		})
	}
}

// Words from outside (a probe's label) are escaped in the row and in the
// words since.js writes back with textContent, and the fold's tokens in
// them stay as written: the label sits in its own attribute, never in the
// words the tokens are filled into. (Review Focus 5)
func TestSinceFoldedWordsAreEscaped(t *testing.T) {
	label := `A<b>&"x {n} {times} {subj}`
	evs := []model.Event{
		sev("app", "probe", label+" is back up", time.Minute),
		sev("app", "probe", label+" is down", 3*time.Minute),
		sev("app", "probe", label+" is back up", 5*time.Minute),
		sev("app", "probe", label+" is down", 7*time.Minute),
	}
	block := sinceBlock(t, serve(sinceSite(evs)))
	if strings.Contains(block, "A<b>") {
		t.Error("a label's markup reached the page unescaped")
	}
	if got := sinceRows(block)["App"]; len(got) != 1 || got[0] != label+" went down and came back 2 times" {
		t.Errorf("App shows %q, want one folded row", got)
	}
	for attr, want := range map[string]string{
		"data-single": label + " is back up",
		"data-subj":   label,
		"data-fold":   "{subj} went down and came back {times}",
	} {
		m := regexp.MustCompile(attr + `="([^"]*)"`).FindStringSubmatch(block)
		if m == nil {
			t.Errorf("the folded row has no %s", attr)
			continue
		}
		if v := html.UnescapeString(m[1]); v != want {
			t.Errorf("%s=%q, want %q (the label escaped once, and only in data-single and data-subj)", attr, v, want)
		}
	}
}

// A project whose changes are all older than 7 days is in the page, hidden,
// for a visitor whose last look is older (since.js shows it); the first
// render neither shows nor counts it. (Review Focus 4)
func TestSinceOlderChangesWaitForSinceJS(t *testing.T) {
	day := 24 * time.Hour
	evs := []model.Event{
		sev("new", "release", "New v1.0.0 tagged", time.Hour),
		sev("old", "release", "Old v2.0.0 tagged", 8*day),
		sev("old", "release", "Old v1.0.0 tagged", 9*day),
	}
	block := sinceBlock(t, serve(sinceSite(evs)))
	seen := sinceRows(firstSight(block))
	if len(seen["Old"]) != 0 || len(seen["New"]) != 1 {
		t.Errorf("first render shows %q, want New's row only", seen)
	}
	if !strings.Contains(block, "<b>1 change</b> in the last 7 days.") {
		t.Error("the summary does not count only the last 7 days")
	}
	if n := strings.Count(block, " data-old>"); n != 2 {
		t.Errorf("%d hidden older rows in the page, want 2", n)
	}
	if !strings.Contains(block, "data-since-rest hidden>") {
		t.Error("the more-projects control shows with no project behind it")
	}
}
