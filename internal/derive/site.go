package derive

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
)

// SiteInput is everything Assemble needs.
type SiteInput struct {
	Env
	Registry *registry.Registry
	Projects []Input
	Ideas    *registry.Ideas // the ideas file; nil when there is none
	// IdeaLabels make a project's open issue an idea (config ideas.labels,
	// any case). IdeaMarker ends a "File as issue" body (config ideas.marker),
	// so the filed issue is found again.
	IdeaLabels []string
	IdeaMarker string
	Filed      map[string]forge.Filed
	Heat       []forge.HeatPoint
	Sources    []model.SourceStatus
	// Meta is the site's configured words and sources; Assemble adds Ideas
	// when there are any and FDroid when a project names a package.
	Meta model.Meta
}

// SiteOutput is the assembled site plus what the caller diffs and logs.
type SiteOutput struct {
	Site         *model.Site
	Fingerprints map[string]Fingerprint
	Warnings     []string
}

// ReleaseWindow is how far back the ship log reaches.
const ReleaseWindowMonths = 18

// Assemble derives every project and the site-wide views. Site.Events is left
// empty for the caller, who owns the event history.
func Assemble(in SiteInput) SiteOutput {
	now := in.Now
	out := SiteOutput{Fingerprints: map[string]Fingerprint{}}
	builds := make([]Built, len(in.Projects))
	bySlug := map[string]int{}
	for i, pi := range in.Projects {
		builds[i] = BuildProject(in.Env, pi)
		bySlug[pi.Project.Slug] = i
	}

	// Relations, both ends.
	addRel := func(p *model.Project, r model.Relation) {
		for _, have := range p.Relations {
			if have.To == r.To && have.Kind == r.Kind && have.Outgoing == r.Outgoing {
				return
			}
		}
		p.Relations = append(p.Relations, r)
	}
	type edgeKey struct{ a, b, kind string }
	edges := map[edgeKey]bool{}
	var graphEdges []model.GraphEdge
	for i, pi := range in.Projects {
		src := &builds[i].Project
		for _, rel := range pi.Project.Relations {
			j, ok := bySlug[rel.To]
			if !ok || rel.To == src.Slug {
				out.Warnings = append(out.Warnings, fmt.Sprintf("project %s: relation to unknown project %q dropped", src.Slug, rel.To))
				continue
			}
			dst := &builds[j].Project
			addRel(src, model.Relation{To: dst.Slug, ToName: dst.Name, Kind: rel.Kind, Outgoing: true})
			addRel(dst, model.Relation{To: src.Slug, ToName: src.Name, Kind: rel.Kind, Outgoing: false})
			k := edgeKey{src.Slug, dst.Slug, rel.Kind}
			if src.Slug > dst.Slug {
				k = edgeKey{dst.Slug, src.Slug, rel.Kind}
			}
			if !edges[k] {
				edges[k] = true
				graphEdges = append(graphEdges, model.GraphEdge{From: src.Slug, To: dst.Slug, Kind: rel.Kind})
			}
		}
	}

	// Ideas: the file's, then the open issues with an ideas label. An issue
	// that a file idea was filed as, or names as existing, shows as that file
	// idea only.
	var ideas []model.Idea
	roundup := ""
	linked := map[string]bool{} // issue URLs the file ideas link, lower case
	addIdea := func(p *model.Project, idea registry.Idea) {
		mi := model.Idea{
			ID: idea.ID, Project: p.Slug, ProjectName: p.Name, Icon: p.Icon, Title: idea.Title, Pitch: idea.Pitch,
			Why: idea.Why, Size: idea.Size, Kind: idea.Kind, ExistingIssue: idea.ExistingIssue, Source: idea.Source, URL: idea.URL,
		}
		if idea.Source == "file" {
			if f, ok := in.Filed[idea.ID]; ok {
				mi.FiledIssue, mi.FiledURL = f.Number, f.URL
				linked[strings.ToLower(f.URL)] = true
			}
			if p.Repo != nil && p.Repo.HTMLURL != "" {
				if mi.ExistingIssue > 0 {
					linked[strings.ToLower(p.Repo.HTMLURL+"/issues/"+strconv.Itoa(mi.ExistingIssue))] = true
				}
				if mi.FiledIssue == 0 && mi.ExistingIssue == 0 {
					mi.FileURL = FileIssueURL(p.Repo.HTMLURL, idea, in.IdeaMarker)
				}
			}
		}
		p.Ideas = append(p.Ideas, mi)
		ideas = append(ideas, mi)
	}
	if in.Ideas != nil {
		roundup = in.Ideas.RoundupDate
		for _, idea := range in.Ideas.Ideas {
			j, ok := bySlug[idea.Project]
			if !ok {
				out.Warnings = append(out.Warnings, fmt.Sprintf("idea %s: unknown project %q dropped", idea.ID, idea.Project))
				continue
			}
			idea.Source, idea.URL = "file", ""
			addIdea(&builds[j].Project, idea)
		}
	}
	for i, pi := range in.Projects {
		for _, idea := range labelIdeas(pi, in.IdeaLabels) {
			if !linked[strings.ToLower(idea.URL)] {
				addIdea(&builds[i].Project, idea)
			}
		}
	}

	projects := make([]model.Project, len(builds))
	for i := range builds {
		p := builds[i].Project
		normalizeProject(&p)
		p.Hash = Hash(p)
		projects[i] = p
		out.Fingerprints[p.Slug] = builds[i].FP
	}
	tierPos := map[model.Tier]int{}
	for i, t := range model.TierOrder {
		tierPos[t] = i
	}
	sort.SliceStable(projects, func(i, j int) bool {
		a, b := projects[i], projects[j]
		if tierPos[a.Tier] != tierPos[b.Tier] {
			return tierPos[a.Tier] < tierPos[b.Tier]
		}
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.After(b.LastActivity)
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})

	site := &model.Site{
		GeneratedAt: now.UTC(),
		Sources:     append([]model.SourceStatus{}, in.Sources...),
		Projects:    projects,
		Events:      []model.Event{},
		Ideas:       ideas,
		RoundupDate: roundup,
		Heatmap:     Heatmap(in.Heat, now, in.Loc),
	}
	if site.Ideas == nil {
		site.Ideas = []model.Idea{}
	}
	site.Meta = in.Meta
	site.Meta.Ideas = in.Meta.Ideas || len(site.Ideas) > 0
	for _, pi := range in.Projects {
		site.Meta.FDroid = site.Meta.FDroid || pi.Project.FDroid != nil
	}

	// Categories in registry order, counted, empty ones left out.
	counts := map[string]int{}
	for _, p := range projects {
		counts[p.Category]++
	}
	site.Categories = []model.Category{}
	if in.Registry != nil {
		for _, c := range in.Registry.Categories {
			if counts[c.Key] > 0 {
				site.Categories = append(site.Categories, model.Category{Key: c.Key, Label: c.Label, Icon: c.Icon, Hue: c.Hue, Count: counts[c.Key]})
			}
		}
	}

	// Releases.
	cutoff := now.AddDate(0, -ReleaseWindowMonths, 0)
	site.Releases = []model.Release{}
	for _, b := range builds {
		for _, r := range b.AllReleases {
			if !r.Date.Before(cutoff) {
				site.Releases = append(site.Releases, r)
			}
		}
	}
	sort.SliceStable(site.Releases, func(i, j int) bool { return site.Releases[i].Date.After(site.Releases[j].Date) })

	// Graph.
	maxAct := 0
	sums := make([]int, len(projects))
	for i, p := range projects {
		for _, n := range p.Activity {
			sums[i] += n
		}
		if sums[i] > maxAct {
			maxAct = sums[i]
		}
	}
	site.Graph.Nodes = []model.GraphNode{}
	for i, p := range projects {
		w := 0.0
		if maxAct > 0 {
			w = float64(sums[i]) / float64(maxAct)
		}
		site.Graph.Nodes = append(site.Graph.Nodes, model.GraphNode{ID: p.Slug, Name: p.Name, Category: p.Category, Tier: p.Tier, Icon: p.Icon, Weight: w})
	}
	site.Graph.Edges = graphEdges
	if site.Graph.Edges == nil {
		site.Graph.Edges = []model.GraphEdge{}
	}

	// Needs you and stats.
	site.NeedsYou = []model.Action{}
	st := model.Stats{Total: len(projects), Ideas: len(site.Ideas)}
	for _, p := range projects {
		site.NeedsYou = append(site.NeedsYou, p.Actions...)
		switch p.Tier {
		case model.TierActive:
			st.Active++
		case model.TierLive:
			st.Live++
		case model.TierQuiet:
			st.Quiet++
		case model.TierDormant:
			st.Dormant++
		case model.TierHold:
			st.OnHold++
		case model.TierArchived:
			st.Archived++
		}
		if p.Repo != nil {
			st.OpenIssues += p.Repo.OpenIssues
			st.OpenPRs += p.Repo.OpenPRs
		}
		if p.GitHub != nil {
			st.OpenIssues += p.GitHub.OpenIssues
		}
	}
	SortActions(site.NeedsYou)
	st.NeedsYou = len(site.NeedsYou)
	month := now.Add(-30 * 24 * time.Hour)
	for _, r := range site.Releases {
		if r.Date.After(month) {
			st.ReleasesLast30d++
		}
	}
	site.Stats = st
	out.Site = site
	return out
}

// FileIssueURL is a prefilled new-issue form; the body's last line carries
// marker<id> (marker as config ideas.marker) so the filed issue can be found
// again.
func FileIssueURL(repoURL string, idea registry.Idea, marker string) string {
	var parts []string
	if s := strings.TrimSpace(idea.Pitch); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(idea.Why); s != "" {
		parts = append(parts, "Why: "+s)
	}
	parts = append(parts, "From the ideas list.\n"+marker+idea.ID)
	body := strings.Join(parts, "\n\n")
	return repoURL + "/issues/new?title=" + url.QueryEscape(idea.Title) + "&body=" + url.QueryEscape(body)
}

// labelIdeas are the ideas from a project's open issues carrying one of
// labels (any case): those of its repos read in full, or of its primary's
// public view when the primary gives no detail; never a mirror's. A
// kind/<kind> label gives the kind and a size/<size> label the size.
func labelIdeas(in Input, labels []string) []registry.Idea {
	if len(labels) == 0 {
		return nil
	}
	type repoIssues struct {
		forge, full string
		issues      []forge.Issue
	}
	var from []repoIssues
	for _, rd := range in.Repos {
		if rd.Detail != nil {
			from = append(from, repoIssues{rd.Repo.Forge, rd.Repo.FullName, rd.Detail.Issues})
		}
	}
	if m := in.Mirror; in.NoDetail && m != nil {
		from = append(from, repoIssues{m.Forge, m.FullName, m.Issues})
	}
	var out []registry.Idea
	for _, r := range from {
		for _, is := range r.issues {
			if is.State != "" && is.State != "open" || !hasLabel(is, labels) {
				continue
			}
			idea := registry.Idea{
				ID: r.forge + ":" + r.full + "#" + strconv.Itoa(is.Number), Project: in.Project.Slug, Title: is.Title,
				ExistingIssue: is.Number, Source: "issue", URL: is.HTMLURL,
			}
			for _, l := range is.Labels {
				key, val, _ := strings.Cut(l.Name, "/")
				val = strings.TrimSpace(val)
				switch {
				case val == "":
				case idea.Kind == "" && strings.EqualFold(key, "kind"):
					idea.Kind = strings.ToLower(val)
				case idea.Size == "" && strings.EqualFold(key, "size"):
					idea.Size = ideaSize(val)
				}
			}
			out = append(out, idea)
		}
	}
	return out
}

// ideaSize maps a size/<s> label onto the page's sizes S, M and L (XL is L);
// any other size is none.
func ideaSize(s string) string {
	switch strings.ToLower(s) {
	case "xs", "s", "small":
		return "S"
	case "m", "medium":
		return "M"
	case "l", "large", "xl":
		return "L"
	}
	return ""
}

func hasLabel(is forge.Issue, labels []string) bool {
	for _, l := range is.Labels {
		for _, want := range labels {
			if strings.EqualFold(l.Name, want) {
				return true
			}
		}
	}
	return false
}
