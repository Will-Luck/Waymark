package gitea

import (
	"context"
	"fmt"
	"time"

	"github.com/Will-Luck/waymark/internal/forge"
)

// FetchDetail reads tags, releases, open issues and PRs, the commit window and
// the newest Actions runs. Any failure fails the whole detail, so the caller
// keeps the previous good one.
func (c *Client) FetchDetail(ctx context.Context, repo forge.Repo, since time.Time, maxCommitPages int, now time.Time) (*forge.Detail, error) {
	full := repo.FullName
	d := &forge.Detail{Repo: repo, Since: since.UTC(), FetchedAt: now.UTC()}
	var err error
	wrap := func(what string, err error) error { return fmt.Errorf("%s %s: %w", full, what, err) }

	if d.Tags, err = c.Tags(ctx, full); err != nil {
		return nil, wrap("tags", err)
	}
	if repo.ReleaseCount > 0 {
		if d.Releases, err = c.Releases(ctx, full); err != nil {
			return nil, wrap("releases", err)
		}
	}
	if repo.OpenIssues > 0 {
		if d.Issues, err = c.OpenIssues(ctx, full); err != nil {
			return nil, wrap("issues", err)
		}
		d.Issues = forge.SortByUpdated(d.Issues)
	}
	if repo.OpenPRs > 0 {
		if d.PRs, err = c.OpenPulls(ctx, full); err != nil {
			return nil, wrap("pulls", err)
		}
		d.PRs = forge.SortByUpdated(d.PRs)
	}
	if !repo.Empty {
		if d.Commits, d.CommitsCapped, err = c.Commits(ctx, full, repo.DefaultBranch, since, maxCommitPages); err != nil {
			return nil, wrap("commits", err)
		}
		if len(d.Commits) > 0 {
			d.LastCommit = &d.Commits[0]
		} else if d.LastCommit, err = c.LastCommit(ctx, full, repo.DefaultBranch); err != nil {
			return nil, wrap("last commit", err)
		}
	}
	if repo.HasActions {
		if d.Runs, err = c.Runs(ctx, full); err != nil {
			return nil, wrap("actions runs", err)
		}
	}
	return d, nil
}

// FetchFiles reads CHANGELOG.md and the newest plans on the default branch.
func (c *Client) FetchFiles(ctx context.Context, repo forge.Repo, planDirs []string, now time.Time) (*forge.Files, error) {
	return forge.ReadFiles(ctx, c, repo, planDirs, now, repo.HTMLURL+"/src/branch/"+repo.DefaultBranch+"/CHANGELOG.md")
}

// FetchMirror reads the public view of a repo owned by another forge: the repo
// itself, its open issues and its newest release.
func (c *Client) FetchMirror(ctx context.Context, full string, now time.Time) (*forge.Mirror, error) {
	var r forge.Repo
	if _, err := c.getJSON(ctx, repoPath(full), nil, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", full, err)
	}
	m := &forge.Mirror{Forge: c.id, FullName: r.FullName, HTMLURL: r.HTMLURL, Private: r.Private, Stars: r.Stars, OpenIssues: r.OpenIssues, FetchedAt: now.UTC()}
	if r.OpenIssues > 0 {
		is, err := c.OpenIssues(ctx, full)
		if err != nil {
			return nil, fmt.Errorf("%s issues: %w", full, err)
		}
		m.Issues = forge.SortByUpdated(is)
	}
	rels, err := c.Releases(ctx, full)
	if err != nil {
		return nil, fmt.Errorf("%s releases: %w", full, err)
	}
	if len(rels) > 0 {
		m.LatestRelease = rels[0].TagName
	}
	return m, nil
}
