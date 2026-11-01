package forge

import (
	"sort"
	"time"
)

// SortByUpdated orders issues newest updated first and keeps at most 10, the
// shape of Detail.Issues, Detail.PRs and Mirror.Issues.
func SortByUpdated(is []Issue) []Issue {
	sort.SliceStable(is, func(i, j int) bool { return is[i].UpdatedAt.After(is[j].UpdatedAt.Time) })
	if len(is) > 10 {
		is = is[:10]
	}
	return is
}

// Detail is everything Waymark reads about one repo, apart from files.
type Detail struct {
	Repo     Repo
	Tags     []Tag
	Releases []Release
	Issues   []Issue // open, newest updated first, max 10
	PRs      []Issue // open, newest updated first, max 10
	// Commits on the default branch since Since, newest first.
	Commits []Commit
	// CommitsCapped is true when more commits exist in the window than were read.
	CommitsCapped bool
	Since         time.Time
	// LastCommit is the newest commit on the default branch, even when it is
	// older than the window. Nil for an empty repo.
	LastCommit *Commit
	Runs       []Run
	FetchedAt  time.Time
}

// InProgress reports whether any fetched run has not finished.
func (d *Detail) InProgress() bool {
	for _, r := range d.Runs {
		if !r.Done() {
			return true
		}
	}
	return false
}

// Files is what Waymark reads from a repo's tree.
type Files struct {
	Changelog    *Changelog // nil when there is no CHANGELOG.md
	ChangelogURL string
	Plans        []Plan // newest first, only plans with checkboxes
	FetchedAt    time.Time
}

// PlanCandidateMax is how many of the newest plan files FetchFiles reads, and
// PlanMaxAge how old one may be; derive keeps the in-progress ones.
const (
	PlanCandidateMax = 8
	PlanMaxAge       = 120 * 24 * time.Hour
)
