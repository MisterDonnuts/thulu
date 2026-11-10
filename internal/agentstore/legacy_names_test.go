package agentstore

import (
	"encoding/base64"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestRenameLegacy(t *testing.T) {
	for in, want := range map[string]string{
		"argus-github@write+tool":             "thulu-github@write+tool",
		"mcp__argus-sandbox__exec":            "mcp__thulu-sandbox__exec",
		"argus.mcp.default.review":            "thulu.mcp.default.review",
		"/workspace/projects/p/.argus/x":      "/workspace/projects/p/.thulu/x",
		"OCTOPUS_MCP_ARGUS_CLOUD_IMAGE":       "OCTOPUS_MCP_CLOUD_IMAGE",
		"MCP_ARGUS_FIGMA_URL":                 "MCP_THULU_FIGMA_URL",
		"ghcr.io/x/octopus-mcp-argus-browser": "ghcr.io/x/octopus-mcp-browser",
		"argus-notes":                         "argus-notes",
		"argus.":                              "argus.",
		"thulu-github and .thulu stay":        "thulu-github and .thulu stay",
	} {
		if got := string(RenameLegacy([]byte(in))); got != want {
			t.Errorf("RenameLegacy(%q) = %q, want %q", in, got, want)
		}
	}
	bin := []byte{0xff, 'a', 'r', 'g', 'u', 's', '-', 'c', 'l', 'o', 'u', 'd'}
	if got := RenameLegacy(bin); string(got) != string(bin) {
		t.Errorf("non-UTF-8 bytes changed: %q", got)
	}
}

func TestImportCatalogRenamesPackFromBefore1Point0(t *testing.T) {
	s, ctx := newStore(t)
	pack := Pack{
		Version: PackVersion,
		Agents: []Agent{{AgentType: types.AgentType{Name: "dev", Type: "dev",
			MCPByStage: map[string][]string{"review": {"argus-github@write"}}}}},
		Content: []PackedFile{
			{Kind: KindAgent, Owner: "dev", Path: "prompts/main.md", Body: base64.StdEncoding.EncodeToString([]byte("use mcp__argus-github__pr_view"))},
			{Kind: KindTool, Owner: "argus-github", Path: "skill.md", Body: base64.StdEncoding.EncodeToString([]byte("manual"))},
		},
	}
	if _, err := s.ImportCatalog(ctx, "ws1", pack, "t"); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetAgent(ctx, "ws1", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.MCPByStage["review"]; len(got) != 1 || got[0] != "thulu-github@write" {
		t.Fatalf("mcp_by_stage = %v", got)
	}
	f, err := s.ReadFile(ctx, "ws1", KindAgent, "dev", "prompts/main.md")
	if err != nil || string(f.Body) != "use mcp__thulu-github__pr_view" {
		t.Fatalf("prompt = %q, %v", f.Body, err)
	}
	if _, err := s.ReadFile(ctx, GlobalTenant, KindTool, "thulu-github", "skill.md"); err != nil {
		t.Fatalf("tool manual: %v", err)
	}
	if pack.Content[1].Owner != "argus-github" || pack.Agents[0].MCPByStage["review"][0] != "argus-github@write" {
		t.Fatal("the caller's pack was modified")
	}
}
