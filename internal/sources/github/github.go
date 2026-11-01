// Package github is a read-only client for the GitHub REST and GraphQL calls
// Waymark makes. Without a token only FetchMirror runs (60 requests an hour);
// full detail needs one. Every GET is conditional on the previous ETag, and the
// token is only ever sent as "Authorization: Bearer <t>" to the configured base.
package github

import (
	"bytes"
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
	defaultBase = "https://api.github.com"
	pageSize    = 100
	maxBody     = 8 << 20
	// cacheBytes bounds the bodies kept for ETags; the least recently used go first.
	cacheBytes = 32 << 20
	jsonType   = "application/vnd.github+json"
	rawType    = "application/vnd.github.raw+json"
)

type cached struct {
	etag string
	body []byte
	link string
	used uint64
}

type Client struct {
	id string
	// Base is the REST root; GraphQL goes to Base + "/graphql".
	Base  string
	Token string
	HTTP  *http.Client
	// UserAgent goes with every request (GitHub asks every API client for
	// one); New sets waymark/dev.
	UserAgent string

	mu        sync.Mutex
	cache     map[string]*cached
	cacheSize int
	maxCache  int
	tick      uint64
	// scope is the search qualifiers for the owners ListRepos last read.
	scope []string
	// now is the clock a Retry-After counts from (nil: time.Now).
	now func() time.Time
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// New returns a client for the forge with config id id. An empty base means
// https://api.github.com.
func New(id, base, token string) *Client {
	if base == "" {
		base = defaultBase
	}
	return &Client{
		id:        id,
		Base:      strings.TrimRight(base, "/"),
		Token:     token,
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		UserAgent: "waymark/dev",
		maxCache:  cacheBytes,
	}
}

// HTTPError is a non-2xx answer other than a spent rate limit.
type HTTPError struct {
	Status int
	Path   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("github %s: HTTP %d", e.Path, e.Status) }

func hasStatus(err error, code int) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == code
}

// IsNotFound reports a 404 (missing file, repo or owner).
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

// rateLimitError names when the spent allowance comes back. It is the same
// for every repo, so callers pass it on unwrapped.
type rateLimitError struct {
	reset   time.Time
	noToken bool
}

func (e *rateLimitError) Error() string {
	msg := "github: rate limit reached"
	if e.noToken {
		msg += " (60 requests an hour without a token)"
	}
	if !e.reset.IsZero() {
		msg += "; resets " + e.reset.UTC().Format("15:04 UTC")
	}
	return msg
}

// RateLimitReset is when the allowance comes back (zero: not said), for
// forge.RateLimited.
func (e *rateLimitError) RateLimitReset() time.Time { return e.reset }

func isRateLimit(err error) bool {
	var rl *rateLimitError
	return errors.As(err, &rl)
}

// rateLimited is the error for a limited answer: reset after Retry-After (a
// secondary limit) when it is given, else at X-RateLimit-Reset.
func (c *Client) rateLimited(h http.Header) error {
	e := &rateLimitError{noToken: c.Token == ""}
	ra := strings.TrimSpace(h.Get("Retry-After"))
	if sec, err := strconv.ParseInt(ra, 10, 64); err == nil && sec >= 0 {
		e.reset = c.clock().Add(time.Duration(sec) * time.Second)
	} else if at, err := http.ParseTime(ra); err == nil {
		e.reset = at
	} else if sec, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		e.reset = time.Unix(sec, 0)
	}
	return e
}

// statusError is a 403 or 429 that says the hourly allowance is spent
// (X-RateLimit-Remaining 0) or carries a secondary limit's Retry-After as a
// rate limit, anything else as an HTTPError.
func (c *Client) statusError(resp *http.Response, where string) error {
	limited := resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) && limited {
		return c.rateLimited(resp.Header)
	}
	return &HTTPError{Status: resp.StatusCode, Path: where}
}

func (c *Client) send(req *http.Request, where string) (*http.Response, error) {
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("github %s: %w", where, err)
	}
	return resp, nil
}

func (c *Client) cacheGet(key string) (cached, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok {
		return cached{}, false
	}
	c.tick++
	e.used = c.tick
	return *e, true
}

func (c *Client) cachePut(key string, e cached) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.cache[key]; ok {
		c.cacheSize -= len(old.body)
		delete(c.cache, key)
	}
	// A body over a quarter of the cache is not worth the evictions.
	if len(e.body) > c.maxCache/4 {
		return
	}
	if c.cache == nil {
		c.cache = map[string]*cached{}
	}
	for c.cacheSize+len(e.body) > c.maxCache && len(c.cache) > 0 {
		var oldest string
		var min uint64
		for k, v := range c.cache {
			if oldest == "" || v.used < min {
				oldest, min = k, v.used
			}
		}
		c.cacheSize -= len(c.cache[oldest].body)
		delete(c.cache, oldest)
	}
	c.tick++
	e.used = c.tick
	c.cache[key] = &e
	c.cacheSize += len(e.body)
}

// do GETs path with query q, accepting accept, conditional on the last ETag.
// A 304 answers from the cache, Link header included.
func (c *Client) do(ctx context.Context, path string, q url.Values, accept string) ([]byte, http.Header, error) {
	where := path
	if len(q) > 0 {
		where += "?" + q.Encode()
	}
	u := strings.TrimRight(c.Base, "/") + where
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", accept)
	key := accept + " " + u
	// A commit window's since moves on every call, so its pages never recur.
	cacheable := !q.Has("since")
	var prev cached
	havePrev := false
	if cacheable {
		if prev, havePrev = c.cacheGet(key); havePrev {
			req.Header.Set("If-None-Match", prev.etag)
		}
	}
	resp, err := c.send(req, where)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified && havePrev {
		h := http.Header{}
		if prev.link != "" {
			h.Set("Link", prev.link)
		}
		return prev.body, h, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.Header, c.statusError(resp, where)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, nil, fmt.Errorf("github %s: %w", where, err)
	}
	if etag := resp.Header.Get("ETag"); cacheable && etag != "" {
		c.cachePut(key, cached{etag: etag, body: body, link: resp.Header.Get("Link")})
	}
	return body, resp.Header, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	body, h, err := c.do(ctx, path, q, jsonType)
	if err != nil {
		return h, err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return h, fmt.Errorf("github %s: %w", path, err)
	}
	return h, nil
}

// graphql POSTs one query to Base + "/graphql" and decodes its data into out.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Base, "/")+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.send(req, "/graphql")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c.statusError(resp, "/graphql")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("github /graphql: %w", err)
	}
	var res struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return fmt.Errorf("github /graphql: %w", err)
	}
	if len(res.Errors) > 0 {
		if res.Errors[0].Type == "RATE_LIMITED" {
			return c.rateLimited(resp.Header)
		}
		return fmt.Errorf("github /graphql: %s", clip(res.Errors[0].Message, 160))
	}
	if err := json.Unmarshal(res.Data, out); err != nil {
		return fmt.Errorf("github /graphql: %w", err)
	}
	return nil
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

func pageQuery(perPage, page int) url.Values {
	return url.Values{"per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
}

// ID is the config id of this forge.
func (c *Client) ID() string { return c.id }

// Kind is always "github".
func (c *Client) Kind() string { return "github" }

// Me returns the user the token belongs to.
func (c *Client) Me(ctx context.Context) (forge.User, error) {
	if c.Token == "" {
		return forge.User{}, forge.ErrNeedsToken
	}
	var u forge.User
	_, err := c.getJSON(ctx, "/user", nil, &u)
	return u, err
}

// ListRepos returns the repos of each owner: the token user's own through
// /user/repos (private ones included), an organisation's through /orgs/, and
// any other user's public ones through /users/. No owners means the token
// user's. An owner that is neither (both answer 404, a typo) gives no repos
// and no error: the app logs an owner with none once. See ghRepo for how the
// fields map.
func (c *Client) ListRepos(ctx context.Context, owners []string) ([]forge.Repo, error) {
	if c.Token == "" {
		return nil, forge.ErrNeedsToken
	}
	me, err := c.Me(ctx)
	if err != nil {
		return nil, err
	}
	if len(owners) == 0 {
		owners = []string{me.Login}
	}
	var all []forge.Repo
	var scope []string
	seen, scoped := map[string]bool{}, map[string]bool{}
	for _, o := range owners {
		var repos []forge.Repo
		var qual string
		if strings.EqualFold(o, me.Login) {
			repos, err = c.repoPages(ctx, "/user/repos", url.Values{"affiliation": {"owner"}})
			qual = "user:" + me.Login
		} else {
			repos, err = c.repoPages(ctx, "/orgs/"+url.PathEscape(o)+"/repos", nil)
			qual = "org:" + o
			if IsNotFound(err) {
				repos, err = c.repoPages(ctx, "/users/"+url.PathEscape(o)+"/repos", nil)
				qual = "user:" + o
			}
		}
		if IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if k := strings.ToLower(qual); !scoped[k] {
			scoped[k] = true
			scope = append(scope, qual)
		}
		for _, r := range repos {
			if k := strings.ToLower(r.FullName); !seen[k] {
				seen[k] = true
				all = append(all, r)
			}
		}
	}
	c.mu.Lock()
	c.scope = scope
	c.mu.Unlock()
	return all, nil
}

// Repo reads one repo by owner/name (GET /repos/{full}, ETag-conditional),
// mapped as the list maps it. One call, with or without a token; a missing
// repo is an error for which IsNotFound is true.
func (c *Client) Repo(ctx context.Context, full string) (forge.Repo, error) {
	if _, _, ok := strings.Cut(full, "/"); !ok {
		return forge.Repo{}, fmt.Errorf("github repo %q is not owner/name", full)
	}
	var g ghRepo
	if _, err := c.getJSON(ctx, repoPath(full), nil, &g); err != nil {
		return forge.Repo{}, err
	}
	return g.repo(c.id), nil
}

func (c *Client) repoPages(ctx context.Context, path string, extra url.Values) ([]forge.Repo, error) {
	var out []forge.Repo
	for page := 1; page <= 40; page++ {
		q := pageQuery(pageSize, page)
		for k, v := range extra {
			q[k] = v
		}
		var batch []ghRepo
		if _, err := c.getJSON(ctx, path, q, &batch); err != nil {
			return nil, err
		}
		for _, g := range batch {
			out = append(out, g.repo(c.id))
		}
		if len(batch) < pageSize {
			break
		}
	}
	return out, nil
}

// FetchMirror is the v0.1 three-call read of a public repo: the repo, its
// open issues (pull requests dropped) and its latest release. It works with
// or without a token, and a rate limit is returned as is.
func (c *Client) FetchMirror(ctx context.Context, full string, now time.Time) (*forge.Mirror, error) {
	if _, _, ok := strings.Cut(full, "/"); !ok {
		return nil, fmt.Errorf("github repo %q is not owner/name", full)
	}
	base := repoPath(full)
	var meta ghRepo
	if _, err := c.getJSON(ctx, base, nil, &meta); err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("github %s: not found (private or renamed?)", full)
		}
		return nil, err
	}
	var all []forge.Issue
	if _, err := c.getJSON(ctx, base+"/issues", url.Values{"state": {"open"}, "per_page": {strconv.Itoa(pageSize)}}, &all); err != nil && !IsNotFound(err) {
		return nil, err
	}
	var issues []forge.Issue
	for _, is := range all {
		if !is.IsPull() {
			issues = append(issues, is)
		}
	}
	m := &forge.Mirror{Forge: c.id, FullName: meta.FullName, HTMLURL: meta.HTMLURL, Private: meta.Private, Stars: meta.Stars, OpenIssues: len(issues), FetchedAt: now.UTC()}
	m.Issues = forge.SortByUpdated(issues)
	var rel forge.Release
	switch _, err := c.getJSON(ctx, base+"/releases/latest", nil, &rel); {
	case err == nil:
		m.LatestRelease = rel.TagName
	case IsNotFound(err):
	default:
		return nil, err
	}
	return m, nil
}

const heatmapQuery = `query {
  viewer {
    contributionsCollection {
      contributionCalendar {
        weeks { contributionDays { date contributionCount } }
      }
    }
  }
}`

// Heatmap is the token user's contribution calendar (the GraphQL viewer, so
// user is not consulted): one point per day with contributions, stamped at
// noon UTC so the calendar day survives any time zone within 12 hours.
// Without a token there is none.
func (c *Client) Heatmap(ctx context.Context, user string) ([]forge.HeatPoint, error) {
	if c.Token == "" {
		return nil, nil
	}
	var res struct {
		Viewer struct {
			ContributionsCollection struct {
				ContributionCalendar struct {
					Weeks []struct {
						ContributionDays []struct {
							Date              string `json:"date"`
							ContributionCount int    `json:"contributionCount"`
						} `json:"contributionDays"`
					} `json:"weeks"`
				} `json:"contributionCalendar"`
			} `json:"contributionsCollection"`
		} `json:"viewer"`
	}
	if err := c.graphql(ctx, heatmapQuery, nil, &res); err != nil {
		return nil, err
	}
	var pts []forge.HeatPoint
	for _, w := range res.Viewer.ContributionsCollection.ContributionCalendar.Weeks {
		for _, d := range w.ContributionDays {
			if d.ContributionCount == 0 {
				continue
			}
			day, err := time.Parse("2006-01-02", d.Date)
			if err != nil {
				return nil, fmt.Errorf("github heatmap: %w", err)
			}
			pts = append(pts, forge.HeatPoint{Timestamp: day.Add(12 * time.Hour).Unix(), Contributions: d.ContributionCount})
		}
	}
	return pts, nil
}

// SearchIssues searches issues (not PRs) in the owners ListRepos last read,
// or the token user's repos before any listing; never all of GitHub.
func (c *Client) SearchIssues(ctx context.Context, query string) ([]forge.Issue, error) {
	if c.Token == "" {
		return nil, forge.ErrNeedsToken
	}
	c.mu.Lock()
	scope := c.scope
	c.mu.Unlock()
	if len(scope) == 0 {
		me, err := c.Me(ctx)
		if err != nil {
			return nil, err
		}
		scope = []string{"user:" + me.Login}
	}
	q := strings.Join(append([]string{query, "is:issue"}, scope...), " ")
	var res struct {
		Items []searchItem `json:"items"`
	}
	if _, err := c.getJSON(ctx, "/search/issues", url.Values{"q": {q}, "per_page": {strconv.Itoa(pageSize)}}, &res); err != nil {
		return nil, err
	}
	var out []forge.Issue
	for _, it := range res.Items {
		out = append(out, it.issue())
	}
	return out, nil
}
