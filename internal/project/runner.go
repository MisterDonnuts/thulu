package project

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/migrate"
)

var runnerMigration = migrate.Migration{ID: 30, SQL: `ALTER TABLE projects ADD COLUMN IF NOT EXISTS runner_id TEXT NOT NULL DEFAULT ''`}

func (p *Project) CodeOnRunner() bool { return p.Source == SourceRunner }

// CodeOnRunner counts even with RunnerID lost, so the run fails on the runner with a reason
// instead of as a Docker run with no code.
func (p *Project) RunsOnRunner() bool { return p.RunnerID != "" || p.CodeOnRunner() }

// repoURL is descriptive only and never fetched by the daemon, hence no ValidateRepoURLResolved.
func (s *Store) CreateOnRunner(ctx context.Context, name, repoURL, runnerID string, opts CreateOpts) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, apperr.Invalid("project.errors.name_required")
	}
	if strings.TrimSpace(opts.WorkspaceID) == "" {
		return nil, apperr.Invalid("project.errors.workspace_required")
	}
	if strings.TrimSpace(runnerID) == "" {
		return nil, apperr.Invalid("project.errors.runner_required")
	}
	if opts.DefaultBranch == "" {
		opts.DefaultBranch = "main"
	}
	if err := ValidateBranchName(opts.DefaultBranch); err != nil {
		return nil, err
	}
	if repoURL = strings.TrimSpace(repoURL); repoURL != "" {
		repoURL = NormalizeRepoURL(repoURL)
	}
	var exists int
	if err := s.db.QueryRowContext(ctx,
		s.rb(`SELECT COUNT(*) FROM projects WHERE workspace_id = ? AND name = ?`),
		opts.WorkspaceID, name,
	).Scan(&exists); err == nil && exists > 0 {
		return nil, apperr.Conflict("project.errors.name_taken", "name", name)
	}
	id := generateID()
	// The directory is never created; claiming the name gives the stage sidecar its Path and avoids
	// colliding with a clone of the same name.
	path, err := s.claimProjectDir(name, id)
	if err != nil {
		return nil, err
	}
	proj := &Project{
		ID:             id,
		Name:           name,
		Path:           path,
		RepoURL:        repoURL,
		DefaultBranch:  opts.DefaultBranch,
		Description:    opts.Description,
		StageTemplate:  opts.StageTemplate,
		DefaultBoardID: opts.DefaultBoardID,
		WorkspaceID:    opts.WorkspaceID,
		Source:         SourceRunner,
		RunnerID:       strings.TrimSpace(runnerID),
		CreatedAt:      time.Now().UTC(),
	}
	if proj.StageTemplate == nil {
		proj.StageTemplate = map[string]string{}
	}
	if err := s.insertProject(ctx, proj, proj.templateJSON()); err != nil {
		return nil, err
	}
	return proj, nil
}

func (s *Store) deleteRunnerProject(ctx context.Context, proj *Project) error {
	stageDir := s.StageDir(proj)
	logx.Warn(os.RemoveAll(stageDir), "project.log.remove_stage_dir", "path", stageDir)
	delCtx, cancelDel := context.WithTimeout(context.WithoutCancel(ctx), landingWriteTimeout)
	defer cancelDel()
	_, err := s.db.ExecContext(delCtx, s.rb(`DELETE FROM projects WHERE id = ?`), proj.ID)
	return err
}

// Echoing the current value always passes so a GET-then-PATCH never fails.
func (p *Project) checkRunnerPatch(runnerID string) error {
	if runnerID == p.RunnerID {
		return nil
	}
	switch {
	case p.CodeOnRunner() && runnerID == "":
		return apperr.Invalid("project.errors.runner_code_on_runner", "name", p.Name)
	case runnerID == "":
		return nil
	case p.Hosted():
		return apperr.Precondition("project.errors.runner_upload", "name", p.Name)
	case !p.CodeOnRunner() && p.RepoURL == "":
		return apperr.Precondition("project.errors.runner_no_remote", "name", p.Name)
	}
	return nil
}
