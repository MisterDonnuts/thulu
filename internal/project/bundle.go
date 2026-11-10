package project

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func (s *Store) ImportProject(ctx context.Context, p Project) error {
	if p.ID == "" {
		return apperr.Invalid("project.errors.import_id_required")
	}
	if p.WorkspaceID == "" {
		return apperr.Invalid("project.errors.import_workspace_required", "id", p.ID)
	}
	// A bundle is untrusted input and the branch becomes a git argument, so it gets the API's
	// check.
	if err := ValidateBranchName(p.DefaultBranch); err != nil {
		return apperr.Wrap(err, "project.errors.import_failed", "id", p.ID)
	}
	// The path is stored verbatim, so it gets the directory name rule Create applies.
	if err := checkProjectDirName(p.Name, filepath.Base(p.Path)); err != nil {
		return apperr.Wrap(err, "project.errors.import_failed", "id", p.ID)
	}
	tpl := p.StageTemplate
	if tpl == nil {
		tpl = map[string]string{}
	}
	tplJSON, err := json.Marshal(tpl)
	if err != nil {
		return apperr.Wrap(err, "project.errors.marshal_template_for", "id", p.ID)
	}
	// Bundles from before Source existed carry none; every project then was a git clone.
	source := p.Source
	if source == "" {
		source = SourceGit
	}
	if source != SourceGit && source != SourceUpload {
		return apperr.Invalid("project.errors.import_bad_source", "id", p.ID, "source", source)
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		`INSERT INTO projects (id, name, path, repo_url, default_branch,
		    description, stage_template, default_board_id, workspace_id,
		    github_installation_id, source, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (id) DO UPDATE SET
		    name = excluded.name, path = excluded.path, repo_url = excluded.repo_url,
		    default_branch = excluded.default_branch, description = excluded.description,
		    stage_template = excluded.stage_template, default_board_id = excluded.default_board_id,
		    workspace_id = excluded.workspace_id,
		    github_installation_id = excluded.github_installation_id,
		    source = excluded.source`),
		p.ID, p.Name, p.Path, p.RepoURL, p.DefaultBranch,
		p.Description, string(tplJSON), p.DefaultBoardID, p.WorkspaceID,
		p.GithubInstallationID, source, p.CreatedAt)
	if err != nil {
		return apperr.Wrap(err, "project.errors.import_failed", "id", p.ID)
	}
	// Rows stamped with the project's workspace must follow it when an import moves the project.
	if _, err := s.db.ExecContext(ctx, s.rb(
		`UPDATE project_init_script_revisions SET workspace_id = ?
		  WHERE project_id = ? AND workspace_id <> ?`),
		p.WorkspaceID, p.ID, p.WorkspaceID); err != nil {
		return apperr.Wrap(err, "project.errors.import_failed", "id", p.ID)
	}
	return nil
}

func (s *Store) ImportEnvironment(ctx context.Context, e Environment) error {
	if e.ID == "" || e.ProjectID == "" {
		return apperr.Invalid("project.errors.import_env_ids_required")
	}
	sel, err := marshalSelectors(e.Selectors)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.rb(
		//i18n:ignore SQL statement, not a message
		`INSERT INTO project_environments (`+envCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (id) DO UPDATE SET
		    project_id = excluded.project_id, name = excluded.name,
		    display_name = excluded.display_name, system = excluded.system,
		    provider = excluded.provider, region = excluded.region, account = excluded.account,
		    selectors = excluded.selectors, is_production = excluded.is_production,
		    url = excluded.url, updated_at = excluded.updated_at`),
		e.ID, e.ProjectID, e.Name, e.System, e.Provider, e.Region, e.Account, sel,
		e.IsProduction, e.URL, e.CreatedAt, e.UpdatedAt, e.DisplayName)
	if err != nil {
		return apperr.Wrap(err, "project.errors.import_env_failed", "id", e.ID)
	}
	return nil
}
