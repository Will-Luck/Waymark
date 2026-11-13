package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/waymark/internal/mdlite"
	"github.com/Will-Luck/waymark/internal/model"
)

// derived is everything computed once per snapshot. Snapshot returns an
// immutable *Site, so the pointer is the cache key.
type derived struct {
	site    *model.Site
	css     *asset
	cssHash string
	search  []byte
	proj    map[string]*model.Project
	heat    heatView
	graph   mapView
}

func (s *server) derived(site *model.Site) *derived {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.der != nil && s.der.site == site {
		return s.der
	}
	d := &derived{site: site, proj: map[string]*model.Project{}}
	for i := range site.Projects {
		d.proj[site.Projects[i].Slug] = &site.Projects[i]
	}
	css := buildCSS(site)
	sum := sha256.Sum256(css)
	d.cssHash = hex.EncodeToString(sum[:])[:12]
	d.css = newAsset(css, contentTypes[".css"])
	d.search = buildSearch(site)
	d.heat = buildHeat(site.Heatmap)
	d.graph = buildMap(site)
	s.der = d
	return d
}

// buildCSS writes the rules that depend on the registry: category hues for
// icon tiles, the CSS-only board filters, and per-project view-transition
// names. Inline styles are ruled out by the CSP, so this is a stylesheet.
func buildCSS(site *model.Site) []byte {
	var b bytes.Buffer
	b.WriteString("/* generated per snapshot: category hues, board filters, view-transition names */\n")
	for _, c := range site.Categories {
		if !identRe.MatchString(c.Key) {
			continue
		}
		fmt.Fprintf(&b, "[data-cat=%q]{--hue:%d}\n", c.Key, ((c.Hue%360)+360)%360)
	}
	for _, c := range site.Categories {
		if !identRe.MatchString(c.Key) {
			continue
		}
		fmt.Fprintf(&b, "body:has(#cat-%s:checked) .card-wrap:not([data-cat=%q]){display:none}\n", c.Key, c.Key)
		fmt.Fprintf(&b, "body:has(#cat-%s:checked) .tier:not(:has(.card-wrap[data-cat=%q])){display:none}\n", c.Key, c.Key)
		fmt.Fprintf(&b, "body:has(#cat-%s:checked) .tier .count[data-n=%q]{display:inline-block}\n", c.Key, c.Key)
	}
	// Ideas: a kind and size pair with no ideas shows the empty message, so
	// the CSS-only filters say so without JS too.
	pairs := ideaPairs(site.Ideas)
	kinds := []string{"all"}
	for _, k := range ideaKinds {
		kinds = append(kinds, k.Key)
	}
	for _, k := range kinds {
		for _, s := range append([]string{"all"}, ideaSizes...) {
			if (k != "all" || s != "all") && pairs[[2]string{k, s}] == 0 {
				fmt.Fprintf(&b, "body:has(#k-%s:checked):has(#s-%s:checked) .ideas-empty{display:flex}\n", k, s)
			}
		}
	}
	// Arc and bar fill: a registered --p animates from 0 (see app.css).
	for i := 0; i <= 100; i++ {
		fmt.Fprintf(&b, "[data-pct=\"%d\"]{--pv:%d}\n", i, i)
	}
	b.WriteString("@media (prefers-reduced-motion: no-preference){\n")
	for _, p := range site.Projects {
		if !identRe.MatchString(p.Slug) {
			continue
		}
		// Only the card being opened carries names, so a live swap elsewhere
		// on the board does not capture all of them.
		fmt.Fprintf(&b, ".card-wrap[data-slug=%q]:is(:hover,:focus-within,:active) .tile,.phead[data-slug=%q] .tile{view-transition-name:p-%s}\n", p.Slug, p.Slug, p.Slug)
		fmt.Fprintf(&b, ".card-wrap[data-slug=%q]:is(:hover,:focus-within,:active) .card-name,.phead[data-slug=%q] .pname{view-transition-name:t-%s}\n", p.Slug, p.Slug, p.Slug)
	}
	b.WriteString("}\n")
	return b.Bytes()
}

// ---- overview -------------------------------------------------------------

type tile struct {
	Label, Icon, Href, Class, Note string
	Value                          int
}

type tierSection struct {
	Tier                        model.Tier
	Label, Icon, Class, Explain string
	Projects                    []*model.Project
	Open                        bool
	// CatCounts is this tier's card count per category, so the header can
	// show the filtered count (see buildCSS).
	CatCounts []catChip
}

type catChip struct {
	Key, Label, Icon string
	Count            int
}

type needItem struct {
	A   model.Action
	Cat string
}

type overviewView struct {
	Tiles     []tile
	Weeks     template.HTML
	Needs     []needItem
	MoreNeeds []needItem
	Groups    []eventGroup
	Rest      []eventGroup
	RestN     int
	Recent    int
	Cats      []catChip
	Total     int
	Tiers     []tierSection
}

const needsVisible = 8

func (d *derived) overview(now time.Time) overviewView {
	site := d.site
	st := site.Stats
	v := overviewView{Total: len(site.Projects)}
	needs := len(site.NeedsYou)
	v.Tiles = []tile{
		{Label: "Projects", Icon: "layers", Href: "#board", Value: len(site.Projects)},
		{Label: "Active", Icon: "zap", Href: "#tier-active", Value: st.Active, Class: "t-active"},
		{Label: "Live", Icon: "radio", Href: "#tier-live", Value: st.Live, Class: "t-live"},
		{Label: "Needs you", Icon: "hand", Href: "#needs-you", Value: needs, Class: "needs"},
		// No Href: no page lists every open issue, and the board lists none.
		{Label: "Open issues", Icon: "circle-dot", Value: st.OpenIssues, Note: plural(st.OpenPRs, "open PR", "open PRs")},
		{Label: "Shipped in 30 days", Icon: "rocket", Href: "/ships", Value: st.ReleasesLast30d},
	}
	if needs == 0 {
		v.Tiles[3].Href = ""
		v.Tiles[3].Class = "needs calm"
	}
	v.Weeks = weekStrip(d.heat)
	for i, a := range site.NeedsYou {
		it := needItem{A: a, Cat: d.cat(a.Project)}
		if i < needsVisible {
			v.Needs = append(v.Needs, it)
		} else {
			v.MoreNeeds = append(v.MoreNeeds, it)
		}
	}
	ref := site.GeneratedAt
	if ref.IsZero() {
		ref = now
	}
	v.Groups, v.Rest, v.RestN, v.Recent = sinceGroups(site.Events, ref, site.Meta.Notes, d.cat)
	for _, c := range site.Categories {
		if identRe.MatchString(c.Key) {
			v.Cats = append(v.Cats, catChip{Key: c.Key, Label: c.Label, Icon: c.Icon, Count: c.Count})
		}
	}
	by := map[model.Tier][]*model.Project{}
	for i := range site.Projects {
		p := &site.Projects[i]
		by[p.Tier] = append(by[p.Tier], p)
	}
	for _, t := range model.TierOrder {
		label := tierLabel(t)
		v.Tiers = append(v.Tiers, tierSection{
			Tier: t, Label: label, Icon: tierIcon(t), Class: tierClass(t), Explain: tierExplain[t],
			Projects:  by[t],
			Open:      t == model.TierActive || t == model.TierLive || t == model.TierHold,
			CatCounts: catCounts(by[t]),
		})
	}
	return v
}

// catCounts counts projects per category, in first-seen order. Keys that are
// not safe idents are left out, as the filter chips leave them out.
func catCounts(ps []*model.Project) []catChip {
	var out []catChip
	idx := map[string]int{}
	for _, p := range ps {
		if !identRe.MatchString(p.Category) {
			continue
		}
		i, ok := idx[p.Category]
		if !ok {
			i = len(out)
			idx[p.Category] = i
			out = append(out, catChip{Key: p.Category})
		}
		out[i].Count++
	}
	return out
}

func (d *derived) cat(slug string) string {
	if p := d.proj[slug]; p != nil {
		return p.Category
	}
	return ""
}

// weekStrip is the thin 53-week activity strip under the hero tiles.
func weekStrip(h heatView) template.HTML {
	if len(h.Weeks) == 0 {
		return ""
	}
	const bw, gap, ht = 10.0, 3.0, 36.0
	maxW := 0
	total := 0
	for _, w := range h.Weeks {
		maxW = max(maxW, w.Count)
		total += w.Count
	}
	var b strings.Builder
	width := float64(len(h.Weeks))*(bw+gap) - gap
	fmt.Fprintf(&b, `<svg class="weeks" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" role="img" aria-label="%s in the last 53 weeks">`,
		width, ht, plural(total, "commit", "commits"))
	for i, w := range h.Weeks {
		hh := 2.0
		if maxW > 0 && w.Count > 0 {
			hh = math.Max(3, (ht-2)*float64(w.Count)/float64(maxW))
		}
		fmt.Fprintf(&b, `<rect class="wk l%d" x="%.0f" y="%.1f" width="%.0f" height="%.1f" rx="2"><title>%s</title></rect>`,
			w.Level, float64(i)*(bw+gap), ht-hh, bw, hh, template.HTMLEscapeString(w.Tip))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// ---- project ---------------------------------------------------------------

type relItem struct {
	Slug, Name, Word, Icon, Cat string
	Known                       bool
}

type ideaItem struct {
	I       model.Idea
	Cat     string
	RepoURL string
	Forge   string // the project's primary forge, where "File as issue" goes
}

type projectView struct {
	P     *model.Project
	Needs []needItem
	Ideas []ideaItem
	// Issues are the primary's open issues, MirrorIssues the mirror's (on
	// MirrorForge).
	Issues       []model.IssueInfo
	MirrorIssues []model.IssueInfo
	MirrorForge  string
	Related      []relItem
	// Deploy is the project's, unless it is a container and the site has no
	// Docker source; Runs says "Where it runs" has something to show.
	Deploy     *model.DeployInfo
	Runs       bool
	Next       bool
	Notes      []noteItem // none when the site has no notes source
	NotesLabel string
	Sources    []stampRow // less those of unconfigured sources
}

// stampRow is a project's source stamp with its name in the page's words.
type stampRow struct {
	model.SourceStamp
	Label string
}

// noteItem is a note with its body split for the panel: Head is the first
// noteLines lines, More the rest (MoreLines of them) behind a disclosure.
type noteItem struct {
	model.NoteInfo
	Head, More string
	MoreLines  int
}

const noteLines = 40

func (d *derived) project(p *model.Project) projectView {
	meta := d.site.Meta
	v := projectView{P: p, Deploy: p.Deploy, NotesLabel: notesLabel(meta)}
	if p.Deploy != nil && p.Deploy.Source == "docker" && !meta.Docker {
		v.Deploy = nil
	}
	v.Runs = v.Deploy != nil || len(p.Probes) > 0
	v.Next = (p.Unreleased != nil && (p.Unreleased.Count > 0 || len(p.Unreleased.Items) > 0)) || len(p.Plans) > 0
	for _, a := range p.Actions {
		v.Needs = append(v.Needs, needItem{A: a, Cat: p.Category})
	}
	if meta.Ideas {
		for _, i := range p.Ideas {
			v.Ideas = append(v.Ideas, d.idea(i))
		}
	}
	for _, is := range p.Issues {
		if is.Mirror {
			v.MirrorIssues = append(v.MirrorIssues, is)
			v.MirrorForge = is.Forge
		} else {
			v.Issues = append(v.Issues, is)
		}
	}
	for _, st := range p.Sources {
		if sourceShown(meta, st.Name) {
			v.Sources = append(v.Sources, stampRow{SourceStamp: st, Label: sourceLabel(st.Name, meta)})
		}
	}
	if meta.Notes {
		for _, n := range p.Notes {
			it := noteItem{NoteInfo: n, Head: n.Body}
			if lines := strings.Split(n.Body, "\n"); len(lines) > noteLines {
				it.Head = strings.Join(lines[:noteLines], "\n")
				it.More = strings.Join(lines[noteLines:], "\n")
				it.MoreLines = len(lines) - noteLines
			}
			v.Notes = append(v.Notes, it)
		}
	}
	for _, r := range p.Relations {
		it := relItem{Slug: r.To, Name: r.ToName, Word: relWord(r.Kind, r.Outgoing), Icon: fallbackIcon}
		if t := d.proj[r.To]; t != nil {
			it.Known, it.Icon, it.Cat = true, t.Icon, t.Category
			if it.Name == "" {
				it.Name = t.Name
			}
		}
		if it.Name == "" {
			it.Name = r.To
		}
		v.Related = append(v.Related, it)
	}
	return v
}

func (d *derived) idea(i model.Idea) ideaItem {
	it := ideaItem{I: i, Cat: d.cat(i.Project)}
	if p := d.proj[i.Project]; p != nil {
		it.Forge = p.Forge
		if p.Repo != nil {
			it.RepoURL = p.Repo.HTMLURL
		}
	}
	return it
}

// ---- ideas -----------------------------------------------------------------

type facet struct {
	Key, Label, Icon string
	Count            int
	// By is the count for each value of the other filter (sizes for a kind,
	// kinds for a size), so a chip can show its count under that choice.
	By []facet
}

var ideaSizes = []string{"S", "M", "L"}

// ideaPairs counts ideas per kind and size, with "all" standing for either
// filter left at All / Any size.
func ideaPairs(ideas []model.Idea) map[[2]string]int {
	n := map[[2]string]int{}
	for _, i := range ideas {
		n[[2]string{i.Kind, i.Size}]++
		n[[2]string{i.Kind, "all"}]++
		n[[2]string{"all", i.Size}]++
	}
	return n
}

type ideasView struct {
	Ideas    []ideaItem
	Kinds    []facet
	Sizes    []facet
	Projects []facet
	Total    int
	// Filed counts the file ideas already filed (or already issues); a label
	// idea is an issue itself, so it is in neither count.
	Filed   int
	Roundup string
	// FromFile: an ideas file (the roundup) is configured or gave ideas;
	// FromLabels: ideas came from issues labelled Labels ("idea or
	// enhancement").
	FromFile, FromLabels bool
	Labels               string
}

func (d *derived) ideas() ideasView {
	site := d.site
	v := ideasView{Total: len(site.Ideas), FromFile: site.Meta.IdeasFile || site.RoundupDate != "", Labels: orList(site.Meta.IdeaLabels)}
	if t, err := time.Parse("2006-01-02", site.RoundupDate); err == nil {
		v.Roundup = t.Format("2 Jan 2006")
	} else {
		v.Roundup = site.RoundupDate
	}
	kinds := map[string]int{}
	sizes := map[string]int{}
	projects := map[string]int{}
	var order []string
	for _, i := range site.Ideas {
		v.Ideas = append(v.Ideas, d.idea(i))
		kinds[i.Kind]++
		sizes[i.Size]++
		if i.Source == "issue" {
			v.FromLabels = true
		} else {
			v.FromFile = true
			if i.FiledIssue > 0 || i.ExistingIssue > 0 {
				v.Filed++
			}
		}
		if _, ok := projects[i.Project]; !ok {
			order = append(order, i.Project)
		}
		projects[i.Project]++
	}
	pairs := ideaPairs(site.Ideas)
	for _, k := range ideaKinds {
		f := facet{Key: k.Key, Label: k.Label, Icon: k.Icon, Count: kinds[k.Key]}
		for _, s := range ideaSizes {
			f.By = append(f.By, facet{Key: s, Count: pairs[[2]string{k.Key, s}]})
		}
		v.Kinds = append(v.Kinds, f)
	}
	for _, s := range ideaSizes {
		f := facet{Key: s, Label: sizeLabel(s), Count: sizes[s]}
		for _, k := range ideaKinds {
			f.By = append(f.By, facet{Key: k.Key, Count: pairs[[2]string{k.Key, s}]})
		}
		v.Sizes = append(v.Sizes, f)
	}
	for _, slug := range order {
		name := slug
		if p := d.proj[slug]; p != nil {
			name = p.Name
		}
		v.Projects = append(v.Projects, facet{Key: slug, Label: name, Count: projects[slug]})
	}
	sort.SliceStable(v.Projects, func(a, b int) bool {
		return strings.ToLower(v.Projects[a].Label) < strings.ToLower(v.Projects[b].Label)
	})
	return v
}

// orList joins words as "a, b or c".
func orList(words []string) string {
	switch n := len(words); n {
	case 0:
		return ""
	case 1:
		return words[0]
	default:
		return strings.Join(words[:n-1], ", ") + " or " + words[n-1]
	}
}

// ---- ship log --------------------------------------------------------------

type heatCell struct {
	Date  string
	Count int
	Level int
	Tip   string
	Pad   bool
	Today bool
}

type heatWeek struct {
	Count, Level int
	Tip          string
}

type heatView struct {
	Cells   []heatCell // column-major: 53 columns of Monday..Sunday
	Months  []string   // one per column, "" or a month label
	Weeks   []heatWeek
	Total   int
	Active  int
	Busiest heatCell
	BusyDay string
	Streak  int
}

const heatCols = 53

func buildHeat(hm model.Heatmap) heatView {
	var v heatView
	if len(hm.Days) == 0 {
		return v
	}
	counts := map[string]int{}
	for _, d := range hm.Days {
		counts[d.Date] = d.Count
	}
	last, err := time.Parse("2006-01-02", hm.Days[len(hm.Days)-1].Date)
	if err != nil {
		return v
	}
	wd := (int(last.Weekday()) + 6) % 7 // Monday = 0
	start := last.AddDate(0, 0, -((heatCols-1)*7 + wd))
	var nonzero []int
	for i := 0; i < heatCols*7; i++ {
		day := start.AddDate(0, 0, i)
		c := heatCell{Date: day.Format("2006-01-02")}
		if day.After(last) {
			c.Pad = true
		} else {
			c.Count = counts[c.Date]
			c.Today = day.Equal(last)
			c.Tip = commitsOn(c.Count, day)
			v.Total += c.Count
			if c.Count > 0 {
				v.Active++
				nonzero = append(nonzero, c.Count)
			}
			if c.Count > v.Busiest.Count {
				v.Busiest = c
				v.BusyDay = day.Format("Mon 2 Jan 2006")
			}
		}
		v.Cells = append(v.Cells, c)
	}
	q := quartiles(nonzero)
	for i := range v.Cells {
		v.Cells[i].Level = levelOf(v.Cells[i].Count, q)
	}
	v.Months = make([]string, heatCols)
	weekTotals := make([]int, heatCols)
	for col := 0; col < heatCols; col++ {
		for row := 0; row < 7; row++ {
			c := v.Cells[col*7+row]
			weekTotals[col] += c.Count
			if c.Pad {
				continue
			}
			if day, _ := time.Parse("2006-01-02", c.Date); day.Day() == 1 && col < heatCols-1 {
				v.Months[col] = day.Format("Jan")
			}
		}
	}
	var wnz []int
	for _, t := range weekTotals {
		if t > 0 {
			wnz = append(wnz, t)
		}
	}
	wq := quartiles(wnz)
	for col, t := range weekTotals {
		day := start.AddDate(0, 0, col*7)
		v.Weeks = append(v.Weeks, heatWeek{Count: t, Level: levelOf(t, wq),
			Tip: "Week of " + day.Format("2 Jan") + ": " + plural(t, "commit", "commits")})
	}
	// The streak counts back from today; a quiet today does not break it yet.
	i := len(v.Cells) - 1
	for i >= 0 && v.Cells[i].Pad {
		i--
	}
	if i >= 0 && v.Cells[i].Count == 0 {
		i--
	}
	for ; i >= 0 && v.Cells[i].Count > 0; i-- {
		v.Streak++
	}
	return v
}

func commitsOn(n int, day time.Time) string {
	if n == 0 {
		return "No commits on " + day.Format("Mon 2 Jan")
	}
	return plural(n, "commit", "commits") + " on " + day.Format("Mon 2 Jan")
}

func quartiles(vals []int) [3]int {
	if len(vals) == 0 {
		return [3]int{1, 1, 1}
	}
	s := append([]int(nil), vals...)
	sort.Ints(s)
	at := func(f float64) int { return s[min(len(s)-1, int(f*float64(len(s))))] }
	return [3]int{at(0.25), at(0.5), at(0.75)}
}

func levelOf(n int, q [3]int) int {
	switch {
	case n <= 0:
		return 0
	case n <= q[0]:
		return 1
	case n <= q[1]:
		return 2
	case n <= q[2]:
		return 3
	}
	return 4
}

type monthGroup struct {
	Label    string
	Releases []model.Release
}

type shipsView struct {
	Heat   heatView
	Months []monthGroup
}

func (d *derived) ships() shipsView {
	v := shipsView{Heat: d.heat}
	for _, r := range d.site.Releases {
		label := r.Date.In(zone(d.site.Meta.Timezone)).Format("January 2006")
		if n := len(v.Months); n == 0 || v.Months[n-1].Label != label {
			v.Months = append(v.Months, monthGroup{Label: label})
		}
		v.Months[len(v.Months)-1].Releases = append(v.Months[len(v.Months)-1].Releases, r)
	}
	return v
}

// ---- map -------------------------------------------------------------------

type mapNode struct {
	ID, Name, Short, Cat, Icon string
	Tier                       model.Tier
	TierLabel, TierClass       string
	X, Y, R                    float64
	IS, IO, LY                 float64 // icon size, icon offset, label baseline
	BX, BR, BI, BO             float64 // tier badge: centre, radius, icon size, icon offset
}

type mapEdge struct {
	From, To, Kind string
	X1, Y1, X2, Y2 float64
}

type mapAnchor struct {
	Key, Label string
	X, Y       float64 // the pull target
	LX, LY     float64 // where the category name settled
}

type mapGroup struct {
	Key, Label, Icon string
	Projects         []*model.Project
}

type mapView struct {
	W, H    float64
	Nodes   []mapNode
	Edges   []mapEdge
	Anchors []mapAnchor
	Groups  []mapGroup
	JSON    template.JS
}

const (
	mapW = 1600.0
	mapH = 940.0
)

// buildMap places category anchors on an ellipse and each category's nodes on
// a golden-angle spiral around its anchor, heaviest first, then pushes
// overlapping nodes apart. It is deterministic, so the page reads the same
// with JS off; map.js then runs the full simulation from here.
func buildMap(site *model.Site) mapView {
	v := mapView{W: mapW, H: mapH}
	var keys []string
	labels := map[string]string{}
	icons := map[string]string{}
	for _, c := range site.Categories {
		keys = append(keys, c.Key)
		labels[c.Key], icons[c.Key] = c.Label, c.Icon
	}
	members := map[string][]model.GraphNode{}
	for _, n := range site.Graph.Nodes {
		if _, ok := labels[n.Category]; !ok {
			keys = append(keys, n.Category)
			labels[n.Category] = n.Category
			icons[n.Category] = "folder-git-2"
			if n.Category == "unsorted" {
				labels[n.Category] = "Unsorted"
			}
		}
		members[n.Category] = append(members[n.Category], n)
	}
	var used []string
	for _, k := range keys {
		if len(members[k]) > 0 {
			used = append(used, k)
		}
	}
	cx, cy := mapW/2, mapH/2
	rx, ry := mapW*0.36, mapH*0.33
	pos := map[string]int{}
	for i, k := range used {
		a := -math.Pi/2 + float64(i)*2*math.Pi/float64(max(len(used), 1))
		ax, ay := cx+rx*math.Cos(a), cy+ry*math.Sin(a)
		v.Anchors = append(v.Anchors, mapAnchor{Key: k, Label: labels[k], X: round1(ax), Y: round1(ay)})
		ns := members[k]
		sort.SliceStable(ns, func(x, y int) bool { return ns[x].Weight > ns[y].Weight })
		for j, n := range ns {
			ang := a + float64(j+1)*2.39996
			dist := 50 * math.Sqrt(float64(j+1))
			w := math.Min(math.Max(n.Weight, 0), 1)
			pos[n.ID] = len(v.Nodes)
			v.Nodes = append(v.Nodes, mapNode{
				ID: n.ID, Name: n.Name, Short: shortName(n.Name), Cat: n.Category, Icon: n.Icon, Tier: n.Tier,
				TierLabel: tierLabel(n.Tier), TierClass: tierClass(n.Tier),
				X: ax + 1.6*dist*math.Cos(ang), Y: ay + dist*math.Sin(ang), R: 14 + 18*w,
			})
		}
	}
	separate(v.Nodes, v.Anchors)
	for i := range v.Nodes {
		n := &v.Nodes[i]
		n.X, n.Y = round1(n.X), round1(n.Y)
		n.IS = round1(n.R * 1.05)
		n.IO = -n.IS / 2
		n.LY = round1(n.R + 18)
		// The tier badge sits on the rim at lower right, scaled with the star
		// and kept clear of the label's halo (which starts about R+5 down).
		n.BR = round1(math.Min(math.Max(n.R/3.2, 8), 10))
		n.BX = round1(math.Min(n.R*math.Sqrt2/2+1, n.R+5-n.BR))
		n.BI = round1(n.BR * 1.35)
		n.BO = -n.BI / 2
	}

	for _, e := range site.Graph.Edges {
		a, ok1 := pos[e.From]
		b, ok2 := pos[e.To]
		if !ok1 || !ok2 || a == b {
			continue
		}
		v.Edges = append(v.Edges, mapEdge{From: e.From, To: e.To, Kind: e.Kind,
			X1: v.Nodes[a].X, Y1: v.Nodes[a].Y, X2: v.Nodes[b].X, Y2: v.Nodes[b].Y})
	}
	byCat := map[string][]*model.Project{}
	for i := range site.Projects {
		p := &site.Projects[i]
		byCat[p.Category] = append(byCat[p.Category], p)
	}
	for _, k := range used {
		v.Groups = append(v.Groups, mapGroup{Key: k, Label: labels[k], Icon: icons[k], Projects: byCat[k]})
	}
	type jn struct {
		ID  string  `json:"id"`
		Cat string  `json:"cat"`
		X   float64 `json:"x"`
		Y   float64 `json:"y"`
		R   float64 `json:"r"`
	}
	type je struct {
		From string `json:"from"`
		To   string `json:"to"`
		Kind string `json:"kind"`
	}
	type ja struct {
		Key string  `json:"key"`
		X   float64 `json:"x"`
		Y   float64 `json:"y"`
	}
	island := struct {
		W       float64 `json:"w"`
		H       float64 `json:"h"`
		Nodes   []jn    `json:"nodes"`
		Edges   []je    `json:"edges"`
		Anchors []ja    `json:"anchors"`
	}{W: mapW, H: mapH, Nodes: []jn{}, Edges: []je{}, Anchors: []ja{}}
	for _, n := range v.Nodes {
		island.Nodes = append(island.Nodes, jn{n.ID, n.Cat, n.X, n.Y, n.R})
	}
	for _, e := range v.Edges {
		island.Edges = append(island.Edges, je{e.From, e.To, e.Kind})
	}
	for _, a := range v.Anchors {
		island.Anchors = append(island.Anchors, ja{a.Key, a.X, a.Y})
	}
	b, _ := json.Marshal(island) // json escapes <, > and & so the island cannot close its script
	v.JSON = template.JS(b)
	return v
}

// separate pushes overlapping stars apart and keeps them on the canvas. Each
// star owns a box: its circle plus the label under it (about 8.4 units per
// character at the map's label size). Category names are boxes too, so stars
// move around them. Each push is along the shorter overlap with a quarter of
// it sideways, so a star wedged in a column slips out instead of jamming.
// map.js uses the same rule with measured label widths.
func separate(ns []mapNode, anchors []mapAnchor) {
	type box struct {
		x, y, hw, hh, oy float64
		fixed            bool
	}
	bs := make([]box, 0, len(ns)+len(anchors))
	for _, n := range ns {
		bs = append(bs, box{x: n.X, y: n.Y, hw: math.Max(n.R, 8.4*float64(utf8.RuneCountInString(n.Short))/2) + 8, hh: n.R + 15, oy: 8})
	}
	for _, a := range anchors {
		bs = append(bs, box{x: a.X, y: a.Y, hw: 7.7*float64(utf8.RuneCountInString(a.Label)) + 12, hh: 16, oy: -7})
	}
	for iter := 0; iter < 400; iter++ {
		moved := false
		for i := range bs {
			for j := i + 1; j < len(bs); j++ {
				a, b := &bs[i], &bs[j]
				if a.fixed && b.fixed {
					continue
				}
				dx, dy := b.x-a.x, (b.y+b.oy)-(a.y+a.oy)
				ox := a.hw + b.hw - math.Abs(dx)
				oy := a.hh + b.hh - math.Abs(dy)
				if ox <= 0 || oy <= 0 {
					continue
				}
				moved = true
				wa := 0.5
				if a.fixed {
					wa = 0
				} else if b.fixed {
					wa = 1
				}
				sx, sy := sign(dx, i), sign(dy, j)
				if ox < oy {
					a.x -= sx * ox * wa
					b.x += sx * ox * (1 - wa)
					a.y -= sy * ox * 0.25 * wa
					b.y += sy * ox * 0.25 * (1 - wa)
				} else {
					a.y -= sy * oy * wa
					b.y += sy * oy * (1 - wa)
					a.x -= sx * oy * 0.25 * wa
					b.x += sx * oy * 0.25 * (1 - wa)
				}
			}
		}
		for i := range bs {
			b := &bs[i]
			b.x = math.Min(math.Max(b.x, b.hw+12), mapW-b.hw-12)
			if i < len(ns) {
				b.y = math.Min(math.Max(b.y, ns[i].R+34), mapH-ns[i].R-34)
			} else {
				b.y = math.Min(math.Max(b.y, 30), mapH-20)
			}
		}
		if !moved {
			break
		}
	}
	for i := range ns {
		ns[i].X, ns[i].Y = bs[i].x, bs[i].y
	}
	for i := range anchors {
		b := bs[len(ns)+i]
		anchors[i].LX, anchors[i].LY = round1(b.x), round1(b.y)
	}
}

// sign is the direction of d, with a parity tie-break so exact stacks split.
func sign(d float64, parity int) float64 {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	case parity%2 == 1:
		return 1
	}
	return -1
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// ---- search ----------------------------------------------------------------

type searchIndex struct {
	Projects []searchProject `json:"projects"`
	Ideas    []searchIdea    `json:"ideas"`
	Issues   []searchIssue   `json:"issues"`
	Commands []searchCommand `json:"commands"`
}

type searchProject struct {
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	Slug      string `json:"slug"`
	Tier      string `json:"tier"`
	TierLabel string `json:"tier_label"`
	Icon      string `json:"icon"`
	Cat       string `json:"cat"`
	URL       string `json:"url"`
}

type searchIdea struct {
	Title   string `json:"title"`
	Project string `json:"project"`
	Name    string `json:"project_name"`
	Kind    string `json:"kind"`
	Icon    string `json:"icon"`
	URL     string `json:"url"`
}

type searchIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Name    string `json:"project_name"`
	Source  string `json:"source"`
	Forge   string `json:"forge"`
	Mirror  bool   `json:"mirror,omitempty"`
	URL     string `json:"url"`
}

type searchCommand struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Icon   string `json:"icon"`
	URL    string `json:"url,omitempty"`
	Action string `json:"action,omitempty"`
}

func buildSearch(site *model.Site) []byte {
	ix := searchIndex{Projects: []searchProject{}, Ideas: []searchIdea{}, Issues: []searchIssue{}}
	for _, p := range site.Projects {
		ix.Projects = append(ix.Projects, searchProject{Name: p.Name, Summary: p.Summary, Slug: p.Slug, Tier: string(p.Tier),
			TierLabel: tierLabel(p), Icon: p.Icon, Cat: p.Category, URL: "/p/" + p.Slug})
		for _, is := range p.Issues {
			ix.Issues = append(ix.Issues, searchIssue{Number: is.Number, Title: is.Title, Project: p.Slug, Name: p.Name, Source: is.Source,
				Forge: is.Forge, Mirror: is.Mirror, URL: is.URL})
		}
	}
	if site.Meta.Ideas {
		for _, i := range site.Ideas {
			ix.Ideas = append(ix.Ideas, searchIdea{Title: mdlite.Plain(i.Title), Project: i.Project, Name: i.ProjectName, Kind: i.Kind,
				Icon: ideaKindIcon(i.Kind), URL: "/p/" + i.Project + "#idea-" + ident(i.ID)})
		}
	}
	ix.Commands = []searchCommand{{ID: "go-overview", Label: "Overview", Icon: "layout-dashboard", URL: "/"}}
	if site.Meta.Ideas {
		ix.Commands = append(ix.Commands, searchCommand{ID: "go-ideas", Label: "Ideas", Icon: "lightbulb", URL: "/ideas"})
	}
	ix.Commands = append(ix.Commands,
		searchCommand{ID: "go-ships", Label: "Ship log", Icon: "ship", URL: "/ships"},
		searchCommand{ID: "go-map", Label: "Map", Icon: "orbit", URL: "/map"},
		searchCommand{ID: "theme", Label: "Toggle theme", Icon: "sun", Action: "theme"},
		searchCommand{ID: "refresh", Label: "Refresh now", Icon: "refresh-cw", Action: "refresh"},
	)
	// Surprise me picks an idea on the Ideas page: with none there is nothing
	// to pick, and with no Ideas page it would lead to a 404.
	if len(ix.Ideas) > 0 {
		ix.Commands = append(ix.Commands, searchCommand{ID: "surprise", Label: "Surprise me", Icon: "shuffle", Action: "surprise"})
	}
	b, _ := json.Marshal(ix)
	return b
}
