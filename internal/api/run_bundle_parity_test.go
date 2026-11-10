package api

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestBundleFromStoreMatchesBundleFromFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	write("agents/developer/docker-compose.yml",
		"services:\n  agent:\n    labels:\n"+
			"      thulu.agent: \"true\"\n"+
			"      thulu.stages: \"in_progress\"\n"+
			"      thulu.skills.default.in_progress: \"worktree-protocol\"\n"+
			"      thulu.mcp.default.in_progress: \"thulu-github@write\"\n")
	write("agents/developer/prompts/main.md", "you are the developer")
	write("agents/developer/prompts/in_progress.md", "implement the task")
	write("agents/developer/auditor/in_progress.md", "audit the work")
	write("skills/_base/prompt.md", "shared rules")
	write("skills/_base/skill.yaml", "name: _base\nenforcement: always")
	write("skills/worktree-protocol/prompt.md", "never commit to main")
	write("skills/worktree-protocol/skill.yaml", "name: worktree-protocol")
	write("skills/acme-secrets/prompt.md", "CLIENT ACME INTERNALS")
	write("tools/thulu-github/skill.md", "how to use github")

	discovered, err := types.Discover(root)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("discovered %d agents, want 1", len(discovered))
	}
	at := discovered[0]

	fromFiles := buildBundle(t, &Server{
		agentCatalog: types.NewCatalog(root),
		agentsDir:    root,
	}, at)

	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	if _, err := store.ImportTree(ctx, agentstore.GlobalTenant, root, "test", false); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := store.AdoptLegacy(ctx, []string{bundleWorkspace}); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	srv := &Server{agentCatalog: types.NewCatalog(root), agentsDir: root}
	srv.SetAgentStore(store)
	fromStore := buildBundle(t, srv, at)

	if !bytes.Equal(fromFiles, fromStore) {
		t.Fatalf("bundle changed across the migration:\n  files (%d bytes): %v\n  store (%d bytes): %v",
			len(fromFiles), keysOf(entries(t, fromFiles)),
			len(fromStore), keysOf(entries(t, fromStore)))
	}

	got := entries(t, fromStore)
	for name, body := range got {
		if bytes.Contains([]byte(body), []byte("CLIENT ACME INTERNALS")) {
			t.Fatalf("an ungranted skill reached the bundle at %s", name)
		}
	}
	for _, want := range []string{
		"prompts/main.md", "prompts/in_progress.md", "auditor/in_progress.md",
		"skills/_base/prompt.md", "skills/_base/skill.yaml",
		"skills/worktree-protocol/prompt.md", "tools/thulu-github/skill.md",
	} {
		if _, ok := got[want]; !ok {
			t.Fatalf("%s missing from the store-built bundle; got %v", want, keysOf(got))
		}
	}
}

const bundleWorkspace = "ws-bundle"

func buildBundle(t *testing.T, srv *Server, at types.AgentType) []byte {
	t.Helper()
	var buf bytes.Buffer
	ctx := context.Background()
	if err := writeRunBundle(ctx, &buf, srv.content(ctx, bundleWorkspace), at, "in_progress"); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	return buf.Bytes()
}

func TestBundleCarriesSystemSkillsWhereTheirMCPIsWired(t *testing.T) {
	t.Parallel()
	store, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()
	if _, err := store.PutSystemSkill(ctx, "octopus-gh", map[string][]byte{
		"prompt.md":  []byte("system github rules"),
		"skill.yaml": []byte("name: octopus-gh\nversion: 1\nmcp: thulu-github\n"),
	}, "test"); err != nil {
		t.Fatalf("put system skill: %v", err)
	}
	if _, err := store.PutFile(ctx, bundleWorkspace, agentstore.KindAgent, "developer",
		"prompts/main.md", []byte("you are the developer"), "test"); err != nil {
		t.Fatalf("put prompt: %v", err)
	}
	srv := &Server{}
	srv.SetAgentStore(store)
	at := types.AgentType{
		Name:         "developer",
		ValidStages:  []types.Stage{"in_progress", "review"},
		MCPByStage:   map[string][]string{"in_progress": {"thulu-github@write"}},
		SystemSkills: []types.SystemSkillRef{{Name: "octopus-gh", MCP: "thulu-github"}},
	}
	got := entries(t, buildBundle(t, srv, at))
	if got["skills/octopus-gh/prompt.md"] != "system github rules" {
		t.Fatalf("system skill missing from a stage that wires its MCP; got %v", keysOf(got))
	}
	var buf bytes.Buffer
	if err := writeRunBundle(ctx, &buf, srv.content(ctx, bundleWorkspace), at, "review"); err != nil {
		t.Fatalf("write review bundle: %v", err)
	}
	if _, ok := entries(t, buf.Bytes())["skills/octopus-gh/prompt.md"]; ok {
		t.Fatal("system skill reached a stage without its MCP")
	}
}
