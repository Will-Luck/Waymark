package derive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/registry"
	"github.com/Will-Luck/waymark/internal/sources/notes"
)

const day = 24 * time.Hour

func action(p model.Project, verb string) *model.Action {
	for i := range p.Actions {
		if p.Actions[i].Verb == verb {
			return &p.Actions[i]
		}
	}
	return nil
}

func decides(p model.Project) []string {
	var out []string
	for _, a := range p.Actions {
		if a.Verb == "Decide" {
			out = append(out, a.Text)
		}
	}
	return out
}

func TestSameDeploy(t *testing.T) {
	cases := []struct {
		clause, latest string
		want           bool
	}{
		{"v2.4.0 tagged + smoke PASS 2026-10-29, deploy awaits Mark", "v2.4.0", true},
		{"NOT DEPLOYED (waits for Mark).", "v2.4.0", true},
		{"v3.2.0 release + deploy await Mark", "v3.1.0", false},
		{"3.1.0 deploy awaits Mark", "v3.1.0", true},
		{"GitHub #9 reply awaits Mark", "v3.1.0", false},
		{"removal awaits Mark", "v2.1.0", false},
	}
	for _, tc := range cases {
		if got := SameDeploy(tc.clause, tc.latest); got != tc.want {
			t.Errorf("SameDeploy(%q, %q) = %v, want %v", tc.clause, tc.latest, got, tc.want)
		}
	}
}

// memoryNote is a note as the claude-memory adapter reads it for the name
// Mark: index line, newest dated heading (when heading is set), clauses.
func memoryNote(t *testing.T, index, heading string, modified time.Time) notes.Note {
	t.Helper()
	d := t.TempDir()
	body := "---\nmodified: " + modified.UTC().Format(time.RFC3339Nano) + "\n---\n"
	if heading != "" {
		body += "## 2026-10-30 " + heading + "\n"
	}
	writeFile(t, filepath.Join(d, "MEMORY.md"), "- [App](app.md): "+index+"\n")
	writeFile(t, filepath.Join(d, "app.md"), body)
	ns, err := notes.NewClaudeMemory(d, []string{"Mark"}).Load([]registry.Project{{Slug: "app"}})
	if err != nil || len(ns["app"]) != 1 {
		t.Fatalf("memory note: %v %+v", err, ns)
	}
	return ns["app"][0]
}

// markdownNote is app.md, with mtime, as the markdown adapter reads it with
// the default markers.
func markdownNote(t *testing.T, body string, mtime time.Time) notes.Note {
	t.Helper()
	d := t.TempDir()
	p := filepath.Join(d, "app.md")
	writeFile(t, p, body)
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	ns, err := notes.NewMarkdown(d, []string{"WAITING:", "@me"}).Load([]registry.Project{{Slug: "app"}})
	if err != nil || len(ns["app"]) != 1 {
		t.Fatalf("markdown note: %v %+v", err, ns)
	}
	return ns["app"][0]
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNoteDeployClauseYieldsToDeployAction(t *testing.T) {
	in := synth{lastCommit: 2 * day, commits: 1, tag: "v1.1.0", tagAge: 3 * day, container: "app:1.0.0", state: "running", declared: true, dockerKnown: true, noteAge: day}.input()
	in.Notes[0] = memoryNote(t, "**v1.0.0 prod; v1.1.0 tagged, deploy awaits Mark**; v1.2.0 release + deploy await Mark; rename awaits Mark", "", in.Notes[0].Modified)
	p := BuildProject(env(t), in).Project
	got := decides(p)
	want := []string{"v1.2.0 release + deploy await Mark", "rename awaits Mark"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("decide actions = %q, want %q", got, want)
	}
	if action(p, "Deploy") == nil {
		t.Error("the Deploy action is missing")
	}
	// The web adds the note's date (At) to the source and ages it in the page.
	if d := action(p, "Decide"); d == nil || !d.Heuristic || d.Source != "Field notes" || !d.At.Equal(in.Notes[0].Modified) {
		t.Errorf("decide action = %+v", d)
	}

	// Without a Deploy action the clause is the only record, so it stays.
	in = synth{lastCommit: 2 * day, commits: 1, tag: "v1.1.0", tagAge: 3 * day, container: "app:1.1.0", state: "running", declared: true, dockerKnown: true, noteAge: day}.input()
	in.Notes[0] = memoryNote(t, "v1.1.0 tagged, deploy awaits Mark", "", in.Notes[0].Modified)
	if got := decides(BuildProject(env(t), in).Project); len(got) != 1 {
		t.Errorf("no Deploy action: decide actions = %q, want the note clause", got)
	}
}

// A markdown note drives the same rules as a memory note: marker items become
// Decide actions in the adapter's words, updated: dates the note for
// staleness, and a project with no repo takes its activity from it.
func TestMarkdownNoteThroughDerive(t *testing.T) {
	e := env(t)
	e.NotesLabel = "Notes"
	stamp := func(age time.Duration) string { return testNow.Add(-age).UTC().Format(time.RFC3339) }

	in := synth{lastCommit: 20 * day, commits: 1}.input()
	in.Notes = []notes.Note{markdownNote(t, "---\nupdated: "+stamp(2*day)+"\n---\n# App\n\n- WAITING: Mark to pick a name\n- [ ] WAITING: a to-do\n", testNow.Add(-30*day))}
	p := BuildProject(e, in).Project
	if got := decides(p); len(got) != 1 || got[0] != "Mark to pick a name" {
		t.Errorf("decide actions = %q", got)
	}
	if d := action(p, "Decide"); d == nil || d.Source != "Notes" || !d.Heuristic || !d.At.Equal(testNow.Add(-2*day)) {
		t.Errorf("decide action = %+v", d)
	}
	if len(p.Notes) != 1 || p.Notes[0].Body != "# App\n\n- WAITING: Mark to pick a name\n- [ ] WAITING: a to-do" || p.Notes[0].Stale {
		t.Errorf("notes = %+v", p.Notes)
	}

	// A date-only updated: covers its day in the site's zone, as modified: does.
	in = synth{lastCommit: 1 * day, commits: 3}.input()
	in.Notes = []notes.Note{markdownNote(t, "---\nupdated: 2026-10-21\n---\nbody\n", testNow)}
	if n := BuildProject(e, in).Project.Notes[0]; !n.Stale || n.StaleReason != "Written 21 Oct; 3 commits since" {
		t.Errorf("stale = %v %q", n.Stale, n.StaleReason)
	}

	bare := Input{
		Project:    registry.Project{Slug: "app", Name: "App", Category: "infrastructure", Icon: "server"},
		GiteaKnown: true,
		Notes:      []notes.Note{markdownNote(t, "---\nupdated: "+stamp(5*day)+"\n---\n", testNow.Add(-90*day))},
	}
	if p := BuildProject(e, bare).Project; p.Tier != model.TierActive || p.TierReason != "Notes updated in the last 3 weeks" || !p.ActivityFromNotes {
		t.Errorf("repo-less project: tier %s %q, from notes %v", p.Tier, p.TierReason, p.ActivityFromNotes)
	}
}

// The note-updated event says whose note it is in the adapter's words.
func TestNoteEventWordingFromTheAdapter(t *testing.T) {
	for label, want := range map[string]string{
		"Field notes": "Field note app.md was updated",
		"Notes":       "Note app.md was updated",
	} {
		e := env(t)
		e.NotesLabel = label
		in := synth{lastCommit: day, commits: 1, noteAge: 3 * day}.input()
		in.NotesAt = testNow
		prev := BuildProject(e, in).FP
		in.Notes[0].Modified = testNow.Add(-time.Hour)
		cur := BuildProject(e, in).FP
		ev, ok := kinds(Diff(map[string]Fingerprint{"app": prev}, map[string]Fingerprint{"app": cur}, testNow))["note"]
		if !ok || ev.Text != want {
			t.Errorf("%s: note event = %+v, want %q", label, ev, want)
		}
	}
}

func TestPlansShowOnlyInProgress(t *testing.T) {
	in := synth{lastCommit: 2 * day, commits: 1}.input()
	plan := func(path string, done, total int, age time.Duration) forge.Plan {
		return forge.Plan{Path: path, Title: path, Date: testNow.Add(-age), Done: done, Total: total}
	}
	in.Repos[0].Files = &forge.Files{Plans: []forge.Plan{
		plan("shipped-no-ticks", 0, 12, 5*day),
		plan("finished", 9, 9, 6*day),
		plan("half", 4, 10, 7*day),
		plan("old-half", 2, 10, 200*day),
		plan("second", 1, 3, 8*day),
		plan("third", 1, 2, 9*day),
		plan("fourth", 1, 2, 10*day),
	}}
	p := BuildProject(env(t), in).Project
	var got []string
	for _, pl := range p.Plans {
		got = append(got, pl.Path)
	}
	want := []string{"half", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("plans = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("plans = %q, want %q", got, want)
		}
	}
}

func TestRepoLessProjectTakesActivityFromNotes(t *testing.T) {
	mk := func(age time.Duration) model.Project {
		in := Input{
			Project:     registry.Project{Slug: "office-network", Name: "Office network", Category: "operations", Icon: "router"},
			GiteaKnown:  true,
			DockerKnown: true,
			FeedAt:      testNow,
		}
		if age > 0 {
			in.Notes = []notes.Note{
				{File: "office_network.md", Name: "office_network", IndexLine: "two switches, four access points", Modified: testNow.Add(-age - 10*day)},
				{File: "router_admin.md", Name: "router_admin", IndexLine: "maintenance window", Modified: testNow.Add(-age)},
			}
		}
		return BuildProject(env(t), in).Project
	}
	cases := []struct {
		name     string
		age      time.Duration
		tier     model.Tier
		reason   string
		headline string
	}{
		{"recent note", 5 * day, model.TierActive, "Notes updated in the last 3 weeks", "Notes updated 5 days ago"},
		{"older note", 40 * day, model.TierQuiet, "No note updates for 40 days", "Notes updated 40 days ago"},
		{"old note", 200 * day, model.TierDormant, "No note updates for 6 months", "No note updates for 6 months"},
		{"no notes", 0, model.TierDormant, "No repo and no notes yet", "No repo and no notes yet"},
	}
	for _, tc := range cases {
		p := mk(tc.age)
		if p.Tier != tc.tier || p.TierReason != tc.reason || p.Headline != tc.headline {
			t.Errorf("%s: tier %q (%q), headline %q; want %q (%q), %q", tc.name, p.Tier, p.TierReason, p.Headline, tc.tier, tc.reason, tc.headline)
		}
		if !p.ActivityFromNotes {
			t.Errorf("%s: ActivityFromNotes is false", tc.name)
		}
		if tc.age > 0 && !p.LastActivity.Equal(testNow.Add(-tc.age)) {
			t.Errorf("%s: LastActivity = %v, want the newest note", tc.name, p.LastActivity)
		}
	}

	// A project with a Gitea repo keeps commit-based activity even when a
	// note is newer.
	in := synth{lastCommit: 50 * day, commits: 1, noteAge: day}.input()
	if p := BuildProject(env(t), in).Project; p.Tier != model.TierQuiet {
		t.Errorf("repo project with a fresh note: tier %q, want quiet", p.Tier)
	}
}

func TestFDroidPackageMissingFromReadIndex(t *testing.T) {
	in := synth{lastCommit: 2 * day, commits: 1, tag: "v0.35.0", tagAge: 3 * day, container: "fdroid:latest", state: "running", declared: true, dockerKnown: true}.input()
	in.Project.FDroid = &registry.FDroid{Index: "http://x/index-v1.json", Package: "uk.app"}

	p := BuildProject(env(t), in).Project
	if s := signal(p, "fdroid-index"); s == nil || s.Text != "F-Droid index not read" || s.Level != model.LevelInfo {
		t.Errorf("index unread: signal = %+v", s)
	}
	in.FDroidIndexRead = true
	p = BuildProject(env(t), in).Project
	if s := signal(p, "fdroid-index"); s == nil || s.Text != "Package not in the F-Droid index" || s.Level != model.LevelWarn {
		t.Errorf("index read, package absent: signal = %+v", s)
	}
}

func TestHoldHeadlineBeatsStaleCI(t *testing.T) {
	since := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	in := synth{lastCommit: 160 * day, commits: 1, ciConclusion: "failure",
		hold: &registry.Hold{Kind: "frozen", Since: since, Reason: "staging only"}}.input()
	p := BuildProject(env(t), in).Project
	if p.Headline != "Frozen since 29 May: staging only" || p.HeadlineLevel != model.LevelInfo {
		t.Errorf("headline = %q (%s)", p.Headline, p.HeadlineLevel)
	}
	if s := signal(p, "ci"); s == nil || s.Level != model.LevelAlert {
		t.Errorf("the CI chip should still show the failure: %+v", s)
	}
	in.Project.Hold = nil
	if p := BuildProject(env(t), in).Project; p.HeadlineLevel != model.LevelAlert {
		t.Errorf("no hold: headline = %q (%s), want the CI alert", p.Headline, p.HeadlineLevel)
	}
}

// needsInput is a project with one gitea repo and the token user "mark".
func needsInput(issues, prs []forge.Issue) Input {
	in := synth{lastCommit: 2 * day, commits: 1}.input()
	in.Repos[0].Repo.Forge = "gitea"
	in.Repos[0].Detail.Issues = issues
	in.Repos[0].Detail.PRs = prs
	in.Me = map[string]string{"gitea": "mark", "github": "mark"}
	return in
}

func needsEnv(t *testing.T) Env {
	e := env(t)
	e.NeedsYou = NeedsYou{Labels: []string{"needs-you", "decision"}, Assigned: true, ReviewRequests: true}
	e.ForgeKinds = map[string]string{"gitea": "gitea", "github": "github"}
	return e
}

func issue(n int, title string, age time.Duration) forge.Issue {
	return forge.Issue{Number: n, Title: title, State: "open", HTMLURL: "https://git.example.com/acme/app/issues/" + itoa(n),
		UpdatedAt: forge.Time{Time: testNow.Add(-age)}}
}

func withLabel(is forge.Issue, name string) forge.Issue {
	is.Labels = append(is.Labels, forge.Label{Name: name})
	return is
}

func assignedTo(is forge.Issue, login string) forge.Issue {
	is.Assignees = append(is.Assignees, forge.User{Login: login})
	return is
}

func decideActions(p model.Project) []model.Action {
	var out []model.Action
	for _, a := range p.Actions {
		if a.Verb == "Decide" || a.Verb == "Review" {
			out = append(out, a)
		}
	}
	return out
}

func TestLabelledIssueIsDecide(t *testing.T) {
	in := needsInput([]forge.Issue{withLabel(issue(7, "Pick a licence", day), "Needs-You"), issue(8, "Plain", day)}, nil)
	got := decideActions(BuildProject(needsEnv(t), in).Project)
	if len(got) != 1 {
		t.Fatalf("actions = %+v, want one", got)
	}
	a := got[0]
	if a.Verb != "Decide" || a.Text != "Pick a licence" || a.Source != "Gitea label" || a.Href != "https://git.example.com/acme/app/issues/7" ||
		!a.At.Equal(testNow.Add(-day)) || a.Level != model.LevelInfo || a.Heuristic {
		t.Errorf("action = %+v", a)
	}
	// A labelled pull request counts too, and a GitHub primary says GitHub.
	pr := withLabel(issue(9, "Rework the parser", 2*day), "decision")
	in = needsInput(nil, []forge.Issue{pr})
	if got := decideActions(BuildProject(needsEnv(t), in).Project); len(got) != 1 || got[0].Text != "Rework the parser" {
		t.Errorf("labelled PR: %+v", got)
	}
	in.Repos[0].Repo.Forge = "github"
	if got := decideActions(BuildProject(needsEnv(t), in).Project); len(got) != 1 || got[0].Source != "GitHub label" {
		t.Errorf("github label: %+v", got)
	}
}

func TestAssignedIssueIsDecide(t *testing.T) {
	in := needsInput([]forge.Issue{assignedTo(issue(3, "Write the docs", day), "Mark"), assignedTo(issue(4, "Someone else's", day), "ann")}, nil)
	got := decideActions(BuildProject(needsEnv(t), in).Project)
	if len(got) != 1 || got[0].Verb != "Decide" || got[0].Text != "Write the docs" || got[0].Source != "Assigned to you" || got[0].Level != model.LevelInfo {
		t.Errorf("actions = %+v", got)
	}
	// No token user known for the forge: nothing matches.
	in.Me = nil
	if got := decideActions(BuildProject(needsEnv(t), in).Project); len(got) != 0 {
		t.Errorf("no Me: %+v", got)
	}
}

func TestAssignedOffByConfig(t *testing.T) {
	in := needsInput([]forge.Issue{assignedTo(issue(3, "Write the docs", day), "mark")},
		[]forge.Issue{func() forge.Issue {
			pr := issue(5, "Add retries", day)
			pr.RequestedReviewers = []forge.User{{Login: "mark"}}
			return pr
		}()})
	e := needsEnv(t)
	e.NeedsYou.Assigned = false
	got := decideActions(BuildProject(e, in).Project)
	if len(got) != 1 || got[0].Verb != "Review" {
		t.Errorf("assigned off: %+v", got)
	}
	e = needsEnv(t)
	e.NeedsYou.ReviewRequests = false
	got = decideActions(BuildProject(e, in).Project)
	if len(got) != 1 || got[0].Verb != "Decide" {
		t.Errorf("review requests off: %+v", got)
	}
}

func TestReviewRequestIsReview(t *testing.T) {
	pr := issue(5, "Add retries", day)
	pr.RequestedReviewers = []forge.User{{Login: "ann"}, {Login: "mark"}}
	other := issue(6, "Not mine", day)
	other.RequestedReviewers = []forge.User{{Login: "ann"}}
	in := needsInput(nil, []forge.Issue{pr, other})
	got := decideActions(BuildProject(needsEnv(t), in).Project)
	if len(got) != 1 {
		t.Fatalf("actions = %+v", got)
	}
	a := got[0]
	if a.Verb != "Review" || a.Text != "Add retries" || a.Source != "Review requested" || a.Level != model.LevelWarn ||
		a.Href != pr.HTMLURL || !a.At.Equal(pr.UpdatedAt.Time) || a.Heuristic {
		t.Errorf("action = %+v", a)
	}
	// An issue that merely lists reviewers is not a review request.
	is := issue(2, "An issue", day)
	is.RequestedReviewers = []forge.User{{Login: "mark"}}
	if got := decideActions(BuildProject(needsEnv(t), needsInput([]forge.Issue{is}, nil)).Project); len(got) != 0 {
		t.Errorf("issue with reviewers: %+v", got)
	}
}

func TestOneActionPerIssue(t *testing.T) {
	both := assignedTo(withLabel(issue(7, "Pick a licence", day), "needs-you"), "mark")
	pr := withLabel(issue(9, "Rework", day), "decision")
	pr.RequestedReviewers = []forge.User{{Login: "mark"}}
	// Stale enough for the v0.1 Review rule as well.
	pr.UpdatedAt = forge.Time{Time: testNow.Add(-5 * day)}
	in := needsInput([]forge.Issue{both}, []forge.Issue{pr})
	got := decideActions(BuildProject(needsEnv(t), in).Project)
	if len(got) != 2 {
		t.Fatalf("actions = %+v, want one per item", got)
	}
	for _, a := range got {
		if a.Source != "Gitea label" {
			t.Errorf("label should beat the other rules: %+v", a)
		}
	}
}

func TestMirrorLabelCountsAssigneeDoesNot(t *testing.T) {
	labelled := withLabel(issue(21, "Choose a name", day), "needs-you")
	assigned := assignedTo(issue(22, "Draft the post", day), "mark")
	in := needsInput(nil, nil)
	in.Mirror = &forge.Mirror{Forge: "github", FullName: "acme/app", Issues: []forge.Issue{labelled, assigned}}
	got := decideActions(BuildProject(needsEnv(t), in).Project)
	if len(got) != 1 || got[0].Text != "Choose a name" || got[0].Source != "GitHub label" {
		t.Errorf("actions = %+v, want only the mirror's labelled issue", got)
	}
}

func TestClosedIssuesIgnored(t *testing.T) {
	// Only open issues are fetched; a mirror that holds one never leaks it.
	in := needsInput(nil, nil)
	if got := decideActions(BuildProject(needsEnv(t), in).Project); len(got) != 0 {
		t.Errorf("empty data: %+v", got)
	}
	closed := withLabel(issue(30, "Already done", day), "needs-you")
	closed.State = "closed"
	in.Mirror = &forge.Mirror{Forge: "github", FullName: "acme/app", Issues: []forge.Issue{closed}}
	if got := decideActions(BuildProject(needsEnv(t), in).Project); len(got) != 0 {
		t.Errorf("closed issue leaked: %+v", got)
	}
}

func TestNeedsYouLeavesV01ActionsAlone(t *testing.T) {
	in := synth{lastCommit: 2 * day, commits: 1, prUpdated: 5 * day}.input()
	in.Repos[0].Repo.Forge = "gitea"
	in.Me = map[string]string{"gitea": "mark"}
	plain := BuildProject(env(t), in).Project.Actions
	with := BuildProject(needsEnv(t), in).Project.Actions
	if len(plain) == 0 || len(plain) != len(with) {
		t.Fatalf("plain %+v, with %+v", plain, with)
	}
	for i := range plain {
		if plain[i] != with[i] {
			t.Errorf("action %d changed: %+v vs %+v", i, plain[i], with[i])
		}
	}
}
