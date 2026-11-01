package derive

import (
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/docker"
	"github.com/Will-Luck/waymark/internal/sources/fdroid"
	"github.com/Will-Luck/waymark/internal/sources/notes"
	"github.com/Will-Luck/waymark/internal/sources/probe"
)

// Env is the clock and place every rule is evaluated in.
type Env struct {
	Now       time.Time
	Loc       *time.Location // the site's time zone
	HostLabel string         // Deploy.Where for docker (config docker.label)
	NeedsYou  NeedsYou
	// ForgeKinds maps a forge id to its kind ("gitea", "forgejo" or "github"), and
	// ForgeNames to its name on the page (the app computes each once:
	// ForgeName, with the id added when two forges share a name). A forge
	// missing from ForgeNames is named from its kind. HomeForge is the first
	// forge's id, which names the forge of a project with no repo.
	ForgeKinds map[string]string
	ForgeNames map[string]string
	HomeForge  string
	// NotesLabel is the notes adapter's label ("Notes", "Field notes"):
	// the source of note actions and the words of note events.
	NotesLabel string
}

// NeedsYou is the forge-side "waits on me" rules (config needs_you).
type NeedsYou struct {
	Labels         []string
	Assigned       bool
	ReviewRequests bool
}

// RepoData is one repo read in full: the listing entry plus whatever detail
// and files have been fetched (nil until the first good fetch).
type RepoData struct {
	Repo   forge.Repo
	Detail *forge.Detail
	Files  *forge.Files
}

// ContainerRef is a container the project runs. C is nil when the feed does
// not list it.
type ContainerRef struct {
	Name     string
	Declared bool // named in the registry (only declared containers can be "missing")
	C        *docker.Container
}

// ProbeData is one probed URL with its last result (nil: not checked yet).
type ProbeData struct {
	Label    string
	URL      string
	ProbeURL string
	Last     *probe.Result
	History  []model.ProbePoint
	// Homepage: a repo's website (config probes.homepages), not a projects
	// file url. It shows in "Where it runs" only: it never makes the tier
	// Live, raises Fix, or gives a signal or an event.
	Homepage bool
}

// Input is everything known about one project.
type Input struct {
	Project registry.Project
	// Repos are the project's repos on its primary's forge, read in full,
	// primary first.
	Repos []RepoData
	// GiteaKnown is false until the primary forge's repo list has been read
	// once: before that, an empty Repos means unknown, not "no repo".
	GiteaKnown bool
	// Mirror is the public view of the project's first mirror (nil until
	// read), or of the primary itself when NoDetail. MirrorURL is that repo's
	// page, linked before the first read, and MirrorForge its forge id.
	Mirror      *forge.Mirror
	MirrorURL   string
	MirrorForge string
	// NoDetail: the primary's forge gives no full detail (GitHub without a
	// token), so Repos is empty, Mirror holds the primary, and the notes date
	// the work as for a project with no repo.
	NoDetail bool
	// Me maps a forge id to the login of the user its token belongs to.
	Me map[string]string
	// DockerKnown is false until the feed has been read once; nothing is said
	// about containers before that.
	DockerKnown bool
	FeedAt      time.Time
	Containers  []ContainerRef
	FDroid      *fdroid.Package
	FDroidAt    time.Time
	// FDroidIndexRead: the project's F-Droid index was read. With FDroid nil
	// it means the package is not in it.
	FDroidIndexRead bool
	Probes          []ProbeData
	Notes           []notes.Note
	NotesAt         time.Time
	// Prev is the project's stored fingerprint (nil: none). It dates the
	// down states, so a Fix action keeps the time the outage was first seen.
	Prev *Fingerprint
}

// Built is one derived project.
type Built struct {
	Project model.Project
	// AllReleases is every tag, newest first (the ship log); Project.Releases
	// keeps the newest 8.
	AllReleases []model.Release
	FP          Fingerprint
}

type commitRef struct {
	c    forge.Commit
	when time.Time
}

var levelRank = map[model.Level]int{model.LevelAlert: 0, model.LevelWarn: 1, model.LevelInfo: 2, model.LevelGood: 3}

// SortActions orders alert, warn, info, then newest first.
func SortActions(as []model.Action) {
	sort.SliceStable(as, func(i, j int) bool {
		if levelRank[as[i].Level] != levelRank[as[j].Level] {
			return levelRank[as[i].Level] < levelRank[as[j].Level]
		}
		return as[i].At.After(as[j].At)
	})
}

func tagURL(repoURL, tag string, rels map[string]forge.Release) string {
	if r, ok := rels[tag]; ok && r.HTMLURL != "" {
		return r.HTMLURL
	}
	if repoURL == "" {
		return ""
	}
	return repoURL + "/releases/tag/" + url.PathEscape(tag)
}

func stateWord(state string) string {
	switch state {
	case "", "missing":
		return "missing"
	case "restarting":
		return "restarting"
	case "paused":
		return "paused"
	}
	return "stopped"
}

func probeProblem(r *probe.Result) string {
	if r.Code > 0 {
		return "HTTP " + itoa(r.Code)
	}
	e := strings.ToLower(r.Err)
	switch {
	case strings.Contains(e, "deadline") || strings.Contains(e, "timeout"):
		return "no answer in 5 s"
	case strings.Contains(e, "refused"):
		return "connection refused"
	case strings.Contains(e, "no such host"):
		return "host not found"
	case r.Err == "":
		return "no answer"
	}
	if len(r.Err) > 60 {
		return r.Err[:60]
	}
	return r.Err
}

// BuildProject derives one project. Relations, ideas and the hash are added
// by Assemble, which sees every project.
func BuildProject(env Env, in Input) Built {
	now, loc := env.Now, env.Loc
	if loc == nil {
		loc = time.UTC
	}
	rp := in.Project
	p := model.Project{
		Slug: rp.Slug, Name: rp.Name, Category: rp.Category, Icon: rp.Icon,
		Summary: rp.Summary, What: rp.What, Stack: rp.Stack, RunsOn: rp.RunsOn, Auto: rp.Auto,
	}
	if p.What == "" && rp.Auto && len(in.Repos) > 0 {
		p.What = strings.TrimSpace(in.Repos[0].Repo.Description)
	}
	primaryForge := rp.Primary().Forge
	if primaryForge == "" {
		primaryForge = env.HomeForge
	}
	p.Forge = forgeName(env, primaryForge)
	mirrorForge := in.MirrorForge
	if in.Mirror != nil && in.Mirror.Forge != "" {
		mirrorForge = in.Mirror.Forge
	}
	var primary *RepoData
	if len(in.Repos) > 0 {
		primary = &in.Repos[0]
	}

	// Commits across every repo of the project, deduplicated by SHA.
	var win commitWindow
	var allTimes []time.Time
	var commits []commitRef
	seenSHA := map[string]bool{}
	commitsKnown := false
	var lastCommit time.Time
	lastSHA := ""
	var fallback time.Time
	for _, rd := range in.Repos {
		if rd.Repo.UpdatedAt.After(fallback) {
			fallback = rd.Repo.UpdatedAt.Time
		}
		d := rd.Detail
		if d == nil {
			continue
		}
		commitsKnown = true
		win = append(win, newRepoWindow(d))
		for _, c := range d.Commits {
			if c.SHA != "" && seenSHA[c.SHA] {
				continue
			}
			seenSHA[c.SHA] = true
			commits = append(commits, commitRef{c: c, when: c.When()})
			allTimes = append(allTimes, c.When())
		}
		if d.LastCommit != nil && d.LastCommit.When().After(lastCommit) {
			lastCommit = d.LastCommit.When()
			lastSHA = d.LastCommit.SHA
		}
	}
	sort.SliceStable(commits, func(i, j int) bool { return commits[i].when.After(commits[j].when) })
	tierCommit := lastCommit
	if !commitsKnown {
		// No detail has ever been read: the listing's updated_at is the only clue.
		tierCommit = fallback
	}
	// A project with no repo read in full (a server, a network, a hardware
	// job, or a GitHub repo without a token) has no commits: its notes are
	// the work record.
	fromNotes := len(rp.FullRepos()) == 0 || in.NoDetail
	if fromNotes {
		for _, n := range in.Notes {
			if n.Modified.After(tierCommit) {
				tierCommit = n.Modified
			}
		}
	}
	p.LastActivity = tierCommit
	p.ActivityFromNotes = fromNotes
	p.Activity = Activity(allTimes, now, loc)

	// Primary repo facts.
	var ri *model.RepoInfo
	var rels map[string]forge.Release
	var latestTag *forge.Tag
	var allReleases []model.Release
	if primary != nil {
		r := primary.Repo
		ri = &model.RepoInfo{
			FullName: r.FullName, HTMLURL: r.HTMLURL, DefaultBranch: r.DefaultBranch, Description: r.Description,
			Private: r.Private, Archived: r.Archived, Mirror: r.Mirror, Stars: r.Stars, UpdatedAt: r.UpdatedAt.Time,
		}
		// The detail's counts: a GitHub listing counts PRs as issues, and the
		// detail splits them.
		for _, rd := range in.Repos {
			counted := rd.Repo
			if rd.Detail != nil {
				counted = rd.Detail.Repo
			}
			ri.OpenIssues += counted.OpenIssues
			ri.OpenPRs += counted.OpenPRs
		}
		if d := primary.Detail; d != nil {
			rels = map[string]forge.Release{}
			for _, rel := range d.Releases {
				rels[rel.TagName] = rel
			}
			if latestTag = PickLatestTag(d.Tags); latestTag != nil {
				ri.LatestTag = latestTag.Name
				ri.LatestTagDate = tagDate(*latestTag, rels)
				// By position of the tag's commit on the branch when it is in
				// the window; by date only as a fallback (a tag's created
				// stamp can differ from its commit's date by seconds).
				pw := newRepoWindow(d)
				if n, ok := pw.sinceSHA(latestTag.Commit.SHA); ok {
					ri.CommitsSinceTag = n
				} else if d.LastCommit == nil || d.LastCommit.SHA != latestTag.Commit.SHA {
					n, lower := pw.countAfter(latestTag.Commit.Created.Time)
					ri.CommitsSinceTag, ri.CommitsSinceTagCapped = n, lower && n > 0
				}
			}
			for _, tg := range d.Tags {
				rel := model.Release{
					Project: p.Slug, ProjectName: p.Name, Icon: p.Icon, Category: p.Category,
					Tag: tg.Name, Date: tagDate(tg, rels), URL: tagURL(r.HTMLURL, tg.Name, rels),
				}
				if rr, ok := rels[tg.Name]; ok && rr.Name != "" && rr.Name != tg.Name {
					rel.Title = rr.Name
				}
				allReleases = append(allReleases, rel)
			}
			sort.SliceStable(allReleases, func(i, j int) bool { return allReleases[i].Date.After(allReleases[j].Date) })
		}
	}
	p.Repo = ri

	// Mirror (model.Project.GitHub keeps its v0.1 name).
	if m := in.Mirror; m != nil {
		p.GitHub = &model.GitHubInfo{FullName: m.FullName, HTMLURL: m.HTMLURL, Stars: m.Stars, OpenIssues: m.OpenIssues, LatestRelease: m.LatestRelease}
	}

	// Deploy.
	var primaryC *ContainerRef
	if len(in.Containers) > 0 {
		primaryC = &in.Containers[0]
	}
	latest := ""
	if ri != nil {
		latest = ri.LatestTag
	}
	var dep *model.DeployInfo
	containerKnown := primaryC != nil && in.DockerKnown
	switch {
	case rp.FDroid != nil && (in.FDroid != nil || containerKnown):
		dep = &model.DeployInfo{Source: "fdroid", Where: "F-Droid", Latest: latest, CheckedAt: in.FDroidAt}
		if in.FDroid != nil {
			dep.Version, dep.State = in.FDroid.VersionName, "published"
		}
		if containerKnown {
			dep.Container = primaryC.Name
			if c := primaryC.C; c != nil {
				dep.Image, dep.Status = c.Image, c.Status
				if c.State != "running" {
					dep.State = c.State
				} else if dep.State == "" {
					dep.State = "running"
				}
			} else {
				dep.State = "missing"
			}
			if dep.CheckedAt.IsZero() {
				dep.CheckedAt = in.FeedAt
			}
		}
	case containerKnown:
		dep = &model.DeployInfo{Source: "docker", Where: env.HostLabel, Container: primaryC.Name, Latest: latest, CheckedAt: in.FeedAt, State: "missing"}
		if c := primaryC.C; c != nil {
			dep.Image, dep.State, dep.Status = c.Image, c.State, c.Status
			dep.Version = DeployedVersion(c.Image, c.Labels)
		}
	}
	if dep != nil {
		dep.Lagging = Lagging(dep.Latest, dep.Version)
	}
	p.Deploy = dep
	running := containerKnown && primaryC.C != nil && primaryC.C.State == "running"
	containerDown := containerKnown && (primaryC.Declared || primaryC.C != nil) && !running
	for i := range allReleases {
		allReleases[i].Deployed = dep != nil && SameVersion(allReleases[i].Tag, dep.Version)
	}
	p.Releases = allReleases
	if len(p.Releases) > 8 {
		p.Releases = append([]model.Release(nil), allReleases[:8]...)
	}

	// Probes. A homepage probe is listed with the others, and that is all
	// (spec ruling 3): only projects-file urls feed the tier and Fix.
	probeUp := false
	var downProbes []ProbeData
	for _, pd := range in.Probes {
		pi := model.ProbeInfo{Label: pd.Label, URL: pd.URL, ProbeURL: pd.ProbeURL, History: pd.History, Homepage: pd.Homepage}
		if pd.Last != nil && pd.Last.Refused {
			// Not checked (a homepage at a local or private address): neither
			// up nor down, and nothing else follows from it.
			pi.NotProbed = "local or private address"
			p.Probes = append(p.Probes, pi)
			continue
		}
		if pd.Last != nil {
			pi.Up, pi.Code, pi.LatencyMS, pi.CheckedAt = pd.Last.Up, pd.Last.Code, pd.Last.LatencyMS, pd.Last.At
			switch {
			case pd.Homepage:
			case pd.Last.Up:
				probeUp = true
			default:
				downProbes = append(downProbes, pd)
			}
		}
		pi.UptimePct = uptime(pd.History, pd.Last)
		p.Probes = append(p.Probes, pi)
	}

	since := downSinceFor(in, primaryC, downProbes)

	// Tier. Before the repo list is read the project's commits are unknown:
	// the tier is shown, but not fingerprinted (no event when the forge answers).
	archived := primary != nil && primary.Repo.Archived
	tierKnown := in.GiteaKnown || fromNotes || rp.Retired
	p.Tier, p.TierReason = Tier(TierInput{
		Retired: rp.Retired, RepoArchived: archived, Hold: rp.Hold, LastCommit: tierCommit,
		Running: running, ProbeUp: probeUp, CommitsUnknown: !tierKnown, FromNotes: fromNotes, Loc: loc, Forge: p.Forge,
	}, now)

	// Hold.
	var holdN int
	var holdLower bool
	if h := rp.Hold; h != nil {
		holdN, holdLower = win.countAfter(endOfDay(h.Since, loc))
		p.Hold = &model.Hold{Kind: h.Kind, Reason: h.Reason, Since: h.Since, CommitsSince: holdN, Contradicted: holdN >= HoldContradictFloor}
	}

	// CI.
	var ciLatest, ciLatestDone *forge.Run
	if primary != nil && primary.Detail != nil {
		for i, r := range primary.Detail.Runs {
			if i >= 5 {
				break
			}
			p.CI = append(p.CI, model.CIRun{
				ID: r.ID, Title: r.DisplayTitle, Branch: r.HeadBranch, Workflow: r.Workflow(), Status: r.Status,
				Conclusion: r.Conclusion, URL: r.HTMLURL, StartedAt: r.StartedAt.Time, CompletedAt: r.CompletedAt.Time,
			})
			run := &primary.Detail.Runs[i]
			if r.HeadBranch != primary.Repo.DefaultBranch {
				continue
			}
			if ciLatest == nil {
				ciLatest = run
			}
			if ciLatestDone == nil && r.Done() {
				ciLatestDone = run
			}
		}
	}
	ciFailing := ciLatestDone != nil && ciLatestDone.Conclusion == "failure"

	// Issues and PRs: the full repos', then the mirror's; 10 each, newest
	// updated first.
	var gi, prs []model.IssueInfo
	for _, rd := range in.Repos {
		if rd.Detail == nil {
			continue
		}
		for _, is := range rd.Detail.Issues {
			gi = append(gi, issueInfo(is, primaryForge, p.Forge))
		}
		for _, is := range rd.Detail.PRs {
			prs = append(prs, issueInfo(is, primaryForge, p.Forge))
		}
	}
	sortIssues(gi)
	sortIssues(prs)
	gi, prs = capN(gi, 10), capN(prs, 10)
	mirrorName := forgeName(env, mirrorForge)
	if m := in.Mirror; m != nil {
		var mi []model.IssueInfo
		for _, is := range m.Issues {
			ii := issueInfo(is, mirrorForge, mirrorName)
			ii.Mirror = true
			mi = append(mi, ii)
		}
		sortIssues(mi)
		gi = append(gi, capN(mi, 10)...)
	}
	p.Issues, p.PRs = gi, prs

	// Recent commits.
	for i, c := range commits {
		if i >= 5 {
			break
		}
		short := c.c.SHA
		if len(short) > 7 {
			short = short[:7]
		}
		p.Commits = append(p.Commits, model.CommitInfo{SHA: c.c.SHA, Short: short, Message: c.c.Subject(), Author: c.c.AuthorName(), Date: c.when, URL: c.c.HTMLURL})
	}

	// Changelog and plans (primary).
	if primary != nil && primary.Files != nil {
		if cl := primary.Files.Changelog; cl != nil {
			items := cl.Unreleased
			if len(items) > 6 {
				items = items[:6]
			}
			p.Unreleased = &model.ChangelogInfo{Count: len(cl.Unreleased), Items: append([]string{}, items...), LastVersion: cl.LastVersion, LastDate: cl.LastDate, URL: primary.Files.ChangelogURL}
		}
		for _, pl := range primary.Files.Plans {
			if len(p.Plans) == MaxPlans {
				break
			}
			if PlanInProgress(pl.Done, pl.Total, pl.Date, now) {
				p.Plans = append(p.Plans, model.PlanInfo{Path: pl.Path, Title: pl.Title, Date: pl.Date, Done: pl.Done, Total: pl.Total, URL: pl.URL})
			}
		}
	}

	// Notes.
	for _, n := range in.Notes {
		ni := model.NoteInfo{File: n.File, Name: n.Name, Description: n.Description, IndexLine: n.IndexLine, Body: n.Body, Modified: n.Modified}
		if commitsKnown {
			written := n.Modified
			if n.ModifiedDateOnly {
				written = endOfDay(written, loc)
			}
			if c, lower := win.countAfter(written); c > 0 {
				ni.Stale = true
				ni.StaleReason = "Written " + ShortDate(n.Modified, now, loc) + "; " + fmtCommits(c, lower) + " since"
			}
		}
		p.Notes = append(p.Notes, ni)
	}

	// Signals.
	p.Signals = signals(p, dep, running, ri, in.Mirror, mirrorName, ciLatest, holdLower, in.FDroidIndexRead)

	// Headline and actions.
	p.Headline, p.HeadlineLevel = headline(env, p, dep, containerDown, primaryC, downProbes, ciFailing, ciLatestDone, holdLower, !tierKnown, fromNotes)
	if p.Tier != model.TierArchived {
		p.Actions = actions(env, p, in, dep, containerDown, primaryC, downProbes, since, holdLower, lastCommit)
	}
	SortActions(p.Actions)

	// Links and sources.
	if ri != nil && ri.HTMLURL != "" {
		p.Links = append(p.Links, model.Link{Label: p.Forge, URL: ri.HTMLURL, Kind: "repo"})
	}
	mirrorKind := "mirror"
	if env.ForgeKinds[mirrorForge] == "github" {
		mirrorKind = "github"
	}
	if p.GitHub != nil && p.GitHub.HTMLURL != "" {
		p.Links = append(p.Links, model.Link{Label: mirrorName, URL: p.GitHub.HTMLURL, Kind: mirrorKind})
	} else if in.MirrorURL != "" {
		p.Links = append(p.Links, model.Link{Label: mirrorName, URL: in.MirrorURL, Kind: mirrorKind})
	}
	for _, u := range rp.URLs {
		if u.URL != "" {
			p.Links = append(p.Links, model.Link{Label: u.Label, URL: u.URL, Kind: "live"})
		}
	}
	if p.Unreleased != nil && p.Unreleased.URL != "" {
		p.Links = append(p.Links, model.Link{Label: "Changelog", URL: p.Unreleased.URL, Kind: "docs"})
	}
	p.Sources = stamps(in, primary, primaryForge)

	fp := fingerprint(p, in, primary, dep, primaryC, commitsKnown, lastCommit, lastSHA, allTimes, rels)
	fp.NotesLabel = env.NotesLabel
	if !tierKnown {
		fp.Tier = ""
	}
	if fp.ContainerState != nil {
		cs := since.container
		fp.ContainerSince = &cs
	}
	if fp.Probes != nil && len(since.probes) > 0 {
		fp.ProbeDown = since.probes
	}
	normalizeProject(&p)
	p.Hash = Hash(p)
	return Built{Project: p, AllReleases: allReleases, FP: fp}
}

func tagDate(tg forge.Tag, rels map[string]forge.Release) time.Time {
	if r, ok := rels[tg.Name]; ok {
		if !r.PublishedAt.IsZero() {
			return r.PublishedAt.Time
		}
		if !r.CreatedAt.IsZero() {
			return r.CreatedAt.Time
		}
	}
	return tg.Commit.Created.Time
}

// issueInfo is an issue or PR read from forge id, which the page calls name.
func issueInfo(is forge.Issue, id, name string) model.IssueInfo {
	ii := model.IssueInfo{Number: is.Number, Title: is.Title, URL: is.HTMLURL, Author: is.User.Login, Comments: is.Comments,
		CreatedAt: is.CreatedAt.Time, UpdatedAt: is.UpdatedAt.Time, Source: id, Forge: name}
	for _, l := range is.Labels {
		ii.Labels = append(ii.Labels, model.Label{Name: l.Name, Color: strings.TrimPrefix(l.Color, "#")})
	}
	return ii
}

func sortIssues(is []model.IssueInfo) {
	sort.SliceStable(is, func(i, j int) bool { return is[i].UpdatedAt.After(is[j].UpdatedAt) })
}

func capN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func uptime(h []model.ProbePoint, last *probe.Result) float64 {
	if len(h) == 0 {
		if last != nil && last.Up {
			return 100
		}
		return 0
	}
	up := 0
	for _, pt := range h {
		if pt.Up {
			up++
		}
	}
	return float64(int(float64(up)*1000/float64(len(h))+0.5)) / 10
}

func itoa(n int) string { return CountText(n, false) }

func signals(p model.Project, dep *model.DeployInfo, running bool, ri *model.RepoInfo, gh *forge.Mirror, ghName string, ci *forge.Run, holdLower, fdroidRead bool) []model.Signal {
	var s []model.Signal
	add := func(kind string, level model.Level, icon, text, href string) {
		s = append(s, model.Signal{Kind: kind, Level: level, Icon: icon, Text: text, Href: href})
	}
	if dep != nil {
		v := dep.Version
		switch {
		case dep.Source == "fdroid" && dep.State == "published":
			text := "On F-Droid"
			if v != "" {
				text = v + " on F-Droid"
			}
			add("deploy", model.LevelGood, "package", text, "")
		case dep.Source == "fdroid" && dep.State == "running":
			// The repo container runs, but the package was not seen in the index.
			add("deploy", model.LevelGood, "package", "F-Droid repo running", "")
			if fdroidRead {
				add("fdroid-index", model.LevelWarn, "circle-dashed", "Package not in the F-Droid index", "")
			} else {
				add("fdroid-index", model.LevelInfo, "circle-dashed", "F-Droid index not read", "")
			}
		case dep.Source == "fdroid":
			add("deploy", model.LevelAlert, "package", "F-Droid repo "+stateWord(dep.State), "")
		case running:
			text := "Running"
			if v != "" {
				text = v + " running"
			}
			add("deploy", model.LevelGood, "container", text, "")
		case dep.State == "missing":
			add("deploy", model.LevelAlert, "container", "Container missing", "")
		default:
			text := "Container " + stateWord(dep.State)
			if v != "" {
				text = v + " " + stateWord(dep.State)
			}
			add("deploy", model.LevelAlert, "container", text, "")
		}
		if dep.Lagging {
			href := ""
			if ri != nil {
				href = ri.HTMLURL + "/releases/tag/" + url.PathEscape(dep.Latest)
			}
			add("lagging", model.LevelWarn, "circle-arrow-up", dep.Latest+" ready", href)
		}
	}
	if ri != nil && ri.LatestTag != "" && ri.CommitsSinceTag > 0 {
		add("since-tag", model.LevelInfo, "git-commit-horizontal", fmtCommits(ri.CommitsSinceTag, ri.CommitsSinceTagCapped)+" since "+ri.LatestTag,
			ri.HTMLURL+"/compare/"+url.PathEscape(ri.LatestTag)+"..."+url.PathEscape(ri.DefaultBranch))
	}
	if ci != nil {
		switch {
		case !ci.Done():
			add("ci", model.LevelInfo, "loader-circle", "CI running", ci.HTMLURL)
		case ci.Conclusion == "success":
			add("ci", model.LevelGood, "circle-check", "CI passing", ci.HTMLURL)
		case ci.Conclusion == "failure":
			add("ci", model.LevelAlert, "circle-x", "CI failing", ci.HTMLURL)
		default:
			add("ci", model.LevelInfo, "circle-minus", "CI "+ci.Conclusion, ci.HTMLURL)
		}
	}
	if ri != nil && ri.OpenIssues > 0 {
		add("issues", model.LevelInfo, "circle-dot", plural(ri.OpenIssues, "issue", "issues"), ri.HTMLURL+"/issues")
	}
	if gh != nil && gh.OpenIssues > 0 {
		add("gh-issues", model.LevelInfo, "github", plural(gh.OpenIssues, ghName+" issue", ghName+" issues"), gh.HTMLURL+"/issues")
	}
	if ri != nil && ri.OpenPRs > 0 {
		add("prs", model.LevelInfo, "git-pull-request", plural(ri.OpenPRs, "PR", "PRs"), ri.HTMLURL+"/pulls")
	}
	if u := p.Unreleased; u != nil && u.Count > 0 {
		add("unreleased", model.LevelInfo, "list-plus", plural(u.Count, "unreleased change", "unreleased changes"), u.URL)
	}
	if len(p.Plans) > 0 {
		pl := p.Plans[0]
		if pl.Done >= pl.Total {
			add("plan", model.LevelGood, "list-checks", "Plan done "+itoa(pl.Done)+"/"+itoa(pl.Total), pl.URL)
		} else {
			add("plan", model.LevelInfo, "list-checks", "Plan "+itoa(pl.Done)+"/"+itoa(pl.Total), pl.URL)
		}
	}
	urlProbes := 0
	for _, pr := range p.Probes {
		if !pr.Homepage {
			urlProbes++
		}
	}
	for _, pr := range p.Probes {
		if pr.CheckedAt.IsZero() || pr.Homepage {
			continue
		}
		prefix := ""
		if urlProbes > 1 {
			prefix = pr.Label + " "
		}
		if pr.Up {
			add("probe", model.LevelGood, "activity", prefix+"up "+itoa(pr.LatencyMS)+" ms", pr.URL)
		} else {
			add("probe", model.LevelAlert, "activity", prefix+"down", pr.URL)
		}
	}
	for _, n := range p.Notes {
		if n.Stale {
			add("note", model.LevelWarn, "notebook-pen", "Note stale: "+strings.TrimPrefix(n.StaleReason[strings.Index(n.StaleReason, ";")+1:], " "), "")
			break
		}
	}
	if h := p.Hold; h != nil {
		icon := "pause"
		if h.Kind == "frozen" {
			icon = "snowflake"
		}
		if h.Contradicted {
			add("hold", model.LevelWarn, icon, "Still "+strings.ToLower(holdWord(h.Kind))+"? "+fmtCommits(h.CommitsSince, holdLower)+" since", "")
		} else {
			add("hold", model.LevelInfo, icon, holdWord(h.Kind), "")
		}
	}
	return s
}

func headline(env Env, p model.Project, dep *model.DeployInfo, containerDown bool, pc *ContainerRef, down []ProbeData, ciFailing bool, ciDone *forge.Run, holdLower, giteaPending, fromNotes bool) (string, model.Level) {
	now, loc := env.Now, env.Loc
	last, none := "Last commit ", ", no commits yet"
	if fromNotes {
		last, none = "Notes updated ", ", no notes yet"
	}
	switch {
	case p.Tier == model.TierArchived:
		return p.TierReason, model.LevelInfo
	case containerDown:
		return containerText(env, pc), model.LevelAlert
	case len(down) > 0:
		return down[0].Label + " is not answering (" + probeProblem(down[0].Last) + ")", model.LevelAlert
	case ciFailing && p.Hold == nil:
		// A held project is not being worked on: its hold says more than a
		// stale red run (the CI chip still shows it).
		return "CI is failing on " + ciDone.HeadBranch + ": " + ciDone.DisplayTitle, model.LevelAlert
	case dep != nil && dep.Lagging:
		if dep.Source == "fdroid" {
			return dep.Latest + " is ready; F-Droid has " + dep.Version, model.LevelWarn
		}
		return dep.Latest + " is ready; production runs " + dep.Version, model.LevelWarn
	case p.Hold != nil && p.Hold.Contradicted:
		return holdWord(p.Hold.Kind) + " since " + ShortDate(p.Hold.Since, now, loc) + ", but " + fmtCommits(p.Hold.CommitsSince, holdLower) + " since", model.LevelWarn
	case p.Hold != nil:
		s := holdWord(p.Hold.Kind) + " since " + ShortDate(p.Hold.Since, now, loc)
		if p.Hold.Reason != "" {
			s += ": " + p.Hold.Reason
		}
		return s, model.LevelInfo
	case p.Tier == model.TierActive && p.Repo != nil && p.Repo.LatestTag != "" && p.Repo.CommitsSinceTag > 0:
		return fmtCommits(p.Repo.CommitsSinceTag, p.Repo.CommitsSinceTagCapped) + " since " + p.Repo.LatestTag, model.LevelGood
	case p.Tier == model.TierActive:
		return last + DaysAgo(p.LastActivity, now, loc), model.LevelGood
	case p.Tier == model.TierLive:
		what := "Answering"
		if dep != nil && (dep.State == "running" || dep.State == "published") {
			what = "Running"
			if dep.Version != "" {
				what += " " + dep.Version
			}
		}
		if p.LastActivity.IsZero() {
			if giteaPending {
				return what + "; " + notReadYet(p.Forge), model.LevelGood
			}
			return what + none, model.LevelGood
		}
		return what + ", quiet for " + For(p.LastActivity, now), model.LevelGood
	case p.Tier == model.TierQuiet:
		return last + DaysAgo(p.LastActivity, now, loc), model.LevelInfo
	}
	return p.TierReason, model.LevelInfo
}

// containerText says what is wrong with the container and nothing about when:
// docker's status ("Exited (1) 3 minutes ago") reads differently every
// minute. The Fix action's At carries when it stopped, and the page ages it.
func containerText(env Env, pc *ContainerRef) string {
	if pc.C == nil {
		return "Container " + pc.Name + " is missing on " + env.HostLabel
	}
	s := "Container " + pc.Name + " is " + stateWord(pc.C.State)
	if st := docker.ParseStatus(pc.C.Status); st.HasExitCode {
		s += " (exit code " + itoa(st.ExitCode) + ")"
	}
	return s
}

// downSince dates the Fix actions: when the primary container entered its
// state and each down probe was first seen down, so the needs-you card ages
// with the outage and the hash does not move on every poll.
type downSince struct {
	container time.Time
	probes    map[string]time.Time // label -> first seen down
}

// downSinceFor keeps the stored date while the state holds and starts a new
// one when it changes: from docker's own age for the state ("Exited (0) 6
// days ago" is six days before the feed was written), or from this feed when
// docker gives none (a missing container).
func downSinceFor(in Input, pc *ContainerRef, down []ProbeData) downSince {
	var ds downSince
	prev := in.Prev
	if pc != nil && in.DockerKnown {
		state := "missing"
		ds.container = in.FeedAt
		if pc.C != nil {
			state = pc.C.State
			if st := docker.ParseStatus(pc.C.Status); st.Aged {
				ds.container = in.FeedAt.Add(-st.Age)
			}
		}
		if prev != nil && prev.ContainerSince != nil && prev.ContainerState != nil && *prev.ContainerState == state && prev.ContainerName == pc.Name {
			ds.container = *prev.ContainerSince
		}
	}
	for _, d := range down {
		if ds.probes == nil {
			ds.probes = map[string]time.Time{}
		}
		at := d.Last.At
		if prev != nil {
			if t, ok := prev.ProbeDown[d.Label]; ok && !t.IsZero() {
				at = t
			}
		}
		ds.probes[d.Label] = at
	}
	return ds
}

func actions(env Env, p model.Project, in Input, dep *model.DeployInfo, containerDown bool, pc *ContainerRef, down []ProbeData, since downSince, holdLower bool, lastCommit time.Time) []model.Action {
	now, loc := env.Now, env.Loc
	var as []model.Action
	add := func(verb, text, source string, level model.Level, at time.Time, href string, heuristic bool) {
		as = append(as, model.Action{Project: p.Slug, ProjectName: p.Name, Icon: p.Icon, Verb: verb, Text: text,
			Source: source, Level: level, At: at, Href: href, Heuristic: heuristic})
	}
	if containerDown {
		add("Fix", containerText(env, pc), "Docker", model.LevelAlert, since.container, "", false)
	}
	for _, d := range down {
		add("Fix", d.Label+" is not answering ("+probeProblem(d.Last)+")", "Probe", model.LevelAlert, since.probes[d.Label], d.URL, false)
	}
	if dep != nil && dep.Lagging {
		// Dated from the tag; an undated tag leaves At zero (the page then
		// shows no time) rather than taking the clock, which would move the
		// hash on every assemble.
		href := ""
		var at time.Time
		if p.Repo != nil {
			href = p.Repo.HTMLURL + "/releases/tag/" + url.PathEscape(dep.Latest)
			at = p.Repo.LatestTagDate
		}
		if dep.Source == "fdroid" {
			add("Deploy", dep.Latest+" to F-Droid, published "+dep.Version, p.Forge+" tags + F-Droid index", model.LevelWarn, at, href, false)
		} else {
			add("Deploy", dep.Latest+" to production, running "+dep.Version, p.Forge+" tags + Docker", model.LevelWarn, at, href, false)
		}
	}
	draft := map[string]bool{} // by URL; a draft is not waiting for review
	for _, rd := range in.Repos {
		if rd.Detail != nil {
			for _, pr := range rd.Detail.PRs {
				if pr.Draft {
					draft[pr.HTMLURL] = true
				}
			}
		}
	}
	flagged := forgeNeeds(env, in, add)
	for _, pr := range p.PRs {
		if !draft[pr.URL] && !flagged[pr.URL] && now.Sub(pr.UpdatedAt) >= StalePRDays*24*time.Hour {
			add("Review", "#"+itoa(pr.Number)+" "+pr.Title+" (no update for "+For(pr.UpdatedAt, now)+")", p.Forge, model.LevelWarn, pr.UpdatedAt, pr.URL, false)
		}
	}
	if h := p.Hold; h != nil && h.Contradicted {
		add("Confirm", "Still "+strings.ToLower(holdWord(h.Kind))+"? "+fmtCommits(h.CommitsSince, holdLower)+" since "+ShortDate(h.Since, now, loc),
			"Projects file hold vs "+p.Forge+" commits", model.LevelWarn, lastCommit, "", false)
	}
	n := 0
	for _, note := range in.Notes {
		for _, c := range note.Clauses {
			if n >= MaxNoteActions {
				break
			}
			if dep != nil && dep.Lagging && SameDeploy(c, dep.Latest) {
				continue
			}
			n++
			// When it was written is At: the page adds it to the source
			// and ages it, so the source holds still between polls.
			add("Decide", c, env.NotesLabel, model.LevelInfo, note.Modified, "", true)
		}
	}
	return as
}

// forgeName is how the page names the forge with config id id.
func forgeName(env Env, id string) string {
	if n := env.ForgeNames[id]; n != "" {
		return n
	}
	return ForgeName(env.ForgeKinds[id], "")
}

// forgeNeeds adds the actions the forges flag as waiting on the user, one per
// issue or PR (a label beats an assignment, which beats a review request),
// and returns the URLs it covered. Labels read the primaries' issues and PRs
// and the mirror's issues; assignments and review requests read the primaries.
func forgeNeeds(env Env, in Input, add func(verb, text, source string, level model.Level, at time.Time, href string, heuristic bool)) map[string]bool {
	ny := env.NeedsYou
	done := map[string]bool{}
	labelled := func(is forge.Issue) bool {
		for _, l := range is.Labels {
			for _, want := range ny.Labels {
				if strings.EqualFold(l.Name, want) {
					return true
				}
			}
		}
		return false
	}
	named := func(us []forge.User, login string) bool {
		if login == "" {
			return false
		}
		for _, u := range us {
			if strings.EqualFold(u.Login, login) {
				return true
			}
		}
		return false
	}
	consider := func(is forge.Issue, forgeID string, isPR, primary bool) {
		if is.State != "" && is.State != "open" || done[is.HTMLURL] {
			return
		}
		me := in.Me[forgeID]
		switch {
		case labelled(is):
			add("Decide", is.Title, forgeName(env, forgeID)+" label", model.LevelInfo, is.UpdatedAt.Time, is.HTMLURL, false)
		case primary && ny.Assigned && named(is.Assignees, me):
			add("Decide", is.Title, "Assigned to you", model.LevelInfo, is.UpdatedAt.Time, is.HTMLURL, false)
		case primary && isPR && ny.ReviewRequests && named(is.RequestedReviewers, me):
			add("Review", is.Title, "Review requested", model.LevelWarn, is.UpdatedAt.Time, is.HTMLURL, false)
		default:
			return
		}
		done[is.HTMLURL] = true
	}
	for _, rd := range in.Repos {
		if rd.Detail == nil {
			continue
		}
		for _, is := range rd.Detail.Issues {
			consider(is, rd.Repo.Forge, false, true)
		}
		for _, is := range rd.Detail.PRs {
			consider(is, rd.Repo.Forge, true, true)
		}
	}
	if m := in.Mirror; m != nil {
		for _, is := range m.Issues {
			consider(is, m.Forge, false, false)
		}
	}
	return done
}

// stamps say what fed the project and when, each forge under its config id
// (the names of the site's source statuses): forgeID is the primary's.
func stamps(in Input, primary *RepoData, forgeID string) []model.SourceStamp {
	var st []model.SourceStamp
	if primary != nil && primary.Detail != nil {
		st = append(st, model.SourceStamp{Name: forgeID, At: primary.Detail.FetchedAt})
	}
	if primary != nil && primary.Files != nil {
		st = append(st, model.SourceStamp{Name: forgeID + " files", At: primary.Files.FetchedAt})
	}
	if len(in.Containers) > 0 && in.DockerKnown {
		st = append(st, model.SourceStamp{Name: "docker", At: in.FeedAt})
	}
	var probeAt time.Time
	for _, pd := range in.Probes {
		if pd.Last != nil && pd.Last.At.After(probeAt) {
			probeAt = pd.Last.At
		}
	}
	if !probeAt.IsZero() {
		st = append(st, model.SourceStamp{Name: "probes", At: probeAt})
	}
	if len(in.Notes) > 0 {
		st = append(st, model.SourceStamp{Name: "notes", At: in.NotesAt})
	}
	if in.FDroid != nil {
		st = append(st, model.SourceStamp{Name: "fdroid", At: in.FDroidAt})
	}
	if in.Mirror != nil {
		st = append(st, model.SourceStamp{Name: in.Mirror.Forge, At: in.Mirror.FetchedAt})
	}
	return st
}

// normalizeProject turns nil slices into empty ones so JSON has arrays.
func normalizeProject(p *model.Project) {
	if p.Stack == nil {
		p.Stack = []string{}
	}
	if p.Probes == nil {
		p.Probes = []model.ProbeInfo{}
	}
	for i := range p.Probes {
		if p.Probes[i].History == nil {
			p.Probes[i].History = []model.ProbePoint{}
		}
	}
	if p.Signals == nil {
		p.Signals = []model.Signal{}
	}
	if p.Actions == nil {
		p.Actions = []model.Action{}
	}
	if p.Releases == nil {
		p.Releases = []model.Release{}
	}
	if p.Plans == nil {
		p.Plans = []model.PlanInfo{}
	}
	if p.Issues == nil {
		p.Issues = []model.IssueInfo{}
	}
	for i := range p.Issues {
		if p.Issues[i].Labels == nil {
			p.Issues[i].Labels = []model.Label{}
		}
	}
	if p.PRs == nil {
		p.PRs = []model.IssueInfo{}
	}
	for i := range p.PRs {
		if p.PRs[i].Labels == nil {
			p.PRs[i].Labels = []model.Label{}
		}
	}
	if p.CI == nil {
		p.CI = []model.CIRun{}
	}
	if p.Commits == nil {
		p.Commits = []model.CommitInfo{}
	}
	if p.Activity == nil {
		p.Activity = make([]int, 12)
	}
	if p.Notes == nil {
		p.Notes = []model.NoteInfo{}
	}
	if p.Ideas == nil {
		p.Ideas = []model.Idea{}
	}
	if p.Relations == nil {
		p.Relations = []model.Relation{}
	}
	if p.Links == nil {
		p.Links = []model.Link{}
	}
	if p.Sources == nil {
		p.Sources = []model.SourceStamp{}
	}
	if p.Unreleased != nil && p.Unreleased.Items == nil {
		p.Unreleased.Items = []string{}
	}
}
