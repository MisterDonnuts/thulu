package agentstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func newStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	db := testpg.New(t)
	s, err := New(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s, context.Background()
}

func TestPutAndReadFile(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "developer",
		"prompts/main.md", []byte("be careful"), "tester"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.ReadFile(ctx, GlobalTenant, KindAgent, "developer", "prompts/main.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got.Body) != "be careful" {
		t.Fatalf("body = %q", got.Body)
	}
	if got.SHA == "" || got.Size != len("be careful") {
		t.Fatalf("content address not populated: %+v", got)
	}
}

func TestIdenticalBodiesShareOneBlob(t *testing.T) {
	s, ctx := newStore(t)

	body := []byte("the same rules for everyone")
	for _, owner := range []string{"developer", "reviewer", "tester"} {
		if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, owner,
			"prompts/main.md", body, "tester"); err != nil {
			t.Fatalf("put %s: %v", owner, err)
		}
	}
	var blobs int
	if err := s.db.QueryRow(`SELECT count(*) FROM content_blobs`).Scan(&blobs); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	if blobs != 1 {
		t.Fatalf("blobs = %d, want 1 (identical bodies must dedupe)", blobs)
	}
	var files int
	if err := s.db.QueryRow(`SELECT count(*) FROM content_files`).Scan(&files); err != nil {
		t.Fatalf("count files: %v", err)
	}
	if files != 3 {
		t.Fatalf("files = %d, want 3", files)
	}
}

func TestReadOwnerIsOrderedByPath(t *testing.T) {
	s, ctx := newStore(t)

	for _, p := range []string{"prompts/mention.md", "auditor/default.md", "prompts/main.md"} {
		if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "developer", p, []byte(p), "tester"); err != nil {
			t.Fatalf("put %s: %v", p, err)
		}
	}
	files, err := s.ReadOwner(ctx, GlobalTenant, KindAgent, "developer")
	if err != nil {
		t.Fatalf("read owner: %v", err)
	}
	want := []string{"auditor/default.md", "prompts/main.md", "prompts/mention.md"}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d", len(files), len(want))
	}
	for i := range want {
		if files[i].Path != want[i] {
			t.Fatalf("file %d = %q, want %q", i, files[i].Path, want[i])
		}
	}
}

func TestPutFileRejectsSecrets(t *testing.T) {
	s, ctx := newStore(t)

	for _, body := range []string{
		"use sk-ant-api03-AAAABBBBCCCCDDDDEEEEFFFF to authenticate",
		"token: ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"-----BEGIN RSA PRIVATE KEY-----",
	} {
		if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "leaky",
			"prompt.md", []byte(body), "tester"); err == nil {
			t.Fatalf("stored a body containing a credential: %q", body)
		}
	}
}

func TestPathValidationRejectsTraversal(t *testing.T) {
	s, ctx := newStore(t)

	for _, p := range []string{"../escape.md", "prompts/../../etc/passwd", "/abs.md", ".hidden", `back\slash`} {
		if _, err := s.PutFile(ctx, GlobalTenant, KindAgent, "developer",
			p, []byte("x"), "tester"); err == nil {
			t.Fatalf("accepted unsafe path %q", p)
		}
	}
}

func TestReservedNamesAreRejected(t *testing.T) {
	if err := ValidateName("octopus"); err == nil {
		t.Fatal("octopus must not be usable as an agent name: it is the data dir")
	}
	if err := ValidateName("_base"); err != nil {
		t.Fatalf("_base must be a valid skill name: %v", err)
	}
}

func TestAgentMayNotTakeASignerName(t *testing.T) {
	for _, name := range []string{"user", "system", "auditor", "automation", "octopus"} {
		if err := ValidateAgentName(name); !errors.Is(err, ErrReservedOwner) {
			t.Errorf("ValidateAgentName(%q) = %v, want ErrReservedOwner", name, err)
		}
	}
	if err := ValidateAgentName("developer"); err != nil {
		t.Errorf("developer must be a valid agent name: %v", err)
	}
	s, ctx := newStore(t)
	if _, err := s.PutAgent(ctx, "", Agent{AgentType: types.AgentType{Name: "user"}}, "test"); !errors.Is(err, ErrReservedOwner) {
		t.Errorf("PutAgent(user) = %v, want ErrReservedOwner", err)
	}
}

func TestAgentRoundTrip(t *testing.T) {
	s, ctx := newStore(t)

	in := Agent{AgentType: types.AgentType{
		Name:             "developer",
		Description:      "writes code",
		ValidStages:      []types.Stage{"in_progress", "chat"},
		Engine:           "claude-code",
		SupportedEngines: []string{"claude-code"},
		Model:            "sonnet",
		SkillsMode:       types.SkillsModeNative,
		WorksWithoutRepo: false,
		TurnsByStage:     map[string]string{"in_progress": "40"},
		SkillsByStage:    map[string][]string{"in_progress": {"worktree-protocol", "pr-authoring"}},
		MCPByStage:       map[string][]string{"in_progress": {"thulu-github@write"}},
		Auditor: &types.AuditorSpec{
			Stages:       []string{"in_progress"},
			ModelByStage: map[string]string{"in_progress": "haiku"},
		},
	}}
	if _, err := s.PutAgent(ctx, GlobalTenant, in, "tester"); err != nil {
		t.Fatalf("put agent: %v", err)
	}
	got, err := s.GetAgent(ctx, GlobalTenant, "developer")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.Description != in.Description || got.Engine != in.Engine || got.Model != in.Model {
		t.Fatalf("scalars lost: %+v", got)
	}
	if len(got.ValidStages) != 2 || got.ValidStages[0] != "in_progress" {
		t.Fatalf("valid stages = %v", got.ValidStages)
	}
	if got.SkillsByStage["in_progress"][1] != "pr-authoring" {
		t.Fatalf("skills lost: %v", got.SkillsByStage)
	}
	if got.MCPByStage["in_progress"][0] != "thulu-github@write" {
		t.Fatalf("mcp grant lost (the @scope suffix is load-bearing): %v", got.MCPByStage)
	}
	if got.Auditor == nil || got.Auditor.ModelByStage["in_progress"] != "haiku" {
		t.Fatalf("auditor lost: %+v", got.Auditor)
	}
}

func TestDisabledAgentLeavesTheCatalogButKeepsItsRow(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutAgent(ctx, GlobalTenant,
		Agent{AgentType: types.AgentType{Name: "refiner"}}, "tester"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.SetDisabled(ctx, GlobalTenant, "refiner", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	list, err := s.ListAgents(ctx, GlobalTenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("disabled agent still listed: %+v", list)
	}
	got, err := s.GetAgent(ctx, GlobalTenant, "refiner")
	if err != nil {
		t.Fatalf("row must survive a disable: %v", err)
	}
	if !got.Disabled() {
		t.Fatal("row is not marked disabled")
	}
}

func TestGCKeepsBlobsTheHistoryStillReferences(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "kept", "prompt.md", []byte("keep"), "t"); err != nil {
		t.Fatalf("put kept: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "dropped", "prompt.md", []byte("drop"), "t"); err != nil {
		t.Fatalf("put dropped: %v", err)
	}
	if _, err := s.DeleteOwner(ctx, GlobalTenant, KindSkill, "dropped"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	n, err := s.GCBlobs(ctx)
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if n != 0 {
		t.Fatalf("gc took %d blob(s) the history still references", n)
	}

	pruned, freed, err := s.Vacuum(ctx, GlobalTenant, 0)
	if err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	if pruned == 0 {
		t.Fatal("vacuum pruned no revisions")
	}
	if freed != 1 {
		t.Fatalf("vacuum freed %d blob(s), want 1", freed)
	}
	if _, err := s.ReadFile(ctx, GlobalTenant, KindSkill, "kept", "prompt.md"); err != nil {
		t.Fatalf("vacuum took a referenced blob: %v", err)
	}
}

func TestImportTreeMovesDefinitionsAndText(t *testing.T) {
	s, ctx := newStore(t)
	root := t.TempDir()

	writeFile(t, filepath.Join(root, "agents", "developer", "docker-compose.yml"),
		"services:\n  agent:\n    labels:\n"+
			"      thulu.agent: \"true\"\n"+
			"      thulu.stages: \"in_progress\"\n"+
			"      thulu.skills.default.in_progress: \"worktree-protocol\"\n"+
			"      thulu.description: \"writes code\"\n")
	writeFile(t, filepath.Join(root, "agents", "developer", "prompts", "main.md"), "identity")
	writeFile(t, filepath.Join(root, "agents", "developer", "auditor", "default.md"), "audit")
	writeFile(t, filepath.Join(root, "agents", "developer", "runs", "old", "session.log"), "noise")
	writeFile(t, filepath.Join(root, "agents", "octopus", "data", "x.db"), "data")
	writeFile(t, filepath.Join(root, "skills", "worktree-protocol", "prompt.md"), "never commit to main")
	writeFile(t, filepath.Join(root, "skills", "worktree-protocol", "skill.yaml"), "name: worktree-protocol")
	writeFile(t, filepath.Join(root, "tools", "thulu-github", "skill.md"), "how to use github")

	stats, err := s.ImportTree(ctx, GlobalTenant, root, "tester", false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stats.Agents != 1 {
		t.Fatalf("imported %d agents, want 1 (octopus/ must be skipped): %+v", stats.Agents, stats)
	}
	if stats.Skills != 1 || stats.Tools != 1 {
		t.Fatalf("skills=%d tools=%d, want 1 and 1", stats.Skills, stats.Tools)
	}
	files, err := s.ReadOwner(ctx, GlobalTenant, KindAgent, "developer")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	want := []string{"auditor/default.md", "prompts/main.md"}
	if len(paths) != len(want) {
		t.Fatalf("agent files = %v, want %v (runs/ must not travel)", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("agent files = %v, want %v", paths, want)
		}
	}
	agent, err := s.GetAgent(ctx, GlobalTenant, "developer")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if agent.Description != "writes code" {
		t.Fatalf("labels not parsed: %+v", agent)
	}
	if agent.Dir != "" {
		t.Fatalf("Dir must not be stored: it pins the agent to a path that is about to be deleted (%q)", agent.Dir)
	}
}

func TestImportTreeIsIdempotent(t *testing.T) {
	s, ctx := newStore(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "agents", "refiner", "docker-compose.yml"),
		"services:\n  agent:\n    labels:\n      thulu.agent: \"true\"\n      thulu.stages: \"refinement\"\n")
	writeFile(t, filepath.Join(root, "agents", "refiner", "prompts", "main.md"), "refine it")

	first, err := s.ImportTree(ctx, GlobalTenant, root, "tester", false)
	if err != nil {
		t.Fatalf("import 1: %v", err)
	}
	second, err := s.ImportTree(ctx, GlobalTenant, root, "tester", false)
	if err != nil {
		t.Fatalf("import 2: %v", err)
	}
	if first.Agents != second.Agents || first.Files != second.Files {
		t.Fatalf("import is not idempotent: %+v then %+v", first, second)
	}
	var files int
	if err := s.db.QueryRow(`SELECT count(*) FROM content_files`).Scan(&files); err != nil {
		t.Fatalf("count: %v", err)
	}
	if files != 1 {
		t.Fatalf("content_files = %d after two imports, want 1", files)
	}
}

func TestPackRoundTrip(t *testing.T) {
	s, ctx := newStore(t)

	if _, err := s.PutAgent(ctx, GlobalTenant, Agent{AgentType: types.AgentType{
		Name:          "developer",
		ValidStages:   []types.Stage{"in_progress"},
		SkillsByStage: map[string][]string{"in_progress": {"worktree-protocol"}},
	}}, "tester"); err != nil {
		t.Fatalf("put agent: %v", err)
	}
	if _, err := s.PutFile(ctx, GlobalTenant, KindSkill, "worktree-protocol",
		"prompt.md", []byte("never commit to main"), "tester"); err != nil {
		t.Fatalf("put file: %v", err)
	}

	pack, err := s.ExportPack(ctx, GlobalTenant)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	other, otherCtx := newStore(t)
	if _, err := other.ImportPack(otherCtx, GlobalTenant, pack, "bootstrap"); err != nil {
		t.Fatalf("import pack: %v", err)
	}
	got, err := other.ReadFile(otherCtx, GlobalTenant, KindSkill, "worktree-protocol", "prompt.md")
	if err != nil {
		t.Fatalf("read after import: %v", err)
	}
	if string(got.Body) != "never commit to main" {
		t.Fatalf("body = %q", got.Body)
	}
	agent, err := other.GetAgent(otherCtx, GlobalTenant, "developer")
	if err != nil {
		t.Fatalf("agent after import: %v", err)
	}
	if agent.SkillsByStage["in_progress"][0] != "worktree-protocol" {
		t.Fatalf("grants lost through the pack: %v", agent.SkillsByStage)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
