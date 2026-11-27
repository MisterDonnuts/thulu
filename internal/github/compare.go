package github

import (
	"context"
	"net/http"
	"regexp"
	"strconv"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// A compare page carries the changed files with their patches; a large diff runs past readLimit.
const compareReadLimit = 16 << 20

const comparePerPage = 100

var commitSHARe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// ValidCommitSHA accepts a full or abbreviated hex commit id; branch names and other refs are
// refused, so a compare between two of them answers the same question tomorrow.
func ValidCommitSHA(s string) bool { return commitSHARe.MatchString(s) }

type CompareCommit struct {
	SHA     string
	Message string
}

type Comparison struct {
	// ahead, behind, identical or diverged, as GitHub reports head against base.
	Status       string
	TotalCommits int
	// Oldest first, at most pages × 100; Complete says whether that is all of them.
	Commits  []CompareCommit
	Complete bool
}

// BaseIsAncestor: head contains base (it is ahead of it, or the same commit).
func (c *Comparison) BaseIsAncestor() bool {
	return c.Status == "ahead" || c.Status == "identical"
}

type compareResponse struct {
	Status       string `json:"status"`
	TotalCommits int    `json:"total_commits"`
	Commits      []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	} `json:"commits"`
}

// Compare reads GET /repos/{owner}/{repo}/compare/{base}...{head}. pages bounds how many pages of
// commits it reads; 1 is enough when only the status matters.
func Compare(ctx context.Context, token, owner, repo, base, head string, pages int) (*Comparison, error) {
	if token == "" {
		return nil, apperr.Invalid("github.errors.token_required")
	}
	if !ValidCommitSHA(base) || !ValidCommitSHA(head) {
		return nil, apperr.Invalid("github.errors.compare_sha_invalid", "base", base, "head", head)
	}
	if pages < 1 {
		pages = 1
	}
	out := &Comparison{}
	for page := 1; page <= pages; page++ {
		var resp compareResponse
		endpoint := reposURL(owner, repo) + "/compare/" + base + "..." + head +
			"?per_page=" + strconv.Itoa(comparePerPage) + "&page=" + strconv.Itoa(page)
		if err := callLimit(ctx, token, http.MethodGet, endpoint, nil, &resp, compareReadLimit); err != nil {
			return nil, err
		}
		out.Status, out.TotalCommits = resp.Status, resp.TotalCommits
		for i := range resp.Commits {
			out.Commits = append(out.Commits, CompareCommit{SHA: resp.Commits[i].SHA, Message: resp.Commits[i].Commit.Message})
		}
		if len(resp.Commits) < comparePerPage || len(out.Commits) >= out.TotalCommits {
			break
		}
	}
	out.Complete = len(out.Commits) >= out.TotalCommits
	return out, nil
}
