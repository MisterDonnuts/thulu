package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func insertLegacyProject(t *testing.T, s *Store, id, aiReview, testing string) *Project {
	t.Helper()
	path := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := s.DB().Exec(`INSERT INTO projects
		(id, name, path, repo_url, default_branch, description,
		 ai_review_cmd, testing_cmd, stage_template, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW())`,
		id, id, path, "file://"+path, "main", "", aiReview, testing, "{}")
	if err != nil {
		t.Fatalf("insert legacy project: %v", err)
	}
	return &Project{ID: id, Name: id, Path: path}
}

func readHook(t *testing.T, s *Store, proj *Project, stage string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.StageDir(proj), "hooks", stage+".sh"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func TestMigrateLegacyStageCmdsWritesHooksAndClearsColumns(t *testing.T) {
	store := newTestStore(t)
	proj := insertLegacyProject(t, store, "legacy1", "npm run lint", "go test ./...")

	n, err := store.MigrateLegacyStageCmds(context.Background())
	if err != nil {
		t.Fatalf("MigrateLegacyStageCmds: %v", err)
	}
	if n != 1 {
		t.Errorf("migrated = %d, want 1", n)
	}

	for stage, want := range map[string]string{"ai_review": "npm run lint", "testing": "go test ./..."} {
		body, ok := readHook(t, store, proj, stage)
		if !ok {
			t.Errorf("hooks/%s.sh not written", stage)
			continue
		}
		if !strings.Contains(body, want) {
			t.Errorf("hooks/%s.sh missing the command: %s", stage, body)
		}
		if !strings.HasPrefix(body, "#!") {
			t.Errorf("hooks/%s.sh missing shebang: %s", stage, body)
		}
	}

	n2, err := store.MigrateLegacyStageCmds(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second run migrated %d, want 0 — the columns weren't cleared", n2)
	}
}

func TestMigrateLegacyStageCmdsNeverOverwritesAnExistingHook(t *testing.T) {
	store := newTestStore(t)
	proj := insertLegacyProject(t, store, "legacy2", "", "go test ./...")

	root, err := store.EnsureStageDirs(proj)
	if err != nil {
		t.Fatalf("EnsureStageDirs: %v", err)
	}
	custom := "#!/usr/bin/env bash\n# hand written\n"
	if err := os.WriteFile(filepath.Join(root, "hooks", "testing.sh"), []byte(custom), 0o750); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	if _, err := store.MigrateLegacyStageCmds(context.Background()); err != nil {
		t.Fatalf("MigrateLegacyStageCmds: %v", err)
	}
	body, _ := readHook(t, store, proj, "testing")
	if body != custom {
		t.Errorf("existing hook was overwritten: %s", body)
	}
}

func TestMigrateLegacyStageCmdsIgnoresCleanProjects(t *testing.T) {
	store := newTestStore(t)
	insertLegacyProject(t, store, "clean", "", "")

	n, err := store.MigrateLegacyStageCmds(context.Background())
	if err != nil {
		t.Fatalf("MigrateLegacyStageCmds: %v", err)
	}
	if n != 0 {
		t.Errorf("migrated = %d, want 0", n)
	}
}
