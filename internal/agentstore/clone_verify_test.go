package agentstore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestCloneAgentCopiesPointersNotBytes(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutAgent(ctx, GlobalTenant, Agent{AgentType: types.AgentType{
		Name:          "developer",
		Type:          "developer",
		ValidStages:   []types.Stage{"in_progress"},
		SkillsByStage: map[string][]string{"in_progress": {"worktree-protocol"}},
	}, IsTemplate: true}, "tester"); err != nil {
		t.Fatalf("put source: %v", err)
	}
	for _, p := range []string{"prompts/main.md", "prompts/in_progress.md", "auditor/default.md"} {
		if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "developer", p, []byte("body of "+p), "tester"); err != nil {
			t.Fatalf("put %s: %v", p, err)
		}
	}
	var blobsBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM content_blobs`).Scan(&blobsBefore); err != nil {
		t.Fatalf("count blobs: %v", err)
	}

	clone, err := s.CloneAgent(ctx, GlobalTenant, "developer", "acme-developer", "tester")
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	var blobsAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM content_blobs`).Scan(&blobsAfter); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if blobsAfter != blobsBefore {
		t.Fatalf("clone wrote %d new blob(s); it must copy pointers only", blobsAfter-blobsBefore)
	}

	files, err := s.ReadOwner(ctx, GlobalTenant, KindAgent, "acme-developer")
	if err != nil {
		t.Fatalf("read clone: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("clone has %d files, want 3", len(files))
	}
	for i := range files {
		if string(files[i].Body) != "body of "+files[i].Path {
			t.Fatalf("clone body at %s = %q", files[i].Path, files[i].Body)
		}
	}

	if clone.IsTemplate {
		t.Fatal("clone of a template is still marked a template; it would never run")
	}
	if clone.Type != "acme-developer" {
		t.Fatalf("clone type = %q, want acme-developer", clone.Type)
	}
	if clone.SkillsByStage["in_progress"][0] != "worktree-protocol" {
		t.Fatalf("grants lost in the clone: %v", clone.SkillsByStage)
	}

	if _, err := s.CloneAgent(ctx, GlobalTenant, "developer", "acme-developer", "tester"); err == nil {
		t.Fatal("clone overwrote an existing agent")
	}
}

func verifyTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("agents/refiner/docker-compose.yml",
		"services:\n  agent:\n    labels:\n"+
			"      thulu.agent: \"true\"\n"+
			"      thulu.stages: \"refinement\"\n"+
			"      thulu.skills.default.refinement: \"task-refinement\"\n"+
			"      thulu.mcp.default.refinement: \"ticket-reader\"\n")
	write("agents/refiner/prompts/main.md", "refine it")
	write("skills/_base/prompt.md", "shared rules")
	write("skills/task-refinement/prompt.md", "how to refine")
	write("tools/ticket-reader/skill.md", "ticket-reader is read-only")
	write("tools/ticket-reader/tool.yaml", "name: ticket-reader\n")
	return root
}

func TestVerifyPassesAfterAFaithfulImport(t *testing.T) {
	s, ctx := newStore(t)
	root := verifyTree(t)

	if _, err := s.ImportTree(ctx, GlobalTenant, root, "tester", false); err != nil {
		t.Fatalf("import: %v", err)
	}
	rep, err := s.VerifyAgainstTree(ctx, GlobalTenant, root)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("faithful import reported as a mismatch: %+v", rep)
	}
	if rep.Agents != 1 || rep.Checked != 1 {
		t.Fatalf("verify checked %d agent(s) / %d pair(s), want 1 and 1", rep.Agents, rep.Checked)
	}
}

func TestVerifyCatchesAlteredContent(t *testing.T) {
	s, ctx := newStore(t)
	root := verifyTree(t)

	if _, err := s.ImportTree(ctx, GlobalTenant, root, "tester", false); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "refiner",
		"prompts/main.md", []byte("refine i"), "tester"); err != nil {
		t.Fatalf("put: %v", err)
	}
	rep, err := s.VerifyAgainstTree(ctx, GlobalTenant, root)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK() {
		t.Fatal("verify passed on altered content; deleting the tree would lose it")
	}
	if len(rep.Mismatches) != 1 {
		t.Fatalf("mismatches = %v, want exactly one naming prompts/main.md", rep.Mismatches)
	}
}

func TestVerifyCatchesMissingContent(t *testing.T) {
	s, ctx := newStore(t)
	root := verifyTree(t)

	if _, err := s.ImportTree(ctx, GlobalTenant, root, "tester", false); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := s.DeleteOwner(ctx, GlobalTenant, KindSkill, "task-refinement"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rep, err := s.VerifyAgainstTree(ctx, GlobalTenant, root)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK() {
		t.Fatal("verify passed with a granted skill missing from the store")
	}
}

func TestVerifyReportsAgentsWithNoRow(t *testing.T) {
	s, ctx := newStore(t)
	root := verifyTree(t)

	rep, err := s.VerifyAgainstTree(ctx, GlobalTenant, root)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK() {
		t.Fatal("verify passed against an empty store")
	}
	if len(rep.MissingRows) != 1 {
		t.Fatalf("missing_rows = %v, want one entry for refiner", rep.MissingRows)
	}
}
