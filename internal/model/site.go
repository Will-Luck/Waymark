// Package model is the contract between the collectors (internal/app) and the
// web layer (internal/web). Everything the pages show is in Site; the web layer
// never reaches a source directly.
package model

import "time"

type Tier string

const (
	TierActive   Tier = "active"
	TierLive     Tier = "live"
	TierQuiet    Tier = "quiet"
	TierDormant  Tier = "dormant"
	TierHold     Tier = "hold"
	TierArchived Tier = "archived"
)

// TierOrder is the board's section order.
var TierOrder = []Tier{TierActive, TierLive, TierHold, TierQuiet, TierDormant, TierArchived}

type Level string

const (
	LevelGood  Level = "good"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelAlert Level = "alert"
)

type Site struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Sources     []SourceStatus `json:"sources"`
	Stats       Stats          `json:"stats"`
	Categories  []Category     `json:"categories"`
	// Projects sorted by TierOrder, then LastActivity newest first.
	Projects []Project `json:"projects"`
	NeedsYou []Action  `json:"needs_you"`
	// Events newest first, last 14 days.
	Events []Event `json:"events"`
	// Releases across all projects, newest first, last 18 months.
	Releases []Release `json:"releases"`
	Heatmap  Heatmap   `json:"heatmap"`
	Ideas    []Idea    `json:"ideas"`
	Graph    Graph     `json:"graph"`
	// RoundupDate is the date the ideas snapshot was produced (YYYY-MM-DD).
	RoundupDate string `json:"roundup_date"`
	Meta        Meta   `json:"meta"`
}

// Meta is what the page says about itself: its title, the notes panel's
// label, and which optional sources it has. A source that is false hides its
// panels, links and status rows.
type Meta struct {
	Title      string `json:"title"`
	NotesLabel string `json:"notes_label"` // the notes adapter's label, e.g. "Notes"
	Notes      bool   `json:"notes"`       // a notes adapter is configured
	Docker     bool   `json:"docker"`      // a Docker feed or host is configured
	Ideas      bool   `json:"ideas"`       // an ideas file is configured, or there are ideas
	FDroid     bool   `json:"fdroid"`      // a project names an F-Droid package
	// ProjectsFile and IdeasFile: a projects file and an ideas file are
	// configured. The registry source reads them, and its row is named
	// after the ones there are; with neither there is no such row.
	ProjectsFile bool `json:"projects_file,omitempty"`
	IdeasFile    bool `json:"ideas_file,omitempty"`
	// IdeaLabels are the issue labels that make an idea (config ideas.labels).
	IdeaLabels []string `json:"idea_labels,omitempty"`
	// Forges names each forge (by config id) as the page shows it: "Gitea",
	// "GitHub", "Codeberg", with the id added when two share a name
	// ("Gitea (work)").
	Forges map[string]string `json:"forges,omitempty"`
	// Timezone is the configured site.timezone (IANA name); the page writes
	// its dates and times in it. "" means UTC.
	Timezone string `json:"timezone,omitempty"`
	// Demo: the data is demo mode's invented world, and every page says so.
	Demo bool `json:"demo,omitempty"`
}

type SourceStatus struct {
	Name        string    `json:"name"` // a forge id, or registry, heatmap, ideas, docker, probes, notes, fdroid
	RefreshedAt time.Time `json:"refreshed_at"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error,omitempty"`
	// Note says why an OK source shows less than it might, such as a forge
	// that lists nothing without a token.
	Note string `json:"note,omitempty"`
}

type Stats struct {
	Total           int `json:"total"`
	Active          int `json:"active"`
	Live            int `json:"live"`
	Quiet           int `json:"quiet"`
	Dormant         int `json:"dormant"`
	OnHold          int `json:"on_hold"`
	Archived        int `json:"archived"`
	NeedsYou        int `json:"needs_you"`
	OpenIssues      int `json:"open_issues"`
	OpenPRs         int `json:"open_prs"`
	ReleasesLast30d int `json:"releases_last_30d"`
	Ideas           int `json:"ideas"`
}

type Category struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Icon  string `json:"icon"`
	Hue   int    `json:"hue"` // OKLCH hue 0-360 for the icon tile tint
	Count int    `json:"count"`
}

type Project struct {
	Slug     string   `json:"slug"`
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Icon     string   `json:"icon"`
	Summary  string   `json:"summary"`
	What     string   `json:"what"`
	Stack    []string `json:"stack"`
	RunsOn   string   `json:"runs_on"`
	// Auto is true for a repo that is not in the registry.
	Auto bool `json:"auto"`

	Tier       Tier   `json:"tier"`
	TierReason string `json:"tier_reason"`
	// Headline is the one derived status sentence; HeadlineLevel its severity.
	Headline      string `json:"headline"`
	HeadlineLevel Level  `json:"headline_level"`
	Hold          *Hold  `json:"hold,omitempty"`

	// Forge is how the page names the project's primary forge ("Gitea",
	// "GitHub", "Codeberg"); for a project with no repo, the first forge's.
	Forge  string      `json:"forge,omitempty"`
	Repo   *RepoInfo   `json:"repo,omitempty"`
	GitHub *GitHubInfo `json:"github,omitempty"` // the first mirror (v0.1: the GitHub repo)
	Deploy *DeployInfo `json:"deploy,omitempty"`
	Probes []ProbeInfo `json:"probes"`

	Signals    []Signal       `json:"signals"`
	Actions    []Action       `json:"actions"`
	Releases   []Release      `json:"releases"` // last 8 tags, newest first
	Unreleased *ChangelogInfo `json:"unreleased,omitempty"`
	Plans      []PlanInfo     `json:"plans"`
	Issues     []IssueInfo    `json:"issues"` // open, the primary's then the mirror's, max 10 each
	PRs        []IssueInfo    `json:"prs"`
	CI         []CIRun        `json:"ci"`      // newest first, max 5
	Commits    []CommitInfo   `json:"commits"` // newest first, max 5
	// Activity is 12 weekly commit counts, oldest first, current week last.
	Activity     []int     `json:"activity"`
	LastActivity time.Time `json:"last_activity"`
	// ActivityFromNotes: the project has no repo read in full, so LastActivity is when
	// its newest note was written, not a commit.
	ActivityFromNotes bool          `json:"activity_from_notes,omitempty"`
	Notes             []NoteInfo    `json:"notes"`
	Ideas             []Idea        `json:"ideas"`
	Relations         []Relation    `json:"relations"`
	Links             []Link        `json:"links"`
	Sources           []SourceStamp `json:"sources"` // what fed this project and when

	// Hash changes whenever anything shown for the project changes.
	Hash string `json:"hash"`
}

type Hold struct {
	Kind         string    `json:"kind"` // parked | frozen
	Reason       string    `json:"reason"`
	Since        time.Time `json:"since"`
	Contradicted bool      `json:"contradicted"`
	CommitsSince int       `json:"commits_since"`
}

type RepoInfo struct {
	FullName        string    `json:"full_name"`
	HTMLURL         string    `json:"html_url"`
	DefaultBranch   string    `json:"default_branch"`
	Description     string    `json:"description"`
	Private         bool      `json:"private"`
	Archived        bool      `json:"archived"`
	Mirror          bool      `json:"mirror"`
	OpenIssues      int       `json:"open_issues"`
	OpenPRs         int       `json:"open_prs"`
	Stars           int       `json:"stars"`
	UpdatedAt       time.Time `json:"updated_at"`
	LatestTag       string    `json:"latest_tag"`
	LatestTagDate   time.Time `json:"latest_tag_date"`
	CommitsSinceTag int       `json:"commits_since_tag"`
	// CommitsSinceTagCapped is true when CommitsSinceTag is a lower bound (the
	// tag is older than the fetched commit window); show it as "150+".
	CommitsSinceTagCapped bool `json:"commits_since_tag_capped,omitempty"`
}

type GitHubInfo struct {
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Stars         int    `json:"stars"`
	OpenIssues    int    `json:"open_issues"`
	LatestRelease string `json:"latest_release"`
}

type DeployInfo struct {
	Source    string    `json:"source"` // docker | fdroid
	Where     string    `json:"where"`  // config docker.label, or "F-Droid"
	Container string    `json:"container,omitempty"`
	Image     string    `json:"image,omitempty"`
	Version   string    `json:"version"` // "" when unknown
	State     string    `json:"state"`   // running | exited | missing | published
	Status    string    `json:"status"`  // docker's human status, e.g. "Up 3 days"
	Latest    string    `json:"latest"`  // the primary repo's latest tag
	Lagging   bool      `json:"lagging"`
	CheckedAt time.Time `json:"checked_at"`
	// Unconfirmed: the container is not running but has not been seen so for
	// 2 minutes yet: the page says it is checking again, and nothing else
	// follows from it (no Fix, tier change or event).
	Unconfirmed bool `json:"unconfirmed,omitempty"`
}

type ProbeInfo struct {
	Label     string       `json:"label"`
	URL       string       `json:"url"`       // the link shown
	ProbeURL  string       `json:"probe_url"` // the URL actually checked
	Up        bool         `json:"up"`
	Code      int          `json:"code"`
	LatencyMS int          `json:"latency_ms"`
	CheckedAt time.Time    `json:"checked_at"`
	UptimePct float64      `json:"uptime_pct"` // last 24 h
	History   []ProbePoint `json:"history"`    // last 24 h, oldest first
	// Homepage: a repo's website, probed for "Where it runs" only. It never
	// makes the project Live, raises Fix, or shows as a signal or event.
	Homepage bool `json:"homepage,omitempty"`
	// NotProbed says why the URL was not checked (a homepage at a local or
	// private address); it is then neither up nor down.
	NotProbed string `json:"not_probed,omitempty"`
	// Unconfirmed: the last check failed but the outage is not confirmed
	// yet (one failed check): the page says it is checking again, and
	// nothing else follows from it (no Fix, tier change or event).
	Unconfirmed bool `json:"unconfirmed,omitempty"`
}

type ProbePoint struct {
	At        time.Time `json:"at"`
	Up        bool      `json:"up"`
	LatencyMS int       `json:"latency_ms"`
}

type Signal struct {
	Kind  string `json:"kind"` // deploy, lagging, since-tag, ci, issues, gh-issues, prs, unreleased, plan, probe, note, hold
	Level Level  `json:"level"`
	Icon  string `json:"icon"`
	Text  string `json:"text"`
	Href  string `json:"href,omitempty"`
}

type Action struct {
	Project     string    `json:"project"`
	ProjectName string    `json:"project_name"`
	Icon        string    `json:"icon"`
	Verb        string    `json:"verb"` // Fix | Deploy | Review | Confirm | Decide
	Text        string    `json:"text"`
	Source      string    `json:"source"` // e.g. "Docker", "Notes" (At says when; the page ages it)
	Heuristic   bool      `json:"heuristic"`
	Level       Level     `json:"level"`
	At          time.Time `json:"at"`
	Href        string    `json:"href,omitempty"`
}

type Release struct {
	Project     string    `json:"project"`
	ProjectName string    `json:"project_name"`
	Icon        string    `json:"icon"`
	Category    string    `json:"category"`
	Tag         string    `json:"tag"`
	Title       string    `json:"title,omitempty"`
	Date        time.Time `json:"date"`
	URL         string    `json:"url"`
	Deployed    bool      `json:"deployed"`
}

type ChangelogInfo struct {
	Count       int      `json:"count"`
	Items       []string `json:"items"` // first 6, markdown-lite source text
	LastVersion string   `json:"last_version,omitempty"`
	LastDate    string   `json:"last_date,omitempty"`
	URL         string   `json:"url"`
}

type PlanInfo struct {
	Path  string    `json:"path"`
	Title string    `json:"title"`
	Date  time.Time `json:"date"`
	Done  int       `json:"done"`
	Total int       `json:"total"`
	URL   string    `json:"url"`
}

type IssueInfo struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Labels    []Label   `json:"labels"`
	Author    string    `json:"author"`
	Comments  int       `json:"comments"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source"` // the id of the forge it was read from
	// Mirror: read through the mirror view (the project's mirror, or a primary
	// its forge gives no full detail for), not the primary's full read.
	Mirror bool   `json:"mirror,omitempty"`
	Forge  string `json:"forge,omitempty"` // that forge's name on the page
}

type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"` // hex without #
}

type CIRun struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Branch      string    `json:"branch"`
	Workflow    string    `json:"workflow"`
	Status      string    `json:"status"`     // queued | in_progress | completed
	Conclusion  string    `json:"conclusion"` // success | failure | cancelled | skipped | ""
	URL         string    `json:"url"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

type CommitInfo struct {
	SHA     string    `json:"sha"`
	Short   string    `json:"short"`
	Message string    `json:"message"` // first line
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
	URL     string    `json:"url"`
}

type NoteInfo struct {
	File        string    `json:"file"`
	Name        string    `json:"name"`
	Description string    `json:"description"` // markdown-lite
	IndexLine   string    `json:"index_line"`  // markdown-lite, from MEMORY.md
	Modified    time.Time `json:"modified"`
	Stale       bool      `json:"stale"`
	StaleReason string    `json:"stale_reason,omitempty"`
	Body        string    `json:"body,omitempty"` // markdown-lite, the note itself (markdown notes only)
}

type Idea struct {
	ID            string `json:"id"`
	Project       string `json:"project"`
	ProjectName   string `json:"project_name"`
	Icon          string `json:"icon"`
	Title         string `json:"title"`
	Pitch         string `json:"pitch"`
	Why           string `json:"why"`
	Size          string `json:"size"` // S | M | L
	Kind          string `json:"kind"` // quick-win | bold | connective | hardening
	ExistingIssue int    `json:"existing_issue,omitempty"`
	FiledIssue    int    `json:"filed_issue,omitempty"`
	FiledURL      string `json:"filed_url,omitempty"`
	// FileURL opens a prefilled new-issue form on the project's primary repo;
	// empty when the project has no repo or the idea is already filed.
	FileURL string `json:"file_url,omitempty"`
	// Source is "file" (the ideas file) or "issue" (an open issue with an
	// ideas label, which is ExistingIssue at URL).
	Source string `json:"source"`
	URL    string `json:"url,omitempty"`
}

type Relation struct {
	To       string `json:"to"`
	ToName   string `json:"to_name"`
	Kind     string `json:"kind"` // depends-on | client-of | fork-of | deploys | sibling | monitors | supersedes
	Outgoing bool   `json:"outgoing"`
}

type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Kind  string `json:"kind"` // live | repo | github (a GitHub mirror) | mirror (another) | docs | other
}

type SourceStamp struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

type Event struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Project     string    `json:"project"`
	ProjectName string    `json:"project_name"`
	Icon        string    `json:"icon"`
	Kind        string    `json:"kind"` // release, deploy, container, ci, commits, issues, probe, note, tier
	Level       Level     `json:"level"`
	Text        string    `json:"text"`
	Href        string    `json:"href,omitempty"`
}

type Heatmap struct {
	// Days covers 371 days ending today in the configured time zone, oldest first.
	Days  []HeatDay `json:"days"`
	Max   int       `json:"max"`
	Total int       `json:"total"`
}

type HeatDay struct {
	Date    string `json:"date"`    // YYYY-MM-DD
	Weekday int    `json:"weekday"` // 0 = Monday
	Count   int    `json:"count"`
}

type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Tier     Tier    `json:"tier"`
	Icon     string  `json:"icon"`
	Weight   float64 `json:"weight"` // 0..1 from 12-week activity
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// ProjectBySlug returns the project or nil.
func (s *Site) ProjectBySlug(slug string) *Project {
	for i := range s.Projects {
		if s.Projects[i].Slug == slug {
			return &s.Projects[i]
		}
	}
	return nil
}
