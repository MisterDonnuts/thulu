package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type pullRequestRow struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	URL         string `json:"url"`
	*github.PRStatus
	Error        string `json:"error,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

type prStatusTokenLookup interface {
	GetPRStatusToken(ctx context.Context, projectID string) (*githubapp.Token, bool, error)
}

func (s *Server) prStatusToken(ctx context.Context, projectID string) (*githubapp.Token, bool, error) {
	if s.gh.tokens == nil {
		return nil, false, githubapp.ErrNoCredential
	}
	if l, ok := s.gh.tokens.(prStatusTokenLookup); ok {
		return l.GetPRStatusToken(ctx, projectID)
	}
	tok, err := s.gh.tokens.GetGithubToken(ctx, projectID)
	return tok, true, err
}

func (s *Server) handleTaskPullRequests(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskRowForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	ids := prProjectOrder(t)
	rows := make([]pullRequestRow, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = s.pullRequestRow(r, t, id)
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"pull_requests": rows})
}

func prProjectOrder(t *task.Task) []string {
	ids := make([]string, 0, len(t.PRURLs))
	for _, id := range t.ProjectIDs {
		if t.PRURLs[id] != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	var rest []string
	for id, url := range t.PRURLs {
		if url != "" && !slices.Contains(ids, id) {
			rest = append(rest, id)
		}
	}
	slices.Sort(rest)
	return append(ids, rest...)
}

func (s *Server) pullRequestRow(r *http.Request, t *task.Task, projectID string) pullRequestRow {
	ctx := r.Context()
	prURL := t.PRURLs[projectID]
	row := pullRequestRow{ProjectID: projectID, ProjectName: projectID, URL: prURL}
	fail := func(err error) pullRequestRow {
		row.Error = apperr.Code(err)
		row.ErrorMessage = apperr.Localize(err, i18n.RequestLocale(r))
		return row
	}
	if s.projectStore != nil {
		p, err := s.projectStore.Get(ctx, projectID)
		if err == nil && p != nil {
			row.ProjectName = p.Name
			// Never spend one workspace's credential on a PR URL another workspace's task carries.
			if p.WorkspaceID != "" && t.WorkspaceID != "" && p.WorkspaceID != t.WorkspaceID {
				return fail(apperr.Forbidden("api.pull_requests.errors.project_other_workspace"))
			}
		}
	}
	owner, repo, number, ok := github.ParsePRURL(prURL)
	if !ok {
		return fail(apperr.Invalid("api.pull_requests.errors.not_a_pull_request"))
	}
	if st, hit := s.gh.prs.get(prURL); hit {
		row.PRStatus = st
		return row
	}
	gen := s.gh.prs.generation()
	tok, checks, err := s.prStatusToken(ctx, projectID)
	if err != nil {
		if !errors.Is(err, githubapp.ErrNoCredential) {
			logx.Warn(err, "api.pull_requests.log.token_failed", "project", projectID)
		}
		return fail(apperr.NotFound("api.pull_requests.errors.no_credential"))
	}
	st, err := github.FetchPRStatus(ctx, tok.Token, owner, repo, number, checks)
	if err != nil {
		return fail(apperr.Wrap(err, "api.pull_requests.errors.load_failed"))
	}
	s.gh.prs.put(gen, prURL, owner, repo, number, st)
	row.PRStatus = st
	return row
}
