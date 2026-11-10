package agentstore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func starterPack(revision int, prompt string) Pack {
	return Pack{
		Version:  PackVersion,
		Revision: revision,
		Agents: []Agent{{AgentType: types.AgentType{
			Name: "helper", Type: "helper", ValidStages: []types.Stage{"in_progress"},
		}}},
		Content: []PackedFile{
			{Kind: KindAgent, Owner: "helper", Path: "prompts/main.md", Body: base64.StdEncoding.EncodeToString([]byte(prompt))},
			{Kind: KindSkill, Owner: "notes", Path: "prompt.md", Body: base64.StdEncoding.EncodeToString([]byte("notes"))},
		},
	}
}

func TestStarterPackSeedsEachWorkspaceOnce(t *testing.T) {
	s, ctx := newStore(t)
	if changed, err := s.SyncStarterPack(ctx, starterPack(1, "v1"), "test"); err != nil || !changed {
		t.Fatalf("sync starter: changed=%v err=%v", changed, err)
	}
	if changed, err := s.SyncStarterPack(ctx, starterPack(1, "v1 again"), "test"); err != nil || changed {
		t.Fatalf("same revision re-synced: changed=%v err=%v", changed, err)
	}

	if err := s.EnsureSeeded(ctx, "ws-a"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	f, err := s.ReadFile(ctx, "ws-a", KindAgent, "helper", "prompts/main.md")
	if err != nil || string(f.Body) != "v1" {
		t.Fatalf("seeded prompt = %q, %v", f.Body, err)
	}
	o, err := s.GetOrigin(ctx, "ws-a", KindAgent, "helper")
	if err != nil || o.Origin != OriginStarter || o.Revision != 1 {
		t.Fatalf("origin = %+v, %v", o, err)
	}
	revs, err := s.ListRevisions(ctx, "ws-a", KindAgent, "helper", "", 0)
	if err != nil || len(revs) != 1 || revs[0].Action != ActionSeeded {
		t.Fatalf("history of a seeded file = %+v, %v", revs, err)
	}

	if err := s.SetDisabled(ctx, "ws-a", "helper", true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := s.EnsureSeeded(ctx, "ws-a"); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	if a, err := s.GetAgent(ctx, "ws-a", "helper"); err != nil || !a.Disabled() {
		t.Fatalf("second seed brought the agent back: %+v, %v", a, err)
	}

	if changed, err := s.SyncStarterPack(ctx, starterPack(2, "v2"), "test"); err != nil || !changed {
		t.Fatalf("sync starter v2: changed=%v err=%v", changed, err)
	}
	if err := s.EnsureSeeded(ctx, "ws-b"); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	if f, _ := s.ReadFile(ctx, "ws-b", KindAgent, "helper", "prompts/main.md"); string(f.Body) != "v2" {
		t.Fatalf("new workspace got %q, want v2", f.Body)
	}
	if f, _ := s.ReadFile(ctx, "ws-a", KindAgent, "helper", "prompts/main.md"); string(f.Body) != "v1" {
		t.Fatalf("old workspace changed to %q", f.Body)
	}
}

func TestReservedTenantsAreNotSeeded(t *testing.T) {
	s, ctx := newStore(t)
	if _, err := s.SyncStarterPack(ctx, starterPack(1, "v1"), "test"); err != nil {
		t.Fatalf("sync starter: %v", err)
	}
	for _, tenant := range []string{GlobalTenant, SystemTenant, StarterTenant} {
		if err := s.EnsureSeeded(ctx, tenant); err != nil {
			t.Fatalf("seed %q: %v", tenant, err)
		}
	}
	if _, err := s.GetAgent(ctx, SystemTenant, "helper"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("system tenant got the starter agent: %v", err)
	}
}

func TestAdoptLegacyCopiesTheGlobalCatalogOnce(t *testing.T) {
	s, ctx := newStore(t)
	if _, err := s.PutAgent(ctx, GlobalTenant, Agent{AgentType: types.AgentType{
		Name: "developer", Type: "developer", ValidStages: []types.Stage{"in_progress"},
	}}, "test"); err != nil {
		t.Fatalf("put agent: %v", err)
	}
	for _, f := range []struct{ kind, owner, path, body string }{
		{KindAgent, "developer", "prompts/main.md", "legacy prompt"},
		{KindSkill, "worktree-protocol", "prompt.md", "never commit to main"},
		{KindTool, "thulu-github", "skill.md", "manual"},
	} {
		if _, err := s.PutFile(ctx, GlobalTenant, f.kind, f.owner, f.path, []byte(f.body), "test"); err != nil {
			t.Fatalf("put %s: %v", f.path, err)
		}
	}

	n, err := s.AdoptLegacy(ctx, []string{"ws-a", "ws-b"})
	if err != nil || n != 2 {
		t.Fatalf("adopt = %d, %v", n, err)
	}
	for _, ws := range []string{"ws-a", "ws-b"} {
		if _, err := s.GetAgent(ctx, ws, "developer"); err != nil {
			t.Fatalf("%s has no developer: %v", ws, err)
		}
		if f, err := s.ReadFile(ctx, ws, KindSkill, "worktree-protocol", "prompt.md"); err != nil || string(f.Body) != "never commit to main" {
			t.Fatalf("%s skill = %q, %v", ws, f.Body, err)
		}
		if _, err := s.ReadFile(ctx, ws, KindTool, "thulu-github", "skill.md"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s got a copy of an MCP manual: %v", ws, err)
		}
		if o, err := s.GetOrigin(ctx, ws, KindSkill, "worktree-protocol"); err != nil || o.Origin != OriginLegacy {
			t.Fatalf("%s skill origin = %+v, %v", ws, o, err)
		}
	}
	if _, err := s.GetAgent(ctx, GlobalTenant, "developer"); err != nil {
		t.Fatalf("adoption removed the original: %v", err)
	}

	if _, err := s.PutFile(ctx, "ws-a", KindAgent, "developer", "prompts/main.md", []byte("mine"), "test"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if n, err := s.AdoptLegacy(ctx, []string{"ws-a", "ws-c"}); err != nil || n != 0 {
		t.Fatalf("second adoption = %d, %v", n, err)
	}
	if f, _ := s.ReadFile(ctx, "ws-a", KindAgent, "developer", "prompts/main.md"); string(f.Body) != "mine" {
		t.Fatalf("second adoption overwrote the workspace's edit: %q", f.Body)
	}
	if _, err := s.GetAgent(ctx, "ws-c", "developer"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a later workspace got the legacy catalog: %v", err)
	}
}

func systemFS(version, prompt string) fstest.MapFS {
	return fstest.MapFS{
		"octopus-gh/prompt.md":  {Data: []byte(prompt)},
		"octopus-gh/skill.yaml": {Data: []byte("name: octopus-gh\nversion: " + version + "\nmcp: thulu-github\n")},
		"octopus-gh/ref/a.md":   {Data: []byte("reference")},
	}
}

func TestSystemSkillsSyncByVersion(t *testing.T) {
	s, ctx := newStore(t)
	written, err := s.SyncSystemSkills(ctx, systemFS("1", "v1"), "octopus")
	if err != nil || len(written) != 1 {
		t.Fatalf("first sync = %v, %v", written, err)
	}
	sk, err := s.GetSystemSkill(ctx, "octopus-gh")
	if err != nil || sk.Version != 1 || sk.MCP != "thulu-github" {
		t.Fatalf("stored = %+v, %v", sk, err)
	}

	if _, err := s.PutSystemSkill(ctx, "octopus-gh", map[string][]byte{
		"prompt.md":  []byte("hotfix"),
		"skill.yaml": []byte("name: octopus-gh\nmcp: thulu-github\n"),
	}, "operator"); err != nil {
		t.Fatalf("hotfix: %v", err)
	}
	if sk, _ := s.GetSystemSkill(ctx, "octopus-gh"); sk.Version != 1 {
		t.Fatalf("hotfix moved the version to %d", sk.Version)
	}
	if _, err := s.ReadFile(ctx, SystemTenant, KindSkill, "octopus-gh", "ref/a.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a full replace kept a file the hotfix dropped: %v", err)
	}

	if written, err := s.SyncSystemSkills(ctx, systemFS("1", "v1"), "octopus"); err != nil || len(written) != 0 {
		t.Fatalf("same-version sync = %v, %v", written, err)
	}
	if f, _ := s.ReadFile(ctx, SystemTenant, KindSkill, "octopus-gh", "prompt.md"); string(f.Body) != "hotfix" {
		t.Fatalf("restart replaced the hotfix with %q", f.Body)
	}

	if written, err := s.SyncSystemSkills(ctx, systemFS("2", "v2"), "octopus"); err != nil || len(written) != 1 {
		t.Fatalf("v2 sync = %v, %v", written, err)
	}
	if f, _ := s.ReadFile(ctx, SystemTenant, KindSkill, "octopus-gh", "prompt.md"); string(f.Body) != "v2" {
		t.Fatalf("v2 did not replace the hotfix: %q", f.Body)
	}

	if _, err := s.PutSystemSkillFile(ctx, "octopus-gh", SkillMetaFile,
		[]byte("name: octopus-gh\nversion: 2\nmcp: thulu-browser\n"), "operator"); err != nil {
		t.Fatalf("put skill.yaml: %v", err)
	}
	if sk, _ := s.GetSystemSkill(ctx, "octopus-gh"); sk.MCP != "thulu-browser" {
		t.Fatalf("mcp = %q after editing skill.yaml", sk.MCP)
	}

	if err := s.DeleteSystemSkill(ctx, "octopus-gh", "operator"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := s.ListSystemSkills(ctx); len(list) != 0 {
		t.Fatalf("deleted skill still listed: %+v", list)
	}
}

func TestSystemSkillNeedsPromptAndMeta(t *testing.T) {
	s, ctx := newStore(t)
	_, err := s.PutSystemSkill(ctx, "octopus-x", map[string][]byte{"prompt.md": []byte("x")}, "operator")
	if !errors.Is(err, ErrSkillIncomplete) {
		t.Fatalf("skill without skill.yaml: %v", err)
	}
}

func TestCatalogPackRoutesByKind(t *testing.T) {
	s, ctx := newStore(t)
	p := starterPack(0, "imported")
	p.Content = append(p.Content, PackedFile{Kind: KindTool, Owner: "thulu-github", Path: "skill.md",
		Body: base64.StdEncoding.EncodeToString([]byte("manual"))})
	if _, err := s.ImportCatalog(ctx, "ws-a", p, "test"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := s.GetAgent(ctx, "ws-a", "helper"); err != nil {
		t.Fatalf("agent not in the workspace: %v", err)
	}
	if _, err := s.ReadFile(ctx, GlobalTenant, KindTool, "thulu-github", "skill.md"); err != nil {
		t.Fatalf("manual not global: %v", err)
	}
	if _, err := s.ReadFile(ctx, "ws-a", KindTool, "thulu-github", "skill.md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("manual copied into the workspace: %v", err)
	}
	if _, err := s.ImportCatalog(ctx, SystemTenant, p, "test"); err == nil {
		t.Fatal("imported into the system tenant")
	}

	out, err := s.ExportCatalog(ctx, "ws-a")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	kinds := map[string]int{}
	for _, f := range out.Content {
		kinds[f.Kind]++
	}
	if kinds[KindAgent] != 1 || kinds[KindSkill] != 1 || kinds[KindTool] != 1 || len(out.Agents) != 1 {
		t.Fatalf("export = %d agents, files by kind %v", len(out.Agents), kinds)
	}
}

func TestEmbeddedCatalogSyncs(t *testing.T) {
	s, ctx := newStore(t)
	written, err := s.SyncSystemSkills(ctx, builtin.SystemSkills(), "octopus")
	if err != nil {
		t.Fatalf("sync embedded system skills: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("the binary embeds no system skills")
	}
	for _, name := range written {
		sk, err := s.GetSystemSkill(ctx, name)
		if err != nil || sk.Version < 1 {
			t.Fatalf("%s: version %d, %v (an embedded skill needs version >= 1 to ever reach a store)", name, sk.Version, err)
		}
		if !strings.HasPrefix(name, "octopus-") {
			t.Fatalf("%s: system skill names start with octopus-, the prefix workspaces cannot use", name)
		}
	}
	var p Pack
	if err := json.Unmarshal(builtin.StarterPack(), &p); err != nil {
		t.Fatalf("decode starter pack: %v", err)
	}
	if _, err := s.SyncStarterPack(ctx, p, "octopus"); err != nil {
		t.Fatalf("sync starter pack: %v", err)
	}
}
