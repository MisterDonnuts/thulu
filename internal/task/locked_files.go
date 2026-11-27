package task

import (
	"context"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/migrate"
	"github.com/MisterDonnuts/thulu/internal/runner"
)

// The default flow's per-task state in one migration. flow_flags stays NULL until a refinement
// sets it: NULL runs every conditional stage, '[]' skips them all.
var flowStateMigration = migrate.Migration{ID: 51, SQL: `ALTER TABLE tasks
			ADD COLUMN IF NOT EXISTS flow_flags TEXT,
			ADD COLUMN IF NOT EXISTS merge_shas TEXT NOT NULL DEFAULT '{}';
		CREATE TABLE IF NOT EXISTS task_locked_files (
			task_id    TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL,
			path       TEXT NOT NULL,
			blob_sha   TEXT NOT NULL,
			stage      TEXT NOT NULL,
			locked_ref TEXT NOT NULL DEFAULT '',
			locked_at  TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (task_id, project_id, path)
		)`}

// nil stays nil (never set); an empty list stays an empty list, never JSON null.
func copyFlowFlags(p *[]string) []string {
	if p == nil {
		return nil
	}
	return append([]string{}, *p...)
}

// A test file a lock_tests stage wrote, with the git blob it had when that stage closed. A later
// stage that changes the blob needs a person's approval.
type LockedFile struct {
	TaskID    string `json:"task_id"`
	ProjectID string `json:"project_id"`
	Path      string `json:"path"`
	// Empty when the stage deleted the file: bringing it back is a change too.
	BlobSHA   string    `json:"blob_sha"`
	Stage     Stage     `json:"stage"`
	LockedRef string    `json:"locked_ref,omitempty"`
	LockedAt  time.Time `json:"locked_at"`
}

// Whether a repo-relative path is a test file by the contract's globs: *_test.go,
// *.test.{ts,tsx,js,jsx}, *.spec.*, test_*.py, *_test.py, *Test.java, and anything under a tests/,
// __tests__/ or src/test/ directory. The directory globs match at any depth, so a monorepo's
// packages/x/tests/ counts too.
func IsTestPath(p string) bool {
	p = strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, `\`, "/")), "/")
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "Test.java"):
		return true
	}
	if stem, ext, ok := cutLastDot(base); ok {
		if _, kind, ok := cutLastDot(stem); ok {
			if kind == "spec" {
				return true
			}
			if kind == "test" && slices.Contains([]string{"ts", "tsx", "js", "jsx"}, ext) {
				return true
			}
		}
	}
	dirs := strings.Split(path.Dir(p), "/")
	for i, d := range dirs {
		if d == "tests" || d == "__tests__" || d == "test" && i > 0 && dirs[i-1] == "src" {
			return true
		}
	}
	return false
}

// "a.spec.ts" → ("a.spec", "ts"); a leading dot (".env") is not an extension.
func cutLastDot(s string) (stem, ext string, ok bool) {
	i := strings.LastIndex(s, ".")
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// One repo-relative path from locked-tests.txt, cleaned; the runner reads the file by the same rule.
func cleanLockedPath(line string) string { return runner.CleanLockedPath(line) }

// The paths of locked-tests.txt, cleaned and deduplicated; the rest are reported back as refused.
func parseLockedList(body string) (paths, refused []string) {
	for _, line := range strings.Split(body, "\n") {
		p := cleanLockedPath(line)
		switch {
		case p != "":
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		case strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#"):
			refused = append(refused, strings.TrimSpace(line))
		}
	}
	return paths, refused
}

func (s *Store) ListLockedFiles(ctx context.Context, taskID string) ([]LockedFile, error) {
	rows, err := s.db.QueryContext(ctx, s.rb(
		`SELECT task_id, project_id, path, blob_sha, stage, locked_ref, locked_at
		 FROM task_locked_files WHERE task_id = ? ORDER BY project_id ASC, path ASC`), taskID)
	if err != nil {
		return nil, apperr.Wrap(err, "task.errors.list_locked_files", "task", taskID)
	}
	defer rows.Close()
	out := []LockedFile{}
	for rows.Next() {
		var f LockedFile
		if err := rows.Scan(&f.TaskID, &f.ProjectID, &f.Path, &f.BlobSHA, &f.Stage, &f.LockedRef, &f.LockedAt); err != nil {
			return nil, apperr.Wrap(err, "task.errors.list_locked_files", "task", taskID)
		}
		f.LockedAt = f.LockedAt.UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// Upsert: a stage that writes a locked file again moves its baseline.
func (s *Store) PutLockedFile(ctx context.Context, f LockedFile) error {
	return s.putLockedFile(ctx, f, `DO UPDATE SET
		    blob_sha = excluded.blob_sha, stage = excluded.stage,
		    locked_ref = excluded.locked_ref, locked_at = excluded.locked_at`)
}

// An export bundle's row never replaces one already there, like every other row an import brings:
// re-importing an older bundle must not move the baseline back.
func (s *Store) ImportLockedFile(ctx context.Context, f LockedFile) error {
	return s.putLockedFile(ctx, f, `DO NOTHING`)
}

// onConflict is one of the two constant clauses above; values travel as bind arguments.
func (s *Store) putLockedFile(ctx context.Context, f LockedFile, onConflict string) error {
	if f.TaskID == "" || f.ProjectID == "" || f.Path == "" {
		return apperr.Invalid("task.errors.locked_file_ids")
	}
	if f.LockedAt.IsZero() {
		f.LockedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO task_locked_files (task_id, project_id, path, blob_sha, stage, locked_ref, locked_at)
		 VALUES (?,?,?,?,?,?,?)
		 ON CONFLICT (task_id, project_id, path) `+onConflict),
		f.TaskID, f.ProjectID, f.Path, f.BlobSHA, string(f.Stage), f.LockedRef, f.LockedAt)
	if err != nil {
		return apperr.Wrap(err, "task.errors.put_locked_file", "task", f.TaskID, "path", f.Path)
	}
	return nil
}
