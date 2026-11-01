package github

import (
	"path"
	"strings"

	"github.com/Will-Luck/waymark/internal/forge"
)

// ghRepo is a repo as GitHub's REST API lists it.
type ghRepo struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	FullName      string     `json:"full_name"`
	Description   string     `json:"description"`
	Owner         forge.User `json:"owner"`
	Private       bool       `json:"private"`
	Fork          bool       `json:"fork"`
	Archived      bool       `json:"archived"`
	MirrorURL     string     `json:"mirror_url"`
	HTMLURL       string     `json:"html_url"`
	Homepage      string     `json:"homepage"`
	DefaultBranch string     `json:"default_branch"`
	Stars         int        `json:"stargazers_count"`
	OpenIssues    int        `json:"open_issues_count"`
	UpdatedAt     forge.Time `json:"updated_at"`
	PushedAt      forge.Time `json:"pushed_at"`
}

// repo maps the listing onto forge.Repo. Where GitHub differs from Gitea:
//   - UpdatedAt is the later of updated_at and pushed_at, because GitHub's
//     updated_at does not move on a push and Gitea's does.
//   - OpenIssues counts open issues and pull requests together (the listing
//     does not split them); FetchDetail's Repo carries the split.
//   - Empty, ReleaseCount and HasActions are not in the listing, so they stay
//     unset and FetchDetail always asks (an empty repo's commits answer 409).
func (g ghRepo) repo(forgeID string) forge.Repo {
	r := forge.Repo{
		ID: g.ID, Name: g.Name, FullName: g.FullName, Description: g.Description, Owner: g.Owner, Forge: forgeID,
		Private: g.Private, Fork: g.Fork, Mirror: g.MirrorURL != "", Archived: g.Archived, HTMLURL: g.HTMLURL,
		Website: g.Homepage, DefaultBranch: g.DefaultBranch, Stars: g.Stars, OpenIssues: g.OpenIssues, UpdatedAt: g.UpdatedAt,
	}
	if g.PushedAt.After(g.UpdatedAt.Time) {
		r.UpdatedAt = g.PushedAt
	}
	return r
}

// ghRun is an Actions workflow run as GitHub reports it.
type ghRun struct {
	ID           int64      `json:"id"`
	RunNumber    int64      `json:"run_number"`
	DisplayTitle string     `json:"display_title"`
	Path         string     `json:"path"`
	Event        string     `json:"event"`
	HeadBranch   string     `json:"head_branch"`
	HeadSHA      string     `json:"head_sha"`
	Status       string     `json:"status"`
	Conclusion   string     `json:"conclusion"`
	HTMLURL      string     `json:"html_url"`
	RunStartedAt forge.Time `json:"run_started_at"`
	UpdatedAt    forge.Time `json:"updated_at"`
}

// run maps a GitHub run onto forge.Run as the Gitea client fills it: Path is
// the workflow file name, and a finished run's last update is its end.
func (g ghRun) run() forge.Run {
	r := forge.Run{
		ID: g.ID, RunNumber: g.RunNumber, DisplayTitle: g.DisplayTitle, Event: g.Event,
		HeadBranch: g.HeadBranch, HeadSHA: g.HeadSHA, HTMLURL: g.HTMLURL, StartedAt: g.RunStartedAt,
	}
	if wf, _, _ := strings.Cut(g.Path, "@"); wf != "" {
		r.Path = path.Base(wf)
	}
	r.Status, r.Conclusion = runState(g.Status, g.Conclusion)
	if r.Done() {
		r.CompletedAt = g.UpdatedAt
	}
	return r
}

// runState maps GitHub's status and conclusion onto the values Gitea's API
// gives: status queued, in_progress or completed; conclusion success, failure,
// cancelled or skipped once completed, else "".
func runState(status, conclusion string) (string, string) {
	switch status {
	case "completed":
	case "in_progress":
		return "in_progress", ""
	default: // requested, queued, pending, waiting: not started
		return "queued", ""
	}
	switch conclusion {
	case "timed_out", "startup_failure":
		return "completed", "failure"
	case "stale":
		return "completed", "cancelled"
	case "neutral":
		return "completed", "skipped"
	case "action_required":
		// It waits for a maintainer's approval and has not run yet.
		return "queued", ""
	}
	return "completed", conclusion
}

// tagNode is one tag from the GraphQL refs query: the target is the commit,
// or an annotated tag object whose target is the commit.
type tagNode struct {
	Name   string `json:"name"`
	Target struct {
		OID           string     `json:"oid"`
		CommittedDate forge.Time `json:"committedDate"`
		Message       string     `json:"message"`
		Target        *struct {
			OID           string     `json:"oid"`
			CommittedDate forge.Time `json:"committedDate"`
		} `json:"target"`
	} `json:"target"`
}

// tag fills forge.Tag as Gitea does: ID is the tag object (the commit for a
// lightweight tag), Commit the tagged commit with its date.
func (n tagNode) tag() forge.Tag {
	t := forge.Tag{Name: n.Name, ID: n.Target.OID, Message: n.Target.Message}
	t.Commit.SHA, t.Commit.Created = n.Target.OID, n.Target.CommittedDate
	if in := n.Target.Target; in != nil {
		t.Commit.SHA, t.Commit.Created = in.OID, in.CommittedDate
	}
	return t
}

// searchItem is one /search/issues result: an issue plus its repo's API URL.
type searchItem struct {
	forge.Issue
	RepositoryURL string `json:"repository_url"`
}

// issue fills Repository from repository_url, as Gitea's search gives it.
func (s searchItem) issue() forge.Issue {
	is := s.Issue
	if i := strings.Index(s.RepositoryURL, "/repos/"); i >= 0 {
		full := s.RepositoryURL[i+len("/repos/"):]
		is.Repository = &struct {
			FullName string `json:"full_name"`
		}{FullName: full}
	}
	return is
}
