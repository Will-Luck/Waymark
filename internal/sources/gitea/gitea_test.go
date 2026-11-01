package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/waymark/internal/forge"
)

const fixtures = "../../../testdata/gitea/"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeGitea serves fixture files by request path and records every request.
type fakeGitea struct {
	t      *testing.T
	routes map[string]func(w http.ResponseWriter, r *http.Request)
	mu     sync.Mutex
	seen   []string
	badTok bool
}

func newFake(t *testing.T) (*fakeGitea, *Client) {
	f := &fakeGitea{t: t, routes: map[string]func(http.ResponseWriter, *http.Request){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "token s3cret" {
			f.badTok = true
		}
		f.mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("non-GET request %s %s", r.Method, r.URL)
		}
		h, ok := f.routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(fixture(t, "raw_404.json"))
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return f, New("gitea", srv.URL+"/api/v1", "s3cret")
}

func (f *fakeGitea) file(path, name string) {
	b := fixture(f.t, name)
	f.routes[path] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) }
}

func (f *fakeGitea) check() {
	f.t.Helper()
	if f.badTok {
		f.t.Error("a request was sent without Authorization: token s3cret")
	}
}

// fixtureNow is when the fixtures were generated (internal/demo).
var fixtureNow = time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)

func beaconRepo(t *testing.T) forge.Repo {
	var repos []forge.Repo
	if err := json.Unmarshal(fixture(t, "user_repos.json"), &repos); err != nil {
		t.Fatal(err)
	}
	for _, r := range repos {
		if r.Name == "beacon" {
			return r
		}
	}
	t.Fatal("beacon not in fixture")
	return forge.Repo{}
}

func TestListRepos(t *testing.T) {
	f, c := newFake(t)
	f.file("/api/v1/user/repos", "user_repos.json")
	repos, err := c.ListRepos(context.Background(), []string{"acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 10 {
		t.Fatalf("repos = %d, want 10", len(repos))
	}
	var b *forge.Repo
	for i := range repos {
		if repos[i].FullName == "acme/beacon" {
			b = &repos[i]
		}
	}
	if b == nil {
		t.Fatal("no beacon")
	}
	if b.OpenIssues != 4 || b.OpenPRs != 2 || b.ReleaseCount != 3 || b.DefaultBranch != "main" || !b.HasActions {
		t.Errorf("beacon = %+v", b)
	}
	if b.HTMLURL != "https://git.example.com/acme/beacon" {
		t.Errorf("html_url = %q", b.HTMLURL)
	}
	// Gitea writes times in its server's zone (+01:00 here); they are kept in UTC.
	if want := time.Date(2026, 10, 31, 17, 0, 0, 0, time.UTC); !b.UpdatedAt.Equal(want) || b.UpdatedAt.Location() != time.UTC {
		t.Errorf("updated_at = %v", b.UpdatedAt)
	}
	if len(f.seen) != 1 || !strings.Contains(f.seen[0], "limit=50") {
		t.Errorf("requests = %v (10 < 50 needs one page)", f.seen)
	}
	f.check()
}

func TestFetchDetailBeacon(t *testing.T) {
	f, c := newFake(t)
	base := "/api/v1/repos/acme/beacon"
	f.file(base+"/tags", "beacon_tags.json")
	f.file(base+"/releases", "beacon_releases.json")
	f.file(base+"/issues", "beacon_issues.json")
	f.file(base+"/actions/runs", "beacon_runs.json")
	commits := fixture(t, "beacon_commits.json")
	f.routes[base+"/commits"] = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("sha") != "main" || q.Get("since") == "" || q.Get("stat") != "false" || q.Get("files") != "false" || q.Get("verification") != "false" {
			t.Errorf("commit query = %s", r.URL.RawQuery)
		}
		w.Header().Set("X-Hasmore", "false")
		_, _ = w.Write(commits)
	}
	repo := beaconRepo(t)
	repo.OpenPRs = 0 // the listing says none, so the pulls are not read
	since := fixtureNow.Add(-84 * 24 * time.Hour)
	d, err := c.FetchDetail(context.Background(), repo, since, 3, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Tags) != 6 || d.Tags[0].Name != "v2.4.0" || d.Tags[0].Commit.SHA[:7] != "ea606c4" {
		t.Errorf("tags = %+v", d.Tags)
	}
	if !d.Tags[0].Commit.Created.Equal(time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("tag created = %v", d.Tags[0].Commit.Created)
	}
	if len(d.Releases) != 3 || d.Releases[0].TagName != "v2.4.0" || d.Releases[0].Name != "v2.4.0: checks by region" {
		t.Errorf("releases = %+v", d.Releases)
	}
	if len(d.Issues) != 4 || d.Issues[0].Number != 21 || d.Issues[0].Labels[0].Name != "needs-you" || d.Issues[0].Labels[0].Color != "e11d48" {
		t.Errorf("issues = %+v", d.Issues)
	}
	if d.PRs != nil {
		t.Errorf("open_pr_counter 0 must not fetch pulls: %+v", d.PRs)
	}
	if len(d.Commits) != 32 || d.CommitsCapped || d.LastCommit == nil || d.LastCommit.SHA[:7] != "c433254" {
		t.Errorf("commits = %d capped=%v last=%v", len(d.Commits), d.CommitsCapped, d.LastCommit)
	}
	if got := d.Commits[0].Subject(); got != "Show incident duration on the status page" {
		t.Errorf("subject = %q", got)
	}
	if got := d.Commits[1].AuthorName(); got != "Priya" {
		t.Errorf("author = %q", got)
	}
	if len(d.Runs) != 3 || d.Runs[0].ID != 1412 || d.Runs[0].Workflow() != "ci.yml" || !d.Runs[0].Done() || d.Runs[0].Conclusion != "success" ||
		d.Runs[2].HeadBranch != "dark-theme" || d.Runs[2].Conclusion != "failure" {
		t.Errorf("runs = %+v", d.Runs)
	}
	if d.InProgress() {
		t.Error("all fixture runs are completed")
	}
	f.check()
}

func TestFetchDetailPulls(t *testing.T) {
	f, c := newFake(t)
	base := "/api/v1/repos/acme/ledger"
	f.routes[base+"/tags"] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) }
	f.file(base+"/pulls", "ledger_pulls.json")
	f.routes[base+"/commits"] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) }
	repo := forge.Repo{FullName: "acme/ledger", Name: "ledger", DefaultBranch: "main", OpenPRs: 1}
	d, err := c.FetchDetail(context.Background(), repo, time.Now().Add(-time.Hour), 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.PRs) != 1 || d.PRs[0].Number != 33 || d.PRs[0].User.Login != "tom" ||
		len(d.PRs[0].RequestedReviewers) != 1 || d.PRs[0].RequestedReviewers[0].Login != "mark" {
		t.Errorf("prs = %+v", d.PRs)
	}
	if d.LastCommit != nil {
		t.Errorf("empty branch answer gives no last commit, got %+v", d.LastCommit)
	}
}

func TestCommitsCappedAndLastCommitFallback(t *testing.T) {
	var one []json.RawMessage
	if err := json.Unmarshal(fixture(t, "beacon_commits.json"), &one); err != nil {
		t.Fatal(err)
	}
	// 50 copies of a real commit, dated inside the window.
	page := "[" + strings.TrimSuffix(strings.Repeat(string(one[0])+",", 50), ",") + "]"
	f, c := newFake(t)
	f.routes["/api/v1/repos/o/r/commits"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("since") == "" {
			// the limit=1 fallback
			_, _ = w.Write([]byte("[" + string(one[4]) + "]"))
			return
		}
		w.Header().Set("X-Hasmore", "true")
		_, _ = w.Write([]byte(page))
	}
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	got, more, err := c.Commits(context.Background(), "o/r", "main", since, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 || !more {
		t.Errorf("got %d more=%v, want 100 true", len(got), more)
	}
	// A window after every commit is empty; LastCommit still finds the newest.
	late := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	got, more, err = c.Commits(context.Background(), "o/r", "main", late, 3)
	if err != nil || len(got) != 0 || more {
		t.Errorf("late window = %d %v %v", len(got), more, err)
	}
	lc, err := c.LastCommit(context.Background(), "o/r", "main")
	if err != nil || lc == nil || lc.SHA[:7] != "9cc9fa7" {
		t.Errorf("last commit = %+v %v", lc, err)
	}
}

func TestEmptyRepoConflict(t *testing.T) {
	f, c := newFake(t)
	f.routes["/api/v1/repos/o/empty/commits"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Git Repository is empty."}`))
	}
	got, more, err := c.Commits(context.Background(), "o/empty", "main", time.Now(), 3)
	if err != nil || got != nil || more {
		t.Errorf("empty repo commits = %v %v %v", got, more, err)
	}
	lc, err := c.LastCommit(context.Background(), "o/empty", "main")
	if err != nil || lc != nil {
		t.Errorf("empty repo last commit = %v %v", lc, err)
	}
}

func TestRunsWithoutActions(t *testing.T) {
	_, c := newFake(t)
	runs, err := c.Runs(context.Background(), "o/noactions")
	if err != nil || runs != nil {
		t.Errorf("404 runs = %v %v", runs, err)
	}
}

func TestErrorsCarryStatusNotToken(t *testing.T) {
	f, c := newFake(t)
	f.routes["/api/v1/user/repos"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"token is required"}`))
	}
	_, err := c.ListRepos(context.Background(), []string{"acme"})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v", err)
	}
	if _, err := c.Raw(context.Background(), "acme/waymark-config", "projects.yaml", ""); !IsNotFound(err) {
		t.Errorf("missing raw file: IsNotFound false for %v", err)
	}
	// A long body is cut at 160 bytes, never inside a character: after 159
	// bytes of ASCII the first "£" (2 bytes) straddles byte 160.
	f.routes["/api/v1/user/repos"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("a", 159) + "££££"))
	}
	_, err = c.ListRepos(context.Background(), []string{"acme"})
	msg := fmt.Sprint(err)
	if !utf8.ValidString(msg) || !strings.HasSuffix(msg, "HTTP 502 "+strings.Repeat("a", 159)) {
		t.Errorf("cut message = %q (valid %v)", msg, utf8.ValidString(msg))
	}
}

func TestFetchFilesBeacon(t *testing.T) {
	f, c := newFake(t)
	base := "/api/v1/repos/acme/beacon"
	f.file(base+"/raw/CHANGELOG.md", "beacon_CHANGELOG.md")
	f.file(base+"/contents/docs/plans", "beacon_plans_dir.json")
	f.file(base+"/raw/docs/plans/2026-10-22-incident-timeline.md", "beacon_plan.md")
	files, err := c.FetchFiles(context.Background(), beaconRepo(t), []string{"docs/plans"}, fixtureNow)
	if err != nil {
		t.Fatal(err)
	}
	cl := files.Changelog
	if cl == nil || len(cl.Unreleased) != 3 || cl.Unreleased[0] != "Incident duration on the status page" || cl.LastVersion != "2.4.0" || cl.LastDate != "2026-10-29" {
		t.Errorf("changelog = %+v", cl)
	}
	if files.ChangelogURL != "https://git.example.com/acme/beacon/src/branch/main/CHANGELOG.md" {
		t.Errorf("changelog url = %q", files.ChangelogURL)
	}
	if len(files.Plans) != 1 {
		t.Fatalf("plans = %+v (the other dated plans are 404 here)", files.Plans)
	}
	p := files.Plans[0]
	if p.Title != "Incident timeline" || p.Done != 3 || p.Total != 7 || p.Date.Format("2006-01-02") != "2026-10-22" {
		t.Errorf("plan = %+v", p)
	}
	if !strings.HasSuffix(p.URL, "/src/branch/main/docs/plans/2026-10-22-incident-timeline.md") {
		t.Errorf("plan url = %q", p.URL)
	}
	var askedPlans []string
	for _, s := range f.seen {
		if strings.Contains(s, "/raw/docs/") {
			askedPlans = append(askedPlans, s)
		}
	}
	// The listing has 10 dated plans: only the newest PlanCandidateMax are
	// read, newest first; the two oldest and the undated files never are.
	want := []string{"2026-10-22-incident-timeline.md", "2026-10-11-status-api/plan.md", "2026-09-21-regions.md",
		"2026-09-13-search-filters.md", "2026-09-05-export-csv.md", "2026-08-29-sso-login.md", "2026-08-17-rss-feed.md", "2026-08-04-slack-alerts.md"}
	if len(askedPlans) != forge.PlanCandidateMax || len(want) != forge.PlanCandidateMax {
		t.Fatalf("plan reads = %v, want the newest %d: %v", askedPlans, forge.PlanCandidateMax, want)
	}
	for _, older := range []string{"maintenance-mode", "audit-log", "README", "alerting-notes"} {
		if strings.Contains(strings.Join(askedPlans, " "), older) {
			t.Errorf("%s was read past the cap: %v", older, askedPlans)
		}
	}
	for i, w := range want {
		if !strings.Contains(askedPlans[i], w) {
			t.Errorf("plan read %d = %s, want %s", i, askedPlans[i], w)
		}
	}
	f.check()
}

func TestHeatmapAndSearchDecode(t *testing.T) {
	f, c := newFake(t)
	f.file("/api/v1/users/mark/heatmap", "heatmap.json")
	f.routes["/api/v1/repos/issues/search"] = func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("q") != "waymark-idea" || q.Get("state") != "all" || q.Get("type") != "issues" {
			t.Errorf("search query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write(fixture(t, "issue_search.json"))
	}
	pts, err := c.Heatmap(context.Background(), "mark")
	if err != nil || len(pts) != 271 || pts[0].Timestamp != 1761562800 || pts[0].Contributions != 5 {
		t.Errorf("heatmap = %d %v", len(pts), err)
	}
	is, err := c.SearchIssues(context.Background(), "waymark-idea")
	if err != nil || len(is) != 1 || is[0].Number != 15 || is[0].Repository == nil || is[0].Repository.FullName != "acme/beacon" {
		t.Errorf("search = %+v %v", is, err)
	}
	f.check()
}

func TestTagsPages(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		n := 50
		if page == "3" {
			n = 7
		}
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"name":"v0.%s.%d"}`, page, i)
		}
		b.WriteString("]")
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()
	c := New("gitea", srv.URL, "t")
	tags, err := c.Tags(context.Background(), "acme/app")
	if err != nil || len(tags) != 107 || strings.Join(pages, ",") != "1,2,3" {
		t.Fatalf("tags = %d, pages %v, err %v", len(tags), pages, err)
	}

	// A server that ignores page must not repeat the first page six times.
	pages = nil
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		var b strings.Builder
		b.WriteString("[")
		for i := 0; i < 50; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"name":"v1.0.%d"}`, i)
		}
		b.WriteString("]")
		_, _ = w.Write([]byte(b.String()))
	}))
	defer same.Close()
	c = New("gitea", same.URL, "t")
	if tags, err := c.Tags(context.Background(), "acme/app"); err != nil || len(tags) != 50 || len(pages) != 2 {
		t.Fatalf("page-blind server: tags = %d, pages %v, err %v", len(tags), pages, err)
	}
}

func TestListReposFiltersOwners(t *testing.T) {
	f, c := newFake(t)
	f.file("/api/v1/user/repos", "user_repos.json")
	all, err := c.ListRepos(context.Background(), []string{"acme"})
	if err != nil {
		t.Fatal(err)
	}
	first := all[0].Owner.Login
	var want int
	for _, r := range all {
		if r.Owner.Login == first {
			want++
		}
	}
	got, err := c.ListRepos(context.Background(), []string{strings.ToUpper(first)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != want || want == 0 {
		t.Fatalf("owner %q: got %d repos, want %d", first, len(got), want)
	}
	for _, r := range got {
		if r.Owner.Login != first || r.Forge != "gitea" {
			t.Errorf("repo %s owner %q forge %q", r.FullName, r.Owner.Login, r.Forge)
		}
	}
	none, err := c.ListRepos(context.Background(), []string{"nobody"})
	if err != nil || len(none) != 0 {
		t.Errorf("unknown owner = %d repos, %v", len(none), err)
	}
}

// /user/repos also lists repos the user collaborates on and its orgs' repos.
// With no owners only the token user's own are kept (as on GitHub); listing
// an org or another user brings theirs in. The user is read once.
func TestListReposNoOwnersMeansOwn(t *testing.T) {
	f, c := newFake(t)
	f.routes["/api/v1/user"] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"login":"Mark"}`)) }
	f.routes["/api/v1/user/repos"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"full_name":"mark/tool","name":"tool","owner":{"login":"mark"}},
			{"full_name":"mark/notes","name":"notes","owner":{"login":"mark"},"private":true},
			{"full_name":"other/secret","name":"secret","owner":{"login":"other"},"private":true},
			{"full_name":"acme/app","name":"app","owner":{"login":"acme"}}]`))
	}
	names := func(owners []string) []string {
		t.Helper()
		repos, err := c.ListRepos(context.Background(), owners)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range repos {
			out = append(out, r.FullName)
		}
		return out
	}
	for _, owners := range [][]string{nil, {}} {
		if got := strings.Join(names(owners), " "); got != "mark/tool mark/notes" {
			t.Errorf("owners %v: repos = %s, want the token user's own only", owners, got)
		}
	}
	if got := strings.Join(names([]string{"acme", "other"}), " "); got != "other/secret acme/app" {
		t.Errorf("owners [acme other]: repos = %s", got)
	}
	users := 0
	for _, s := range f.seen {
		if strings.HasPrefix(s, "/api/v1/user?") || s == "/api/v1/user" {
			users++
		}
	}
	if users != 1 {
		t.Errorf("GET /user %d times, want once: %v", users, f.seen)
	}
	f.check()
}

func TestFetchFilesUsesPlanDirs(t *testing.T) {
	f, c := newFake(t)
	base := "/api/v1/repos/acme/beacon"
	f.file(base+"/raw/CHANGELOG.md", "beacon_CHANGELOG.md")
	f.file(base+"/contents/docs/plans", "beacon_plans_dir.json")
	f.routes[base+"/contents/docs/other"] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("[]")) }
	read := func(dirs []string) []string {
		f.seen = nil
		if _, err := c.FetchFiles(context.Background(), beaconRepo(t), dirs, fixtureNow); err != nil {
			t.Fatal(err)
		}
		var listed []string
		for _, s := range f.seen {
			if strings.Contains(s, "/contents/") {
				listed = append(listed, s)
			}
		}
		return listed
	}
	if got := read([]string{"docs/plans"}); len(got) != 1 || !strings.Contains(got[0], "/contents/docs/plans?") {
		t.Errorf("listed = %v", got)
	}
	got := read([]string{"docs/other"})
	if len(got) != 1 || strings.Contains(strings.Join(got, " "), "docs/plans") {
		t.Errorf("only the dir passed in is read: %v", got)
	}
}

func TestMe(t *testing.T) {
	f, c := newFake(t)
	f.routes["/api/v1/user"] = func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"login":"mark"}`)) }
	u, err := c.Me(context.Background())
	if err != nil || u.Login != "mark" || c.ID() != "gitea" || c.Kind() != "gitea" {
		t.Errorf("me = %+v %v", u, err)
	}
	f.check()
}

// The client names itself: waymark/dev until the app sets its version.
func TestUserAgent(t *testing.T) {
	f, c := newFake(t)
	var got []string
	f.routes["/api/v1/user"] = func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.UserAgent())
		_, _ = w.Write([]byte(`{"login":"mark"}`))
	}
	for _, ua := range []string{"", "waymark/1.2.3"} {
		if ua != "" {
			c.UserAgent = ua
		}
		if _, err := c.Me(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(got, " ") != "waymark/dev waymark/1.2.3" {
		t.Errorf("User-Agents = %q", got)
	}
}

func TestFetchMirror(t *testing.T) {
	f, c := newFake(t)
	base := "/api/v1/repos/acme/app"
	f.routes[base] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"full_name":"acme/app","html_url":"https://git.example.com/acme/app","private":true,"stars_count":4,"open_issues_count":2}`))
	}
	f.routes[base+"/issues"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"number":1,"title":"old","updated_at":"2026-10-01T00:00:00Z"},{"number":2,"title":"new","updated_at":"2026-10-02T00:00:00Z"}]`))
	}
	f.routes[base+"/releases"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v2.0.0"},{"tag_name":"v1.0.0"}]`))
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	m, err := c.FetchMirror(context.Background(), "acme/app", now)
	if err != nil {
		t.Fatal(err)
	}
	if m.Forge != "gitea" || m.FullName != "acme/app" || !m.Private || m.Stars != 4 || m.OpenIssues != 2 || m.LatestRelease != "v2.0.0" || !m.FetchedAt.Equal(now) {
		t.Errorf("mirror = %+v", m)
	}
	if len(m.Issues) != 2 || m.Issues[0].Number != 2 {
		t.Errorf("issues = %+v", m.Issues)
	}
	f.check()
}

// A repo the projects file names is read by name, shaped as the list gives it.
func TestRepoByName(t *testing.T) {
	f, c := newFake(t)
	f.routes["/api/v1/repos/other/lib"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"lib","full_name":"other/lib","owner":{"login":"other"},"private":true,"website":"https://lib.example.com",` +
			`"html_url":"https://git.example.com/other/lib","default_branch":"main","open_issues_count":3,"updated_at":"2026-10-30T12:00:00Z"}`))
	}
	r, err := c.Repo(context.Background(), "other/lib")
	if err != nil {
		t.Fatal(err)
	}
	if r.Forge != "gitea" || r.FullName != "other/lib" || !r.Private || r.Website != "https://lib.example.com" || r.OpenIssues != 3 || r.UpdatedAt.IsZero() {
		t.Errorf("repo = %+v", r)
	}
	if _, err := c.Repo(context.Background(), "other/gone"); !IsNotFound(err) {
		t.Errorf("missing repo err = %v", err)
	}
	f.check()
}
