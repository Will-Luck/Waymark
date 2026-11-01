package forge

import (
	"encoding/json"
	"strings"
	"time"
)

// Time accepts RFC 3339, null and "" (Gitea uses all three), and is kept in UTC.
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		t.Time = time.Time{}
		return nil
	}
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return err
	}
	t.Time = v.UTC()
	return nil
}

type User struct {
	Login string `json:"login"`
}

type Repo struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Owner       User   `json:"owner"`
	// Forge is the config id of the forge that listed the repo.
	Forge         string `json:"-"`
	Empty         bool   `json:"empty"`
	Private       bool   `json:"private"`
	Fork          bool   `json:"fork"`
	Mirror        bool   `json:"mirror"`
	Archived      bool   `json:"archived"`
	HTMLURL       string `json:"html_url"`
	Website       string `json:"website"`
	DefaultBranch string `json:"default_branch"`
	Stars         int    `json:"stars_count"`
	// OpenIssues counts open issues and OpenPRs open pull requests. A GitHub
	// listing cannot split them: it counts issues plus PRs in OpenIssues and
	// leaves OpenPRs at 0. Detail.Repo carries the split.
	OpenIssues   int  `json:"open_issues_count"`
	OpenPRs      int  `json:"open_pr_counter"`
	ReleaseCount int  `json:"release_counter"`
	HasActions   bool `json:"has_actions"`
	UpdatedAt    Time `json:"updated_at"`
}

type Tag struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	ID      string `json:"id"`
	Commit  struct {
		SHA     string `json:"sha"`
		Created Time   `json:"created"`
	} `json:"commit"`
}

type Release struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	CreatedAt   Time   `json:"created_at"`
	PublishedAt Time   `json:"published_at"`
}

type Label struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Issue is an issue or a pull request (the shapes match for what is read).
type Issue struct {
	Number  int     `json:"number"`
	Title   string  `json:"title"`
	Body    string  `json:"body"`
	State   string  `json:"state"`
	HTMLURL string  `json:"html_url"`
	User    User    `json:"user"`
	Labels  []Label `json:"labels"`
	// Assignees and RequestedReviewers are empty when the forge does not say.
	Assignees          []User          `json:"assignees"`
	RequestedReviewers []User          `json:"requested_reviewers"`
	Comments           int             `json:"comments"`
	CreatedAt          Time            `json:"created_at"`
	UpdatedAt          Time            `json:"updated_at"`
	Draft              bool            `json:"draft"`
	PullRequest        json.RawMessage `json:"pull_request"`
	Repository         *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// IsPull reports whether an issue-list entry is really a pull request.
func (i Issue) IsPull() bool {
	s := strings.TrimSpace(string(i.PullRequest))
	return s != "" && s != "null"
}

type Commit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Created Time   `json:"created"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
			Date Time   `json:"date"`
		} `json:"author"`
		Committer struct {
			Name string `json:"name"`
			Date Time   `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Author *User `json:"author"`
}

// When is the commit time used everywhere (committer date, falling back to
// the API's created stamp).
func (c Commit) When() time.Time {
	if !c.Commit.Committer.Date.IsZero() {
		return c.Commit.Committer.Date.Time
	}
	return c.Created.Time
}

// Subject is the first line of the message.
func (c Commit) Subject() string {
	s, _, _ := strings.Cut(strings.TrimSpace(c.Commit.Message), "\n")
	return strings.TrimSpace(s)
}

// AuthorName prefers the commit author's name over the forge login.
func (c Commit) AuthorName() string {
	if c.Commit.Author.Name != "" {
		return c.Commit.Author.Name
	}
	if c.Author != nil {
		return c.Author.Login
	}
	return ""
}

type Run struct {
	ID           int64  `json:"id"`
	RunNumber    int64  `json:"run_number"`
	DisplayTitle string `json:"display_title"`
	Path         string `json:"path"`
	Event        string `json:"event"`
	HeadBranch   string `json:"head_branch"`
	HeadSHA      string `json:"head_sha"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HTMLURL      string `json:"html_url"`
	StartedAt    Time   `json:"started_at"`
	CompletedAt  Time   `json:"completed_at"`
}

// Workflow is the workflow file name from Path ("test.yml@refs/heads/main").
func (r Run) Workflow() string {
	w, _, _ := strings.Cut(r.Path, "@")
	return w
}

// Done reports whether the run has finished.
func (r Run) Done() bool { return r.Status == "completed" }

// Entry is one item of a contents (directory) listing.
type Entry struct {
	Name              string `json:"name"`
	Path              string `json:"path"`
	Type              string `json:"type"` // file | dir | symlink | submodule
	HTMLURL           string `json:"html_url"`
	LastCommitterDate Time   `json:"last_committer_date"`
}

// HeatPoint is one bucket of the user heatmap.
type HeatPoint struct {
	Timestamp     int64 `json:"timestamp"`
	Contributions int   `json:"contributions"`
}
