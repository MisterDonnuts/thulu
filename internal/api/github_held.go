package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/chat"
	ghrest "github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/project"
)

const (
	heldMergeRoute  = "/internal/github/{owner}/{repo}/pulls/{number}/merge"
	heldReviewRoute = "/internal/github/{owner}/{repo}/runs/{runId}/deployment-review"
)

func heldGithubTool(route string) string {
	switch route {
	case heldMergeRoute:
		return "pr_merge"
	case heldReviewRoute:
		return "deployment_approve"
	}
	return ""
}

// Set only by handleConfirmChatAction; its presence is what lets the held GitHub routes run.
type confirmedActionKey struct{}

func withConfirmedAction(ctx context.Context, a *chat.Action) context.Context {
	return context.WithValue(ctx, confirmedActionKey{}, a)
}

func confirmedAction(ctx context.Context) *chat.Action {
	if a, ok := ctx.Value(confirmedActionKey{}).(*chat.Action); ok {
		return a
	}
	return nil
}

type heldMergeBody struct {
	MergeMethod   string `json:"merge_method"`
	CommitTitle   string `json:"commit_title"`
	CommitMessage string `json:"commit_message"`
	SHA           string `json:"sha"`
}

type heldReviewBody struct {
	State          string  `json:"state"`
	EnvironmentIDs []int64 `json:"environment_ids"`
	Comment        string  `json:"comment"`
}

func decodeHeldBody(raw []byte, v any) error {
	if strings.TrimSpace(string(raw)) == "" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "api.server.errors.invalid_body")
	}
	return nil
}

func parseHeldMerge(r *http.Request, raw []byte) (*ghrest.MergeReq, error) {
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || n <= 0 {
		return nil, apperr.Invalid("api.github.errors.pr_number_invalid", "number", r.PathValue("number"))
	}
	var body heldMergeBody
	if derr := decodeHeldBody(raw, &body); derr != nil {
		return nil, derr
	}
	if !ghrest.MergeMethods[body.MergeMethod] {
		return nil, apperr.Invalid("api.github.errors.merge_method_invalid", "method", body.MergeMethod)
	}
	return &ghrest.MergeReq{
		Owner: r.PathValue("owner"), Repo: r.PathValue("repo"), Number: n,
		Method: body.MergeMethod, CommitTitle: body.CommitTitle, CommitMessage: body.CommitMessage, SHA: body.SHA,
	}, nil
}

func parseHeldReview(r *http.Request, raw []byte) (*ghrest.DeploymentReview, error) {
	runID, err := strconv.ParseInt(r.PathValue("runId"), 10, 64)
	if err != nil || runID <= 0 {
		return nil, apperr.Invalid("api.github.errors.run_id_invalid", "id", r.PathValue("runId"))
	}
	var body heldReviewBody
	if derr := decodeHeldBody(raw, &body); derr != nil {
		return nil, derr
	}
	if body.State == "" {
		body.State = "approved"
	}
	if body.State != "approved" && body.State != "rejected" {
		return nil, apperr.Invalid("api.github.errors.review_state_invalid", "state", body.State)
	}
	return &ghrest.DeploymentReview{
		Owner: r.PathValue("owner"), Repo: r.PathValue("repo"), RunID: runID,
		EnvironmentIDs: body.EnvironmentIDs, State: body.State, Comment: body.Comment,
	}, nil
}

func (s *Server) heldGithubProject(r *http.Request) (*project.Project, error) {
	raw := r.PathValue("owner") + "/" + r.PathValue("repo")
	ref, ok := repoRef(raw)
	if !ok {
		return nil, apperr.Invalid("api.github.errors.repo_invalid", "repo", raw)
	}
	if s.projectStore == nil {
		return nil, apperr.Unavailable("api.server.errors.project_store_unavailable")
	}
	projects, err := s.projectsForRepo(r, ref)
	if err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		return nil, apperr.NotFound("api.github.errors.repo_not_in_workspace", "repo", ref)
	}
	return &projects[0], nil
}

func (s *Server) describeHeldGithub(r *http.Request, tool string, raw []byte) (string, error) {
	scope := auth.APIKeyRunScope(r.Context())
	if !githubToolGranted(s.chatStageTools(r.Context(), scope), tool) {
		return "", apperr.Forbidden("api.github.errors.chat_tool_not_granted", "tool", tool, "server", githubMCPServer)
	}
	proj, err := s.heldGithubProject(r)
	if err != nil {
		return "", err
	}
	ref, _ := repoRef(proj.RepoURL)
	if tool == "pr_merge" {
		return describeHeldMerge(r, raw, ref)
	}
	return describeHeldReview(r, raw, ref)
}

func describeHeldMerge(r *http.Request, raw []byte, ref string) (string, error) {
	m, err := parseHeldMerge(r, raw)
	if err != nil {
		return "", err
	}
	method := m.Method
	if method == "" {
		method = "merge"
	}
	return i18n.Text("api.chats.labels.held_merge_pr", "number", m.Number, "repo", ref, "method", method), nil
}

func describeHeldReview(r *http.Request, raw []byte, ref string) (string, error) {
	rv, err := parseHeldReview(r, raw)
	if err != nil {
		return "", err
	}
	if rv.State == "rejected" {
		return i18n.Text("api.chats.labels.held_deployment_rejected", "run", rv.RunID, "repo", ref), nil
	}
	return i18n.Text("api.chats.labels.held_deployment_approved", "run", rv.RunID, "repo", ref), nil
}

func (s *Server) confirmedGithubCall(w http.ResponseWriter, r *http.Request, perms githubapp.Permissions) (token string, raw []byte, ok bool) {
	if confirmedAction(r.Context()) == nil {
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.needs_confirmation")
		return "", nil, false
	}
	if s.gh.tokens == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.no_credential_source")
		return "", nil, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHeldBody))
	if err != nil {
		writeErrKey(w, r, http.StatusRequestEntityTooLarge, "api.chats.errors.held_body_too_large")
		return "", nil, false
	}
	proj, err := s.heldGithubProject(r)
	if err != nil {
		writeError(w, r, 0, err)
		return "", nil, false
	}
	rec, err := s.githubTokenForProjectWith(r.Context(), proj.ID, perms)
	if errors.Is(err, githubapp.ErrNoCredential) {
		writeErrKey(w, r, http.StatusNotFound, "api.github.errors.no_token")
		return "", nil, false
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, err)
		return "", nil, false
	}
	return rec.Token, raw, true
}

func (s *Server) handleHeldPullMerge(w http.ResponseWriter, r *http.Request) {
	token, raw, ok := s.confirmedGithubCall(w, r, githubapp.PermsMerge)
	if !ok {
		return
	}
	req, err := parseHeldMerge(r, raw)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	res, err := ghrest.MergePR(r.Context(), token, req)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleHeldDeploymentReview(w http.ResponseWriter, r *http.Request) {
	token, raw, ok := s.confirmedGithubCall(w, r, githubapp.PermsDeploymentReview)
	if !ok {
		return
	}
	req, err := parseHeldReview(r, raw)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	if len(req.EnvironmentIDs) == 0 {
		req.EnvironmentIDs, err = reviewableEnvironments(r.Context(), token, req)
		if err != nil {
			writeError(w, r, 0, err)
			return
		}
	}
	created, err := ghrest.ReviewPendingDeployments(r.Context(), token, req)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state":           req.State,
		"environment_ids": req.EnvironmentIDs,
		"deployment_ids":  created,
	})
}

func reviewableEnvironments(ctx context.Context, token string, req *ghrest.DeploymentReview) ([]int64, error) {
	pending, err := ghrest.PendingDeployments(ctx, token, req.Owner, req.Repo, req.RunID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for i := range pending {
		if pending[i].CurrentUserCanApprove && pending[i].Environment.ID != 0 {
			ids = append(ids, pending[i].Environment.ID)
		}
	}
	if len(ids) == 0 {
		return nil, apperr.Conflict("api.github.errors.no_reviewable_environment", "run", req.RunID)
	}
	return ids, nil
}
