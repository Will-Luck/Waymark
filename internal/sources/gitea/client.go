// Package gitea is a read-only client for the few Gitea API calls Waymark
// makes. The token is only ever sent as "Authorization: token <t>" to the
// configured base URL.
package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Will-Luck/waymark/internal/forge"
)

const (
	pageSize = 50
	maxBody  = 16 << 20
)

type Client struct {
	id string
	// Base is the API root, e.g. https://git.example.com/api/v1.
	Base  string
	Token string
	HTTP  *http.Client
	// UserAgent goes with every request; New sets waymark/dev.
	UserAgent string

	mu sync.Mutex
	me string // the token user's login, once read
}

func New(id, base, token string) *Client {
	return &Client{
		id:        id,
		Base:      strings.TrimRight(base, "/"),
		Token:     token,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: "waymark/dev",
	}
}

// HTTPError is a non-2xx answer.
type HTTPError struct {
	Status int
	Path   string
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("gitea %s: HTTP %d %s", e.Path, e.Status, clip(strings.TrimSpace(e.Body), 160))
}

// clip cuts s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// IsNotFound reports a 404 (missing file, repo without Actions, ...).
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// IsNotFound is the package's IsNotFound, for forge.FileSource.
func (c *Client) IsNotFound(err error) bool { return IsNotFound(err) }

func (c *Client) do(ctx context.Context, path string, q url.Values) ([]byte, http.Header, error) {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		// The URL carries no secret, but keep the message short.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, nil, fmt.Errorf("gitea %s: %w", path, ue.Err)
		}
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, nil, fmt.Errorf("gitea %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.Header, &HTTPError{Status: resp.StatusCode, Path: path, Body: string(body)}
	}
	return body, resp.Header, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	body, h, err := c.do(ctx, path, q)
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return h, fmt.Errorf("gitea %s: %w", path, err)
	}
	return h, nil
}

// repoPath turns "owner/name" into "/repos/owner/name".
func repoPath(full string) string {
	owner, name, _ := strings.Cut(full, "/")
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func filePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// ID is the config id of this forge.
func (c *Client) ID() string { return c.id }

// Kind is always "gitea".
func (c *Client) Kind() string { return "gitea" }

// Me returns the user the token belongs to.
func (c *Client) Me(ctx context.Context) (forge.User, error) {
	var u forge.User
	_, err := c.getJSON(ctx, "/user", nil, &u)
	return u, err
}

type runList struct {
	WorkflowRuns []forge.Run `json:"workflow_runs"`
	TotalCount   int         `json:"total_count"`
}

// login is the token user's login, read once.
func (c *Client) login(ctx context.Context) (string, error) {
	c.mu.Lock()
	me := c.me
	c.mu.Unlock()
	if me != "" {
		return me, nil
	}
	u, err := c.Me(ctx)
	if err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("gitea /user: no login in the answer")
	}
	c.mu.Lock()
	c.me = u.Login
	c.mu.Unlock()
	return u.Login, nil
}

// ListRepos returns the repos owned by one of owners (compared
// case-insensitively) among those the token can see (/user/repos, paginated).
// That list also holds repos the user collaborates on and its organisations'
// repos, so no owners means the token user's own repos only, as on GitHub.
func (c *Client) ListRepos(ctx context.Context, owners []string) ([]forge.Repo, error) {
	if len(owners) == 0 {
		me, err := c.login(ctx)
		if err != nil {
			return nil, err
		}
		owners = []string{me}
	}
	var all []forge.Repo
	for page := 1; page <= 40; page++ {
		var batch []forge.Repo
		q := url.Values{"limit": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}
		if _, err := c.getJSON(ctx, "/user/repos", q, &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			r.Forge = c.id
			if ownedBy(r, owners) {
				all = append(all, r)
			}
		}
		if len(batch) < pageSize {
			break
		}
	}
	return all, nil
}

// Repo reads one repo by owner/name (GET /repos/{full}). A missing repo is an
// error for which IsNotFound is true.
func (c *Client) Repo(ctx context.Context, full string) (forge.Repo, error) {
	var r forge.Repo
	if _, err := c.getJSON(ctx, repoPath(full), nil, &r); err != nil {
		return forge.Repo{}, err
	}
	r.Forge = c.id
	return r, nil
}

func ownedBy(r forge.Repo, owners []string) bool {
	owner := r.Owner.Login
	if owner == "" {
		owner, _, _ = strings.Cut(r.FullName, "/")
	}
	for _, o := range owners {
		if strings.EqualFold(o, owner) {
			return true
		}
	}
	return false
}

// TagPages caps the tag pages read per repo: 300 tags, more than a busy repo
// makes in years of releases.
const TagPages = 6

func (c *Client) Tags(ctx context.Context, full string) ([]forge.Tag, error) {
	var tags []forge.Tag
	seen := map[string]bool{}
	for page := 1; page <= TagPages; page++ {
		var batch []forge.Tag
		q := url.Values{"limit": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}}
		if _, err := c.getJSON(ctx, repoPath(full)+"/tags", q, &batch); err != nil {
			return nil, err
		}
		fresh := 0
		for _, t := range batch {
			if !seen[t.Name] {
				seen[t.Name] = true
				tags = append(tags, t)
				fresh++
			}
		}
		// A short page is the last; a page of names already seen means the
		// server ignored the page number.
		if len(batch) < pageSize || fresh == 0 {
			break
		}
	}
	return tags, nil
}

// Releases returns published releases (drafts dropped).
func (c *Client) Releases(ctx context.Context, full string) ([]forge.Release, error) {
	var rels []forge.Release
	if _, err := c.getJSON(ctx, repoPath(full)+"/releases", url.Values{"limit": {strconv.Itoa(pageSize)}}, &rels); err != nil {
		return nil, err
	}
	out := rels[:0]
	for _, r := range rels {
		if !r.Draft {
			out = append(out, r)
		}
	}
	return out, nil
}

// OpenIssues returns up to 10 open issues (pull requests excluded).
func (c *Client) OpenIssues(ctx context.Context, full string) ([]forge.Issue, error) {
	var issues []forge.Issue
	q := url.Values{"state": {"open"}, "type": {"issues"}, "limit": {"10"}}
	if _, err := c.getJSON(ctx, repoPath(full)+"/issues", q, &issues); err != nil {
		return nil, err
	}
	out := issues[:0]
	for _, i := range issues {
		if !i.IsPull() {
			out = append(out, i)
		}
	}
	return out, nil
}

// OpenPulls returns up to 10 open pull requests, most recently updated first.
func (c *Client) OpenPulls(ctx context.Context, full string) ([]forge.Issue, error) {
	var prs []forge.Issue
	q := url.Values{"state": {"open"}, "limit": {"10"}, "sort": {"recentupdate"}}
	_, err := c.getJSON(ctx, repoPath(full)+"/pulls", q, &prs)
	return prs, err
}

func commitQuery(branch string, limit, page int) url.Values {
	q := url.Values{
		"limit":        {strconv.Itoa(limit)},
		"page":         {strconv.Itoa(page)},
		"stat":         {"false"},
		"verification": {"false"},
		"files":        {"false"},
	}
	if branch != "" {
		q.Set("sha", branch)
	}
	return q
}

// Commits returns the branch's commits after since, newest first, reading at
// most maxPages pages of 50. more is true when the window was cut short.
func (c *Client) Commits(ctx context.Context, full, branch string, since time.Time, maxPages int) (commits []forge.Commit, more bool, err error) {
	for page := 1; page <= maxPages; page++ {
		q := commitQuery(branch, pageSize, page)
		q.Set("since", since.UTC().Format(time.RFC3339))
		var batch []forge.Commit
		h, err := c.getJSON(ctx, repoPath(full)+"/commits", q, &batch)
		if err != nil {
			if isEmptyRepo(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		// Gitea's since filter is trusted, but a commit older than the window
		// must never inflate a count, and reaching one means the window is done.
		passed := false
		for _, cm := range batch {
			if cm.When().Before(since) {
				passed = true
				continue
			}
			commits = append(commits, cm)
		}
		hasMore := strings.EqualFold(h.Get("X-Hasmore"), "true")
		if passed || len(batch) < pageSize || (!hasMore && h.Get("X-Hasmore") != "") {
			return commits, false, nil
		}
		if page == maxPages {
			return commits, true, nil
		}
	}
	return commits, false, nil
}

// LastCommit returns the newest commit on the branch, or nil for an empty repo.
func (c *Client) LastCommit(ctx context.Context, full, branch string) (*forge.Commit, error) {
	var batch []forge.Commit
	if _, err := c.getJSON(ctx, repoPath(full)+"/commits", commitQuery(branch, 1, 1), &batch); err != nil {
		if isEmptyRepo(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(batch) == 0 {
		return nil, nil
	}
	return &batch[0], nil
}

// isEmptyRepo: Gitea answers 409 for commit lists of an empty repository.
func isEmptyRepo(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusConflict
}

// Runs returns the newest 5 Actions runs; a repo without Actions gives none.
func (c *Client) Runs(ctx context.Context, full string) ([]forge.Run, error) {
	var rl runList
	if _, err := c.getJSON(ctx, repoPath(full)+"/actions/runs", url.Values{"limit": {"5"}}, &rl); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(rl.WorkflowRuns) > 5 {
		rl.WorkflowRuns = rl.WorkflowRuns[:5]
	}
	return rl.WorkflowRuns, nil
}

// Raw returns a file's bytes at ref ("" = default branch). A missing file is
// an error for which IsNotFound is true.
func (c *Client) Raw(ctx context.Context, full, path, ref string) ([]byte, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	b, _, err := c.do(ctx, repoPath(full)+"/raw/"+filePath(path), q)
	return b, err
}

// Dir lists a directory at ref.
func (c *Client) Dir(ctx context.Context, full, path, ref string) ([]forge.Entry, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	var entries []forge.Entry
	_, err := c.getJSON(ctx, repoPath(full)+"/contents/"+filePath(path), q, &entries)
	return entries, err
}

// Heatmap is the owner's contribution heatmap (15-minute buckets).
func (c *Client) Heatmap(ctx context.Context, user string) ([]forge.HeatPoint, error) {
	var pts []forge.HeatPoint
	_, err := c.getJSON(ctx, "/users/"+url.PathEscape(user)+"/heatmap", nil, &pts)
	return pts, err
}

// SearchIssues searches issues in every repo the token can see.
func (c *Client) SearchIssues(ctx context.Context, query string) ([]forge.Issue, error) {
	var issues []forge.Issue
	q := url.Values{"q": {query}, "state": {"all"}, "type": {"issues"}, "limit": {strconv.Itoa(pageSize)}}
	_, err := c.getJSON(ctx, "/repos/issues/search", q, &issues)
	return issues, err
}
