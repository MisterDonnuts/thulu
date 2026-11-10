package workshop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/builtin"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const tenant = "ws-test"

func newState(t *testing.T) (StoreState, context.Context) {
	t.Helper()
	s, err := agentstore.New(testpg.New(t))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return StoreState{Store: s, Tenant: tenant}, context.Background()
}

func catalogFS(agentRev, skillRev int, skillPrompt string) fstest.MapFS {
	agent := map[string]any{
		"revision": agentRev,
		"tags":     []string{"docs"},
		"definition": map[string]any{
			"name":            "writer",
			"description":     "writes",
			"valid_stages":    []string{"in_progress"},
			"skills_by_stage": map[string][]string{"in_progress": {"style"}},
		},
	}
	raw, _ := json.Marshal(agent)
	return fstest.MapFS{
		"agents/writer/agent.json":      {Data: raw},
		"agents/writer/prompts/main.md": {Data: []byte("write it")},
		"skills/style/skill.yaml": {Data: []byte(
			"name: style\ndescription: how\nversion: " + itoa(skillRev) + "\nrequires: [tone]\n")},
		"skills/style/prompt.md": {Data: []byte(skillPrompt)},
		"skills/tone/skill.yaml": {Data: []byte("name: tone\nversion: 1\n")},
		"skills/tone/prompt.md":  {Data: []byte("be plain")},
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func mustCatalog(t *testing.T, fsys fstest.MapFS) *Catalog {
	t.Helper()
	c, err := LoadCatalog(fsys)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return c
}

func plan(t *testing.T, ctx context.Context, st StoreState, set *Set, source string, choices map[string]Resolution) *Plan {
	t.Helper()
	p, err := MakePlan(ctx, st, set, source, Choices{Resolutions: choices}, Env{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return p
}

func apply(t *testing.T, ctx context.Context, st StoreState, set *Set, p *Plan) Result {
	t.Helper()
	res, err := Apply(ctx, st.Store, st.Tenant, set, p, "tester")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return res
}

func itemOf(t *testing.T, p *Plan, kind, name string) Item {
	t.Helper()
	it, ok := p.item(kind, name)
	if !ok {
		t.Fatalf("plan has no %s/%s: %+v", kind, name, p.Items)
	}
	return it
}

func TestEmbeddedCatalogLoads(t *testing.T) {
	c, err := LoadCatalog(builtin.Workshop())
	if err != nil {
		t.Fatalf("the embedded Workshop catalog is broken: %v", err)
	}
	for _, a := range c.Agents() {
		sel, err := c.Select([]Ref{{Kind: KindAgent, Name: a.Def.Name}})
		if err != nil {
			t.Fatalf("select %s: %v", a.Def.Name, err)
		}
		for _, ref := range SkillRefs(a.Def) {
			if _, ok := sel.Skill(ref); !ok && !strings.HasPrefix(ref, systemSkillPrefix) {
				t.Errorf("agent %s uses skill %s, which the catalog does not ship", a.Def.Name, ref)
			}
		}
		if a.Revision < 1 {
			t.Errorf("agent %s has no revision", a.Def.Name)
		}
	}
	for _, sk := range c.Skills() {
		if sk.Revision < 1 {
			t.Errorf("skill %s has no version in skill.yaml", sk.Name)
		}
		if strings.HasPrefix(sk.Name, systemSkillPrefix) {
			t.Errorf("skill %s uses the reserved prefix", sk.Name)
		}
	}
}

func TestInstallBringsSkillsAndIsIdempotent(t *testing.T) {
	st, ctx := newState(t)
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, err := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Skills) != 2 {
		t.Fatalf("selecting the agent should bring style and the tone it requires, got %d skills", len(set.Skills))
	}
	p := plan(t, ctx, st, set, SourceWorkshop, nil)
	if p.Writes != 3 {
		t.Fatalf("writes = %d, want 3", p.Writes)
	}
	res := apply(t, ctx, st, set, p)
	if len(res.Installed) != 3 {
		t.Fatalf("installed = %+v", res.Installed)
	}

	again := plan(t, ctx, st, set, SourceWorkshop, nil)
	for _, it := range again.Items {
		if it.Action != ActionSame {
			t.Errorf("%s: action %s after a clean install, want same", it.Key(), it.Action)
		}
	}
	entries, err := List(ctx, st, c, "en")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Status != StatusInstalled {
			t.Errorf("%s/%s: status %s, want installed", e.Kind, e.Name, e.Status)
		}
	}
}

func TestUntouchedInstallUpdates(t *testing.T) {
	st, ctx := newState(t)
	v1 := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := v1.Select([]Ref{{Kind: KindSkill, Name: "style"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, nil))

	v2 := mustCatalog(t, catalogFS(1, 2, "v2"))
	set2, _ := v2.Select([]Ref{{Kind: KindSkill, Name: "style"}})
	p := plan(t, ctx, st, set2, SourceWorkshop, nil)
	it := itemOf(t, p, KindSkill, "style")
	if it.Action != ActionUpdate || it.Resolution != ResolveUpdate || it.Installed != 1 {
		t.Fatalf("style = %+v, want an update from revision 1", it)
	}
	apply(t, ctx, st, set2, p)
	got, err := st.Store.ReadFile(ctx, tenant, agentstore.KindSkill, "style", "prompt.md")
	if err != nil || string(got.Body) != "v2" {
		t.Fatalf("after update prompt = %q, %v", got.Body, err)
	}
}

func TestEditedInstallIsAConflictThatKeepsByDefault(t *testing.T) {
	st, ctx := newState(t)
	v1 := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := v1.Select([]Ref{{Kind: KindSkill, Name: "style"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, nil))

	// Origin and edit timestamps can share a microsecond on a fast machine; the edit must land
	// after the install.
	time.Sleep(5 * time.Millisecond)
	if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindSkill, "style", "prompt.md", []byte("mine"), "person"); err != nil {
		t.Fatal(err)
	}

	v2 := mustCatalog(t, catalogFS(1, 2, "v2"))
	set2, _ := v2.Select([]Ref{{Kind: KindSkill, Name: "style"}})
	p := plan(t, ctx, st, set2, SourceWorkshop, nil)
	it := itemOf(t, p, KindSkill, "style")
	if it.Action != ActionConflict || it.Resolution != ResolveKeep || !it.Edited {
		t.Fatalf("style = %+v, want a conflict kept by default", it)
	}
	apply(t, ctx, st, set2, p)
	got, _ := st.Store.ReadFile(ctx, tenant, agentstore.KindSkill, "style", "prompt.md")
	if string(got.Body) != "mine" {
		t.Fatalf("the default overwrote the person's edit: %q", got.Body)
	}

	p = plan(t, ctx, st, set2, SourceWorkshop, map[string]Resolution{"skill/style": ResolveReplace})
	apply(t, ctx, st, set2, p)
	got, _ = st.Store.ReadFile(ctx, tenant, agentstore.KindSkill, "style", "prompt.md")
	if string(got.Body) != "v2" {
		t.Fatalf("replace left %q", got.Body)
	}
	revs, err := st.Store.ListRevisions(ctx, tenant, agentstore.KindSkill, "style", "prompt.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) < 3 {
		t.Fatalf("history has %d revisions, want install, edit and replace", len(revs))
	}
}

func TestCopyRenamesReferences(t *testing.T) {
	st, ctx := newState(t)
	for _, name := range []string{"style", "tone"} {
		for path, body := range map[string]string{"skill.yaml": "name: " + name + "\n", "prompt.md": "own " + name} {
			if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindSkill, name, path, []byte(body), "person"); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	p := plan(t, ctx, st, set, SourceWorkshop, map[string]Resolution{
		"skill/style": ResolveCopy, "skill/tone": ResolveCopy,
	})
	if it := itemOf(t, p, KindSkill, "style"); it.Target != "style-2" {
		t.Fatalf("copy target = %q", it.Target)
	}
	res := apply(t, ctx, st, set, p)
	if len(res.Copied) != 2 {
		t.Fatalf("copied = %+v", res.Copied)
	}

	a, err := st.Store.GetAgent(ctx, tenant, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.SkillsByStage["in_progress"]; len(got) != 1 || got[0] != "style-2" {
		t.Fatalf("the agent still points at %v, want the copy", got)
	}
	meta, _ := st.Store.ReadFile(ctx, tenant, agentstore.KindSkill, "style-2", "skill.yaml")
	if !strings.Contains(string(meta.Body), "name: style-2") || !strings.Contains(string(meta.Body), "tone-2") {
		t.Fatalf("copied skill.yaml = %q", meta.Body)
	}
	own, _ := st.Store.ReadFile(ctx, tenant, agentstore.KindSkill, "style", "prompt.md")
	if string(own.Body) != "own style" {
		t.Fatalf("the copy touched the workspace's own skill: %q", own.Body)
	}
	if _, found, _ := st.Origin(ctx, KindSkill, "style-2"); found {
		t.Fatal("a copy is the workspace's own and carries no origin")
	}
}

func TestReservedAndEnvNotices(t *testing.T) {
	st, ctx := newState(t)
	b := Bundle{Format: Format, Version: FormatVersion,
		Agents: []Agent{{Definition: types.AgentType{
			Name:          "bot",
			Image:         "example/bot:1",
			SkillsByStage: map[string][]string{"review": {"nowhere"}},
			MCPByStage:    map[string][]string{"review": {"ghost-server@write"}},
		}}},
		Skills: []Skill{{Name: "octopus-fake", Files: []File{
			{Path: "skill.yaml", Body: "name: octopus-fake\n"}, {Path: "prompt.md", Body: "x"},
		}}},
	}
	set, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	p, err := MakePlan(ctx, st, set, SourceImport, Choices{}, Env{
		KnownMCP:   map[string]bool{"octopus-api": true},
		StageKnown: func(id string) bool { return id == "in_progress" },
		SaaS:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, n := range p.Notices {
		codes[n.Code] = true
	}
	for _, want := range []string{NoticeReserved, NoticeMissingSkill, NoticeMissingMCP, NoticeUnknownStage, NoticeImageImport} {
		if !codes[want] {
			t.Errorf("missing notice %s in %+v", want, p.Notices)
		}
	}
	if p.Blocked {
		t.Fatalf("plan blocked: %+v", p.Notices)
	}
	if it := itemOf(t, p, KindSkill, "octopus-fake"); it.Resolution != ResolveSkip {
		t.Fatalf("reserved skill = %+v, want it left out", it)
	}
	res := apply(t, ctx, st, set, p)
	if len(res.Installed) != 1 || len(res.Skipped) != 1 {
		t.Fatalf("result = %+v", res)
	}
	a, err := st.Store.GetAgent(ctx, tenant, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if a.Image != "" {
		t.Fatalf("an imported agent kept its image %q", a.Image)
	}
	if have, _ := st.Files(ctx, agentstore.KindSkill, "octopus-fake"); len(have) != 0 {
		t.Fatal("the reserved skill was written")
	}
}

func TestReservedSkillInstallsUnderAnotherName(t *testing.T) {
	st, ctx := newState(t)
	set, err := Decode(Bundle{Format: Format, Version: FormatVersion,
		Agents: []Agent{{Definition: types.AgentType{
			Name: "bot", SkillsByStage: map[string][]string{"in_progress": {"octopus-fake"}},
		}}},
		Skills: []Skill{{Name: "octopus-fake", Files: []File{
			{Path: "skill.yaml", Body: "name: octopus-fake\n"}, {Path: "prompt.md", Body: "x"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := MakePlan(ctx, st, set, SourceImport, Choices{
		Resolutions: map[string]Resolution{"skill/octopus-fake": ResolveCopy},
	}, Env{})
	if err != nil {
		t.Fatal(err)
	}
	if it := itemOf(t, p, KindSkill, "octopus-fake"); it.Target != "fake" {
		t.Fatalf("reserved copy target = %q, want the name without the prefix", it.Target)
	}

	if _, err := MakePlan(ctx, st, set, SourceImport, Choices{
		Resolutions: map[string]Resolution{"skill/octopus-fake": ResolveCopy},
		Names:       map[string]string{"skill/octopus-fake": "octopus-other"},
	}, Env{}); err == nil {
		t.Fatal("a copy took a reserved name")
	}
	p, err = MakePlan(ctx, st, set, SourceImport, Choices{
		Resolutions: map[string]Resolution{"skill/octopus-fake": ResolveCopy},
		Names:       map[string]string{"skill/octopus-fake": "team-notes"},
	}, Env{})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, ctx, st, set, p)
	a, err := st.Store.GetAgent(ctx, tenant, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.SkillsByStage["in_progress"]; len(got) != 1 || got[0] != "team-notes" {
		t.Fatalf("the agent points at %v, want the renamed skill", got)
	}
}

func TestSecretLeavesOnlyThatItemOut(t *testing.T) {
	st, ctx := newState(t)
	raw, _ := json.Marshal(Bundle{Format: Format, Version: FormatVersion,
		Agents: []Agent{
			{Definition: types.AgentType{Name: "leaky"}, Files: []File{{Path: "prompts/main.md", Body: "AKIA" + "IOSFODNN7EXAMPLE"}}},
			{Definition: types.AgentType{Name: "clean"}, Files: []File{{Path: "prompts/main.md", Body: "hi"}}},
		}})
	set, err := Parse(raw)
	if err != nil {
		t.Fatalf("a secret in one item failed the whole file: %v", err)
	}
	if len(set.Agents) != 1 || len(set.Rejected) != 1 || set.Rejected[0].Path != "prompts/main.md" {
		t.Fatalf("set = %+v", set)
	}
	p := plan(t, ctx, st, set, SourceImport, nil)
	if it := itemOf(t, p, KindAgent, "leaky"); it.Action != ActionRejected || writes(it.Resolution) {
		t.Fatalf("leaky = %+v", it)
	}
	res := apply(t, ctx, st, set, p)
	if len(res.Installed) != 1 || res.Installed[0].Name != "clean" {
		t.Fatalf("result = %+v", res)
	}
	if _, ok, _ := st.Agent(ctx, "leaky"); ok {
		t.Fatal("the item with a secret was written")
	}
}

func TestSaaSCatalogImageBlocksUntilLeftOut(t *testing.T) {
	st, ctx := newState(t)
	set := &Set{Agents: []AgentItem{{Def: types.AgentType{Name: "bot", Type: "bot", Image: "example/bot:1"}, Revision: 1}}}
	p, err := MakePlan(ctx, st, set, SourceWorkshop, Choices{}, Env{SaaS: true})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Blocked {
		t.Fatal("a catalog agent with an image installed in SaaS")
	}
	p, err = MakePlan(ctx, st, set, SourceWorkshop, Choices{
		Resolutions: map[string]Resolution{"agent/bot": ResolveSkip},
	}, Env{SaaS: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Blocked || p.Writes != 0 {
		t.Fatalf("leaving the agent out still blocks: %+v", p)
	}
}

func TestParseRejects(t *testing.T) {
	skill := func(name string, files ...File) []byte {
		raw, _ := json.Marshal(Bundle{Format: Format, Version: FormatVersion, Skills: []Skill{{Name: name, Files: files}}})
		return raw
	}
	meta := func(name string) File { return File{Path: "skill.yaml", Body: "name: " + name + "\n"} }
	prompt := File{Path: "prompt.md", Body: "x"}
	cases := map[string][]byte{
		"not json":       []byte("{"),
		"foreign json":   []byte(`{"hello": 1}`),
		"future version": []byte(`{"format":"octopus-workshop","version":9}`),
		"traversal":      skill("a", meta("a"), prompt, File{Path: "../x", Body: "x"}),
		"absolute":       skill("a", meta("a"), prompt, File{Path: "/etc/x", Body: "x"}),
		"no prompt":      skill("a", meta("a")),
		"name mismatch":  skill("a", meta("b"), prompt),
		"bad name":       skill("A B", meta("A B"), prompt),
		"bad base64":     skill("a", meta("a"), prompt, File{Path: "x.bin", Body: "!!", Encoding: "base64"}),
	}
	for name, raw := range cases {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestExportRoundTrip(t *testing.T) {
	st, ctx := newState(t)
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, nil))

	out, err := ExportAgent(ctx, st, "writer")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Encode(out, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"dir"`) {
		t.Fatal("an export carries the definition's dir")
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse own export: %v", err)
	}
	if len(back.Agents) != 1 || len(back.Skills) != 2 {
		t.Fatalf("export carries %d agents and %d skills, want 1 and 2", len(back.Agents), len(back.Skills))
	}
	p := plan(t, ctx, st, back, SourceImport, nil)
	for _, it := range p.Items {
		if it.Action != ActionSame {
			t.Errorf("%s: %s, want same", it.Key(), it.Action)
		}
	}
}

func TestLegacyPackSkipsTools(t *testing.T) {
	pack := agentstore.Pack{
		Version: agentstore.PackVersion,
		Agents:  []agentstore.Agent{{AgentType: types.AgentType{Name: "dev", Type: "dev"}}},
		Content: []agentstore.PackedFile{
			{Kind: agentstore.KindAgent, Owner: "dev", Path: "prompts/main.md", Body: b64("hi")},
			{Kind: agentstore.KindTool, Owner: "legacy-x", Path: "skill.md", Body: b64("manual")},
		},
	}
	raw, _ := json.Marshal(pack)
	set, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if set.SkippedTools != 1 || len(set.Agents) != 1 {
		t.Fatalf("set = %+v", set)
	}
	if string(set.Agents[0].Files["prompts/main.md"]) != "hi" {
		t.Fatal("pack body not decoded")
	}
}

func TestBundleFromBefore1Point0GetsCurrentNames(t *testing.T) {
	raw, _ := json.Marshal(Bundle{Format: Format, Version: FormatVersion, Agents: []Agent{{
		Definition: types.AgentType{Name: "dev", MCPByStage: map[string][]string{"review": {"argus-github@write+pr_merge", "argus-notes"}}},
		Files: []File{
			{Path: "prompts/main.md", Body: "call mcp__argus-browser__evidence_publish"},
			{Path: "prompts/extra.md", Body: b64("read .argus/notes and argus.mcp.default.review"), Encoding: encodingBase64},
		},
	}}})
	set, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	a := set.Agents[0]
	if got := a.Def.MCPByStage["review"]; got[0] != "thulu-github@write+pr_merge" || got[1] != "argus-notes" {
		t.Fatalf("mcp_by_stage = %v", got)
	}
	if got := string(a.Files["prompts/main.md"]); got != "call mcp__thulu-browser__evidence_publish" {
		t.Fatalf("plain file = %q", got)
	}
	if got := string(a.Files["prompts/extra.md"]); got != "read .thulu/notes and thulu.mcp.default.review" {
		t.Fatalf("base64 file = %q", got)
	}
}

func TestDisabledAgentIsAvailableAndReinstallEnables(t *testing.T) {
	st, ctx := newState(t)
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, nil))
	if err := st.Store.SetDisabled(ctx, tenant, "writer", true); err != nil {
		t.Fatal(err)
	}

	entries, err := List(ctx, st, c, "en")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == KindAgent && (e.Status != StatusAvailable || !e.Disabled) {
			t.Fatalf("deleted agent listed as %+v, want available", e)
		}
	}
	p := plan(t, ctx, st, set, SourceWorkshop, nil)
	if it := itemOf(t, p, KindAgent, "writer"); it.Action != ActionInstall || !it.Disabled {
		t.Fatalf("writer = %+v", it)
	}
	apply(t, ctx, st, set, p)
	a, err := st.Store.GetAgent(ctx, tenant, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if a.Disabled() {
		t.Fatal("reinstalling left the agent disabled")
	}
}

func TestReplaceKeepsTheDefinitionInHistory(t *testing.T) {
	st, ctx := newState(t)
	mine := types.AgentType{Name: "writer", Type: "writer", Model: "opus", ValidStages: []types.Stage{"review"}}
	if _, err := st.Store.PutAgent(ctx, tenant, agentstore.Agent{AgentType: mine}, "person"); err != nil {
		t.Fatal(err)
	}
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, map[string]Resolution{"agent/writer": ResolveReplace}))

	revs, err := st.Store.ListDefRevisions(ctx, tenant, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 || revs[0].Reason != agentstore.DefReasonWorkshop || revs[0].Definition.Model != "opus" {
		t.Fatalf("history = %+v, want the replaced definition", revs)
	}
	back, err := st.Store.RestoreDefRevision(ctx, tenant, "writer", revs[0].ID, "person")
	if err != nil {
		t.Fatal(err)
	}
	if back.Model != "opus" || len(back.ValidStages) != 1 || back.ValidStages[0] != "review" {
		t.Fatalf("restored = %+v", back.AgentType)
	}
	revs, _ = st.Store.ListDefRevisions(ctx, tenant, "writer")
	if len(revs) != 2 || revs[0].Reason != agentstore.DefReasonRestore {
		t.Fatalf("history after restore = %+v", revs)
	}
}

func TestAgentWithSkillCopyStillUpdates(t *testing.T) {
	st, ctx := newState(t)
	for path, body := range map[string]string{"skill.yaml": "name: style\n", "prompt.md": "own style"} {
		if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindSkill, "style", path, []byte(body), "person"); err != nil {
			t.Fatal(err)
		}
	}
	v1 := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := v1.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, map[string]Resolution{"skill/style": ResolveCopy}))

	again := plan(t, ctx, st, set, SourceWorkshop, nil)
	if it := itemOf(t, again, KindAgent, "writer"); it.Action != ActionSame {
		t.Fatalf("writer after a copy install = %+v, want same", it)
	}
	v2 := mustCatalog(t, catalogFS(2, 1, "v1"))
	set2, _ := v2.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	set2.Agents[0].Def.Description = "writes better"
	p := plan(t, ctx, st, set2, SourceWorkshop, nil)
	if it := itemOf(t, p, KindAgent, "writer"); it.Action != ActionUpdate {
		t.Fatalf("writer = %+v, want an update", it)
	}
	apply(t, ctx, st, set2, p)
	a, _ := st.Store.GetAgent(ctx, tenant, "writer")
	if got := a.SkillsByStage["in_progress"]; len(got) != 1 || got[0] != "style-2" {
		t.Fatalf("the update dropped the copy: %v", got)
	}
}

func TestKeptSkillWarns(t *testing.T) {
	st, ctx := newState(t)
	for path, body := range map[string]string{"skill.yaml": "name: style\n", "prompt.md": "own style"} {
		if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindSkill, "style", path, []byte(body), "person"); err != nil {
			t.Fatal(err)
		}
	}
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	p := plan(t, ctx, st, set, SourceWorkshop, nil)
	found := false
	for _, n := range p.Notices {
		if n.Code == NoticeKeptSkill && n.Name == "writer" && len(n.Refs) == 1 && n.Refs[0] == "style" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no kept_skill notice in %+v", p.Notices)
	}
	p = plan(t, ctx, st, set, SourceWorkshop, map[string]Resolution{"skill/style": ResolveCopy})
	for _, n := range p.Notices {
		if n.Code == NoticeKeptSkill {
			t.Fatalf("kept_skill with the skill copied: %+v", n)
		}
	}
}

func TestUntouchedImportUpdates(t *testing.T) {
	st, ctx := newState(t)
	bundle := func(prompt string) *Set {
		set, err := Decode(Bundle{Format: Format, Version: FormatVersion,
			Agents: []Agent{{Definition: types.AgentType{Name: "bot"}, Files: []File{{Path: "prompts/main.md", Body: prompt}}}}})
		if err != nil {
			t.Fatal(err)
		}
		return set
	}
	first := bundle("v1")
	apply(t, ctx, st, first, plan(t, ctx, st, first, SourceImport, nil))

	second := bundle("v2")
	p := plan(t, ctx, st, second, SourceImport, nil)
	if it := itemOf(t, p, KindAgent, "bot"); it.Action != ActionUpdate || it.Resolution != ResolveUpdate {
		t.Fatalf("bot = %+v, want an update of an untouched import", it)
	}

	if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindAgent, "bot", "prompts/main.md", []byte("mine"), "person"); err != nil {
		t.Fatal(err)
	}
	p = plan(t, ctx, st, second, SourceImport, nil)
	if it := itemOf(t, p, KindAgent, "bot"); it.Action != ActionConflict || !it.Edited {
		t.Fatalf("bot = %+v, want an edited conflict", it)
	}
}

func TestReimportWithImageIsSame(t *testing.T) {
	st, ctx := newState(t)
	set, err := Decode(Bundle{Format: Format, Version: FormatVersion,
		Agents: []Agent{{Definition: types.AgentType{Name: "bot", Image: "example/bot:1"}, Files: []File{{Path: "prompts/main.md", Body: "v1"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceImport, nil))

	p := plan(t, ctx, st, set, SourceImport, nil)
	if it := itemOf(t, p, KindAgent, "bot"); it.Action != ActionSame || it.Resolution != ResolveKeep {
		t.Fatalf("bot = %+v, want the same agent on a second import of the same file", it)
	}
}

func TestExportOverByteLimit(t *testing.T) {
	st, ctx := newState(t)
	if _, err := st.Store.PutAgent(ctx, tenant, agentstore.Agent{AgentType: types.AgentType{Name: "big", Type: "big"}}, "person"); err != nil {
		t.Fatal(err)
	}
	body := []byte(strings.Repeat("a", 900<<10))
	for i := range 4 {
		if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindAgent, "big", "prompts/p"+itoa(i)+".md", body, "person"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Export(ctx, st, []Ref{{Kind: KindAgent, Name: "big"}})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Key != "workshop.errors.bundle_too_large" {
		t.Fatalf("export of %d bytes: err = %v, want bundle_too_large", 4*len(body), err)
	}
}

func TestExportSeveral(t *testing.T) {
	st, ctx := newState(t)
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, nil))
	if _, err := st.Store.PutAgent(ctx, tenant, agentstore.Agent{AgentType: types.AgentType{
		Name: "other", Type: "other", SkillsByStage: map[string][]string{"review": {"style"}},
	}}, "person"); err != nil {
		t.Fatal(err)
	}
	out, err := Export(ctx, st, []Ref{{Kind: KindAgent, Name: "writer"}, {Kind: KindAgent, Name: "other"}, {Kind: KindSkill, Name: "tone"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Agents) != 2 || len(out.Skills) != 2 {
		t.Fatalf("export carries %d agents and %d skills, want 2 and 2 (each skill once)", len(out.Agents), len(out.Skills))
	}
}

func TestCatalogDescriptionByLocale(t *testing.T) {
	st, ctx := newState(t)
	fsys := catalogFS(1, 1, "v1")
	fsys["skills/tone/skill.yaml"] = &fstest.MapFile{Data: []byte("name: tone\nversion: 1\ndescription: be plain\ndescriptions:\n  es: escribí simple\n")}
	c := mustCatalog(t, fsys)
	for locale, want := range map[string]string{"es": "escribí simple", "en": "be plain", "fr": "be plain"} {
		entries, err := List(ctx, st, c, locale)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name == "tone" && e.Description != want {
				t.Errorf("%s: description %q, want %q", locale, e.Description, want)
			}
		}
	}
}

func TestEmbeddedCatalogSpeaksSpanish(t *testing.T) {
	c, err := LoadCatalog(builtin.Workshop())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Agents() {
		if a.Descriptions["es"] == "" {
			t.Errorf("agent %s has no Spanish description", a.Def.Name)
		}
	}
	for _, sk := range c.Skills() {
		if sk.Meta.Descriptions["es"] == "" {
			t.Errorf("skill %s has no Spanish description", sk.Name)
		}
	}
}

func TestInstallWritesTheDescriptionTheListShowed(t *testing.T) {
	st, ctx := newState(t)
	fsys := catalogFS(1, 1, "v1")
	raw, _ := json.Marshal(map[string]any{
		"revision":     1,
		"descriptions": map[string]string{"es": "escribe"},
		"definition": map[string]any{
			"name":            "writer",
			"description":     "writes",
			"valid_stages":    []string{"in_progress"},
			"skills_by_stage": map[string][]string{"in_progress": {"style"}},
		},
	})
	fsys["agents/writer/agent.json"] = &fstest.MapFile{Data: raw}
	c := mustCatalog(t, fsys)
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	planIn := func(locale string) *Plan {
		t.Helper()
		p, err := MakePlan(ctx, st, set, SourceWorkshop, Choices{}, Env{Locale: locale})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		return p
	}
	apply(t, ctx, st, set, planIn("es"))
	a, err := st.Store.GetAgent(ctx, tenant, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if a.Description != "escribe" {
		t.Fatalf("description = %q, want the Spanish one", a.Description)
	}
	for _, locale := range []string{"es", "en"} {
		if it := itemOf(t, planIn(locale), KindAgent, "writer"); it.Action != ActionSame || it.Edited {
			t.Errorf("%s: writer = %+v, want same and untouched", locale, it)
		}
	}
}

func TestSkillInstallsInTheReadersLanguage(t *testing.T) {
	st, ctx := newState(t)
	fsys := catalogFS(1, 1, "v1")
	styleMeta := func(rev int, es string) []byte {
		return []byte("name: style\ndescription: how\ndescriptions:\n  es: " + es +
			"\nversion: " + itoa(rev) + "\nrequires: [tone]\n")
	}
	fsys["skills/style/skill.yaml"] = &fstest.MapFile{Data: styleMeta(1, "cómo")}
	c := mustCatalog(t, fsys)
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	planIn := func(set *Set, locale string, choices map[string]Resolution) *Plan {
		t.Helper()
		p, err := MakePlan(ctx, st, set, SourceWorkshop, Choices{Resolutions: choices}, Env{Locale: locale})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		return p
	}
	descOf := func(name string) SkillMeta {
		t.Helper()
		have, err := st.Files(ctx, agentstore.KindSkill, name)
		if err != nil {
			t.Fatal(err)
		}
		var m SkillMeta
		if err := yaml.Unmarshal(have[agentstore.SkillMetaFile], &m); err != nil {
			t.Fatalf("%s skill.yaml: %v", name, err)
		}
		return m
	}

	apply(t, ctx, st, set, planIn(set, "es", nil))
	if m := descOf("style"); m.Description != "cómo" || m.Descriptions["es"] != "cómo" || m.Name != "style" {
		t.Fatalf("style installed as %+v, want the Spanish description and its translations", m)
	}
	if m := descOf("tone"); m.Description != "" || m.Name != "tone" {
		t.Fatalf("tone, with no translation, installed as %+v, want it as the catalog has it", m)
	}
	for _, locale := range []string{"es", "en"} {
		if it := itemOf(t, planIn(set, locale, nil), KindSkill, "style"); it.Action != ActionSame || it.Edited {
			t.Errorf("%s: style = %+v, want same and untouched", locale, it)
		}
	}

	fsys2 := catalogFS(1, 2, "v2")
	fsys2["skills/style/skill.yaml"] = &fstest.MapFile{Data: styleMeta(2, "cómo, mejor")}
	set2, _ := mustCatalog(t, fsys2).Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	if it := itemOf(t, planIn(set2, "en", nil), KindSkill, "style"); it.Action != ActionUpdate || it.Edited {
		t.Fatalf("style at revision 2 = %+v, want an untouched update", it)
	}

	st2, _ := newState(t)
	st = st2
	for path, body := range map[string]string{"skill.yaml": "name: style\n", "prompt.md": "own style"} {
		if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindSkill, "style", path, []byte(body), "person"); err != nil {
			t.Fatal(err)
		}
	}
	apply(t, ctx, st, set, planIn(set, "es", map[string]Resolution{"skill/style": ResolveCopy}))
	if m := descOf("style-2"); m.Description != "cómo" || m.Name != "style-2" {
		t.Fatalf("style-2 installed as %+v, want the Spanish description", m)
	}
	for _, locale := range []string{"es", "en"} {
		it := itemOf(t, planIn(set, locale, nil), KindSkill, "style")
		if !it.AsCopy || it.Target != "style-2" || it.Action != ActionSame {
			t.Errorf("%s: style = %+v, want the same as style-2", locale, it)
		}
	}
}

func TestReinstallingADeletedAgentKeepsItsSkillCopy(t *testing.T) {
	st, ctx := newState(t)
	for path, body := range map[string]string{"skill.yaml": "name: style\n", "prompt.md": "own style"} {
		if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindSkill, "style", path, []byte(body), "person"); err != nil {
			t.Fatal(err)
		}
	}
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, map[string]Resolution{"skill/style": ResolveCopy}))
	if err := st.Store.SetDisabled(ctx, tenant, "writer", true); err != nil {
		t.Fatal(err)
	}

	p := plan(t, ctx, st, set, SourceWorkshop, nil)
	if it := itemOf(t, p, KindAgent, "writer"); it.SkillRenames["style"] != "style-2" {
		t.Fatalf("writer = %+v, want it to keep style-2", it)
	}
	for _, n := range p.Notices {
		if n.Code == NoticeKeptSkill {
			t.Fatalf("plan says the agent takes the workspace's own skill: %+v", n)
		}
	}
	apply(t, ctx, st, set, p)
	a, _ := st.Store.GetAgent(ctx, tenant, "writer")
	if got := a.SkillsByStage["in_progress"]; len(got) != 1 || got[0] != "style-2" {
		t.Fatalf("reinstalled writer points at %v, want style-2", got)
	}
}

func TestReinstalledAgentsSkillIsItsCopy(t *testing.T) {
	st, ctx := newState(t)
	for path, body := range map[string]string{"skill.yaml": "name: style\n", "prompt.md": "own style"} {
		if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindSkill, "style", path, []byte(body), "person"); err != nil {
			t.Fatal(err)
		}
	}
	c := mustCatalog(t, catalogFS(1, 1, "v1"))
	set, _ := c.Select([]Ref{{Kind: KindAgent, Name: "writer"}})
	apply(t, ctx, st, set, plan(t, ctx, st, set, SourceWorkshop, map[string]Resolution{"skill/style": ResolveCopy}))

	it := itemOf(t, plan(t, ctx, st, set, SourceWorkshop, nil), KindSkill, "style")
	if !it.AsCopy || it.Target != "style-2" || it.Action != ActionSame {
		t.Fatalf("style with writer installed = %+v, want the same as style-2", it)
	}

	if err := st.Store.SetDisabled(ctx, tenant, "writer", true); err != nil {
		t.Fatal(err)
	}
	p := plan(t, ctx, st, set, SourceWorkshop, map[string]Resolution{"skill/style": ResolveCopy})
	it = itemOf(t, p, KindSkill, "style")
	if !it.AsCopy || it.Target != "style-2" || it.Action != ActionSame || it.Resolution != ResolveKeep {
		t.Fatalf("style on reinstall = %+v, want kept as style-2", it)
	}
	if len(it.Options) != 1 || len(it.UsedBy) != 1 || it.UsedBy[0] != "writer" {
		t.Fatalf("style options %v used by %v, want only keep, by writer", it.Options, it.UsedBy)
	}
	apply(t, ctx, st, set, p)
	if have, _ := st.Files(ctx, agentstore.KindSkill, "style-3"); len(have) > 0 {
		t.Fatal("the reinstall made a second copy, style-3")
	}
	a, _ := st.Store.GetAgent(ctx, tenant, "writer")
	if got := a.SkillsByStage["in_progress"]; len(got) != 1 || got[0] != "style-2" {
		t.Fatalf("reinstalled writer points at %v, want style-2", got)
	}

	if _, err := st.Store.PutFile(ctx, tenant, agentstore.KindSkill, "style-2", "prompt.md", []byte("my take"), "person"); err != nil {
		t.Fatal(err)
	}
	p = plan(t, ctx, st, set, SourceWorkshop, map[string]Resolution{"skill/style": ResolveReplace})
	it = itemOf(t, p, KindSkill, "style")
	if it.Action != ActionConflict || it.Resolution != ResolveReplace || contains(optionNames(it), "copy") {
		t.Fatalf("edited copy = %+v, want a conflict with keep and replace", it)
	}
	apply(t, ctx, st, set, p)
	body, _ := st.Store.ReadFile(ctx, tenant, agentstore.KindSkill, "style-2", "prompt.md")
	meta, _ := st.Store.ReadFile(ctx, tenant, agentstore.KindSkill, "style-2", "skill.yaml")
	if string(body.Body) != "v1" || !strings.Contains(string(meta.Body), "name: style-2") {
		t.Fatalf("replaced copy = %q / %q", body.Body, meta.Body)
	}
	own, _ := st.Store.ReadFile(ctx, tenant, agentstore.KindSkill, "style", "prompt.md")
	if string(own.Body) != "own style" {
		t.Fatalf("replacing the copy touched the workspace's own skill: %q", own.Body)
	}
	if _, found, _ := st.Origin(ctx, KindSkill, "style-2"); found {
		t.Fatal("a replaced copy is still the workspace's own and carries no origin")
	}
	if again := itemOf(t, plan(t, ctx, st, set, SourceWorkshop, nil), KindSkill, "style"); again.Action != ActionSame {
		t.Fatalf("style after replacing the copy = %+v, want the same", again)
	}
}

func optionNames(it Item) []string {
	out := make([]string, len(it.Options))
	for i, o := range it.Options {
		out[i] = string(o)
	}
	return out
}

func TestImportedAgentDropsAReservedSkillNobodyProvides(t *testing.T) {
	st, ctx := newState(t)
	set, err := Decode(Bundle{Format: Format, Version: FormatVersion,
		Agents: []Agent{{Definition: types.AgentType{
			Name: "qa-bot",
			SkillsByStage: map[string][]string{
				"in_progress": {"octopus-checklist", "notes"},
				"review":      {"octopus-checklist"},
			},
		}}},
		Skills: []Skill{
			{Name: "octopus-checklist", Files: []File{
				{Path: "skill.yaml", Body: "name: octopus-checklist\n"}, {Path: "prompt.md", Body: "x"},
			}},
			{Name: "notes", Files: []File{
				{Path: "skill.yaml", Body: "name: notes\n"}, {Path: "prompt.md", Body: "y"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := plan(t, ctx, st, set, SourceImport, nil)
	apply(t, ctx, st, set, p)
	a, err := st.Store.GetAgent(ctx, tenant, "qa-bot")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.SkillsByStage["in_progress"]; len(got) != 1 || got[0] != "notes" {
		t.Fatalf("in_progress = %v, want only notes", got)
	}
	if _, ok := a.SkillsByStage["review"]; ok {
		t.Fatalf("review kept a stage with no skills: %v", a.SkillsByStage)
	}
	if it := itemOf(t, plan(t, ctx, st, set, SourceImport, nil), KindAgent, "qa-bot"); it.Action != ActionSame {
		t.Fatalf("re-import = %+v, want same", it)
	}
}
