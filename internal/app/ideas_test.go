package app

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/forge"
	"github.com/Will-Luck/waymark/internal/model"
)

// writeIdeas writes an ideas file and returns its path.
func writeIdeas(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ideas.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func ideaByID(s *model.Site, id string) (model.Idea, bool) {
	for _, i := range s.Ideas {
		if i.ID == id {
			return i, true
		}
	}
	return model.Idea{}, false
}

func ideaIDs(s *model.Site) []string {
	var out []string
	for _, i := range s.Ideas {
		out = append(out, i.ID)
	}
	return out
}

// fileBody is the body a File-as-issue link prefills.
func fileBody(t *testing.T, i model.Idea) string {
	t.Helper()
	u, err := url.Parse(i.FileURL)
	if err != nil || i.FileURL == "" {
		t.Fatalf("idea %s: file url %q", i.ID, i.FileURL)
	}
	return u.Query().Get("body")
}

// queryForge records the filed-idea searches.
type queryForge struct {
	forge.Forge
	mu      sync.Mutex
	queries []string
}

func (q *queryForge) SearchIssues(ctx context.Context, query string) ([]forge.Issue, error) {
	q.mu.Lock()
	q.queries = append(q.queries, query)
	q.mu.Unlock()
	return q.Forge.SearchIssues(ctx, query)
}

// Open issues with an ideas label (any case), on the repos read in full,
// become ideas: one per issue, its kind and size from kind/* and size/*
// labels. A mirror's issues, PRs and other issues do not. With no ideas file
// there is no roundup date.
func TestLabelledIssuesBecomeIdeas(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day),
		Issues: []acmeIssue{
			{N: 4, Title: "Dark mode", Updated: fixedNow.Add(-time.Hour), Labels: []string{"Idea", "kind/quick-win", "size/s"}},
			{N: 5, Title: "Crash on start", Updated: fixedNow.Add(-2 * time.Hour), Labels: []string{"bug"}},
			{N: 6, Title: "Plugin system", Updated: fixedNow.Add(-3 * time.Hour), Labels: []string{"enhancement", "Kind/Bold", "SIZE/L"}},
		},
		PRs: []acmeIssue{{N: 7, Title: "Add dark mode", Updated: fixedNow.Add(-time.Hour), Labels: []string{"idea"}}}})
	gh := h.forge("github", "github", "t2", []string{"acme"},
		&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day),
			Issues: []acmeIssue{{N: 9, Title: "Mirror wish", Updated: fixedNow.Add(-time.Hour), Labels: []string{"idea"}}}},
		&acmeRepo{Owner: "acme", Name: "app", Updated: fixedNow.Add(-day),
			Issues: []acmeIssue{{N: 2, Title: "Offline mode", Updated: fixedNow.Add(-time.Hour), Labels: []string{"idea"}}},
			PRs:    []acmeIssue{{N: 3, Title: "Offline cache", Updated: fixedNow.Add(-time.Hour), Labels: []string{"idea"}}}})
	s := runOnce(t, h.app(func(c *config.Config) { c.Ideas.Labels = []string{"idea", "enhancement"} }, g, gh))

	want := []model.Idea{
		{ID: "gitea:acme/tool#4", Project: "tool", Title: "Dark mode", Kind: "quick-win", Size: "S", ExistingIssue: 4,
			Source: "issue", URL: "https://gitea.example.com/acme/tool/issues/4"},
		{ID: "gitea:acme/tool#6", Project: "tool", Title: "Plugin system", Kind: "bold", Size: "L", ExistingIssue: 6,
			Source: "issue", URL: "https://gitea.example.com/acme/tool/issues/6"},
		{ID: "github:acme/app#2", Project: "app", Title: "Offline mode", ExistingIssue: 2,
			Source: "issue", URL: "https://github.example.com/acme/app/issues/2"},
	}
	var got []model.Idea
	for _, i := range s.Ideas {
		got = append(got, model.Idea{ID: i.ID, Project: i.Project, Title: i.Title, Kind: i.Kind, Size: i.Size,
			ExistingIssue: i.ExistingIssue, Source: i.Source, URL: i.URL})
		if i.FileURL != "" || i.FiledIssue != 0 || i.ProjectName == "" {
			t.Errorf("idea %s: file url %q, filed #%d, project name %q", i.ID, i.FileURL, i.FiledIssue, i.ProjectName)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("ideas =\n%+v\nwant\n%+v", got, want)
	}
	if n := len(find(s, "tool").Ideas); n != 2 {
		t.Errorf("tool shows %d ideas, want 2", n)
	}
	if s.RoundupDate != "" || s.Stats.Ideas != 3 {
		t.Errorf("roundup %q, ideas stat %d", s.RoundupDate, s.Stats.Ideas)
	}

	// A primary that gives no detail without a token is read like a mirror;
	// its issues are still the primary's.
	h = newAcmeHost(t)
	g = h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)})
	gh = h.forge("github", "github", "", nil, &acmeRepo{Owner: "acme", Name: "cli", Updated: fixedNow.Add(-day),
		Issues: []acmeIssue{{N: 9, Title: "Add a flag", Updated: fixedNow.Add(-time.Hour), Labels: []string{"idea"}}}})
	projects := filepath.Join(t.TempDir(), "projects.yaml")
	if err := os.WriteFile(projects, []byte("projects:\n  - slug: cli\n    name: CLI\n    repos:\n      - github: acme/cli\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s = runOnce(t, h.app(func(c *config.Config) { c.Projects, c.Ideas.Labels = config.FileRef{File: projects}, []string{"idea"} }, g, gh))
	if i, ok := ideaByID(s, "github:acme/cli#9"); !ok || i.Project != "cli" || i.URL != "https://github.example.com/acme/cli/issues/9" {
		t.Errorf("tokenless primary: ideas = %+v", s.Ideas)
	}
}

// An issue filed from a file idea shows once, as the file idea with its
// issue link, though it carries an ideas label; so does the issue a file
// idea names as existing_issue. Other labelled issues are ideas of their own.
func TestFiledFileIdeaNotDuplicated(t *testing.T) {
	h := newAcmeHost(t)
	idea := []string{"idea"}
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day),
		Filed: []string{"waymark-idea:tool-01"}, // issue #50
		Issues: []acmeIssue{
			{N: 50, Title: "Sleep timer", Updated: fixedNow.Add(-time.Hour), Labels: idea},
			{N: 8, Title: "Export", Updated: fixedNow.Add(-2 * time.Hour), Labels: idea},
			{N: 9, Title: "Themes", Updated: fixedNow.Add(-3 * time.Hour), Labels: idea},
		}})
	file := writeIdeas(t, `{"roundup_date":"2026-10-31","ideas":[`+
		`{"id":"tool-01","project":"tool","title":"Sleep timer","size":"S","kind":"quick-win"},`+
		`{"id":"tool-02","project":"tool","title":"Export","size":"M","kind":"bold","existing_issue":8}]}`)
	s := runOnce(t, h.app(func(c *config.Config) {
		c.Ideas.Labels, c.Ideas.File = []string{"idea"}, config.FileRef{File: file}
	}, g))
	if got, want := ideaIDs(s), []string{"tool-01", "tool-02", "gitea:acme/tool#9"}; !slices.Equal(got, want) {
		t.Fatalf("ideas = %v, want %v", got, want)
	}
	filed, _ := ideaByID(s, "tool-01")
	if filed.Source != "file" || filed.FiledIssue != 50 || filed.FiledURL != "https://gitea.example.com/acme/tool/issues/50" || filed.FileURL != "" {
		t.Errorf("filed file idea = %+v", filed)
	}
	if ex, _ := ideaByID(s, "tool-02"); ex.Source != "file" || ex.ExistingIssue != 8 || ex.Kind != "bold" {
		t.Errorf("existing-issue file idea = %+v", ex)
	}
	if s.RoundupDate != "2026-10-31" || len(find(s, "tool").Ideas) != 3 {
		t.Errorf("roundup %q, tool ideas %d", s.RoundupDate, len(find(s, "tool").Ideas))
	}
}

// No ideas file and no issue with an ideas label (none configured, or none
// matching): no ideas, no roundup date, no filed-idea search and no ideas
// source. The same fixture with a matching label has an idea, so it can show
// one at all.
func TestNoIdeasConfigured(t *testing.T) {
	for _, tc := range []struct {
		labels []string
		want   int
	}{{nil, 0}, {[]string{"enhancement"}, 0}, {[]string{"idea"}, 1}} {
		h := newAcmeHost(t)
		g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day),
			Issues: []acmeIssue{
				{N: 1, Title: "Crash on start", Updated: fixedNow.Add(-time.Hour), Labels: []string{"bug"}},
				{N: 2, Title: "Dark mode", Updated: fixedNow.Add(-time.Hour), Labels: []string{"idea"}},
			}})
		s := runOnce(t, h.app(func(c *config.Config) { c.Ideas.Labels = tc.labels }, g))
		if len(s.Ideas) != tc.want || s.Stats.Ideas != tc.want || s.Ideas == nil {
			t.Errorf("labels %v: ideas = %+v (stat %d), want %d", tc.labels, s.Ideas, s.Stats.Ideas, tc.want)
		}
		if s.RoundupDate != "" {
			t.Errorf("labels %v: roundup date %q", tc.labels, s.RoundupDate)
		}
		if got := h.requests("gitea", "/repos/issues/search"); len(got) != 0 {
			t.Errorf("labels %v: searched for filed ideas with no ideas file: %v", tc.labels, got)
		}
		if _, ok := sourceStatus(s, "ideas"); ok {
			t.Errorf("labels %v: sources = %v, want no ideas source", tc.labels, sourceNamesOf(s))
		}
	}
}

// The filed-idea marker is ideas.marker: the search looks for it, a filed
// issue is found by it (not by the default marker), and "File as issue" ends
// its body with it.
func TestIdeaMarkerConfigurable(t *testing.T) {
	h := newAcmeHost(t)
	g := h.forge("gitea", "gitea", "t1", []string{"acme"}, &acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day),
		Filed: []string{"acme-idea:tool-01", "waymark-idea:tool-02"}}) // issues #50, #51
	file := writeIdeas(t, `{"ideas":[{"id":"tool-01","project":"tool","title":"Sleep timer"},{"id":"tool-02","project":"tool","title":"Export"}]}`)
	cfg := h.config(func(c *config.Config) { c.Ideas = config.Ideas{File: config.FileRef{File: file}, Marker: "acme-idea:"} }, g)
	clients := NewForges(cfg)
	rec := &queryForge{Forge: clients[0]}
	clients[0] = rec
	s := runOnce(t, h.start(cfg, clients))
	if !slices.Equal(rec.queries, []string{"acme-idea"}) {
		t.Errorf("searches = %q, want the marker without its colon", rec.queries)
	}
	if one, _ := ideaByID(s, "tool-01"); one.FiledIssue != 50 || one.FiledURL != "https://gitea.example.com/acme/tool/issues/50" {
		t.Errorf("tool-01 = %+v, want filed as #50 by the configured marker", one)
	}
	two, _ := ideaByID(s, "tool-02")
	if two.FiledIssue != 0 {
		t.Errorf("tool-02 = %+v, found by the default marker", two)
	}
	if body := fileBody(t, two); !strings.HasSuffix(body, "\nacme-idea:tool-02") {
		t.Errorf("file-as-issue body = %q, want it to end with the configured marker", body)
	}
	if st, ok := sourceStatus(s, "ideas"); !ok || !st.OK {
		t.Errorf("ideas status = %+v (present %v)", st, ok)
	}
}

// include_private false: a private repo's labelled issue is not an idea, and
// a file idea filed into a private repo shows no link to it, also when the
// app does not know that repo (the search sees every repo the token can, and
// does not say which are private). Included, both show, so the fixture can
// show them.
func TestIncludePrivateFalseHidesIdeas(t *testing.T) {
	for _, include := range []bool{true, false} {
		h := newAcmeHost(t)
		g := h.forge("gitea", "gitea", "t1", []string{"acme"},
			&acmeRepo{Owner: "acme", Name: "tool", Updated: fixedNow.Add(-day)},
			&acmeRepo{Owner: "acme", Name: "secret", Private: true, Updated: fixedNow.Add(-day),
				Issues: []acmeIssue{{N: 3, Title: "Rotate the keys", Updated: fixedNow.Add(-time.Hour), Labels: []string{"idea"}}}},
			// Outside owners and not in the projects file: unknown to the app.
			&acmeRepo{Owner: "other", Name: "vault", Private: true, Updated: fixedNow.Add(-day), Filed: []string{"waymark-idea:tool-01"}})
		file := writeIdeas(t, `{"ideas":[{"id":"tool-01","project":"tool","title":"Sleep timer"}]}`)
		s := runOnce(t, h.app(func(c *config.Config) {
			c.IncludePrivate = include
			c.Ideas.Labels, c.Ideas.File = []string{"idea"}, config.FileRef{File: file}
		}, g))
		_, label := ideaByID(s, "gitea:acme/secret#3")
		filed, ok := ideaByID(s, "tool-01")
		if !ok {
			t.Fatalf("include_private %v: ideas = %v", include, ideaIDs(s))
		}
		linked := strings.Contains(filed.FiledURL, "other/vault")
		if label != include || linked != include {
			t.Errorf("include_private %v: private labelled issue an idea %v, filed link %q", include, label, filed.FiledURL)
		}
		if !include && filed.FileURL == "" {
			t.Errorf("the unlinked file idea has no File as issue link: %+v", filed)
		}
		if got := h.requests("gitea", "/repos/acme/secret"); !include && len(got) != 0 {
			t.Errorf("the private repo was read: %v", got)
		}
	}
}
