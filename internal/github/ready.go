package github

import (
	"context"
	"net/http"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
)

//i18n:ignore GraphQL query, not text
const markReadyMutation = `mutation($id: ID!) {
  markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } }
}`

// Takes a draft pull request out of draft. REST cannot change draft, so it reads the PR's node id
// and runs the GraphQL mutation. False with no error when the PR was not a draft (or is closed):
// nothing to do.
func MarkPRReady(ctx context.Context, token, owner, repo string, number int) (bool, error) {
	pr, err := GetPR(ctx, token, owner, repo, number)
	if err != nil {
		return false, err
	}
	if !pr.Draft || pr.State != PRStateOpen {
		return false, nil
	}
	if pr.NodeID == "" {
		return false, apperr.New("github.errors.pr_node_id_missing", "number", number)
	}
	var out struct {
		Data struct {
			Mark *struct {
				PullRequest struct {
					IsDraft bool `json:"isDraft"`
				} `json:"pullRequest"`
			} `json:"markPullRequestReadyForReview"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	payload := map[string]any{"query": markReadyMutation, "variables": map[string]any{"id": pr.NodeID}}
	if err := call(ctx, token, http.MethodPost, githubcfg.GraphQLURL(), payload, &out); err != nil {
		return false, err
	}
	// GraphQL reports a refused mutation with 200 and an errors list.
	if len(out.Errors) > 0 {
		msgs := make([]string, 0, len(out.Errors))
		for _, e := range out.Errors {
			msgs = append(msgs, e.Message)
		}
		return false, apperr.New("github.errors.graphql", "message", strings.Join(msgs, "; "))
	}
	if out.Data.Mark == nil || out.Data.Mark.PullRequest.IsDraft {
		return false, apperr.New("github.errors.pr_still_draft", "number", number)
	}
	return true, nil
}
