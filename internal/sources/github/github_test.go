package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/waymark/internal/forge"
)

const fixtures = "../../../testdata/github/"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeGitHub serves REST routes by path and GraphQL through gql, and records
// every request with the Authorization header it carried.
type fakeGitHub struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	gql    func(w http.ResponseWriter, query string, vars map[string]any)
	seen   []string
	auths  []string
}

func newFake(t *testing.T, token string) (*fakeGitHub, *Client) {
	f := &fakeGitHub{t: t, routes: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.Method+" "+r.URL.RequestURI())
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		h, gql := f.routes[r.URL.Path], f.gql
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql" && gql != nil:
			var req struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("graphql body: %v", err)
			}
			gql(w, req.Query, req.Variables)
		case r.Method == http.MethodGet && h != nil:
			h(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return f, New("github", srv.URL, token)
}

func (f *fakeGitHub) route(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[path] = h
}

func (f *fakeGitHub) file(path, name string) {
	b := fixture(f.t, name)
	f.route(path, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) })
}

func (f *fakeGitHub) text(path, body string) {
	f.route(path, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
}

func (f *fakeGitHub) graphql(h func(w http.ResponseWriter, query string, vars map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gql = h
}

func (f *fakeGitHub) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen, f.auths = nil, nil
}

// requests lists the recorded requests that contain sub.
func (f *fakeGitHub) requests(sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.seen {
		if strings.Contains(s, sub) {
			out = append(out, s)
		}
	}
	return out
}

// checkAuth fails unless every request carried exactly want.
func (f *fakeGitHub) checkAuth(want string) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.auths {
		if a != want {
			f.t.Errorf("%s sent Authorization %q, want %q", f.seen[i], a, want)
		}
	}
}

// noTags answers the tag query with no tags.
func noTags(w http.ResponseWriter, query string, vars map[string]any) {
	_, _ = w.Write([]byte(`{"data":{"repository":{"refs":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}}`))
}

func date(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestGitHubImplementsForge(t *testing.T) {
	var _ forge.Forge = (*Client)(nil)
	if c := New("gh-work", "", "t"); c.ID() != "gh-work" || c.Kind() != "github" || c.Base != "https://api.github.com" {
		t.Errorf("client = %q %q %q", c.ID(), c.Kind(), c.Base)
	}
	if c := New("github", "https://ghe.example.com/api/v3/", ""); c.Base != "https://ghe.example.com/api/v3" {
		t.Errorf("base = %q", c.Base)
	}
	f, c := newFake(t, "s3cret")
	f.file("/user", "user.json")
	u, err := c.Me(context.Background())
	if err != nil || u.Login != "acme" {
		t.Errorf("me = %+v %v", u, err)
	}
	f.checkAuth("Bearer s3cret")
}

// REST and GraphQL calls name the client (GitHub asks every API client for a
// User-Agent): waymark/dev until the app sets its version.
func TestUserAgent(t *testing.T) {
	f, c := newFake(t, "s3cret")
	var mu sync.Mutex
	var got []string
	record := func(r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, r.Method+" "+r.UserAgent())
	}
	f.route("/user", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write(fixture(t, "user.json"))
	})
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.UserAgent = "waymark/1.2.3"
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	gql := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer gql.Close()
	c.Base = gql.URL
	if err := c.graphql(context.Background(), "query { viewer { login } }", nil, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if want := "GET waymark/dev,GET waymark/1.2.3,POST waymark/1.2.3"; strings.Join(got, ",") != want {
		t.Errorf("User-Agents = %q, want %s", got, want)
	}
}

func TestListReposOrgAndUser(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.file("/user", "user.json")
	userRepos := fixture(t, "user_repos.json")
	f.route("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("affiliation") != "owner" || q.Get("per_page") != "100" || q.Get("page") != "1" {
			t.Errorf("user repos query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write(userRepos)
	})
	f.file("/orgs/example-org/repos", "org_repos.json")
	// example-user is a person, not an organisation: /orgs/ answers 404.
	f.route("/users/example-user/repos", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		n := map[string]int{"1": 100, "2": 1}[page]
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, fmt.Sprintf(`{"name":"tool-%s-%d","full_name":"example-user/tool-%s-%d","owner":{"login":"example-user"},"default_branch":"main"}`, page, i, page, i))
		}
		_, _ = w.Write([]byte("[" + strings.Join(parts, ",") + "]"))
	})

	repos, err := c.ListRepos(context.Background(), []string{"ACME", "example-org", "example-user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 3+1+101 {
		t.Fatalf("repos = %d, want 105", len(repos))
	}
	byName := map[string]forge.Repo{}
	for _, r := range repos {
		if r.Forge != "github" {
			t.Errorf("%s forge = %q", r.FullName, r.Forge)
		}
		byName[r.FullName] = r
	}
	app := byName["acme/app"]
	if app.Name != "app" || app.Owner.Login != "acme" || app.Private || app.Archived || app.Mirror || app.DefaultBranch != "main" ||
		app.Stars != 12 || app.OpenIssues != 4 || app.Website != "https://app.example.com" || app.HTMLURL != "https://github.com/acme/app" ||
		app.Description != "A small app for tracking parcels" || app.ID != 700001 {
		t.Errorf("app = %+v", app)
	}
	// Gitea's updated_at moves on every push; GitHub's pushed_at is the one that does.
	if !app.UpdatedAt.Equal(date("2026-10-31T09:58:00Z")) {
		t.Errorf("app updated = %v, want the push time", app.UpdatedAt)
	}
	if nt := byName["acme/notes-tool"]; !nt.Private || nt.DefaultBranch != "trunk" || !nt.UpdatedAt.Equal(date("2026-09-01T07:30:00Z")) || nt.Description != "" {
		t.Errorf("notes-tool = %+v", nt)
	}
	if old := byName["acme/old-site"]; !old.Archived || !old.Mirror || old.Website != "" {
		t.Errorf("old-site = %+v", old)
	}
	if site := byName["example-org/site"]; site.Owner.Login != "example-org" || site.Stars != 7 {
		t.Errorf("org repo = %+v", site)
	}
	if got := f.requests("/orgs/example-user/repos"); len(got) != 1 {
		t.Errorf("org probe for a user = %v, want one", got)
	}
	if got := f.requests("/users/example-user/repos"); len(got) != 2 {
		t.Errorf("user repo pages = %v, want 2", got)
	}
	if got := f.requests("/orgs/acme/"); len(got) != 0 {
		t.Errorf("the token user's own repos come from /user/repos, not %v", got)
	}

	// No owners: the token user's repos.
	mine, err := c.ListRepos(context.Background(), nil)
	if err != nil || len(mine) != 3 {
		t.Fatalf("own repos = %d %v", len(mine), err)
	}
	f.checkAuth("Bearer s3cret")
}

// An owner that is neither an organisation nor a user (both answer 404) is
// skipped: the other owners are still listed and the search leaves it out.
// Any other failure still fails the list.
func TestListReposSkipsMissingOwner(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.file("/user", "user.json")
	f.file("/orgs/example-org/repos", "org_repos.json")
	repos, err := c.ListRepos(context.Background(), []string{"exmaple-org", "example-org"})
	if err != nil || len(repos) != 1 || repos[0].FullName != "example-org/site" {
		t.Fatalf("repos = %+v, err %v", repos, err)
	}
	if got := f.requests("/users/exmaple-org/repos"); len(got) != 1 {
		t.Errorf("user lookup of the missing owner = %v, want one", got)
	}
	c.mu.Lock()
	scope := c.scope
	c.mu.Unlock()
	if strings.Join(scope, " ") != "org:example-org" {
		t.Errorf("search scope = %v", scope)
	}
	f.route("/users/exmaple-org/repos", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if _, err := c.ListRepos(context.Background(), []string{"exmaple-org", "example-org"}); !hasStatus(err, http.StatusBadGateway) {
		t.Errorf("a 502 for an owner: err = %v, want it to fail the list", err)
	}
}

func TestFetchDetailMapsRuns(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.graphql(noTags)
	f.text("/repos/acme/app/releases", "[]")
	f.text("/repos/acme/app/commits", "[]")
	runs := fixture(t, "app_runs.json")
	f.route("/repos/acme/app/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("per_page") != "20" {
			t.Errorf("runs query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write(runs)
	})
	repo := forge.Repo{FullName: "acme/app", Name: "app", DefaultBranch: "main", HTMLURL: "https://github.com/acme/app"}
	now := date("2026-11-01T00:00:00Z")
	d, err := c.FetchDetail(context.Background(), repo, now.Add(-84*24*time.Hour), 3, now)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture has 7 runs; like Gitea, only the newest 5 are kept.
	want := []struct {
		id                 int64
		status, conclusion string
	}{
		{9006, "in_progress", ""},
		{9005, "completed", "failure"},
		{9004, "completed", "success"},
		{9003, "queued", ""},           // waiting for an environment's approval
		{9002, "completed", "failure"}, // timed_out
	}
	if len(d.Runs) != len(want) {
		t.Fatalf("runs = %d, want %d", len(d.Runs), len(want))
	}
	for i, w := range want {
		r := d.Runs[i]
		if r.ID != w.id || r.Status != w.status || r.Conclusion != w.conclusion {
			t.Errorf("run %d = %d %q/%q, want %d %q/%q", i, r.ID, r.Status, r.Conclusion, w.id, w.status, w.conclusion)
		}
		if r.Workflow() != map[bool]string{true: "deploy.yml", false: "test.yml"}[r.ID == 9003] {
			t.Errorf("run %d workflow = %q (path %q)", r.ID, r.Workflow(), r.Path)
		}
	}
	running, failed := d.Runs[0], d.Runs[1]
	if running.Done() || !running.StartedAt.Equal(date("2026-10-31T09:58:25Z")) || !running.CompletedAt.IsZero() || running.HeadBranch != "main" {
		t.Errorf("running = %+v", running)
	}
	if !failed.Done() || !failed.CompletedAt.Equal(date("2026-10-28T16:51:40Z")) || failed.RunNumber != 45 || failed.Event != "push" ||
		failed.HeadSHA != "2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e" || failed.HTMLURL != "https://github.com/acme/app/actions/runs/9005" ||
		failed.DisplayTitle != "Merge pull request #11 from example-dev/labels" {
		t.Errorf("failed = %+v", failed)
	}
	if !d.InProgress() {
		t.Error("an in_progress run must keep the detail refreshing")
	}
	if d.Repo.FullName != "acme/app" || !d.FetchedAt.Equal(now) || d.Issues != nil || d.PRs != nil {
		t.Errorf("detail = %+v", d)
	}
	f.checkAuth("Bearer s3cret")

	// Every GitHub state lands on one Gitea gives.
	for _, row := range []struct{ status, conclusion, wantStatus, wantConclusion string }{
		{"requested", "", "queued", ""},
		{"queued", "", "queued", ""},
		{"pending", "", "queued", ""},
		{"waiting", "", "queued", ""},
		{"in_progress", "", "in_progress", ""},
		{"completed", "success", "completed", "success"},
		{"completed", "failure", "completed", "failure"},
		{"completed", "timed_out", "completed", "failure"},
		{"completed", "startup_failure", "completed", "failure"},
		{"completed", "cancelled", "completed", "cancelled"},
		{"completed", "stale", "completed", "cancelled"},
		{"completed", "skipped", "completed", "skipped"},
		{"completed", "neutral", "completed", "skipped"},
		{"completed", "action_required", "queued", ""}, // approval before it runs
	} {
		r := ghRun{Status: row.status, Conclusion: row.conclusion, UpdatedAt: forge.Time{Time: date("2026-10-30T10:00:00Z")}}.run()
		if r.Status != row.wantStatus || r.Conclusion != row.wantConclusion || r.Done() != r.CompletedAt.Equal(date("2026-10-30T10:00:00Z")) {
			t.Errorf("%s/%s = %s/%s (completed %v), want %s/%s", row.status, row.conclusion, r.Status, r.Conclusion, r.CompletedAt, row.wantStatus, row.wantConclusion)
		}
	}
}

func TestRunsCappedAtFive(t *testing.T) {
	f, c := newFake(t, "s3cret")
	var parts []string
	for i := 0; i < 5; i++ {
		parts = append(parts, fmt.Sprintf(`{"id":%d,"head_branch":"main","path":".github/workflows/test.yml","status":"completed","conclusion":"success"}`, 100-i))
	}
	// Sixth newest: a fork's run that waits for approval, perhaps for ever.
	parts = append(parts, `{"id":94,"head_branch":"typo-fix","path":".github/workflows/test.yml","status":"completed","conclusion":"action_required"}`)
	f.text("/repos/acme/app/actions/runs", `{"total_count":6,"workflow_runs":[`+strings.Join(parts, ",")+`]}`)
	runs, err := c.Runs(context.Background(), "acme/app")
	if err != nil || len(runs) != 5 || runs[0].ID != 100 || runs[4].ID != 96 {
		t.Fatalf("runs = %+v %v", runs, err)
	}
	if (&forge.Detail{Runs: runs}).InProgress() {
		t.Error("a waiting run past the newest 5 must not keep the detail refreshing")
	}
}

// tagPage is one page of the tag query: names, all dated 2026-10-01.
func tagPage(names []string, next string) []byte {
	nodes := make([]map[string]any, len(names))
	for i, n := range names {
		nodes[i] = map[string]any{"name": n, "target": map[string]any{"oid": fmt.Sprintf("%040d", i), "committedDate": "2026-10-01T00:00:00Z"}}
	}
	var cursor any
	if next != "" {
		cursor = next
	}
	b, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"refs": map[string]any{
		"pageInfo": map[string]any{"hasNextPage": next != "", "endCursor": cursor}, "nodes": nodes,
	}}}})
	return b
}

func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

func TestTagsPage(t *testing.T) {
	f, c := newFake(t, "s3cret")
	tags := fixture(t, "app_tags.graphql.json")
	f.graphql(func(w http.ResponseWriter, query string, vars map[string]any) {
		if !strings.Contains(query, `refPrefix: "refs/tags/"`) || !strings.Contains(query, "TAG_COMMIT_DATE") || vars["owner"] != "acme" || vars["name"] != "app" || vars["after"] != nil {
			t.Errorf("tag query = %s %v", query, vars)
		}
		_, _ = w.Write(tags)
	})
	got, err := c.Tags(context.Background(), "acme/app")
	if err != nil || len(got) != 3 {
		t.Fatalf("tags = %+v %v", got, err)
	}
	// An annotated tag is peeled to its commit; the tag object is the ID.
	if a := got[0]; a.Name != "v1.2.0" || a.ID != "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0" || a.Commit.SHA != "4f1c2a9e8b7d6c5a4f3e2d1c0b9a8f7e6d5c4b3a" ||
		!a.Commit.Created.Equal(date("2026-10-31T09:58:00Z")) || a.Message != "Parcel labels\n" {
		t.Errorf("annotated = %+v", a)
	}
	if l := got[2]; l.Name != "v1.1.0" || l.ID != l.Commit.SHA || l.Commit.SHA != "6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d" || !l.Commit.Created.Equal(date("2026-09-14T16:20:00Z")) {
		t.Errorf("lightweight = %+v", l)
	}
	f.checkAuth("Bearer s3cret")

	var mu sync.Mutex
	var cursors []string
	serve := func(page func(after string) ([]string, string)) {
		mu.Lock()
		cursors = nil
		mu.Unlock()
		f.graphql(func(w http.ResponseWriter, query string, vars map[string]any) {
			after, _ := vars["after"].(string)
			mu.Lock()
			cursors = append(cursors, after)
			mu.Unlock()
			ns, next := page(after)
			_, _ = w.Write(tagPage(ns, next))
		})
	}
	// 107 tags: a full page of 100, then 7.
	serve(func(after string) ([]string, string) {
		if after == "" {
			return names("v0.1.", 100), "p1"
		}
		return names("v0.2.", 7), ""
	})
	if got, err := c.Tags(context.Background(), "acme/app"); err != nil || len(got) != 107 || strings.Join(cursors, ",") != ",p1" {
		t.Fatalf("tags = %d, cursors %q, err %v", len(got), cursors, err)
	}
	// A server that ignores the cursor must not repeat the first page six times.
	serve(func(after string) ([]string, string) { return names("v1.0.", 100), "p1" })
	if got, err := c.Tags(context.Background(), "acme/app"); err != nil || len(got) != 100 || len(cursors) != 2 {
		t.Fatalf("cursor-blind server: tags = %d, cursors %q, err %v", len(got), cursors, err)
	}
	// Endless tags stop at TagPages pages of 100.
	serve(func(after string) ([]string, string) { return names("v"+after+".", 100), after + "x" })
	if got, err := c.Tags(context.Background(), "acme/app"); err != nil || len(got) != TagPages*100 || len(cursors) != TagPages {
		t.Fatalf("endless: tags = %d, pages %d, err %v", len(got), len(cursors), err)
	}
}

func TestReviewRequestsAndAssignees(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.graphql(noTags)
	f.file("/repos/acme/app/releases", "app_releases.json")
	f.text("/repos/acme/app/commits", "[]")
	f.text("/repos/acme/app/actions/runs", `{"total_count":0,"workflow_runs":[]}`)
	open := func(name string) http.HandlerFunc {
		b := fixture(t, name)
		return func(w http.ResponseWriter, r *http.Request) {
			if q := r.URL.Query(); q.Get("state") != "open" || q.Get("sort") != "updated" || q.Get("direction") != "desc" || q.Get("per_page") != "100" {
				t.Errorf("%s query = %s", r.URL.Path, r.URL.RawQuery)
			}
			_, _ = w.Write(b)
		}
	}
	f.route("/repos/acme/app/issues", open("app_issues.json"))
	f.route("/repos/acme/app/pulls", open("app_pulls.json"))
	// The listing counts issues and PRs together (2 + 2).
	repo := forge.Repo{FullName: "acme/app", Name: "app", DefaultBranch: "main", OpenIssues: 4}
	now := date("2026-11-01T00:00:00Z")
	d, err := c.FetchDetail(context.Background(), repo, now.Add(-84*24*time.Hour), 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Issues) != 2 || d.Issues[0].Number != 12 || d.Issues[1].Number != 9 {
		t.Fatalf("issues = %+v", d.Issues)
	}
	if is := d.Issues[0]; len(is.Assignees) != 1 || is.Assignees[0].Login != "mark-example" || is.Labels[0].Name != "bug" || is.Labels[0].Color != "d73a4a" ||
		is.Comments != 2 || is.User.Login != "example-user" || is.HTMLURL != "https://github.com/acme/app/issues/12" {
		t.Errorf("issue 12 = %+v", is)
	}
	if len(d.Issues[1].Assignees) != 0 {
		t.Errorf("issue 9 assignees = %+v", d.Issues[1].Assignees)
	}
	if len(d.PRs) != 2 || d.PRs[0].Number != 15 || d.PRs[1].Number != 13 {
		t.Fatalf("prs = %+v", d.PRs)
	}
	// The pulls list has no comment count; the issue list's entry for the PR has.
	if pr := d.PRs[0]; len(pr.RequestedReviewers) != 1 || pr.RequestedReviewers[0].Login != "acme" || pr.Draft || pr.Comments != 3 || pr.User.Login != "example-dev" {
		t.Errorf("pr 15 = %+v", pr)
	}
	if pr := d.PRs[1]; !pr.Draft || len(pr.RequestedReviewers) != 0 || pr.Assignees[0].Login != "acme" || pr.Comments != 1 {
		t.Errorf("pr 13 = %+v", pr)
	}
	// Detail.Repo splits the listing's count the way Gitea reports it.
	if d.Repo.OpenIssues != 2 || d.Repo.OpenPRs != 2 {
		t.Errorf("open = %d issues, %d PRs; want 2 and 2", d.Repo.OpenIssues, d.Repo.OpenPRs)
	}
	if len(d.Releases) != 2 || d.Releases[0].TagName != "v1.2.0" || !d.Releases[0].PublishedAt.Equal(date("2026-10-31T10:30:00Z")) || !d.Releases[1].Prerelease {
		t.Errorf("releases (draft dropped) = %+v", d.Releases)
	}
	// A stale listing count gives way to a complete issue page.
	repo.OpenIssues = 5
	if d, err := c.FetchDetail(context.Background(), repo, now.Add(-time.Hour), 3, now); err != nil || d.Repo.OpenIssues != 2 || d.Repo.OpenPRs != 2 {
		t.Errorf("stale count: %+v %v", d.Repo, err)
	}

	// Nothing open: neither list is read.
	f.reset()
	d, err = c.FetchDetail(context.Background(), forge.Repo{FullName: "acme/app", DefaultBranch: "main"}, now.Add(-time.Hour), 3, now)
	if err != nil || d.Issues != nil || d.PRs != nil {
		t.Fatalf("nothing open = %+v %v", d, err)
	}
	if got := append(f.requests("/issues"), f.requests("/pulls")...); len(got) != 0 {
		t.Errorf("requests = %v", got)
	}
}

func TestHeatmapGraphQL(t *testing.T) {
	f, c := newFake(t, "s3cret")
	cal := fixture(t, "heatmap.graphql.json")
	f.graphql(func(w http.ResponseWriter, query string, vars map[string]any) {
		if !strings.Contains(query, "viewer") || !strings.Contains(query, "contributionCalendar") {
			t.Errorf("heatmap query = %s", query)
		}
		_, _ = w.Write(cal)
	})
	pts, err := c.Heatmap(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	// Days without contributions are left out, like Gitea's buckets.
	if len(pts) != 5 {
		t.Fatalf("points = %+v", pts)
	}
	total := 0
	for _, p := range pts {
		total += p.Contributions
	}
	// Noon UTC keeps the calendar day in every time zone within 12 hours.
	if first := pts[0]; first.Timestamp != date("2026-10-19T12:00:00Z").Unix() || first.Contributions != 3 || total != 11 {
		t.Errorf("first = %+v, total = %d", first, total)
	}
	if got := f.requests("POST /graphql"); len(got) != 1 {
		t.Errorf("requests = %v", got)
	}
	f.checkAuth("Bearer s3cret")
}

func TestNoTokenSkipsFullDetail(t *testing.T) {
	f, c := newFake(t, "")
	ctx := context.Background()
	now := date("2026-11-01T00:00:00Z")
	repo := forge.Repo{FullName: "acme/app", Name: "app", DefaultBranch: "main"}
	if _, err := c.ListRepos(ctx, nil); !errors.Is(err, forge.ErrNeedsToken) {
		t.Errorf("list repos err = %v", err)
	}
	if _, err := c.ListRepos(ctx, []string{"acme"}); !errors.Is(err, forge.ErrNeedsToken) {
		t.Errorf("list owner repos err = %v", err)
	}
	if _, err := c.FetchDetail(ctx, repo, now.Add(-time.Hour), 3, now); !errors.Is(err, forge.ErrNeedsToken) {
		t.Errorf("detail err = %v", err)
	}
	if _, err := c.FetchFiles(ctx, repo, []string{"docs/plans"}, now); !errors.Is(err, forge.ErrNeedsToken) {
		t.Errorf("files err = %v", err)
	}
	if _, err := c.SearchIssues(ctx, "waymark-idea"); !errors.Is(err, forge.ErrNeedsToken) {
		t.Errorf("search err = %v", err)
	}
	if _, err := c.Me(ctx); !errors.Is(err, forge.ErrNeedsToken) {
		t.Errorf("me err = %v", err)
	}
	if pts, err := c.Heatmap(ctx, "acme"); pts != nil || err != nil {
		t.Errorf("heatmap = %v %v", pts, err)
	}
	if got := f.requests(""); len(got) != 0 {
		t.Fatalf("no token must mean no full-detail requests, got %v", got)
	}

	// The mirror view needs no token.
	f.file("/repos/acme/app", "app_repo.json")
	f.file("/repos/acme/app/issues", "app_issues.json")
	f.file("/repos/acme/app/releases/latest", "app_release_latest.json")
	m, err := c.FetchMirror(ctx, "acme/app", now)
	if err != nil || m.Stars != 12 || m.LatestRelease != "v1.2.0" {
		t.Fatalf("mirror = %+v %v", m, err)
	}
	if got := f.requests(""); len(got) != 3 {
		t.Errorf("mirror requests = %v, want 3", got)
	}
	f.checkAuth("")
}

func TestRateLimitIsNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1793500440")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := New("github", srv.URL, "")
	_, err := c.FetchMirror(context.Background(), "acme/app", time.Now())
	// No time in the words: when to ask again is the app's to say, and it
	// may be sooner than the reset (app.RateLimitMaxWait).
	want := "github: rate limit reached (60 requests an hour without a token)"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	// With a token the advice goes; full detail (GraphQL first) names it the
	// same way for every repo, so one message covers them all.
	c = New("github", srv.URL, "t")
	_, err = c.FetchDetail(context.Background(), forge.Repo{FullName: "acme/app", DefaultBranch: "main"}, time.Now(), 3, time.Now())
	want = "github: rate limit reached"
	if err == nil || err.Error() != want {
		t.Errorf("detail err = %v, want %q", err, want)
	}
	// The app learns when to ask again, however the error is wrapped.
	if reset, ok := forge.RateLimited(fmt.Errorf("acme/app: %w", err)); !ok || !reset.Equal(time.Unix(1793500440, 0)) {
		t.Errorf("RateLimited = %v %v, want the X-RateLimit-Reset time", reset, ok)
	}
}

// A secondary limit (403 or 429 with Retry-After, the hourly allowance not
// spent) is a rate limit too, named the same way and reset Retry-After
// seconds on. A 403 without either header is a plain error.
func TestSecondaryRateLimit(t *testing.T) {
	now := time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)
	for _, code := range []int{http.StatusTooManyRequests, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.Header().Set("X-RateLimit-Remaining", "4000")
			w.WriteHeader(code)
		}))
		c := New("github", srv.URL, "t")
		c.now = func() time.Time { return now }
		_, err := c.Releases(context.Background(), "acme/app")
		srv.Close()
		if want := "github: rate limit reached"; err == nil || err.Error() != want {
			t.Errorf("%d: err = %v, want %q", code, err, want)
		}
		if reset, ok := forge.RateLimited(err); !ok || !reset.Equal(now.Add(2*time.Minute)) {
			t.Errorf("%d: RateLimited = %v %v, want Retry-After from now", code, reset, ok)
		}
	}
	f, c := newFake(t, "t")
	f.route("/repos/acme/app/releases", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	_, err := c.Releases(context.Background(), "acme/app")
	if _, ok := forge.RateLimited(err); ok || !hasStatus(err, http.StatusForbidden) {
		t.Errorf("a plain 403 = %v (rate limited %v)", err, ok)
	}
}

func TestErrorsCarryStatusNotToken(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.route("/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`))
	})
	_, err := c.ListRepos(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("list err = %v", err)
	}
	f.graphql(func(w http.ResponseWriter, query string, vars map[string]any) {
		_, _ = w.Write([]byte(`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve to a Repository with the name 'acme/gone'."}]}`))
	})
	_, err = c.FetchDetail(context.Background(), forge.Repo{FullName: "acme/gone", DefaultBranch: "main"}, time.Now(), 3, time.Now())
	if err == nil || err.Error() != "acme/gone tags: github /graphql: Could not resolve to a Repository with the name 'acme/gone'." {
		t.Errorf("graphql err = %v", err)
	}
	// A long message is cut at 160 bytes, never inside a character: after
	// 159 bytes of ASCII the first "£" (2 bytes) straddles byte 160.
	long := strings.Repeat("a", 159) + "££££"
	f.graphql(func(w http.ResponseWriter, query string, vars map[string]any) {
		b, _ := json.Marshal(map[string]any{"errors": []map[string]string{{"type": "INTERNAL", "message": long}}})
		_, _ = w.Write(b)
	})
	_, err = c.Tags(context.Background(), "acme/app")
	msg := strings.TrimPrefix(fmt.Sprint(err), "github /graphql: ")
	if !utf8.ValidString(msg) || msg != strings.Repeat("a", 159) {
		t.Errorf("cut message = %q (valid %v)", msg, utf8.ValidString(msg))
	}
	if _, err := c.Raw(context.Background(), "acme/app", "CHANGELOG.md", "main"); !IsNotFound(err) || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("missing file: IsNotFound false for %v", err)
	}
}

func TestETagReused(t *testing.T) {
	repo, issues, rel := fixture(t, "app_repo.json"), fixture(t, "app_issues.json"), fixture(t, "app_release_latest.json")
	var hits, notModified atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		etag := `W/"` + r.URL.Path + `"`
		if r.Header.Get("If-None-Match") == etag {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		switch r.URL.Path {
		case "/repos/acme/app":
			_, _ = w.Write(repo)
		case "/repos/acme/app/issues":
			if r.URL.Query().Get("state") != "open" {
				t.Errorf("issues query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write(issues)
		case "/repos/acme/app/releases/latest":
			_, _ = w.Write(rel)
		case "/repos/acme/app/contents/CHANGELOG.md":
			if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
				t.Errorf("raw accept = %q", r.Header.Get("Accept"))
			}
			_, _ = w.Write([]byte("# Changelog\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New("github", srv.URL, "")
	now := date("2026-11-01T00:00:00Z")
	for round := 0; round < 2; round++ {
		m, err := c.FetchMirror(context.Background(), "acme/app", now)
		if err != nil {
			t.Fatal(err)
		}
		if m.Forge != "github" || m.FullName != "acme/app" || m.HTMLURL != "https://github.com/acme/app" || m.Stars != 12 || !m.FetchedAt.Equal(now) {
			t.Errorf("mirror = %+v", m)
		}
		// Open issues count without PRs; newest updated first.
		if m.OpenIssues != 2 || len(m.Issues) != 2 || m.Issues[0].Number != 12 || m.Issues[0].User.Login == "" {
			t.Errorf("issues = %+v", m.Issues)
		}
		if m.LatestRelease != "v1.2.0" {
			t.Errorf("latest release = %q", m.LatestRelease)
		}
	}
	if hits.Load() != 6 || notModified.Load() != 3 {
		t.Errorf("hits = %d, 304s = %d; the second round must be conditional", hits.Load(), notModified.Load())
	}
	// Raw file bytes are conditional too.
	for round := 0; round < 2; round++ {
		b, err := c.Raw(context.Background(), "acme/app", "CHANGELOG.md", "main")
		if err != nil || string(b) != "# Changelog\n" {
			t.Errorf("raw round %d = %q %v", round, b, err)
		}
	}
	if hits.Load() != 8 || notModified.Load() != 4 {
		t.Errorf("raw: hits = %d, 304s = %d", hits.Load(), notModified.Load())
	}
}

func TestETagCacheBounded(t *testing.T) {
	var mu sync.Mutex
	conditional := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conditional[r.URL.RequestURI()] = r.Header.Get("If-None-Match") != ""
		mu.Unlock()
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"`+r.URL.Path+`"`)
		_, _ = w.Write([]byte(`"` + strings.Repeat("x", 998) + `"`))
	}))
	defer srv.Close()
	c := New("github", srv.URL, "t")
	c.maxCache = 4000 // four 1000-byte bodies
	get := func(path string, q url.Values) bool {
		t.Helper()
		var s string
		if _, err := c.getJSON(context.Background(), path, q, &s); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		u := path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		return conditional[u]
	}
	for i := 0; i < 10; i++ {
		get(fmt.Sprintf("/repos/acme/r%d", i), nil)
	}
	if !get("/repos/acme/r9", nil) {
		t.Error("the newest body must stay cached")
	}
	if get("/repos/acme/r0", nil) {
		t.Error("the oldest body must have gone: the cache is bounded")
	}
	// A commit window's since moves on every call: its pages are not kept.
	since := url.Values{"since": {"2026-08-09T00:00:00Z"}}
	get("/repos/acme/app/commits", since)
	if get("/repos/acme/app/commits", since) {
		t.Error("a since query must not be cached")
	}
}

func TestFetchFiltersPullsAndHandlesNoRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r":
			_, _ = w.Write([]byte(`{"full_name":"o/r","html_url":"https://github.com/o/r","stargazers_count":2}`))
		case "/repos/o/r/issues":
			_, _ = w.Write([]byte(`[{"number":2,"title":"a PR","pull_request":{"url":"x"}},{"number":1,"title":"bug","updated_at":"2026-10-01T00:00:00Z"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New("github", srv.URL, "tok")
	m, err := c.FetchMirror(context.Background(), "o/r", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if m.OpenIssues != 1 || m.Issues[0].Number != 1 || m.LatestRelease != "" {
		t.Errorf("mirror = %+v", m)
	}
	if _, err := c.FetchMirror(context.Background(), "o/gone", time.Now()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing repo err = %v", err)
	}
	if _, err := c.FetchMirror(context.Background(), "noslash", time.Now()); err == nil {
		t.Error("bad name must fail")
	}
}

func commitJSON(sha string, at time.Time) string {
	ts := at.UTC().Format(time.RFC3339)
	return fmt.Sprintf(`{"sha":%q,"html_url":"https://github.com/acme/app/commit/%s","commit":{"author":{"name":"Mark Example","date":%q},"committer":{"name":"Mark Example","date":%q},"message":"Change %s"},"author":null}`, sha, sha, ts, ts, sha)
}

func commitPage(prefix string, n int, newest time.Time) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = commitJSON(fmt.Sprintf("%s%038d", prefix, i), newest.Add(-time.Duration(i)*time.Minute))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestCommitsWindowAndLastCommit(t *testing.T) {
	f, c := newFake(t, "s3cret")
	ctx := context.Background()
	commits := fixture(t, "app_commits.json")
	f.route("/repos/acme/app/commits", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("sha") != "main" || q.Get("since") != "2026-10-01T00:00:00Z" || q.Get("per_page") != "100" || q.Get("page") != "1" {
			t.Errorf("commit query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write(commits)
	})
	got, more, err := c.Commits(ctx, "acme/app", "main", date("2026-10-01T00:00:00Z"), 3)
	if err != nil || more || len(got) != 2 {
		t.Fatalf("window = %d %v %v (the September commit is outside it)", len(got), more, err)
	}
	if got[0].Subject() != "Fix the export header" || got[0].AuthorName() != "Mark Example" || !got[0].When().Equal(date("2026-10-31T09:58:00Z")) ||
		got[0].HTMLURL != "https://github.com/acme/app/commit/4f1c2a9e8b7d6c5a4f3e2d1c0b9a8f7e6d5c4b3a" || got[0].Author.Login != "mark-example" {
		t.Errorf("commit 0 = %+v", got[0])
	}
	// Dated by the committer (the merge), not the author; no linked account.
	if got[1].Author != nil || !got[1].When().Equal(date("2026-10-28T16:45:00Z")) || got[1].AuthorName() != "Example Dev" {
		t.Errorf("commit 1 = %+v", got[1])
	}

	// Full pages with a next link: the cap cuts the window short.
	newest := date("2026-10-31T12:00:00Z")
	f.route("/repos/acme/app/commits", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("since") == "" {
			_, _ = w.Write([]byte(commitPage("ff", 1, newest)))
			return
		}
		w.Header().Set("Link", `<https://api.github.com/repositories/700001/commits?page=9>; rel="next", <https://api.github.com/repositories/700001/commits?page=40>; rel="last"`)
		_, _ = w.Write([]byte(commitPage("p"+q.Get("page"), 100, newest)))
	})
	f.reset()
	got, more, err = c.Commits(ctx, "acme/app", "main", date("2026-10-01T00:00:00Z"), 2)
	if err != nil || len(got) != 200 || !more || len(f.requests("/commits")) != 2 {
		t.Fatalf("capped = %d %v %v, requests %v", len(got), more, err, f.requests("/commits"))
	}

	// A full last page: the Link header has no next.
	f.route("/repos/acme/app/commits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://api.github.com/repositories/700001/commits?page=1>; rel="first"`)
		_, _ = w.Write([]byte(commitPage("aa", 100, newest)))
	})
	f.reset()
	got, more, err = c.Commits(ctx, "acme/app", "main", date("2026-10-01T00:00:00Z"), 5)
	if err != nil || len(got) != 100 || more || len(f.requests("/commits")) != 1 {
		t.Fatalf("last page = %d %v %v, requests %v", len(got), more, err, f.requests("/commits"))
	}

	// A window after every commit is empty; FetchDetail still finds the newest.
	f.route("/repos/acme/app/commits", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("since") != "" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		if r.URL.Query().Get("per_page") != "1" {
			t.Errorf("last commit query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(commitPage("bb", 1, newest)))
	})
	f.graphql(noTags)
	f.text("/repos/acme/app/releases", "[]")
	f.text("/repos/acme/app/actions/runs", `{"total_count":0,"workflow_runs":[]}`)
	now := date("2026-11-01T00:00:00Z")
	d, err := c.FetchDetail(ctx, forge.Repo{FullName: "acme/app", DefaultBranch: "main"}, now, 10, now)
	if err != nil || len(d.Commits) != 0 || d.CommitsCapped || d.LastCommit == nil || d.LastCommit.SHA[:2] != "bb" || !d.Since.Equal(now) {
		t.Errorf("late window = %+v %v", d, err)
	}
}

func TestEmptyRepoConflict(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.route("/repos/acme/empty/commits", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"Git Repository is empty.","documentation_url":"https://docs.github.com/rest","status":"409"}`))
	})
	got, more, err := c.Commits(context.Background(), "acme/empty", "main", time.Now(), 3)
	if err != nil || got != nil || more {
		t.Errorf("empty repo commits = %v %v %v", got, more, err)
	}
	lc, err := c.LastCommit(context.Background(), "acme/empty", "main")
	if err != nil || lc != nil {
		t.Errorf("empty repo last commit = %v %v", lc, err)
	}
	if runs, err := c.Runs(context.Background(), "acme/empty"); err != nil || runs != nil {
		t.Errorf("404 runs = %v %v", runs, err)
	}
}

func TestFetchFilesContentsAPI(t *testing.T) {
	f, c := newFake(t, "s3cret")
	content := func(name, accept string) http.HandlerFunc {
		b := fixture(t, name)
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept") != accept || r.URL.Query().Get("ref") != "main" {
				t.Errorf("%s: accept %q, query %s", r.URL.Path, r.Header.Get("Accept"), r.URL.RawQuery)
			}
			_, _ = w.Write(b)
		}
	}
	const raw, listing = "application/vnd.github.raw+json", "application/vnd.github+json"
	base := "/repos/acme/app/contents/"
	f.route(base+"CHANGELOG.md", content("app_CHANGELOG.md", raw))
	f.route(base+"docs/plans", content("app_plans_dir.json", listing))
	f.route(base+"docs/plans/2026-10-12-importer.md", content("app_plan_importer.md", raw))
	f.route(base+"docs/plans/2026-10-20-sync/plan.md", content("app_plan_sync.md", raw))
	repo := forge.Repo{FullName: "acme/app", DefaultBranch: "main", HTMLURL: "https://github.com/acme/app"}
	now := date("2026-11-01T00:00:00Z")
	files, err := c.FetchFiles(context.Background(), repo, []string{"docs/plans", "docs/missing"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if cl := files.Changelog; cl == nil || len(cl.Unreleased) != 2 || cl.LastVersion != "1.2.0" || cl.LastDate != "2026-10-31" {
		t.Errorf("changelog = %+v", files.Changelog)
	}
	if files.ChangelogURL != "https://github.com/acme/app/blob/main/CHANGELOG.md" || !files.FetchedAt.Equal(now) {
		t.Errorf("changelog url = %q", files.ChangelogURL)
	}
	if len(files.Plans) != 2 {
		t.Fatalf("plans = %+v", files.Plans)
	}
	// A plan directory's plan.md is a file: its link is a blob, not a tree.
	if p := files.Plans[0]; p.Title != "Sync plan" || p.Done != 2 || p.Total != 2 || p.Path != "docs/plans/2026-10-20-sync/plan.md" ||
		p.URL != "https://github.com/acme/app/blob/main/docs/plans/2026-10-20-sync/plan.md" {
		t.Errorf("plan 0 = %+v", p)
	}
	if p := files.Plans[1]; p.Title != "Importer plan" || p.Done != 1 || p.Total != 3 || p.Date.Format("2006-01-02") != "2026-10-12" ||
		p.URL != "https://github.com/acme/app/blob/main/docs/plans/2026-10-12-importer.md" {
		t.Errorf("plan 1 = %+v", p)
	}
	if got := f.requests("old-idea"); len(got) != 0 {
		t.Errorf("a plan older than PlanMaxAge is not read: %v", got)
	}

	// No CHANGELOG.md and no plan dir is not an error.
	files, err = c.FetchFiles(context.Background(), forge.Repo{FullName: "acme/bare", DefaultBranch: "main", HTMLURL: "https://github.com/acme/bare"}, []string{"docs/plans"}, now)
	if err != nil || files.Changelog != nil || files.ChangelogURL != "" || files.Plans != nil {
		t.Errorf("bare repo = %+v %v", files, err)
	}
}

func TestSearchIssuesScopedToOwners(t *testing.T) {
	f, c := newFake(t, "s3cret")
	f.file("/user", "user.json")
	var mu sync.Mutex
	var queries []string
	results := fixture(t, "search_issues.json")
	f.route("/search/issues", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("q"))
		mu.Unlock()
		_, _ = w.Write(results)
	})
	ctx := context.Background()
	// Before any listing the search covers the token user's repos, never all of GitHub.
	is, err := c.SearchIssues(ctx, "waymark-idea")
	if err != nil || len(is) != 1 {
		t.Fatalf("search = %+v %v", is, err)
	}
	if got := is[0]; got.Number != 21 || got.Repository == nil || got.Repository.FullName != "acme/app" || !strings.Contains(got.Body, "waymark-idea:csv-export") {
		t.Errorf("result = %+v", got)
	}
	f.text("/user/repos", "[]")
	f.text("/orgs/example-org/repos", "[]")
	f.text("/users/example-user/repos", "[]")
	if _, err := c.ListRepos(ctx, []string{"acme", "example-org", "example-user"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SearchIssues(ctx, "waymark-idea"); err != nil {
		t.Fatal(err)
	}
	want := []string{"waymark-idea is:issue user:acme", "waymark-idea is:issue user:acme org:example-org user:example-user"}
	if strings.Join(queries, "|") != strings.Join(want, "|") {
		t.Errorf("queries = %q, want %q", queries, want)
	}
}

// A repo the projects file names is read by name, mapped as the list maps it,
// with or without a token, conditional on its ETag.
func TestRepoByName(t *testing.T) {
	f, c := newFake(t, "")
	app := fixture(t, "app_repo.json")
	var conditional atomic.Int32
	f.route("/repos/acme/app", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"app-1"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"app-1"`)
		_, _ = w.Write(app)
	})
	f.text("/repos/acme/secret", `{"name":"secret","full_name":"acme/secret","private":true,"owner":{"login":"acme"},"html_url":"https://github.com/acme/secret","stargazers_count":3}`)
	ctx := context.Background()
	for round := 0; round < 2; round++ {
		r, err := c.Repo(ctx, "acme/app")
		if err != nil {
			t.Fatal(err)
		}
		if r.Forge != "github" || r.FullName != "acme/app" || r.Private || r.Website != "https://app.example.com" || r.OpenIssues != 4 ||
			!r.UpdatedAt.Equal(date("2026-10-31T09:58:00Z")) {
			t.Errorf("round %d repo = %+v", round, r)
		}
	}
	if got := f.requests("/repos/acme/app"); len(got) != 2 || conditional.Load() != 1 {
		t.Errorf("requests = %v, %d conditional; want one call per read, the second conditional", got, conditional.Load())
	}
	if r, err := c.Repo(ctx, "acme/secret"); err != nil || !r.Private {
		t.Errorf("private repo = %+v %v", r, err)
	}
	if _, err := c.Repo(ctx, "acme/gone"); !IsNotFound(err) {
		t.Errorf("missing repo err = %v", err)
	}
	if _, err := c.Repo(ctx, "noslash"); err == nil {
		t.Error("a name without an owner must fail")
	}
	// The mirror view says whether the repo is private.
	f.text("/repos/acme/secret/issues", `[]`)
	if m, err := c.FetchMirror(ctx, "acme/secret", time.Now()); err != nil || !m.Private {
		t.Errorf("private mirror = %+v %v", m, err)
	}
	f.checkAuth("")
}
