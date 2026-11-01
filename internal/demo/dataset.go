// Package demo is Waymark's synthetic world: an invented team, acme, with
// about ten repos on a Gitea forge and a GitHub forge, a Docker feed, notes,
// probes and an F-Droid index, chosen so that every state the site shows
// appears at least once. Demo mode serves it through an in-process fake
// forge (Handler), and the tests and fixtures are built from it. Every date
// is an offset from the now the world is built for, so the demo always looks
// current and a test that passes a fixed now gets the same world each time.
package demo

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/waymark/internal/model"
)

const (
	// Owner owns every repo on both forges.
	Owner = "acme"
	// Login is the user both forges' tokens belong to.
	Login = "mark"
	// GiteaWeb and GitHubWeb are the web roots repo links use. They are
	// placeholders: the fake forge answers the API wherever it listens.
	GiteaWeb  = "https://git.example.com"
	GitHubWeb = "https://github.example.com"
	// ConfigRepo holds the projects file and the ideas file on Gitea.
	ConfigRepo = "waymark-config"
	// baseToken in a served file is replaced by World.Base.
	baseToken = "{base}"
)

const (
	hour = time.Hour
	day  = 24 * time.Hour
)

// World is the whole synthetic dataset. The fake forge reads it under its
// lock on every request, so a test may change it between cycles with Edit.
type World struct {
	mu sync.Mutex

	Now time.Time
	// Base is the address of the server that answers the probes and the
	// F-Droid index; the projects file points at it. Run sets it (SetBase).
	Base string

	Repos      []*Repo
	Containers []Container
	// Probes is the status code /probe/<name> answers.
	Probes map[string]int
	FDroid []App
	// Notes are the markdown notes demo mode writes to its notes folder.
	Notes []Note
	// Events seed the activity feed, so a fresh demo has some history.
	Events []model.Event
	// Labels are the colours (hex without #) the forges give each label.
	Labels map[string]string
	// FailList makes Gitea's repo list answer 502 Bad Gateway.
	FailList bool
}

// Repo is one repo on one forge.
type Repo struct {
	Forge       string // "gitea" or "github"
	Owner, Name string
	ID          int64
	Description string
	Website     string
	Private     bool
	Archived    bool
	Empty       bool
	// Mirror is the forge's own pull-mirror flag (Gitea's "mirror").
	Mirror bool
	// MirrorOf names the repo ("forge:owner/name") this one is a copy of on
	// another forge. Its commits are that repo's, so they count once, there.
	MirrorOf string
	Stars    int
	Created  time.Time
	Commits  []Commit // newest first, the whole default branch
	Tags     []Tag    // newest first
	Releases []Release
	Issues   []Issue // open issues
	PRs      []Issue // open pull requests
	Runs     []CIRun // newest first
	// Actions: the repo has Actions turned on (runs or not).
	Actions bool
	// Files are the default branch's files by path; "{base}" in one is
	// replaced by World.Base when served.
	Files map[string]string
}

type Commit struct {
	SHA, Message string
	Author       Person
	At           time.Time
}

type Person struct{ Login, Name string }

// Tag is a tag on a commit. Message makes it an annotated tag.
type Tag struct {
	Name    string
	SHA     string // the tagged commit
	At      time.Time
	Message string
}

type Release struct {
	Tag, Name  string
	At         time.Time
	Prerelease bool
}

type Issue struct {
	Number           int
	Title, Body      string
	Author           string
	Labels           []string
	Assignees        []string
	Reviewers        []string
	Comments         int
	Created, Updated time.Time
	Draft            bool
	Branch           string // a pull request's head branch
}

type CIRun struct {
	ID, Number         int64
	Title, Branch, SHA string
	Status, Conclusion string // Gitea's words: queued | in_progress | completed; success | failure | ...
	Started, Completed time.Time
}

// Container is one line of the Docker feed.
type Container struct {
	ID, Name, Image string
	State, Status   string
	Created         time.Time
	Labels          map[string]string
}

// App is one package in the F-Droid index.
type App struct {
	Package, Name string
	Versions      []AppVersion // newest first
}

type AppVersion struct {
	Name  string
	Code  int64
	Added time.Time
}

// Note is one markdown note file.
type Note struct {
	File    string
	Body    string
	Updated time.Time
}

// Edit changes the world under its lock, so a running fake forge never
// serves half an edit.
func (w *World) Edit(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn()
}

// SetBase records where the probes and the F-Droid index are served.
func (w *World) SetBase(base string) { w.Edit(func() { w.Base = strings.TrimRight(base, "/") }) }

// Repo returns the repo with that name on forge ("gitea" or "github"), or nil.
func (w *World) Repo(forge, name string) *Repo {
	for _, r := range w.Repos {
		if r.Forge == forge && strings.EqualFold(r.Name, name) {
			return r
		}
	}
	return nil
}

// Container returns the feed's container with that name, or nil.
func (w *World) Container(name string) *Container {
	for i := range w.Containers {
		if w.Containers[i].Name == name {
			return &w.Containers[i]
		}
	}
	return nil
}

// ProjectsYAML is the projects file as the forge serves it.
func (w *World) ProjectsYAML() []byte { return w.configFile("projects.yaml") }

// IdeasJSON is the ideas file as the forge serves it.
func (w *World) IdeasJSON() []byte { return w.configFile("ideas.json") }

func (w *World) configFile(path string) []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return []byte(w.expand(w.Repo("gitea", ConfigRepo).Files[path]))
}

// expand puts the base address into a served file; w.mu is held.
func (w *World) expand(s string) string { return strings.ReplaceAll(s, baseToken, w.Base) }

func (r *Repo) FullName() string { return r.Owner + "/" + r.Name }

// HTMLURL is the repo's web page.
func (r *Repo) HTMLURL() string {
	if r.Forge == "github" {
		return GitHubWeb + "/" + r.FullName()
	}
	return GiteaWeb + "/" + r.FullName()
}

// Updated is when the repo last changed: its newest commit, or its creation.
func (r *Repo) Updated() time.Time {
	if len(r.Commits) > 0 {
		return r.Commits[0].At
	}
	return r.Created
}

// Commit returns the commit at exactly t, which the dataset must hold.
func (r *Repo) commitAt(t time.Time) *Commit {
	for i := range r.Commits {
		if r.Commits[i].At.Equal(t) {
			return &r.Commits[i]
		}
	}
	panic(fmt.Sprintf("demo: %s has no commit at %s", r.FullName(), t))
}

var (
	mark  = Person{"mark", "Mark"}
	priya = Person{"priya", "Priya"}
	lena  = Person{"lena", "Lena"}
	tom   = Person{"tom", "Tom"}
)

// labelColour is a label's colour, grey for one the world does not list.
func (w *World) labelColour(name string) string {
	if c, ok := w.Labels[name]; ok {
		return c
	}
	return "cccccc"
}

// builder makes one repo's history from offsets before now.
type builder struct {
	now time.Time
	r   *Repo
}

func (b *builder) ago(d time.Duration) time.Time { return b.now.Add(-d) }

// commit adds a commit d before now; commits are sorted newest first at the end.
func (b *builder) commit(d time.Duration, who Person, msg string) {
	b.r.Commits = append(b.r.Commits, Commit{Message: msg, Author: who, At: b.ago(d)})
}

// filler adds a commit every step from newest back to oldest (both before now).
func (b *builder) filler(newest, oldest, step time.Duration, msgs []string) {
	i := 0
	for d := newest; d <= oldest; d += step {
		who := mark
		if i%3 == 1 {
			who = priya
		}
		b.commit(d, who, msgs[i%len(msgs)])
		i++
	}
}

// tag tags the commit d before now (renamed "Release <tag>"), with a release
// when rel is set.
func (b *builder) tag(name string, d time.Duration, rel, annotated bool) {
	c := b.r.commitAt(b.ago(d))
	c.Message = "Release " + name
	t := Tag{Name: name, At: c.At}
	if annotated {
		t.Message = name
	}
	b.r.Tags = append(b.r.Tags, t)
	if rel {
		b.r.Releases = append(b.r.Releases, Release{Tag: name, Name: name, At: c.At})
	}
}

// done sorts the history and gives every commit and tag its SHA.
func (b *builder) done() *Repo {
	r := b.r
	sort.SliceStable(r.Commits, func(i, j int) bool { return r.Commits[i].At.After(r.Commits[j].At) })
	for i := range r.Commits {
		r.Commits[i].SHA = sha(r.FullName(), r.Commits[i].At.UTC().Format(time.RFC3339), r.Commits[i].Message)
	}
	for i := range r.Tags {
		r.Tags[i].SHA = r.commitAt(r.Tags[i].At).SHA
	}
	sort.SliceStable(r.Tags, func(i, j int) bool { return r.Tags[i].At.After(r.Tags[j].At) })
	sort.SliceStable(r.Releases, func(i, j int) bool { return r.Releases[i].At.After(r.Releases[j].At) })
	for i := range r.Runs {
		if r.Runs[i].SHA == "" && len(r.Commits) > 0 {
			r.Runs[i].SHA = r.Commits[0].SHA
		}
	}
	return r
}

// sha is a stable 40-hex id for the parts.
func sha(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

func date(t time.Time) string { return t.UTC().Format("2006-01-02") }

var fillerMessages = []string{
	"Update dependencies", "Add tests for the edge cases", "Tidy the README", "Refactor the config loader",
	"Log slow requests", "Fix a typo in the docs", "Speed up the start-up", "Handle an empty response",
}

// Dataset builds the world as it stands at now.
func Dataset(now time.Time) *World {
	now = now.UTC().Truncate(time.Second)
	w := &World{Now: now, Probes: map[string]int{"beacon": 200, "harbour": 200, "atlas": 503}, Labels: map[string]string{
		"needs-you": "e11d48", "decision": "f97316", "idea": "a855f7", "enhancement": "84b6eb",
		"bug": "d73a4a", "docs": "0075ca", "kind/quick-win": "22c55e", "size/S": "64748b",
	}}
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	repo := func(forge, name, desc string, id int64, created time.Duration) *builder {
		return &builder{now: now, r: &Repo{Forge: forge, Owner: Owner, Name: name, ID: id, Description: desc, Created: ago(created), Files: map[string]string{}}}
	}

	// Beacon: the lagging deploy, the plan in progress, the changelog with
	// unreleased items, a labelled needs-you issue, a stale PR, an idea and
	// a filed idea, two notes, and relations to three other projects.
	b := repo("gitea", "beacon", "Status pages and uptime alerts for small teams.", 101, 400*day)
	b.r.Website = "https://status.example.com"
	b.r.Stars, b.r.Actions = 14, true
	b.commit(5*hour, mark, "Show incident duration on the status page")
	b.commit(26*hour, priya, "Retry failed webhook posts once")
	b.commit(44*hour, mark, "Tidy the alert e-mail template")
	b.commit(2*day, mark, "Release v2.4.0")
	b.commit(3*day, priya, "Group checks by region")
	b.commit(5*day, mark, "Cache DNS lookups between checks")
	b.commit(8*day, mark, "Add per-check timeouts")
	b.commit(12*day, mark, "Release v2.3.1")
	b.filler(13*day, 160*day, 3*day, fillerMessages)
	b.tag("v2.4.0", 2*day, true, false)
	b.tag("v2.3.1", 12*day, true, false)
	b.tag("v2.3.0", 34*day, true, false)
	b.tag("v2.2.0", 70*day, false, false)
	b.tag("v2.1.0", 109*day, false, false)
	b.tag("v2.0.0", 139*day, false, false)
	b.r.Releases[0].Name = "v2.4.0: checks by region"
	b.r.Issues = []Issue{
		{Number: 21, Title: "Pick the default sound for new alerts", Author: "priya", Labels: []string{"needs-you"}, Comments: 2, Created: ago(3 * day), Updated: ago(1 * day)},
		{Number: 19, Title: "Webhook retries with backoff", Author: "lena", Labels: []string{"idea"}, Comments: 1, Created: ago(9 * day), Updated: ago(4 * day)},
		{Number: 17, Title: "The status page shows the wrong time zone", Author: "tom", Labels: []string{"bug"}, Comments: 3, Created: ago(10 * day), Updated: ago(6 * day)},
		{Number: 15, Title: "Status badge for READMEs", Author: "mark", Body: "An SVG badge with the current status, for project READMEs.\n\nFrom the ideas list.\nwaymark-idea:beacon-01",
			Created: ago(9 * day), Updated: ago(9 * day)},
	}
	b.r.PRs = []Issue{
		{Number: 22, Title: "Dark theme for status pages", Author: "priya", Branch: "dark-theme", Comments: 4, Created: ago(11 * day), Updated: ago(6 * day)},
		{Number: 23, Title: "SMS alerts", Author: "lena", Branch: "sms", Draft: true, Created: ago(4 * day), Updated: ago(3 * day)},
	}
	b.r.Runs = []CIRun{
		{ID: 1412, Number: 412, Title: "Show incident duration on the status page", Branch: "main", Status: "completed", Conclusion: "success", Started: ago(5*hour - 2*time.Minute), Completed: ago(5*hour - 6*time.Minute)},
		{ID: 1411, Number: 411, Title: "Retry failed webhook posts once", Branch: "main", Status: "completed", Conclusion: "success", Started: ago(26*hour - time.Minute), Completed: ago(26*hour - 5*time.Minute)},
		{ID: 1410, Number: 410, Title: "Dark theme for status pages", Branch: "dark-theme", Status: "completed", Conclusion: "failure", Started: ago(6 * day), Completed: ago(6*day - 4*time.Minute)},
	}
	b.r.Files["CHANGELOG.md"] = "# Changelog\n\nAll notable changes to Beacon are listed here.\n\n## [Unreleased]\n\n### Added\n\n" +
		"- Incident duration on the status page\n- Webhook posts are retried once\n\n### Changed\n\n- A shorter alert e-mail\n\n" +
		"## [2.4.0] - " + date(ago(2*day)) + "\n\n### Added\n\n- Checks grouped by region\n- DNS lookups are cached between checks\n\n" +
		"## [2.3.1] - " + date(ago(12*day)) + "\n\n### Fixed\n\n- The RSS feed's date format\n\n" +
		"## [2.3.0] - " + date(ago(34*day)) + "\n\n### Added\n\n- An RSS feed of incidents\n"
	planDir := "docs/plans/"
	b.r.Files[planDir+date(ago(9*day))+"-incident-timeline.md"] = "# Incident timeline\n\nEvery incident gets a timeline of what happened when.\n\n" +
		"- [x] Store each state change\n- [x] A timeline API\n- [x] The timeline on the status page\n- [ ] Edit an incident's notes\n" +
		"- [ ] A weekly e-mail digest\n- [ ] Export a timeline as CSV\n- [ ] Write the docs\n"
	b.r.Files[planDir+date(ago(20*day))+"-status-api/plan.md"] = "# Status API\n\n- [x] Read-only API\n- [x] Tokens\n- [x] Rate limits\n- [x] Docs\n"
	b.r.Files[planDir+date(ago(40*day))+"-regions.md"] = "# Checks by region\n\n- [x] Region field\n- [x] Group the page\n- [x] Migrate old checks\n"
	b.r.Files[planDir+date(ago(75*day))+"-rss-feed.md"] = "# RSS feed\n\n- [x] Feed of incidents\n- [x] Link from the page\n"
	// Finished plans, enough that the newest ones fill the read cap
	// (forge.PlanCandidateMax): none is in progress, so the board is the same.
	for _, pl := range []struct {
		age   time.Duration
		name  string
		steps int
	}{{48 * day, "search-filters", 3}, {56 * day, "export-csv", 2}, {63 * day, "sso-login", 4},
		{88 * day, "slack-alerts", 2}, {97 * day, "maintenance-mode", 3}, {110 * day, "audit-log", 2}} {
		body := "# " + strings.ReplaceAll(pl.name, "-", " ") + "\n\n"
		for i := 1; i <= pl.steps; i++ {
			body += fmt.Sprintf("- [x] Step %d\n", i)
		}
		b.r.Files[planDir+date(ago(pl.age))+"-"+pl.name+".md"] = body
	}
	b.r.Files[planDir+"README.md"] = "# Plans\n\nOne file per piece of work, named by its start date.\n"
	b.r.Files[planDir+"alerting-notes.md"] = "Loose notes on alerting, not a plan.\n- [ ] not counted\n"
	beacon := b.done()

	// Beacon for Android: the F-Droid package, served by a container whose
	// image is a bare ID.
	ba := repo("gitea", "beacon-app", "Incident alerts for Android.", 102, 300*day)
	ba.commit(34*day, mark, "Release v1.6.0")
	ba.filler(36*day, 150*day, 5*day, fillerMessages)
	ba.tag("v1.6.0", 34*day, true, false)
	ba.tag("v1.5.2", 61*day, true, false)
	ba.tag("v1.5.0", 91*day, true, false)
	ba.r.Issues = []Issue{{Number: 4, Title: "Android Auto support", Author: "tom", Labels: []string{"enhancement"}, Created: ago(50 * day), Updated: ago(40 * day)}}
	ba.r.Files["CHANGELOG.md"] = "# Changelog\n\n## [Unreleased]\n\n## [1.6.0] - " + date(ago(34*day)) + "\n\n- Quiet hours\n"
	beaconApp := ba.done()

	// Harbour: the mirror pair, Gitea primary and a GitHub mirror with stars
	// and issues; deployed and answering, no commits for six weeks.
	hb := repo("gitea", "harbour", "A small container registry with garbage collection.", 103, 500*day)
	hb.r.Actions = true
	hb.commit(45*day, mark, "Release v3.1.0")
	hb.filler(47*day, 170*day, 6*day, fillerMessages)
	hb.tag("v3.1.0", 45*day, true, true)
	hb.tag("v3.0.0", 95*day, true, true)
	hb.r.Issues = []Issue{{Number: 11, Title: "Quotas per namespace", Author: "priya", Created: ago(60 * day), Updated: ago(30 * day)}}
	hb.r.Runs = []CIRun{{ID: 803, Number: 203, Title: "Release v3.1.0", Branch: "main", Status: "completed", Conclusion: "success", Started: ago(45 * day), Completed: ago(45*day - 7*time.Minute)}}
	harbour := hb.done()
	ghHarbour := &Repo{Forge: "github", Owner: Owner, Name: "harbour", ID: 9003, MirrorOf: "gitea:" + harbour.FullName(), Description: harbour.Description, Stars: 128, Created: ago(480 * day),
		Commits: harbour.Commits, Tags: harbour.Tags, Releases: harbour.Releases, Files: map[string]string{},
		Issues: []Issue{
			{Number: 7, Title: "Publish an arm64 image", Author: "lena", Labels: []string{"enhancement"}, Comments: 5, Created: ago(20 * day), Updated: ago(2 * day)},
			{Number: 9, Title: "Garbage collection skips untagged layers", Author: "tom", Labels: []string{"bug"}, Comments: 1, Created: ago(8 * day), Updated: ago(8 * day)},
		}}

	// Atlas: active, deployed and up to date, but its probe is down.
	at := repo("gitea", "atlas", "Map tiles for the team's apps.", 104, 250*day)
	at.r.Actions = true
	at.commit(3*day, priya, "Render street names at zoom 16")
	at.commit(10*day, mark, "Release v0.7.2")
	at.filler(12*day, 140*day, 4*day, fillerMessages)
	at.tag("v0.7.2", 10*day, true, false)
	at.tag("v0.7.0", 40*day, true, false)
	at.r.Issues = []Issue{{Number: 6, Title: "Tiles blur at zoom 18", Author: "tom", Labels: []string{"bug"}, Comments: 2, Created: ago(12 * day), Updated: ago(5 * day)}}
	at.r.Runs = []CIRun{{ID: 2077, Number: 77, Title: "Render street names at zoom 16", Branch: "main", Status: "completed", Conclusion: "success", Started: ago(3 * day), Completed: ago(3*day - 9*time.Minute)}}
	at.r.Files["CHANGELOG.md"] = "# Changelog\n\n## [0.7.2] - " + date(ago(10*day)) + "\n\n### Fixed\n\n- Tiles at the date line\n"
	atlas := at.done()

	// Inkwell: parked six weeks ago, with two commits since (Confirm).
	ik := repo("gitea", "inkwell", "A shared writing pad with comments.", 105, 350*day)
	ik.commit(12*day, priya, "Fix the comment count on long pads")
	ik.commit(25*day, priya, "Update dependencies")
	ik.commit(52*day, mark, "Release v0.4.0")
	ik.filler(56*day, 200*day, 7*day, fillerMessages)
	ik.tag("v0.4.0", 52*day, true, false)
	inkwell := ik.done()

	// Ledger: an auto project (not in the projects file) with CI failing on
	// main, a review request, an assigned issue, an idea, and a container
	// matched by its OCI source label.
	lg := repo("gitea", "ledger", "Billing and invoices API.", 106, 420*day)
	lg.r.Actions = true
	lg.commit(20*hour, mark, "Fix rounding in VAT totals")
	lg.commit(2*day, tom, "Add invoice numbering per year")
	lg.commit(6*day, mark, "Release 1.8.0")
	lg.filler(7*day, 150*day, 3*day, fillerMessages)
	lg.tag("1.8.0", 6*day, true, false)
	lg.tag("1.7.3", 31*day, true, false)
	lg.r.Issues = []Issue{
		{Number: 31, Title: "Choose the rounding rule for VAT", Author: "tom", Assignees: []string{"mark"}, Comments: 6, Created: ago(5 * day), Updated: ago(22 * hour)},
		{Number: 29, Title: "Export invoices as PDF", Author: "priya", Labels: []string{"enhancement"}, Created: ago(30 * day), Updated: ago(14 * day)},
	}
	lg.r.PRs = []Issue{{Number: 33, Title: "Batch import from CSV", Author: "tom", Branch: "csv-import", Reviewers: []string{"mark"}, Comments: 1, Created: ago(3 * day), Updated: ago(1 * day)}}
	lg.r.Runs = []CIRun{
		{ID: 3305, Number: 305, Title: "Fix rounding in VAT totals", Branch: "main", Status: "completed", Conclusion: "failure", Started: ago(20 * hour), Completed: ago(20*hour - 3*time.Minute)},
		{ID: 3304, Number: 304, Title: "Batch import from CSV", Branch: "csv-import", Status: "completed", Conclusion: "success", Started: ago(26 * hour), Completed: ago(26*hour - 4*time.Minute)},
		{ID: 3303, Number: 303, Title: "Add invoice numbering per year", Branch: "main", Status: "completed", Conclusion: "success", Started: ago(2 * day), Completed: ago(2*day - 4*time.Minute)},
	}
	lg.r.Files["CHANGELOG.md"] = "# Changelog\n\n## [Unreleased]\n\n- Invoice numbers restart each year\n\n## [1.8.0] - " + date(ago(6*day)) + "\n\n- Credit notes\n"
	ledger := lg.done()

	// Vault: a private repo, quiet for a month, whose container has stopped.
	vt := repo("gitea", "vault", "Shared secrets for the deploy scripts.", 107, 200*day)
	vt.r.Private, vt.r.Actions = true, true
	vt.commit(30*day, mark, "Release v1.2.0")
	vt.filler(32*day, 150*day, 9*day, fillerMessages)
	vt.tag("v1.2.0", 30*day, true, false)
	vt.r.Runs = []CIRun{{ID: 4120, Number: 20, Title: "Release v1.2.0", Branch: "main", Status: "completed", Conclusion: "success", Started: ago(30 * day), Completed: ago(30*day - 2*time.Minute)}}
	vault := vt.done()

	// Sandbox: an empty repo. Old site: archived, and retired in the
	// projects file.
	sandbox := repo("gitea", "sandbox", "", 108, 15*day).done()
	sandbox.Empty = true
	old := repo("gitea", "old-site", "The first company website, replaced in the spring.", 109, 900*day)
	old.r.Archived = true
	old.commit(210*day, mark, "Point the home page at the new site")
	old.filler(230*day, 400*day, 30*day, fillerMessages)
	oldSite := old.done()

	// The projects file and the ideas file, hidden from the board.
	cf := repo("gitea", ConfigRepo, "Waymark's projects and ideas files.", 110, 100*day)
	cf.commit(26*hour, mark, "Add the inkwell hold")
	cf.r.Files["projects.yaml"] = projectsYAML(now)
	cf.r.Files["ideas.json"] = ideasJSON(now)
	cfgRepo := cf.done()

	// Kite: on GitHub only, read in full with the token: tags by GraphQL
	// (one annotated), a run in progress, an open PR and label ideas.
	kt := repo("github", "kite", "A command-line client for the team's APIs.", 9001, 260*day)
	kt.r.Stars, kt.r.Actions = 41, true
	kt.r.Website = "https://kite.example.net"
	kt.commit(25*time.Minute, mark, "Add shell completions")
	kt.commit(6*day, mark, "Release v0.9.0")
	kt.commit(9*day, lena, "Retry on HTTP 429")
	kt.filler(11*day, 120*day, 4*day, fillerMessages)
	kt.tag("v0.9.0", 6*day, true, true)
	kt.tag("v0.8.1", 31*day, true, false)
	kt.r.Issues = []Issue{
		{Number: 3, Title: "Shell completions for fish", Author: "tom", Labels: []string{"enhancement"}, Created: ago(20 * day), Updated: ago(2 * day)},
		{Number: 5, Title: "Read the config from TOML", Author: "lena", Labels: []string{"idea", "kind/quick-win", "size/S"}, Comments: 2, Created: ago(14 * day), Updated: ago(7 * day)},
	}
	kt.r.PRs = []Issue{{Number: 6, Title: "Add shell completions", Author: "mark", Branch: "completions", Created: ago(1 * day), Updated: ago(25 * time.Minute)}}
	kt.r.Runs = []CIRun{
		{ID: 5201, Number: 52, Title: "Add shell completions", Branch: "main", Status: "in_progress", Started: ago(20 * time.Minute)},
		{ID: 5200, Number: 51, Title: "Release v0.9.0", Branch: "main", Status: "completed", Conclusion: "success", Started: ago(6 * day), Completed: ago(6*day - 3*time.Minute)},
	}
	kt.r.Files["CHANGELOG.md"] = "# Changelog\n\n## [Unreleased]\n\n- Shell completions for bash and zsh\n\n## [0.9.0] - " + date(ago(6*day)) + "\n\n- Retries on HTTP 429\n"
	kite := kt.done()

	w.Repos = []*Repo{beacon, beaconApp, harbour, atlas, inkwell, ledger, vault, sandbox, oldSite, cfgRepo, kite, ghHarbour}

	img := "registry.example.com/acme/"
	src := func(r *Repo) map[string]string {
		return map[string]string{"org.opencontainers.image.source": r.HTMLURL(), "org.opencontainers.image.title": r.Name}
	}
	ledgerLabels := src(ledger)
	ledgerLabels["org.opencontainers.image.version"] = "1.8.0"
	w.Containers = []Container{
		{ID: "4f1a9c2b7d3e", Name: "beacon", Image: img + "beacon:v2.3.1", State: "running", Status: "Up 4 days", Created: ago(4 * day), Labels: src(beacon)},
		{ID: "3e2d1c0b9a87", Name: "beacon-prev", Image: img + "beacon:v2.3.0", State: "exited", Status: "Exited (0) 6 days ago", Created: ago(34 * day), Labels: src(beacon)},
		{ID: "8b2e6d4f1a90", Name: "ledger-api", Image: img + "ledger:1.8.0", State: "running", Status: "Up 6 days", Created: ago(6 * day), Labels: ledgerLabels},
		{ID: "c3d4e5f6a7b8", Name: "harbour", Image: img + "harbour:3.1.0", State: "running", Status: "Up 19 hours", Created: ago(19 * hour), Labels: src(harbour)},
		{ID: "d9e8f7a6b5c4", Name: "atlas", Image: img + "atlas:v0.7.2", State: "running", Status: "Up 6 hours", Created: ago(6 * hour), Labels: src(atlas)},
		{ID: "e1f2a3b4c5d6", Name: "vault", Image: img + "vault:v1.2.0", State: "exited", Status: "Exited (1) 3 hours ago", Created: ago(8 * day), Labels: src(vault)},
		{ID: "f0e1d2c3b4a5", Name: "postgres", Image: "postgres:16.4", State: "running", Status: "Up 6 days", Created: ago(30 * day), Labels: map[string]string{}},
		{ID: "5d1e0c2a9b47", Name: "fdroid-repo", Image: "5d1e0c2a9b47", State: "running", Status: "Up 20 hours", Created: ago(20 * hour), Labels: map[string]string{}},
		// A label value with commas, as CI systems write build metadata.
		{ID: "a1b2c3d4e5f6", Name: "proxy", Image: "traefik:v3.1", State: "running", Status: "Up 6 days", Created: ago(30*day + 2*hour + 29*time.Minute + 43*time.Second),
			Labels: map[string]string{"traefik.enable": "true", "org.opencontainers.image.source": "https://github.com/traefik/traefik",
				"com.example.build": `{"ref": "main", "ci": {"run": 42, "ok": true}, "tags": ["v3.1", "latest"]}`}},
	}
	w.FDroid = []App{{Package: "com.example.beacon", Name: "Beacon", Versions: []AppVersion{
		{Name: "1.6.0", Code: 10600, Added: ago(33 * day)}, {Name: "1.5.2", Code: 10502, Added: ago(60 * day)},
	}}}
	w.Notes = []Note{
		{File: "office-network.md", Updated: ago(2 * day), Body: "---\nupdated: " + ago(2*day).Format(time.RFC3339) + "\n---\n# Office network\n\n" +
			"The routers, switches and Wi-Fi in the office. There is no code: these notes are the record.\n\n" +
			"- WAITING: Mark to choose between the two access point models\n- The guest network moved to its own VLAN.\n" +
			"- @me renew the router support contract before December\n"},
		{File: "beacon.md", Updated: ago(10 * day), Body: "# Beacon\n\nRelease steps: tag, wait for CI, then pull the new image on web-01.\n\n" +
			"Regions shipped in v2.4.0; the timeline is next.\n"},
		{File: "release-checklist.md", Updated: ago(1 * day), Body: "---\nproject: beacon\ntitle: Release checklist\n---\n" +
			"1. Update CHANGELOG.md\n2. Tag\n3. Deploy to **web-01**\n4. Post in the team channel\n"},
		{File: "beacon_app.md", Updated: ago(30 * day), Body: "# Beacon for Android\n\nPublished through the team's F-Droid repo.\n"},
		{File: "inkwell.md", Updated: ago(40 * day), Body: "# Inkwell\n\nParked until the editor library ships its v2.\n"},
	}
	w.Events = seededEvents(w)
	return w
}

// seededEvents are two weeks of plausible activity in the words the site's
// own events use, newest first.
func seededEvents(w *World) []model.Event {
	ago := func(d time.Duration) time.Time { return w.Now.Add(-d) }
	stamp := func(t time.Time) string { return t.UTC().Format("200601021504") }
	ev := func(at time.Time, slug, name, icon, kind string, lv model.Level, key, text, href string) model.Event {
		return model.Event{ID: slug + ":" + kind + ":" + key, At: at, Project: slug, ProjectName: name, Icon: icon, Kind: kind, Level: lv, Text: text, Href: href}
	}
	b, lg, kt := w.Repo("gitea", "beacon"), w.Repo("gitea", "ledger"), w.Repo("github", "kite")
	return []model.Event{
		ev(ago(25*time.Minute), "kite", "Kite", "command", "commits", model.LevelInfo, kt.Commits[0].SHA, "1 new commit", kt.HTMLURL()+"/commits/main"),
		ev(ago(3*hour), "vault", "Vault", "lock", "container", model.LevelAlert, "exited:"+stamp(ago(3*hour)), "Container vault stopped", ""),
		ev(ago(5*hour), "beacon", "Beacon", "bell-ring", "commits", model.LevelInfo, b.Commits[0].SHA, "1 new commit", b.HTMLURL()+"/commits/branch/main"),
		ev(ago(6*hour), "atlas", "Atlas", "map", "probe", model.LevelAlert, "Live:down:"+stamp(ago(6*hour)), "Live is down", ""),
		ev(ago(20*hour-3*time.Minute), "ledger", "ledger", "folder-git-2", "ci", model.LevelAlert, "3305", "CI failed on main: Fix rounding in VAT totals", lg.HTMLURL()+"/actions/runs/305"),
		ev(ago(19*hour), "harbour", "Harbour", "ship", "container", model.LevelGood, "running:"+stamp(ago(19*hour)), "Container harbour is running again", ""),
		ev(ago(2*day), "beacon", "Beacon", "bell-ring", "release", model.LevelGood, "v2.4.0", "Beacon v2.4.0 tagged", b.HTMLURL()+"/releases/tag/v2.4.0"),
		ev(ago(6*day), "ledger", "ledger", "folder-git-2", "deploy", model.LevelGood, "1.8.0:"+stamp(ago(6*day)), "ledger now runs 1.8.0 (was 1.7.3)", ""),
		ev(ago(6*day+time.Hour), "ledger", "ledger", "folder-git-2", "release", model.LevelGood, "1.8.0", "ledger 1.8.0 tagged", lg.HTMLURL()+"/releases/tag/1.8.0"),
		ev(ago(10*day), "beacon", "Beacon", "bell-ring", "note", model.LevelInfo, "beacon.md:"+ago(10*day).UTC().Format("20060102150405"), "Note beacon.md was updated", ""),
		ev(ago(12*day), "inkwell", "Inkwell", "pen-line", "commits", model.LevelInfo, w.Repo("gitea", "inkwell").Commits[0].SHA, "1 new commit", ""),
	}
}

// projectsYAML is the projects file: identity only, never state.
func projectsYAML(now time.Time) string {
	parked := date(now.Add(-40 * day))
	return `# Identity only, never state: Waymark derives the rest.
owner: acme
categories:
  - {key: services, label: Services, icon: server, hue: 150}
  - {key: apps, label: Apps, icon: layout-dashboard, hue: 210}
  - {key: mobile, label: Mobile, icon: smartphone, hue: 300}
  - {key: tools, label: Tools, icon: package, hue: 40}
  - {key: operations, label: Operations, icon: network, hue: 20}
hide_repos: [waymark-config]
projects:
  - slug: beacon
    name: Beacon
    category: services
    icon: bell-ring
    summary: Status pages and uptime alerts for small teams.
    what: Beacon checks the team's sites every minute and posts incidents to a public status page.
    stack: [Go, PostgreSQL]
    runs_on: Docker on web-01
    repos:
      - gitea: beacon
    containers: [beacon]
    urls:
      - {label: Live, url: "https://status.example.com", probe: "{base}/probe/beacon"}
    relations:
      - {to: ledger, kind: depends-on}
      - {to: atlas, kind: monitors}
      - {to: harbour, kind: monitors}
  - slug: beacon-app
    name: Beacon for Android
    category: mobile
    icon: smartphone
    summary: Incident alerts on your phone.
    what: A small Android app that shows Beacon's incidents, published through the team's F-Droid repo.
    stack: [Kotlin]
    runs_on: F-Droid
    repos:
      - gitea: beacon-app
    containers: [fdroid-repo]
    fdroid: {index: "{base}/fdroid/repo/index-v1.json", package: com.example.beacon}
    notes: [beacon_app.md]
    relations:
      - {to: beacon, kind: client-of}
  - slug: harbour
    name: Harbour
    category: services
    icon: ship
    summary: A small container registry with garbage collection.
    what: Holds the team's images. Public on GitHub, where most of its issues are filed.
    stack: [Go]
    runs_on: Docker on web-01
    repos:
      - gitea: harbour
      - github: harbour
    containers: [harbour]
    urls:
      - {label: Live, url: "https://registry.example.com", probe: "{base}/probe/harbour"}
  - slug: atlas
    name: Atlas
    category: apps
    icon: map
    summary: Map tiles for the team's apps.
    stack: [Go, SQLite]
    runs_on: Docker on web-01
    repos:
      - gitea: atlas
    urls:
      - {label: Live, url: "https://maps.example.com", probe: "{base}/probe/atlas"}
  - slug: inkwell
    name: Inkwell
    category: apps
    icon: pen-line
    summary: A shared writing pad with comments.
    stack: [TypeScript]
    repos:
      - gitea: inkwell
    hold: {kind: parked, since: "` + parked + `", reason: Waiting for the new editor library}
  - slug: vault
    name: Vault
    category: tools
    icon: lock
    summary: Shared secrets for the deploy scripts.
    stack: [Go]
    runs_on: Docker on web-01
    repos:
      - gitea: vault
    containers: [vault]
    relations:
      - {to: beacon, kind: sibling}
  - slug: kite
    name: Kite
    category: tools
    icon: command
    summary: A command-line client for the team's APIs.
    stack: [Go]
    repos:
      - github: kite
    relations:
      - {to: ledger, kind: client-of}
  - slug: old-site
    name: Old site
    category: apps
    icon: globe
    summary: The first company website, replaced in the spring.
    repos:
      - gitea: old-site
    retired: true
  - slug: office-network
    name: Office network
    category: operations
    icon: router
    summary: The office routers, switches and Wi-Fi.
    what: No code here; the notes are the record of the work.
`
}

// ideasJSON is the ideas file: a dated roundup of ideas, one already filed.
func ideasJSON(now time.Time) string {
	return `{
  "roundup_date": "` + date(now.Add(-day)) + `",
  "ideas": [
    {"id": "beacon-01", "project": "beacon", "title": "Status badge for READMEs", "pitch": "An SVG badge with the current status.", "why": "Asked for twice this month.", "size": "S", "kind": "quick-win"},
    {"id": "beacon-02", "project": "beacon", "title": "Maintenance windows", "pitch": "Schedule quiet periods so planned work raises no alerts.", "why": "Planned work pages people at night.", "size": "M", "kind": "hardening"},
    {"id": "atlas-01", "project": "atlas", "title": "Offline tile packs", "pitch": "Download a region's tiles for use without a network.", "why": "Field work happens where there is no signal.", "size": "L", "kind": "bold"},
    {"id": "ledger-01", "project": "ledger", "title": "A Beacon incident when payments fail", "pitch": "Post an incident when card payments start failing.", "why": "Failures are found by customers first.", "size": "M", "kind": "connective"},
    {"id": "kite-01", "project": "kite", "title": "Kite commands for Beacon", "pitch": "Open and close incidents from the terminal.", "size": "S", "kind": "connective"},
    {"id": "harbour-01", "project": "harbour", "title": "Signed images", "pitch": "Sign every image the registry serves.", "why": "Supply-chain checks ask for it.", "size": "L", "kind": "hardening"},
    {"id": "inkwell-01", "project": "inkwell", "title": "Read-only share links", "pitch": "Share a pad without edit rights.", "size": "S", "kind": "quick-win"}
  ]
}
`
}
