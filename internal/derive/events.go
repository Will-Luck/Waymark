package derive

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
)

// CIState is what an event needs to know about one run.
type CIState struct {
	Conclusion string `json:"conclusion,omitempty"` // "" while queued or running
	Title      string `json:"title,omitempty"`
	Branch     string `json:"branch,omitempty"`
	URL        string `json:"url,omitempty"`
}

// Fingerprint is the per-project state that events are diffed from. A nil
// pointer or map, or an empty Tier, means "not known yet": nothing is emitted
// across an unknown.
type Fingerprint struct {
	Name    string     `json:"name"`
	Icon    string     `json:"icon"`
	Tier    model.Tier `json:"tier"`
	RepoURL string     `json:"repo_url,omitempty"`

	LatestTag *string `json:"latest_tag,omitempty"`
	TagURL    string  `json:"tag_url,omitempty"`

	DeployVersion  *string `json:"deploy_version,omitempty"`
	DeploySource   string  `json:"deploy_source,omitempty"`
	ContainerName  string  `json:"container_name,omitempty"`
	ContainerState *string `json:"container_state,omitempty"`
	// ContainerSince is when ContainerState was first seen.
	ContainerSince *time.Time `json:"container_since,omitempty"`

	CI map[int64]CIState `json:"ci,omitempty"`

	LastCommitAt  *time.Time `json:"last_commit_at,omitempty"`
	LastCommitSHA string     `json:"last_commit_sha,omitempty"`
	// Commits are the window's commit times (current snapshot only).
	Commits    []time.Time `json:"-"`
	CommitsURL string      `json:"commits_url,omitempty"`

	OpenIssues *int `json:"open_issues,omitempty"`

	Probes map[string]bool `json:"probes,omitempty"` // label -> up, checked probes only
	// ProbeDown is when each probe that is down was first seen down.
	ProbeDown map[string]time.Time `json:"probe_down,omitempty"`
	Notes     map[string]time.Time `json:"notes,omitempty"` // file -> modified
	// NotesLabel is Env.NotesLabel, for the words of note events (current
	// snapshot only).
	NotesLabel string `json:"-"`
}

// ciMemory is how many run IDs a fingerprint remembers.
const ciMemory = 20

func fingerprint(p model.Project, in Input, primary *RepoData, dep *model.DeployInfo, pc *ContainerRef, commitsKnown bool, lastCommit time.Time, lastSHA string, times []time.Time, rels map[string]forge.Release) Fingerprint {
	fp := Fingerprint{Name: p.Name, Icon: p.Icon, Tier: p.Tier}
	if p.Repo != nil {
		fp.RepoURL = p.Repo.HTMLURL
		oi := p.Repo.OpenIssues
		fp.OpenIssues = &oi
	}
	if primary != nil && primary.Detail != nil {
		tag := ""
		if p.Repo != nil {
			tag = p.Repo.LatestTag
		}
		fp.LatestTag = &tag
		if tag != "" {
			fp.TagURL = tagURL(primary.Repo.HTMLURL, tag, rels)
		}
		fp.CI = map[int64]CIState{}
		for _, r := range primary.Detail.Runs {
			st := CIState{Title: r.DisplayTitle, Branch: r.HeadBranch, URL: r.HTMLURL}
			if r.Done() {
				st.Conclusion = r.Conclusion
				if st.Conclusion == "" {
					st.Conclusion = "unknown"
				}
			}
			fp.CI[r.ID] = st
		}
	}
	if commitsKnown {
		lc := lastCommit
		fp.LastCommitAt = &lc
		fp.LastCommitSHA = lastSHA
		fp.Commits = append([]time.Time(nil), times...)
		if p.Repo != nil {
			fp.CommitsURL = p.Repo.HTMLURL + "/commits/branch/" + url.PathEscape(p.Repo.DefaultBranch)
		}
	}
	if dep != nil {
		known := (dep.Source == "fdroid" && in.FDroid != nil) || (dep.Source == "docker" && in.DockerKnown)
		if known {
			v := dep.Version
			fp.DeployVersion = &v
			fp.DeploySource = dep.Source
		}
	}
	if pc != nil && in.DockerKnown {
		s := "missing"
		if pc.C != nil {
			s = pc.C.State
		}
		fp.ContainerName = pc.Name
		fp.ContainerState = &s
	}
	for _, pr := range p.Probes {
		if pr.CheckedAt.IsZero() || pr.Homepage {
			continue
		}
		if fp.Probes == nil {
			fp.Probes = map[string]bool{}
		}
		fp.Probes[pr.Label] = pr.Up
	}
	if !in.NotesAt.IsZero() {
		fp.Notes = map[string]time.Time{}
		for _, n := range in.Notes {
			fp.Notes[n.File] = n.Modified
		}
	}
	return fp
}

// Merge is what gets persisted: cur, with prev's values wherever cur does not
// know, and the union of remembered CI runs (newest 20).
func Merge(prev, cur Fingerprint) Fingerprint {
	m := cur
	if m.Tier == "" {
		m.Tier = prev.Tier
	}
	if m.LatestTag == nil {
		m.LatestTag, m.TagURL = prev.LatestTag, prev.TagURL
	}
	if m.DeployVersion == nil {
		m.DeployVersion, m.DeploySource = prev.DeployVersion, prev.DeploySource
	}
	if m.ContainerState == nil {
		m.ContainerState, m.ContainerName, m.ContainerSince = prev.ContainerState, prev.ContainerName, prev.ContainerSince
	}
	if m.LastCommitAt == nil {
		m.LastCommitAt, m.LastCommitSHA, m.CommitsURL = prev.LastCommitAt, prev.LastCommitSHA, prev.CommitsURL
	}
	if m.OpenIssues == nil {
		m.OpenIssues = prev.OpenIssues
	}
	if m.Probes == nil {
		m.Probes, m.ProbeDown = prev.Probes, prev.ProbeDown
	}
	if m.Notes == nil {
		m.Notes = prev.Notes
	}
	if m.CI == nil {
		m.CI = prev.CI
	} else if prev.CI != nil {
		u := map[int64]CIState{}
		for id, st := range prev.CI {
			u[id] = st
		}
		for id, st := range cur.CI {
			u[id] = st
		}
		m.CI = u
	}
	if len(m.CI) > ciMemory {
		ids := make([]int64, 0, len(m.CI))
		for id := range m.CI {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
		keep := map[int64]CIState{}
		for _, id := range ids[:ciMemory] {
			keep[id] = m.CI[id]
		}
		m.CI = keep
	}
	return m
}

// Diff turns two fingerprint snapshots into events. A project missing from
// prev (first sighting, or an empty state.json) emits nothing. Event IDs are
// deterministic so a replay never duplicates.
func Diff(prev, cur map[string]Fingerprint, now time.Time) []model.Event {
	var evs []model.Event
	minute := now.UTC().Format("200601021504")
	slugs := make([]string, 0, len(cur))
	for s := range cur {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		c := cur[slug]
		p, ok := prev[slug]
		if !ok {
			continue
		}
		add := func(kind, key string, level model.Level, text, href string) {
			evs = append(evs, model.Event{
				ID: slug + ":" + kind + ":" + key, At: now.UTC(), Project: slug, ProjectName: c.Name, Icon: c.Icon,
				Kind: kind, Level: level, Text: text, Href: href,
			})
		}
		if p.LatestTag != nil && c.LatestTag != nil && *c.LatestTag != "" && *p.LatestTag != *c.LatestTag {
			add("release", *c.LatestTag, model.LevelGood, c.Name+" "+*c.LatestTag+" tagged", c.TagURL)
		}
		if p.DeployVersion != nil && c.DeployVersion != nil && *p.DeployVersion != "" && *c.DeployVersion != "" && *p.DeployVersion != *c.DeployVersion {
			verb := "now runs"
			if c.DeploySource == "fdroid" {
				verb = "is now on F-Droid as"
			}
			add("deploy", *c.DeployVersion+":"+minute, model.LevelGood, c.Name+" "+verb+" "+*c.DeployVersion+" (was "+*p.DeployVersion+")", "")
		}
		if p.ContainerState != nil && c.ContainerState != nil && *p.ContainerState != *c.ContainerState {
			was, is := *p.ContainerState == "running", *c.ContainerState == "running"
			switch {
			case is:
				add("container", "running:"+minute, model.LevelGood, "Container "+c.ContainerName+" is running again", "")
			case was || *c.ContainerState == "missing":
				text := "Container " + c.ContainerName + " stopped"
				if *c.ContainerState == "missing" {
					text = "Container " + c.ContainerName + " is missing"
				}
				add("container", *c.ContainerState+":"+minute, model.LevelAlert, text, "")
			}
		}
		if p.CI != nil && c.CI != nil {
			ids := make([]int64, 0, len(c.CI))
			for id := range c.CI {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, id := range ids {
				st := c.CI[id]
				if st.Conclusion == "" {
					continue
				}
				if before, seen := p.CI[id]; seen && before.Conclusion != "" {
					continue
				}
				level, verb := model.LevelInfo, "CI "+st.Conclusion
				switch st.Conclusion {
				case "success":
					level, verb = model.LevelGood, "CI passed"
				case "failure":
					level, verb = model.LevelAlert, "CI failed"
				case "cancelled":
					verb = "CI cancelled"
				}
				text := verb
				if st.Branch != "" {
					text += " on " + st.Branch
				}
				if st.Title != "" {
					text += ": " + st.Title
				}
				add("ci", itoa64(id), level, text, st.URL)
			}
		}
		if p.LastCommitAt != nil && c.LastCommitAt != nil && c.LastCommitAt.After(*p.LastCommitAt) {
			n := 0
			for _, t := range c.Commits {
				if t.After(*p.LastCommitAt) {
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			key := c.LastCommitSHA
			if key == "" {
				key = c.LastCommitAt.UTC().Format("20060102150405")
			}
			add("commits", key, model.LevelInfo, plural(n, "new commit", "new commits"), c.CommitsURL)
		}
		if p.OpenIssues != nil && c.OpenIssues != nil && *p.OpenIssues != *c.OpenIssues {
			d := *c.OpenIssues - *p.OpenIssues
			dir := "up"
			if d < 0 {
				dir, d = "down", -d
			}
			href := ""
			if c.RepoURL != "" {
				href = c.RepoURL + "/issues"
			}
			add("issues", itoa(*c.OpenIssues)+":"+minute, model.LevelInfo, "Open issues "+dir+" by "+itoa(d)+" ("+itoa(*c.OpenIssues)+" open)", href)
		}
		if p.Probes != nil && c.Probes != nil {
			labels := make([]string, 0, len(c.Probes))
			for l := range c.Probes {
				labels = append(labels, l)
			}
			sort.Strings(labels)
			for _, l := range labels {
				was, seen := p.Probes[l]
				if !seen || was == c.Probes[l] {
					continue
				}
				if c.Probes[l] {
					add("probe", l+":up:"+minute, model.LevelGood, l+" is back up", "")
				} else {
					add("probe", l+":down:"+minute, model.LevelAlert, l+" is down", "")
				}
			}
		}
		if p.Notes != nil && c.Notes != nil {
			files := make([]string, 0, len(c.Notes))
			for f := range c.Notes {
				files = append(files, f)
			}
			sort.Strings(files)
			for _, f := range files {
				before, seen := p.Notes[f]
				if !seen || !c.Notes[f].After(before) {
					continue
				}
				add("note", f+":"+c.Notes[f].UTC().Format("20060102150405"), model.LevelInfo, noteWord(c.NotesLabel)+" "+f+" was updated", "")
			}
		}
		if p.Tier != "" && c.Tier != "" && p.Tier != c.Tier {
			add("tier", string(c.Tier)+":"+minute, model.LevelInfo, "Now "+tierLabel(c.Tier)+" (was "+tierLabel(p.Tier)+")", "")
		}
	}
	return evs
}

// noteWord is one note in the adapter's words: "Field notes" gives
// "Field note", "Notes" gives "Note".
func noteWord(label string) string {
	if label == "" {
		return "Note"
	}
	return strings.TrimSuffix(label, "s")
}

func tierLabel(t model.Tier) string {
	switch t {
	case model.TierHold:
		return "On hold"
	case "":
		return ""
	}
	s := string(t)
	return strings.ToUpper(s[:1]) + s[1:]
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
