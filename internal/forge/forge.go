// Package forge holds the forge-neutral types Waymark works with and the
// interface each forge client (Gitea, GitHub) implements.
package forge

import (
	"context"
	"errors"
	"time"
)

// ErrNeedsToken means the forge would not give this detail without a token.
var ErrNeedsToken = errors.New("token needed for full detail")

// RateLimited reports whether err, or an error it wraps, says the forge's
// request allowance is spent, and when it comes back (zero when the forge did
// not say). A client's error says so with a RateLimitReset method.
func RateLimited(err error) (reset time.Time, ok bool) {
	var rl interface{ RateLimitReset() time.Time }
	if errors.As(err, &rl) {
		return rl.RateLimitReset(), true
	}
	return time.Time{}, false
}

// Mirror is the public view of a repo on a forge other than the one that owns it.
type Mirror struct {
	Forge, FullName, HTMLURL string
	Private                  bool
	Stars, OpenIssues        int
	Issues                   []Issue // newest updated first, max 10, PRs excluded
	LatestRelease            string
	FetchedAt                time.Time
}

// Forge is one configured source of repos.
type Forge interface {
	ID() string   // config id, e.g. "gitea"
	Kind() string // "gitea" or "github"
	Me(ctx context.Context) (User, error)
	ListRepos(ctx context.Context, owners []string) ([]Repo, error) // owners empty: the token user's repos
	// Repo reads one repo by owner/name, shaped as ListRepos gives it (a repo
	// the projects file names, whatever its owner).
	Repo(ctx context.Context, full string) (Repo, error)
	FetchDetail(ctx context.Context, repo Repo, since time.Time, maxCommitPages int, now time.Time) (*Detail, error)
	FetchFiles(ctx context.Context, repo Repo, planDirs []string, now time.Time) (*Files, error)
	FetchMirror(ctx context.Context, full string, now time.Time) (*Mirror, error)
	Heatmap(ctx context.Context, user string) ([]HeatPoint, error)
	SearchIssues(ctx context.Context, query string) ([]Issue, error)
}
