package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// The hook is written even when the stage is not on the user's board, so no configuration is lost;
// the orphan is visible and prunable in the UI.
var legacyCmdStages = []struct {
	column string
	stage  string
}{
	{"ai_review_cmd", "ai_review"},
	{"testing_cmd", "testing"},
}

func (s *Store) MigrateLegacyStageCmds(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT id, name, path, ai_review_cmd, testing_cmd FROM projects
		 WHERE ai_review_cmd <> '' OR testing_cmd <> ''`))
	if err != nil {
		return 0, apperr.Wrap(err, "project.errors.read_legacy_cmds")
	}
	defer rows.Close()

	type pending struct {
		id, name, path, aiReview, testing string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.name, &p.path, &p.aiReview, &p.testing); err != nil {
			return 0, apperr.Wrap(err, "project.errors.scan_legacy_cmds")
		}
		todo = append(todo, p)
	}
	if err := rows.Err(); err != nil {
		return 0, apperr.Wrap(err, "project.errors.read_legacy_cmds")
	}

	migrated := 0
	var firstErr error
	for _, p := range todo {
		proj := &Project{ID: p.id, Name: p.name, Path: p.path}
		cmds := map[string]string{"ai_review_cmd": p.aiReview, "testing_cmd": p.testing}
		if err := s.writeLegacyHooks(proj, cmds); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue // leave the columns set so the next boot retries
		}
		if _, err := s.db.ExecContext(ctx, s.rb(
			`UPDATE projects SET ai_review_cmd = '', testing_cmd = '' WHERE id = ?`), p.id); err != nil {
			if firstErr == nil {
				firstErr = apperr.Wrap(err, "project.errors.clear_legacy_cmds", "id", p.id)
			}
			continue
		}
		migrated++
	}
	return migrated, firstErr
}

//i18n:ignore shell interpreter line, not a message
const hookShebang = "#!/usr/bin/env bash\n"

func (s *Store) writeLegacyHooks(proj *Project, cmds map[string]string) error {
	root, err := s.EnsureStageDirs(proj)
	if err != nil {
		return apperr.Wrap(err, "project.errors.stage_dirs", "id", proj.ID)
	}
	for _, lc := range legacyCmdStages {
		cmd := strings.TrimSpace(cmds[lc.column])
		if cmd == "" {
			continue
		}
		hookPath := filepath.Join(root, "hooks", lc.stage+".sh")
		if _, err := os.Stat(hookPath); err == nil {
			continue
		}
		body := hookShebang +
			i18n.Text("project.hooks.migrated_header", "column", lc.column) +
			"set -e\n" + cmd + "\n"
		if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
			return apperr.Wrap(err, "project.errors.write_migrated_hook", "path", hookPath)
		}
	}
	return nil
}
