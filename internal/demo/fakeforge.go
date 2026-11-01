package demo

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// giteaZone is the offset the fake Gitea writes its times in, as a real one
// writes them in its server's zone.
var giteaZone = time.FixedZone("", 60*60)

// Handler serves w as three things at once: Gitea's API under /api/v1,
// GitHub's REST and GraphQL APIs at the root, and the sites the repos deploy
// (/probe/<name> answers World.Probes[name], /fdroid/repo/index-v1.json is
// the F-Droid index). It only reads: anything but GET (and GitHub's GraphQL
// POST) is refused.
func Handler(w *World) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		p := r.URL.Path
		graphql := r.Method == http.MethodPost && p == "/graphql"
		if r.Method != http.MethodGet && !graphql {
			jsonError(rw, http.StatusMethodNotAllowed, "read only")
			return
		}
		switch {
		case strings.HasPrefix(p, "/api/v1/"):
			w.gitea(rw, r, strings.TrimPrefix(p, "/api/v1"))
		case strings.HasPrefix(p, "/probe/"):
			code, ok := w.Probes[strings.TrimPrefix(p, "/probe/")]
			if !ok {
				code = http.StatusNotFound
			}
			rw.WriteHeader(code)
			_, _ = rw.Write([]byte(http.StatusText(code) + "\n"))
		case p == "/fdroid/repo/index-v1.json":
			writeJSON(rw, r, w.fdroidIndex(), false)
		case graphql:
			w.graphql(rw, r)
		default:
			w.github(rw, r, p)
		}
	})
}

func jsonError(rw http.ResponseWriter, code int, msg string) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(code)
	_, _ = fmt.Fprintf(rw, "{\"message\":%q}\n", msg)
}

// writeJSON writes v; with etag it answers If-None-Match with 304, as GitHub does.
func writeJSON(rw http.ResponseWriter, r *http.Request, v any, etag bool) {
	b, err := json.Marshal(v)
	if err != nil {
		jsonError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	if etag {
		sum := sha1.Sum(b)
		tag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
		rw.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			rw.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = rw.Write(append(b, '\n'))
}

func intParam(q url.Values, key string, def int) int {
	if n, err := strconv.Atoi(q.Get(key)); err == nil && n > 0 {
		return n
	}
	return def
}

// page cuts items to the page asked for.
func page[T any](items []T, size, n int) (out []T, more bool) {
	start := (n - 1) * size
	if start >= len(items) {
		return []T{}, false
	}
	end := min(start+size, len(items))
	return items[start:end], end < len(items)
}

func (w *World) repos(forge string) []*Repo {
	var out []*Repo
	for _, r := range w.Repos {
		if r.Forge == forge {
			out = append(out, r)
		}
	}
	return out
}

// repoAt splits /repos/{owner}/{name}/{rest} and finds the repo on forge.
func (w *World) repoAt(forge, p string) (r *Repo, rest string, ok bool) {
	tail, found := strings.CutPrefix(p, "/repos/")
	if !found {
		return nil, "", false
	}
	parts := strings.SplitN(tail, "/", 3)
	if len(parts) < 2 {
		return nil, "", false
	}
	for _, c := range w.repos(forge) {
		if strings.EqualFold(c.Owner, parts[0]) && strings.EqualFold(c.Name, parts[1]) {
			if len(parts) == 3 {
				rest = parts[2]
			}
			return c, rest, true
		}
	}
	return nil, "", false
}

// dir lists the files and folders directly under dir.
func (r *Repo) dir(dir string) (files, dirs []string) {
	prefix := strings.Trim(dir, "/") + "/"
	seen := map[string]bool{}
	for p := range r.Files {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok {
			continue
		}
		if sub, _, nested := strings.Cut(rest, "/"); nested {
			if !seen[sub] {
				seen[sub] = true
				dirs = append(dirs, prefix+sub)
			}
			continue
		}
		files = append(files, p)
	}
	sort.Strings(files)
	sort.Strings(dirs)
	return files, dirs
}

// fileDate is when a file last changed: the date its name starts with, or
// the repo's newest commit.
func (r *Repo) fileDate(p string) time.Time {
	if base := path.Base(p); len(base) >= 10 {
		if t, err := time.Parse("2006-01-02", base[:10]); err == nil {
			return t.Add(10 * time.Hour)
		}
	}
	return r.Updated()
}

// ---- Gitea ----

func gtime(t time.Time) string { return t.In(giteaZone).Format(time.RFC3339) }

func (w *World) giteaUser(login string) map[string]any {
	return map[string]any{"id": len(login), "login": login, "username": login, "full_name": strings.ToUpper(login[:1]) + login[1:],
		"email": login + "@example.com", "avatar_url": GiteaWeb + "/avatars/" + login}
}

func (w *World) giteaRepo(r *Repo) map[string]any {
	web := r.HTMLURL()
	return map[string]any{
		"id": r.ID, "owner": w.giteaUser(r.Owner), "name": r.Name, "full_name": r.FullName(), "description": r.Description,
		"empty": r.Empty, "private": r.Private, "fork": false, "template": false, "mirror": r.Mirror, "size": 120 + len(r.Commits),
		"html_url": web, "url": GiteaWeb + "/api/v1/repos/" + r.FullName(), "clone_url": web + ".git", "website": r.Website,
		"stars_count": r.Stars, "forks_count": 0, "watchers_count": 1, "open_issues_count": len(r.Issues), "open_pr_counter": len(r.PRs),
		"release_counter": len(r.Releases), "default_branch": "main", "archived": r.Archived, "has_actions": r.Actions || len(r.Runs) > 0,
		"has_issues": true, "has_pull_requests": true, "created_at": gtime(r.Created), "updated_at": gtime(r.Updated()),
	}
}

func (w *World) giteaLabels(names []string) []any {
	out := []any{}
	for i, n := range names {
		out = append(out, map[string]any{"id": 10 + i, "name": n, "color": w.labelColour(n), "description": "", "exclusive": false})
	}
	return out
}

func (w *World) giteaUsers(logins []string) any {
	if len(logins) == 0 {
		return nil
	}
	out := []any{}
	for _, l := range logins {
		out = append(out, w.giteaUser(l))
	}
	return out
}

func (w *World) giteaIssue(r *Repo, is Issue, pull bool) map[string]any {
	kind := "issues"
	if pull {
		kind = "pulls"
	}
	m := map[string]any{
		"id": r.ID*1000 + int64(is.Number), "url": GiteaWeb + "/api/v1/repos/" + r.FullName() + "/issues/" + strconv.Itoa(is.Number),
		"html_url": r.HTMLURL() + "/" + kind + "/" + strconv.Itoa(is.Number), "number": is.Number, "user": w.giteaUser(is.Author),
		"title": is.Title, "body": is.Body, "labels": w.giteaLabels(is.Labels), "milestone": nil, "assignee": nil,
		"assignees": w.giteaUsers(is.Assignees), "state": "open", "is_locked": false, "comments": is.Comments,
		"created_at": gtime(is.Created), "updated_at": gtime(is.Updated), "closed_at": nil, "due_date": nil, "pull_request": nil,
		"repository": map[string]any{"id": r.ID, "name": r.Name, "owner": r.Owner, "full_name": r.FullName()},
	}
	if len(is.Assignees) > 0 {
		m["assignee"] = w.giteaUser(is.Assignees[0])
	}
	if pull {
		delete(m, "pull_request")
		delete(m, "repository")
		m["requested_reviewers"] = w.giteaUsers(is.Reviewers)
		m["draft"] = is.Draft
		m["mergeable"], m["merged"], m["merged_at"] = true, false, nil
		m["head"] = map[string]any{"ref": is.Branch, "sha": sha(r.FullName(), is.Branch)}
		m["base"] = map[string]any{"ref": "main"}
	}
	return m
}

func (w *World) giteaCommit(r *Repo, i int) map[string]any {
	c := r.Commits[i]
	who := map[string]any{"name": c.Author.Name, "email": c.Author.Login + "@example.com", "date": gtime(c.At)}
	var parents []any
	if i+1 < len(r.Commits) {
		parents = append(parents, map[string]any{"sha": r.Commits[i+1].SHA})
	}
	return map[string]any{
		"url": GiteaWeb + "/api/v1/repos/" + r.FullName() + "/git/commits/" + c.SHA, "sha": c.SHA, "created": gtime(c.At),
		"html_url": r.HTMLURL() + "/commit/" + c.SHA,
		"commit":   map[string]any{"message": c.Message + "\n", "author": who, "committer": who, "tree": map[string]any{"sha": sha(c.SHA, "tree")}, "verification": nil},
		"author":   w.giteaUser(c.Author.Login), "committer": w.giteaUser(c.Author.Login), "parents": parents, "files": nil, "stats": nil,
	}
}

func (w *World) giteaRun(r *Repo, run CIRun) map[string]any {
	completed := "0001-01-01T00:00:00Z"
	if !run.Completed.IsZero() {
		completed = gtime(run.Completed)
	}
	return map[string]any{
		"id": run.ID, "url": GiteaWeb + "/api/v1/repos/" + r.FullName() + "/actions/runs/" + strconv.FormatInt(run.ID, 10),
		"html_url": r.HTMLURL() + "/actions/runs/" + strconv.FormatInt(run.Number, 10), "display_title": run.Title,
		"path": "ci.yml@refs/heads/" + run.Branch, "event": "push", "run_attempt": 1, "run_number": run.Number,
		"head_sha": run.SHA, "head_branch": run.Branch, "status": run.Status, "conclusion": run.Conclusion,
		"actor": w.giteaUser(Login), "started_at": gtime(run.Started), "completed_at": completed,
	}
}

func (w *World) giteaEntry(r *Repo, p, kind string) map[string]any {
	web := r.HTMLURL() + "/src/branch/main/" + p
	m := map[string]any{
		"name": path.Base(p), "path": p, "sha": sha(r.FullName(), p), "last_commit_sha": sha(r.FullName(), p, "last"),
		"last_committer_date": gtime(r.fileDate(p)), "last_author_date": gtime(r.fileDate(p)), "type": kind,
		"size": len(r.Files[p]), "encoding": nil, "content": nil, "target": nil,
		"url": GiteaWeb + "/api/v1/repos/" + r.FullName() + "/contents/" + p + "?ref=main", "html_url": web,
		"download_url": nil, "submodule_git_url": nil,
	}
	if kind == "file" {
		m["download_url"] = r.HTMLURL() + "/raw/branch/main/" + p
	}
	return m
}

func (w *World) gitea(rw http.ResponseWriter, r *http.Request, p string) {
	q := r.URL.Query()
	switch {
	case p == "/user":
		writeJSON(rw, r, w.giteaUser(Login), false)
		return
	case p == "/user/repos":
		if w.FailList {
			http.Error(rw, "Bad Gateway", http.StatusBadGateway)
			return
		}
		all := []any{}
		for _, repo := range w.repos("gitea") {
			all = append(all, w.giteaRepo(repo))
		}
		items, _ := page(all, intParam(q, "limit", 30), intParam(q, "page", 1))
		rw.Header().Set("X-Total-Count", strconv.Itoa(len(all)))
		writeJSON(rw, r, items, false)
		return
	case p == "/users/"+Login+"/heatmap":
		writeJSON(rw, r, w.giteaHeatmap(), false)
		return
	case strings.HasPrefix(p, "/users/") && strings.HasSuffix(p, "/heatmap"):
		writeJSON(rw, r, []any{}, false)
		return
	case p == "/repos/issues/search":
		term := strings.ToLower(q.Get("q"))
		out := []any{}
		for _, repo := range w.repos("gitea") {
			for _, is := range repo.Issues {
				if term != "" && strings.Contains(strings.ToLower(is.Title+"\n"+is.Body), term) {
					out = append(out, w.giteaIssue(repo, is, false))
				}
			}
		}
		writeJSON(rw, r, out, false)
		return
	}
	repo, rest, ok := w.repoAt("gitea", p)
	if !ok {
		jsonError(rw, http.StatusNotFound, "The target couldn't be found.")
		return
	}
	limit := intParam(q, "limit", 30)
	switch {
	case rest == "":
		writeJSON(rw, r, w.giteaRepo(repo), false)
	case rest == "tags":
		all := []any{}
		for _, t := range repo.Tags {
			id := t.SHA
			if t.Message != "" {
				id = sha(t.SHA, "tag", t.Name)
			}
			all = append(all, map[string]any{"name": t.Name, "message": t.Message, "id": id,
				"commit":      map[string]any{"url": GiteaWeb + "/api/v1/repos/" + repo.FullName() + "/git/commits/" + t.SHA, "sha": t.SHA, "created": gtime(t.At)},
				"zipball_url": repo.HTMLURL() + "/archive/" + t.Name + ".zip", "tarball_url": repo.HTMLURL() + "/archive/" + t.Name + ".tar.gz"})
		}
		items, _ := page(all, limit, intParam(q, "page", 1))
		writeJSON(rw, r, items, false)
	case rest == "releases":
		all := []any{}
		for i, rel := range repo.Releases {
			all = append(all, map[string]any{"id": repo.ID*100 + int64(i), "tag_name": rel.Tag, "target_commitish": "main", "name": rel.Name,
				"body": "", "html_url": repo.HTMLURL() + "/releases/tag/" + rel.Tag, "draft": false, "prerelease": rel.Prerelease,
				"created_at": gtime(rel.At), "published_at": gtime(rel.At), "author": w.giteaUser(Login), "assets": []any{}})
		}
		items, _ := page(all, limit, 1)
		writeJSON(rw, r, items, false)
	case rest == "issues":
		out := []any{}
		for _, is := range repo.Issues {
			out = append(out, w.giteaIssue(repo, is, false))
		}
		items, _ := page(out, limit, 1)
		writeJSON(rw, r, items, false)
	case rest == "pulls":
		prs := append([]Issue(nil), repo.PRs...)
		sort.SliceStable(prs, func(i, j int) bool { return prs[i].Updated.After(prs[j].Updated) })
		out := []any{}
		for _, is := range prs {
			out = append(out, w.giteaIssue(repo, is, true))
		}
		items, _ := page(out, limit, 1)
		writeJSON(rw, r, items, false)
	case rest == "commits":
		if repo.Empty || len(repo.Commits) == 0 {
			jsonError(rw, http.StatusConflict, "Git Repository is empty.")
			return
		}
		var since time.Time
		if s := q.Get("since"); s != "" {
			since, _ = time.Parse(time.RFC3339, s)
		}
		var idx []int
		for i, c := range repo.Commits {
			if !c.At.Before(since) {
				idx = append(idx, i)
			}
		}
		got, more := page(idx, limit, intParam(q, "page", 1))
		out := []any{}
		for _, i := range got {
			out = append(out, w.giteaCommit(repo, i))
		}
		rw.Header().Set("X-Total-Count", strconv.Itoa(len(idx)))
		rw.Header().Set("X-Hasmore", strconv.FormatBool(more))
		writeJSON(rw, r, out, false)
	case rest == "actions/runs":
		if !repo.Actions && len(repo.Runs) == 0 {
			jsonError(rw, http.StatusNotFound, "Actions are not enabled.")
			return
		}
		out := []any{}
		for _, run := range repo.Runs {
			out = append(out, w.giteaRun(repo, run))
		}
		items, _ := page(out, limit, 1)
		writeJSON(rw, r, map[string]any{"workflow_runs": items, "total_count": len(out)}, false)
	case strings.HasPrefix(rest, "raw/"):
		body, ok := repo.Files[strings.TrimPrefix(rest, "raw/")]
		if !ok || repo.Empty {
			jsonError(rw, http.StatusNotFound, "The target couldn't be found.")
			return
		}
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = rw.Write([]byte(w.expand(body)))
	case strings.HasPrefix(rest, "contents/"):
		dir := strings.Trim(strings.TrimPrefix(rest, "contents/"), "/")
		if _, isFile := repo.Files[dir]; isFile {
			writeJSON(rw, r, w.giteaEntry(repo, dir, "file"), false)
			return
		}
		files, dirs := repo.dir(dir)
		if len(files)+len(dirs) == 0 {
			jsonError(rw, http.StatusNotFound, "GetContentsOrList")
			return
		}
		out := []any{}
		for _, d := range dirs {
			out = append(out, w.giteaEntry(repo, d, "dir"))
		}
		for _, f := range files {
			out = append(out, w.giteaEntry(repo, f, "file"))
		}
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].(map[string]any)["name"].(string) < out[j].(map[string]any)["name"].(string)
		})
		writeJSON(rw, r, out, false)
	default:
		jsonError(rw, http.StatusNotFound, "The target couldn't be found.")
	}
}

// background is a steady pattern of contributions for days before the
// repos' histories, so the heatmap looks lived in: none at weekends, 0-6 on
// weekdays, taken from the date.
func background(d time.Time) int {
	if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return 0
	}
	s := sha(date(d))
	n, _ := strconv.ParseInt(s[:2], 16, 64)
	return int(n) % 7
}

// dayCounts are the token user's contributions per day on one forge over
// the heatmap's 371 days: commits plus the background on Gitea.
func (w *World) dayCounts(forge string, withBackground bool) map[string]int {
	counts := map[string]int{}
	start := w.Now.Add(-371 * day)
	for _, r := range w.repos(forge) {
		if r.MirrorOf != "" {
			continue // a copy: its commits count on the repo it mirrors
		}
		for _, c := range r.Commits {
			if c.Author.Login == Login && c.At.After(start) {
				counts[date(c.At)]++
			}
		}
	}
	if withBackground {
		for t := start; !t.After(w.Now); t = t.Add(day) {
			counts[date(t)] += background(t)
		}
	}
	return counts
}

func (w *World) giteaHeatmap() []any {
	counts := w.dayCounts("gitea", true)
	days := make([]string, 0, len(counts))
	for d := range counts {
		days = append(days, d)
	}
	sort.Strings(days)
	out := []any{}
	for _, d := range days {
		if counts[d] == 0 {
			continue
		}
		t, _ := time.Parse("2006-01-02", d)
		out = append(out, map[string]any{"timestamp": t.Add(11 * time.Hour).Unix(), "contributions": counts[d]})
	}
	return out
}

// ---- GitHub ----

func gh(t time.Time) string { return t.UTC().Format(time.RFC3339) }

const githubAPI = "https://api.github.example.com"

func (w *World) ghUser(login string) map[string]any {
	kind := "User"
	if login == Owner {
		kind = "Organization"
	}
	return map[string]any{"login": login, "id": 1000 + len(login), "type": kind, "html_url": GitHubWeb + "/" + login}
}

func (w *World) ghRepo(r *Repo) map[string]any {
	vis := "public"
	if r.Private {
		vis = "private"
	}
	return map[string]any{
		"id": r.ID, "node_id": "R_" + sha(r.FullName())[:12], "name": r.Name, "full_name": r.FullName(), "private": r.Private,
		"owner": w.ghUser(r.Owner), "html_url": r.HTMLURL(), "description": r.Description, "fork": false,
		"url": githubAPI + "/repos/" + r.FullName(), "homepage": r.Website, "size": 300 + len(r.Commits), "stargazers_count": r.Stars,
		"watchers_count": r.Stars, "language": "Go", "forks_count": r.Stars / 10, "archived": r.Archived, "disabled": false,
		"open_issues_count": len(r.Issues) + len(r.PRs), "topics": []any{}, "visibility": vis, "default_branch": "main",
		"created_at": gh(r.Created), "updated_at": gh(r.Created.Add(30 * day)), "pushed_at": gh(r.Updated()), "mirror_url": nil,
	}
}

func (w *World) ghLabels(names []string) []any {
	out := []any{}
	for i, n := range names {
		out = append(out, map[string]any{"id": 20 + i, "name": n, "color": w.labelColour(n), "description": nil, "default": false})
	}
	return out
}

func (w *World) ghUsers(logins []string) []any {
	out := []any{}
	for _, l := range logins {
		out = append(out, w.ghUser(l))
	}
	return out
}

func (w *World) ghIssue(r *Repo, is Issue, pull bool) map[string]any {
	kind := "issues"
	if pull {
		kind = "pull"
	}
	html := r.HTMLURL() + "/" + kind + "/" + strconv.Itoa(is.Number)
	m := map[string]any{
		"url": githubAPI + "/repos/" + r.FullName() + "/issues/" + strconv.Itoa(is.Number), "repository_url": githubAPI + "/repos/" + r.FullName(),
		"html_url": html, "id": r.ID*1000 + int64(is.Number), "number": is.Number, "title": is.Title, "user": w.ghUser(is.Author),
		"labels": w.ghLabels(is.Labels), "state": "open", "locked": false, "assignee": nil, "assignees": w.ghUsers(is.Assignees),
		"milestone": nil, "comments": is.Comments, "created_at": gh(is.Created), "updated_at": gh(is.Updated), "closed_at": nil,
		"author_association": "MEMBER", "body": is.Body,
	}
	if len(is.Assignees) > 0 {
		m["assignee"] = w.ghUser(is.Assignees[0])
	}
	if pull {
		m["draft"] = is.Draft
		m["pull_request"] = map[string]any{"url": githubAPI + "/repos/" + r.FullName() + "/pulls/" + strconv.Itoa(is.Number), "html_url": html, "merged_at": nil}
	}
	return m
}

// ghPull is a pull as /pulls lists it: reviewers and draft, but no comment count.
func (w *World) ghPull(r *Repo, is Issue) map[string]any {
	m := w.ghIssue(r, is, true)
	delete(m, "comments")
	delete(m, "pull_request")
	delete(m, "repository_url")
	m["html_url"] = r.HTMLURL() + "/pull/" + strconv.Itoa(is.Number)
	m["requested_reviewers"] = w.ghUsers(is.Reviewers)
	m["requested_teams"] = []any{}
	m["merged_at"] = nil
	m["head"] = map[string]any{"ref": is.Branch, "sha": sha(r.FullName(), is.Branch)}
	m["base"] = map[string]any{"ref": "main"}
	return m
}

func (w *World) ghCommit(r *Repo, i int) map[string]any {
	c := r.Commits[i]
	who := map[string]any{"name": c.Author.Name, "email": c.Author.Login + "@example.com", "date": gh(c.At)}
	parents := []any{}
	if i+1 < len(r.Commits) {
		parents = append(parents, map[string]any{"sha": r.Commits[i+1].SHA})
	}
	return map[string]any{
		"sha": c.SHA, "node_id": "C_" + c.SHA[:12], "html_url": r.HTMLURL() + "/commit/" + c.SHA, "url": githubAPI + "/repos/" + r.FullName() + "/commits/" + c.SHA,
		"commit": map[string]any{"author": who, "committer": who, "message": c.Message, "comment_count": 0},
		"author": w.ghUser(c.Author.Login), "committer": w.ghUser(c.Author.Login), "parents": parents,
	}
}

func (w *World) ghRun(r *Repo, run CIRun) map[string]any {
	status, conclusion := run.Status, any(nil)
	if run.Status == "completed" {
		conclusion = run.Conclusion
	}
	updated := run.Completed
	if updated.IsZero() {
		updated = run.Started
	}
	return map[string]any{
		"id": run.ID, "name": "CI", "head_branch": run.Branch, "head_sha": run.SHA, "path": ".github/workflows/ci.yml",
		"display_title": run.Title, "run_number": run.Number, "event": "push", "status": status, "conclusion": conclusion,
		"workflow_id": 7, "html_url": r.HTMLURL() + "/actions/runs/" + strconv.FormatInt(run.ID, 10),
		"created_at": gh(run.Started), "updated_at": gh(updated), "run_attempt": 1, "run_started_at": gh(run.Started),
	}
}

func (w *World) ghEntry(r *Repo, p, kind string) map[string]any {
	way := "blob"
	if kind == "dir" {
		way = "tree"
	}
	m := map[string]any{"name": path.Base(p), "path": p, "sha": sha(r.FullName(), p), "size": len(r.Files[p]),
		"url": githubAPI + "/repos/" + r.FullName() + "/contents/" + p + "?ref=main", "html_url": r.HTMLURL() + "/" + way + "/main/" + p,
		"download_url": nil, "type": kind}
	if kind == "file" {
		m["download_url"] = "https://raw.example.com/" + r.FullName() + "/main/" + p
	}
	return m
}

func (w *World) github(rw http.ResponseWriter, r *http.Request, p string) {
	q := r.URL.Query()
	owned := func(owner string) []any {
		out := []any{}
		for _, repo := range w.repos("github") {
			if strings.EqualFold(repo.Owner, owner) {
				out = append(out, w.ghRepo(repo))
			}
		}
		items, _ := page(out, intParam(q, "per_page", 30), intParam(q, "page", 1))
		return items
	}
	switch {
	case p == "/user":
		writeJSON(rw, r, w.ghUser(Login), true)
		return
	case p == "/user/repos":
		writeJSON(rw, r, owned(Login), true)
		return
	case strings.HasPrefix(p, "/orgs/") && strings.HasSuffix(p, "/repos"):
		if org := strings.TrimSuffix(strings.TrimPrefix(p, "/orgs/"), "/repos"); strings.EqualFold(org, Owner) {
			writeJSON(rw, r, owned(org), true)
			return
		}
		jsonError(rw, http.StatusNotFound, "Not Found")
		return
	case strings.HasPrefix(p, "/users/") && strings.HasSuffix(p, "/repos"):
		writeJSON(rw, r, owned(strings.TrimSuffix(strings.TrimPrefix(p, "/users/"), "/repos")), true)
		return
	case p == "/search/issues":
		term, _, _ := strings.Cut(strings.ToLower(q.Get("q")), " ")
		items := []any{}
		for _, repo := range w.repos("github") {
			for _, is := range repo.Issues {
				if term != "" && strings.Contains(strings.ToLower(is.Title+"\n"+is.Body), term) {
					items = append(items, w.ghIssue(repo, is, false))
				}
			}
		}
		writeJSON(rw, r, map[string]any{"total_count": len(items), "incomplete_results": false, "items": items}, true)
		return
	}
	repo, rest, ok := w.repoAt("github", p)
	if !ok {
		jsonError(rw, http.StatusNotFound, "Not Found")
		return
	}
	per := intParam(q, "per_page", 30)
	switch {
	case rest == "":
		writeJSON(rw, r, w.ghRepo(repo), true)
	case rest == "issues":
		var all []Issue
		all = append(all, repo.Issues...)
		all = append(all, repo.PRs...)
		sort.SliceStable(all, func(i, j int) bool { return all[i].Updated.After(all[j].Updated) })
		out := []any{}
		for _, is := range all {
			out = append(out, w.ghIssue(repo, is, isPR(repo, is)))
		}
		items, _ := page(out, per, 1)
		writeJSON(rw, r, items, true)
	case rest == "pulls":
		prs := append([]Issue(nil), repo.PRs...)
		sort.SliceStable(prs, func(i, j int) bool { return prs[i].Updated.After(prs[j].Updated) })
		out := []any{}
		for _, is := range prs {
			out = append(out, w.ghPull(repo, is))
		}
		writeJSON(rw, r, out, true)
	case rest == "releases" || rest == "releases/latest":
		out := []any{}
		for i, rel := range repo.Releases {
			out = append(out, map[string]any{"id": repo.ID*100 + int64(i), "tag_name": rel.Tag, "target_commitish": "main", "name": rel.Name,
				"html_url": repo.HTMLURL() + "/releases/tag/" + rel.Tag, "draft": false, "prerelease": rel.Prerelease,
				"created_at": gh(rel.At), "published_at": gh(rel.At), "assets": []any{}, "body": ""})
		}
		if rest == "releases" {
			writeJSON(rw, r, out, true)
			return
		}
		if len(out) == 0 {
			jsonError(rw, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(rw, r, out[0], true)
	case rest == "commits":
		if repo.Empty || len(repo.Commits) == 0 {
			jsonError(rw, http.StatusConflict, "Git Repository is empty.")
			return
		}
		var since time.Time
		if s := q.Get("since"); s != "" {
			since, _ = time.Parse(time.RFC3339, s)
		}
		var idx []int
		for i, c := range repo.Commits {
			if !c.At.Before(since) {
				idx = append(idx, i)
			}
		}
		n := intParam(q, "page", 1)
		got, more := page(idx, per, n)
		if more {
			next := *r.URL
			qq := next.Query()
			qq.Set("page", strconv.Itoa(n+1))
			next.RawQuery = qq.Encode()
			rw.Header().Set("Link", `<`+githubAPI+next.RequestURI()+`>; rel="next"`)
		}
		out := []any{}
		for _, i := range got {
			out = append(out, w.ghCommit(repo, i))
		}
		writeJSON(rw, r, out, false)
	case rest == "actions/runs":
		if !repo.Actions && len(repo.Runs) == 0 {
			writeJSON(rw, r, map[string]any{"total_count": 0, "workflow_runs": []any{}}, true)
			return
		}
		out := []any{}
		for _, run := range repo.Runs {
			out = append(out, w.ghRun(repo, run))
		}
		items, _ := page(out, per, 1)
		writeJSON(rw, r, map[string]any{"total_count": len(out), "workflow_runs": items}, true)
	case strings.HasPrefix(rest, "contents/"):
		p := strings.Trim(strings.TrimPrefix(rest, "contents/"), "/")
		if body, isFile := repo.Files[p]; isFile {
			if strings.Contains(r.Header.Get("Accept"), "raw") {
				rw.Header().Set("Content-Type", "application/vnd.github.raw+json; charset=utf-8")
				_, _ = rw.Write([]byte(w.expand(body)))
				return
			}
			writeJSON(rw, r, w.ghEntry(repo, p, "file"), true)
			return
		}
		files, dirs := repo.dir(p)
		if len(files)+len(dirs) == 0 {
			jsonError(rw, http.StatusNotFound, "Not Found")
			return
		}
		out := []any{}
		for _, d := range dirs {
			out = append(out, w.ghEntry(repo, d, "dir"))
		}
		for _, f := range files {
			out = append(out, w.ghEntry(repo, f, "file"))
		}
		writeJSON(rw, r, out, true)
	default:
		jsonError(rw, http.StatusNotFound, "Not Found")
	}
}

func isPR(r *Repo, is Issue) bool {
	for _, pr := range r.PRs {
		if pr.Number == is.Number {
			return true
		}
	}
	return false
}

// graphql answers the two queries the GitHub client sends: a repository's
// tag refs (newest commit first, pages of 100) and the viewer's
// contribution calendar.
func (w *World) graphql(rw http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(rw, http.StatusBadRequest, "Problems parsing JSON")
		return
	}
	switch {
	case strings.Contains(req.Query, "contributionCalendar"):
		writeJSON(rw, r, map[string]any{"data": map[string]any{"viewer": map[string]any{"contributionsCollection": map[string]any{
			"contributionCalendar": w.ghCalendar()}}}}, false)
	case strings.Contains(req.Query, "refs("):
		owner, _ := req.Variables["owner"].(string)
		name, _ := req.Variables["name"].(string)
		var repo *Repo
		for _, c := range w.repos("github") {
			if strings.EqualFold(c.Owner, owner) && strings.EqualFold(c.Name, name) {
				repo = c
			}
		}
		if repo == nil {
			writeJSON(rw, r, map[string]any{"data": map[string]any{"repository": nil},
				"errors": []any{map[string]any{"type": "NOT_FOUND", "message": "Could not resolve to a Repository with the name '" + owner + "/" + name + "'."}}}, false)
			return
		}
		start := 0
		if after, _ := req.Variables["after"].(string); after != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(after, "cursor:"))
		}
		nodes := []any{}
		end := min(start+100, len(repo.Tags))
		for _, t := range repo.Tags[min(start, end):end] {
			commit := map[string]any{"oid": t.SHA, "committedDate": gh(t.At)}
			target := commit
			if t.Message != "" {
				target = map[string]any{"oid": sha(t.SHA, "tag", t.Name), "message": t.Message, "target": commit}
			}
			nodes = append(nodes, map[string]any{"name": t.Name, "target": target})
		}
		writeJSON(rw, r, map[string]any{"data": map[string]any{"repository": map[string]any{"refs": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": end < len(repo.Tags), "endCursor": "cursor:" + strconv.Itoa(end)}, "nodes": nodes}}}}, false)
	default:
		writeJSON(rw, r, map[string]any{"errors": []any{map[string]any{"type": "UNKNOWN", "message": "query not served by the demo"}}}, false)
	}
}

// ghCalendar is the token user's GitHub contributions, by week from Sunday.
func (w *World) ghCalendar() map[string]any {
	counts := w.dayCounts("github", false)
	end := time.Date(w.Now.Year(), w.Now.Month(), w.Now.Day(), 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -52*7-int(end.Weekday()))
	var weeks []any
	var days []any
	total := 0
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		n := counts[date(d)]
		total += n
		days = append(days, map[string]any{"date": date(d), "contributionCount": n, "weekday": int(d.Weekday())})
		if d.Weekday() == time.Saturday {
			weeks = append(weeks, map[string]any{"contributionDays": days})
			days = nil
		}
	}
	if len(days) > 0 {
		weeks = append(weeks, map[string]any{"contributionDays": days})
	}
	return map[string]any{"totalContributions": total, "weeks": weeks}
}

// ---- F-Droid and the feed ----

func (w *World) fdroidIndex() map[string]any {
	apps, pkgs := []any{}, map[string]any{}
	for _, a := range w.FDroid {
		apps = append(apps, map[string]any{"packageName": a.Package, "name": a.Name, "suggestedVersionCode": strconv.FormatInt(a.Versions[0].Code, 10),
			"license": "MIT", "added": a.Versions[len(a.Versions)-1].Added.UnixMilli(), "lastUpdated": a.Versions[0].Added.UnixMilli()})
		// Oldest first: the reader must take the highest versionCode, not the first.
		var vs []any
		for _, v := range slices.Backward(a.Versions) {
			vs = append(vs, map[string]any{"versionName": v.Name, "versionCode": v.Code, "added": v.Added.UnixMilli(),
				"apkName": a.Package + "_" + strconv.FormatInt(v.Code, 10) + ".apk", "packageName": a.Package, "size": 4200000})
		}
		pkgs[a.Package] = vs
	}
	return map[string]any{"repo": map[string]any{"name": "Acme F-Droid", "timestamp": w.Now.UnixMilli(), "version": 21, "address": GiteaWeb + "/fdroid/repo"},
		"requests": map[string]any{"install": []any{}, "uninstall": []any{}}, "apps": apps, "packages": pkgs}
}

// FeedJSONL is the Docker feed: `docker ps -a --format '{{json .}}'`, one
// container a line.
func (w *World) FeedJSONL() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	var buf bytes.Buffer
	for _, c := range w.Containers {
		keys := make([]string, 0, len(c.Labels))
		for k := range c.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		labels := make([]string, len(keys))
		for i, k := range keys {
			labels[i] = k + "=" + c.Labels[k]
		}
		line := map[string]string{
			"Command": `"/app"`, "CreatedAt": c.Created.UTC().Format("2006-01-02 15:04:05 -0700 MST"), "ID": c.ID, "Image": c.Image,
			"Labels": strings.Join(labels, ","), "LocalVolumes": "0", "Mounts": "", "Names": c.Name, "Networks": "web",
			"Ports": "", "RunningFor": "", "Size": "0B", "State": c.State, "Status": c.Status,
		}
		b, _ := json.Marshal(line)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
