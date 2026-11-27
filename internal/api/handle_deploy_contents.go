package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/deploycontents"
	ghrest "github.com/MisterDonnuts/thulu/internal/github"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
)

// Tasks checked per report. A deploy that carries more than this is rare, and each one costs a
// compare against GitHub.
const deployContentsMax = 50

const (
	deploySourceGitHub = "github"
	deploySourceGit    = "git"
)

type deployContentsResp struct {
	deploycontents.Report
	ProjectID string `json:"project_id"`
	// github (compare API) or git (the daemon's clone).
	Source string `json:"source"`
	// More tasks waited in the stage than were checked.
	Truncated bool `json:"truncated,omitempty"`
}

// GET /projects/{id}/deploy-contents?sha=&prev=&stage=
//
// Which tasks waiting in the merge_target stage (or in ?stage=) a deployed commit carries.
func (s *Server) handleDeployContents(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.projectForCaller(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	q := r.URL.Query()
	sha, prev := strings.TrimSpace(q.Get("sha")), strings.TrimSpace(q.Get("prev"))
	if !ghrest.ValidCommitSHA(sha) || (prev != "" && !ghrest.ValidCommitSHA(prev)) {
		writeErrKey(w, r, http.StatusBadRequest, "api.deploy_contents.errors.sha_invalid")
		return
	}
	cands, truncated, err := s.deployCandidates(r.Context(), proj, task.Stage(strings.TrimSpace(q.Get("stage"))))
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	repo, source, err := s.deployRepo(r.Context(), proj)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	rep, err := deploycontents.Build(r.Context(), repo, sha, prev, cands)
	if err != nil {
		writeError(w, r, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, deployContentsResp{Report: rep, ProjectID: proj.ID, Source: source, Truncated: truncated})
}

// deployCandidates lists the project's tasks in stage, or, without one, in each board's merge_target
// stage.
func (s *Server) deployCandidates(ctx context.Context, proj *project.Project, stage task.Stage) ([]deploycontents.Candidate, bool, error) {
	stages := []task.Stage{stage}
	if stage == "" {
		stages = s.mergeTargetStages()
	}
	// Without ?stage= the board filter below drops tasks, so the query cannot apply the cap: a stage
	// of the same name on other boards would fill it with tasks that never count.
	limit := deployContentsMax + 1
	if stage == "" {
		limit = 0
	}
	var out []deploycontents.Candidate
	truncated := false
	for _, st := range stages {
		tasks, err := s.taskStore.ListProjectTasksInStage(ctx, proj.WorkspaceID, proj.ID, st, limit)
		if err != nil {
			return nil, false, err
		}
		for i := range tasks {
			t := &tasks[i]
			// Two boards can name a stage alike; only the board where it is the merge_target counts.
			if stage == "" && !s.isMergeTarget(t) {
				continue
			}
			if len(out) == deployContentsMax {
				truncated = true
				break
			}
			out = append(out, deploycontents.Candidate{
				TaskID: t.ID, Title: t.Title, Stage: string(t.Stage), BoardID: t.BoardID, MergeSHA: t.MergeSHAs[proj.ID],
			})
		}
	}
	return out, truncated, nil
}

func (s *Server) mergeTargetStages() []task.Stage {
	var out []task.Stage
	for _, b := range s.boards.BoardList() {
		if st, ok := mergeTargetOf(s.boards.Board(b.ID)); ok && !slices.Contains(out, st) {
			out = append(out, st)
		}
	}
	return out
}

func (s *Server) isMergeTarget(t *task.Task) bool {
	info, ok := s.stagesFor(t).Get(t.Stage)
	return ok && info.MergeTarget
}

// deployRepo picks where the history is read: GitHub's compare API when the project's remote is on
// GitHub and a credential reaches it, else the daemon's clone. A runner project's code lives on the
// customer's machine, so GitHub is its only source.
func (s *Server) deployRepo(ctx context.Context, proj *project.Project) (deploycontents.Repo, string, error) {
	owner, name, onGitHub := ghrest.ParseRemote(proj.RepoURL)
	if onGitHub && !proj.Hosted() {
		tok, _, err := s.prStatusToken(ctx, proj.ID)
		switch {
		case err == nil:
			return deploycontents.GitHubRepo{Token: tok.Token, Owner: owner, Name: name}, deploySourceGitHub, nil
		case !errors.Is(err, githubapp.ErrNoCredential):
			return nil, "", err
		}
	}
	if proj.RunnerID != "" {
		return nil, "", apperr.Conflict("api.deploy_contents.errors.runner_needs_github", "project", proj.Name)
	}
	// Without a credential the fetch only reaches a public remote; the clone answers either way.
	h, err := s.projectStore.History(ctx, proj.ID, "")
	if err != nil {
		return nil, "", err
	}
	return deploycontents.GitRepo{History: h}, deploySourceGit, nil
}
