package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/buildinfo"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/githubcfg"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/strx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Run keys always pass; other API keys only on routes that return no raw secret.
// Browser sessions never: a stolen cookie or a CSRF hole would hand out tokens.
func (s *Server) requireInternalCaller(w http.ResponseWriter, r *http.Request, rawSecret bool) bool {
	ctx := r.Context()
	if auth.IsAPIKeyAuthed(ctx) {
		if auth.APIKeyRunScope(ctx).IsRun() || !rawSecret {
			return true
		}
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.run_key_required")
		return false
	}
	if _, unrestricted := s.callerWorkspace(r); unrestricted {
		return true
	}
	writeErrKey(w, r, http.StatusForbidden, "api.github.errors.internal_not_for_sessions")
	return false
}

func (s *Server) internalVisible(r *http.Request, resourceWID string) bool {
	wid, unrestricted := s.callerWorkspace(r)
	return workspaceVisible(resourceWID, wid, unrestricted)
}

// A run key with no task (an unlinked chat) is refused here too: it owns no task's tokens.
func requireTaskScope(w http.ResponseWriter, r *http.Request, taskID string) bool {
	if scope := auth.APIKeyRunScope(r.Context()); scope.IsRun() && scope.TaskID != taskID {
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.key_scoped_to_other_task")
		return false
	}
	return true
}

func (s *Server) requireGithubProjectScope(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if !s.runReachesGithubProject(r, projectID) {
		writeErrKey(w, r, http.StatusForbidden, "api.github.errors.key_task_not_in_project")
		return false
	}
	if !s.internalProjectVisible(r, projectID) {
		writeErrKey(w, r, http.StatusNotFound, "api.github.errors.project_not_found")
		return false
	}
	return true
}

func (s *Server) requireProjectScopeQuiet(r *http.Request, projectID string) bool {
	scope := auth.APIKeyRunScope(r.Context())
	if !scope.IsRun() {
		return true
	}
	if scope.TaskID == "" {
		return false
	}
	t, err := s.taskStore.Get(r.Context(), scope.TaskID)
	return err == nil && slices.Contains(t.ProjectIDs, projectID)
}

func (s *Server) internalProjectVisible(r *http.Request, projectID string) bool {
	proj, err := s.projectStore.Get(r.Context(), projectID)
	return err == nil && proj != nil && s.internalVisible(r, proj.WorkspaceID)
}

func (s *Server) internalTaskFor(w http.ResponseWriter, r *http.Request, rawSecret bool) (*task.Task, bool) {
	if !s.requireInternalCaller(w, r, rawSecret) {
		return nil, false
	}
	taskID := r.PathValue("id")
	if !requireTaskScope(w, r, taskID) {
		return nil, false
	}
	t, err := s.taskStore.Get(r.Context(), taskID)
	if err != nil || !s.internalVisible(r, t.WorkspaceID) {
		writeErrKey(w, r, http.StatusNotFound, "api.github.errors.task_not_found")
		return nil, false
	}
	return t, true
}

type internalGithubTokenResp struct {
	ProjectID string `json:"project_id"`
	Token     string `json:"token"`
	Login     string `json:"login,omitempty"`
}

func (s *Server) handleInternalProjectGithubToken(w http.ResponseWriter, r *http.Request) {
	if s.gh.tokens == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.no_credential_source")
		return
	}
	if !s.requireInternalCaller(w, r, true) {
		return
	}
	projectID := r.PathValue("id")
	if !s.requireGithubProjectScope(w, r, projectID) {
		return
	}
	perms, err := s.tokenPermissionsFor(r, s.runTask(r))
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	s.writeProjectGithubToken(w, r, projectID, perms)
}

// In Kubernetes this worktree only mirrors what the agent pushed; call syncAgentResult first.
func (s *Server) worktreeGitForProject(ctx context.Context, t *task.Task, projID string) (project.WorktreeGit, error) {
	if s.kube == nil {
		return s.projectStore.HostWorktreeGit(ctx, projID, t.ID)
	}
	return s.projectStore.HostWorktreeGit(ctx, projID, t.SandboxRootID())
}

func (s *Server) syncAgentResult(ctx context.Context, t *task.Task, projID string) error {
	if s.kube == nil {
		return nil
	}
	root := t.SandboxRootID()
	_, err := s.projectStore.IntegrateAgentResult(ctx, projID, root, project.ResultBranchName(root))
	return err
}

// Every push path (branch_push, publishAfterAgentPush, dispatcher auto-push) must go through here.
// Runner projects are refused: the runner pushes from its machine and a push from this mirror would
// rewrite its work.
// Kubernetes skips the rebase: the agent rebases in its pod, where it can resolve conflicts.
func (s *Server) pushTaskBranchForProject(ctx context.Context, t *task.Task, projID string) (bool, project.PushResult, error) {
	if p, err := s.projectStore.Get(ctx, projID); err == nil && p.RunsOnRunner() {
		return false, project.PushResult{}, apperr.Precondition("api.runners.errors.push_on_runner", "project", p.Name)
	}
	if s.kube != nil {
		lock := s.agentGit.publishLock(t.SandboxRootID() + "/" + projID)
		lock.Lock()
		defer lock.Unlock()
		if err := s.syncAgentResult(ctx, t, projID); err != nil {
			return false, project.PushResult{}, err
		}
	}
	wt, err := s.worktreeGitForProject(ctx, t, projID)
	if err != nil {
		return false, project.PushResult{}, err
	}
	ahead, err := wt.HasCommitsAhead()
	if err != nil {
		return false, project.PushResult{}, err
	}
	if !ahead {
		return false, project.PushResult{}, nil
	}
	token := s.projectGithubTokenWith(ctx, projID, pushPermissions(wt))
	if s.kube != nil {
		if _, err = wt.Publish(token); err != nil {
			return false, project.PushResult{}, workflowPushHint(err)
		}
		return true, project.PushResult{}, nil
	}
	result, err := wt.Push(token)
	if err != nil {
		return false, result, workflowPushHint(err)
	}
	return true, result, nil
}

// The workflows permission is derived from the commits, never from the request, so an agent cannot
// get it by asking.
func pushPermissions(wt project.WorktreeGit) githubapp.Permissions {
	if !githubapp.WorkflowWritesAllowed() {
		return githubapp.PermsPush
	}
	touches, err := wt.TouchesWorkflows()
	if err != nil {
		logx.Warn(err, "api.github.log.workflow_check_failed", "branch", wt.Branch)
		return githubapp.PermsPush
	}
	if touches {
		return githubapp.PermsPush.With(githubapp.PermWorkflowsWrite)
	}
	return githubapp.PermsPush
}

// Matches GitHub's push refusal text for App tokens lacking the workflows permission.
func workflowPushHint(err error) error {
	if !strings.Contains(err.Error(), "without `workflows` permission") || githubapp.WorkflowWritesAllowed() {
		return err
	}
	return apperr.Wrap(err, "api.github.errors.workflow_push_hint", "env", githubapp.EnvAllowWorkflowWrites)
}

// Resolution errors fall through to a tokenless push on purpose: public repos still work, private
// ones fail visibly in git.
func (s *Server) projectGithubToken(ctx context.Context, projID string) string {
	return s.projectGithubTokenWith(ctx, projID, githubapp.PermsPush)
}

func (s *Server) projectGithubTokenWith(ctx context.Context, projID string, perms githubapp.Permissions) string {
	rec, err := s.githubTokenForProjectWith(ctx, projID, perms)
	if err == nil && rec != nil {
		return rec.Token
	}
	if err != nil && !errors.Is(err, githubapp.ErrNoCredential) {
		logx.Warn(err, "api.github.log.resolve_push_credential_failed", "project", projID)
	}
	return ""
}

func (s *Server) handleInternalTaskPR(w http.ResponseWriter, r *http.Request) {
	t, ok := s.internalTaskFor(w, r, false)
	if !ok {
		return
	}
	var body struct {
		Repo   string `json:"repo"`
		URL    string `json:"url"`
		Number int    `json:"number,omitempty"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.URL) == "" {
		writeErrKey(w, r, http.StatusBadRequest, "api.github.errors.url_required")
		return
	}
	updated, projectID, err := s.taskDispatcher.RecordAgentPR(r.Context(), t, body.Repo, body.URL)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":    updated.ID,
		"project_id": projectID,
		"pr_url":     updated.PRURLs[projectID],
		"number":     body.Number,
	})
}

func (s *Server) handleInternalTaskCommit(w http.ResponseWriter, r *http.Request) {
	t, ok := s.internalTaskFor(w, r, false)
	if !ok {
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if !decodeOptionalBody(w, r, &body) {
		return
	}
	if body.Message == "" {
		body.Message = i18n.Text("api.github.labels.default_commit_message", "task", t.ID)
	}
	type entry struct {
		ProjectID string `json:"project_id"`
		Committed bool   `json:"committed"`
		Skipped   string `json:"skipped,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	resp := struct {
		TaskID  string  `json:"task_id"`
		Message string  `json:"message"`
		Results []entry `json:"results"`
	}{TaskID: t.ID, Message: body.Message}

	if s.kube != nil {
		for _, projID := range t.ProjectIDs {
			resp.Results = append(resp.Results, entry{ProjectID: projID,
				Error: i18n.TCtx(r.Context(), "api.github.hints.commit_in_terminal")})
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	for _, projID := range t.ProjectIDs {
		e := entry{ProjectID: projID}
		wt, err := s.worktreeGitForProject(r.Context(), t, projID)
		if err != nil {
			e.Error = apperr.LocalizeCtx(r.Context(), err)
			resp.Results = append(resp.Results, e)
			continue
		}
		dirty, err := wt.HasChanges()
		if err != nil {
			e.Error = apperr.LocalizeCtx(r.Context(), err)
			resp.Results = append(resp.Results, e)
			continue
		}
		if !dirty {
			e.Skipped = i18n.TCtx(r.Context(), "api.github.hints.nothing_to_commit")
			resp.Results = append(resp.Results, e)
			continue
		}
		if _, err := wt.Commit(body.Message); err != nil {
			e.Error = apperr.LocalizeCtx(r.Context(), err)
			resp.Results = append(resp.Results, e)
			continue
		}
		e.Committed = true
		resp.Results = append(resp.Results, e)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleInternalTaskBranchPush(w http.ResponseWriter, r *http.Request) {
	t, ok := s.internalTaskFor(w, r, false)
	if !ok {
		return
	}
	type entry struct {
		ProjectID      string   `json:"project_id"`
		Branch         string   `json:"branch"`
		Pushed         bool     `json:"pushed"`
		Rebased        bool     `json:"rebased,omitempty"`
		Skipped        string   `json:"skipped,omitempty"`
		Error          string   `json:"error,omitempty"`
		Conflict       bool     `json:"conflict,omitempty"`
		ConflictFiles  []string `json:"conflict_files,omitempty"`
		ConflictBase   string   `json:"conflict_base,omitempty"`
		ConflictAction string   `json:"conflict_action,omitempty"`
	}
	resp := struct {
		TaskID  string  `json:"task_id"`
		Hint    string  `json:"hint,omitempty"`
		Results []entry `json:"results"`
	}{TaskID: t.ID}
	if s.kube != nil {
		resp.Hint = i18n.TCtx(r.Context(), "api.github.hints.push_from_terminal")
	}

	for _, projID := range t.ProjectIDs {
		e := entry{ProjectID: projID, Branch: s.taskBranchFor(t)}
		pushed, result, err := s.pushTaskBranchForProject(r.Context(), t, projID)
		if err != nil {
			var cerr *project.RebaseConflictError
			if errors.As(err, &cerr) {
				e.Conflict = true
				e.ConflictBase = cerr.Base
				e.ConflictFiles = cerr.Files
				e.ConflictAction = i18n.TCtx(r.Context(), "api.github.hints.resolve_conflicts")
				e.Error = apperr.LocalizeCtx(r.Context(), cerr)
				resp.Results = append(resp.Results, e)
				continue
			}
			e.Error = apperr.LocalizeCtx(r.Context(), err)
			e.Rebased = result.Rebased
			resp.Results = append(resp.Results, e)
			continue
		}
		if !pushed {
			e.Skipped = i18n.TCtx(r.Context(), "api.github.hints.no_commits_ahead")
			resp.Results = append(resp.Results, e)
			continue
		}
		e.Pushed = true
		e.Rebased = result.Rebased
		resp.Results = append(resp.Results, e)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleInternalTaskBranchStatus(w http.ResponseWriter, r *http.Request) {
	t, ok := s.internalTaskFor(w, r, false)
	if !ok {
		return
	}
	type entry struct {
		ProjectID string                `json:"project_id"`
		Status    *project.BranchStatus `json:"status,omitempty"`
		Error     string                `json:"error,omitempty"`
	}
	resp := struct {
		TaskID  string  `json:"task_id"`
		Results []entry `json:"results"`
	}{TaskID: t.ID}
	for _, projID := range t.ProjectIDs {
		e := entry{ProjectID: projID}
		st, err := s.branchStatusForProject(r.Context(), t, projID)
		if err != nil {
			e.Error = apperr.LocalizeCtx(r.Context(), err)
		} else {
			e.Status = &st
		}
		resp.Results = append(resp.Results, e)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) branchStatusForProject(ctx context.Context, t *task.Task, projID string) (project.BranchStatus, error) {
	if s.kube != nil {
		lock := s.agentGit.publishLock(t.SandboxRootID() + "/" + projID)
		lock.Lock()
		defer lock.Unlock()
		if err := s.syncAgentResult(ctx, t, projID); err != nil {
			return project.BranchStatus{}, err
		}
	}
	wt, err := s.worktreeGitForProject(ctx, t, projID)
	if err != nil {
		return project.BranchStatus{}, err
	}
	return wt.Status(s.projectGithubToken(ctx, projID))
}

func (s *Server) taskBranchFor(t *task.Task) string {
	if s.kube != nil {
		return project.TaskBranchName(t.SandboxRootID())
	}
	return project.TaskBranchName(t.ID)
}

func (s *Server) handleInternalTaskGithubToken(w http.ResponseWriter, r *http.Request) {
	if s.gh.tokens == nil {
		writeErrKey(w, r, http.StatusServiceUnavailable, "api.github.errors.no_credential_source")
		return
	}
	t, ok := s.internalTaskFor(w, r, true)
	if !ok {
		return
	}
	perms, err := s.tokenPermissionsFor(r, t)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	for _, projID := range t.ProjectIDs {
		rec, err := s.githubTokenForProjectWith(r.Context(), projID, perms)
		if errors.Is(err, githubapp.ErrNoCredential) {
			continue
		}
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, internalGithubTokenResp{
			ProjectID: rec.ProjectID,
			Token:     rec.Token,
			Login:     rec.Login,
		})
		return
	}
	writeErrKey(w, r, http.StatusNotFound, "api.github.errors.no_task_token")
}

// GitHub omits X-OAuth-Scopes for fine-grained PATs, so empty scopes do not mean no access.
func validateGithubToken(ctx context.Context, token string) (string, []string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubcfg.APIURL()+"/user", http.NoBody)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return "", nil, apperr.Wrap(err, "api.github.errors.read_user_response")
	}
	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Message string `json:"message"`
		}
		logx.Warn(json.Unmarshal(body, &errResp), "api.github.log.parse_error_body_failed")
		msg := errResp.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", nil, fmt.Errorf("%s", msg)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return "", nil, apperr.Wrap(err, "api.github.errors.decode_user_response")
	}
	scopes := parseScopesHeader(resp.Header.Get("X-OAuth-Scopes"))
	return user.Login, scopes, nil
}

func parseScopesHeader(h string) []string { return strx.SplitCSV(h) }
