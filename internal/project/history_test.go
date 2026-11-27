package project

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	gitRunT(t, dir, "init", "-q", "-b", "main")
	gitRunT(t, dir, "config", "user.email", "test@example.com")
	gitRunT(t, dir, "config", "user.name", "test")
	var shas []string
	for i := range 4 {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(strconv.Itoa(i)), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRunT(t, dir, "add", ".")
		gitRunT(t, dir, "commit", "-q", "-m", "c"+strconv.Itoa(i)+"\n\nbody "+strconv.Itoa(i))
		shas = append(shas, strings.TrimSpace(gitOutT(t, dir, "rev-parse", "HEAD")))
	}
	h := OpenHistory(dir)
	ctx := t.Context()

	if ok, err := h.IsAncestor(ctx, shas[0][:8], shas[3]); err != nil || !ok {
		t.Errorf("IsAncestor(c0, c3) = %v, %v; want true", ok, err)
	}
	if ok, err := h.IsAncestor(ctx, shas[3], shas[0]); err != nil || ok {
		t.Errorf("IsAncestor(c3, c0) = %v, %v; want false", ok, err)
	}
	if _, err := h.IsAncestor(ctx, strings.Repeat("e", 40), shas[0]); err == nil {
		t.Error("a commit the clone lacks answered instead of failing")
	}
	if _, err := h.IsAncestor(ctx, "--output=/tmp/x", shas[0]); err == nil {
		t.Error("an option passed as a SHA went through")
	}

	all, complete, err := h.Commits(ctx, shas[0], shas[3], 10)
	if err != nil || !complete || len(all) != 3 {
		t.Fatalf("Commits(c0..c3) = %d commits, complete %v, %v; want 3, true", len(all), complete, err)
	}
	if all[0].SHA != shas[3] || all[0].Message != "c3\n\nbody 3" {
		t.Errorf("newest commit = %+v, want c3 with its body", all[0])
	}
	some, complete, err := h.Commits(ctx, shas[0], shas[3], 2)
	if err != nil || complete || len(some) != 2 {
		t.Errorf("Commits with limit 2 = %d, complete %v, %v; want 2, false", len(some), complete, err)
	}
}
