package deploycontents

import (
	"context"

	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/project"
)

// How much history after a merge is read to look for its revert: 3 GitHub pages, or as many commits
// from the clone.
const (
	githubPages  = 3
	gitMaxCommit = 300
)

// GitHubRepo reads GET /repos/{owner}/{repo}/compare. The only source for a project whose code lives
// on a runner.
type GitHubRepo struct {
	Token, Owner, Name string
}

func (g GitHubRepo) Compare(ctx context.Context, base, head string, commits bool) (Comparison, error) {
	pages := 1
	if commits {
		pages = githubPages
	}
	c, err := github.Compare(ctx, g.Token, g.Owner, g.Name, base, head, pages)
	if err != nil {
		return Comparison{}, err
	}
	out := Comparison{Ancestor: c.BaseIsAncestor(), Complete: c.Complete}
	if commits {
		out.Commits = make([]Commit, len(c.Commits))
		for i := range c.Commits {
			out.Commits[i] = Commit{SHA: c.Commits[i].SHA, Message: c.Commits[i].Message}
		}
	}
	return out, nil
}

// GitRepo reads the daemon's clone: uploaded projects, and projects without a GitHub credential.
type GitRepo struct {
	History project.History
}

func (g GitRepo) Compare(ctx context.Context, base, head string, commits bool) (Comparison, error) {
	ok, err := g.History.IsAncestor(ctx, base, head)
	if err != nil {
		return Comparison{}, err
	}
	out := Comparison{Ancestor: ok, Complete: true}
	if !commits {
		return out, nil
	}
	list, complete, err := g.History.Commits(ctx, base, head, gitMaxCommit)
	if err != nil {
		return Comparison{}, err
	}
	out.Complete = complete
	out.Commits = make([]Commit, len(list))
	for i := range list {
		out.Commits[i] = Commit{SHA: list[i].SHA, Message: list[i].Message}
	}
	return out, nil
}
