package builtin

import (
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The Thulu flow agents (workshop/agents/thulu-*) grant MCP tools by explicit list. This test
// keeps those lists honest: every tool they name exists in a shipped tool.yaml or is one the flow
// plan adds in a later wave, no agent gets pr_merge or deployment_approve, only the release agent
// gets run_dispatch, and octopus-api and thulu-github are never granted bare or as @write.

// Tools the flow plan names before their server ships them. Remove an entry once its tool.yaml
// lists it.
var plannedFlowTools = map[string][]string{
	"thulu-browser": {"browser_metrics", "browser_findings", "a11y_scan"},
	"octopus-api":   {"stage_tasks_list", "tasks_move_batch", "evidence_batch_run", "evidence_batch_get"},
}

// Scopes the plan adds to octopus-api for the release agent's tools.
var plannedFlowScopes = map[string][]string{"octopus-api": {"evidence", "release"}}

var (
	toolNameRe  = regexp.MustCompile(`(?m)^\s*- name: (\S+)`)
	toolScopeRe = regexp.MustCompile(`(?m)^\s*scope: (\S+)`)
)

type flowAgent struct {
	Definition struct {
		Name          string              `json:"name"`
		ValidStages   []string            `json:"valid_stages"`
		SkillsByStage map[string][]string `json:"skills_by_stage"`
		MCPByStage    map[string][]string `json:"mcp_by_stage"`
	} `json:"definition"`
}

type serverCatalog struct{ tools, scopes map[string]bool }

func nativeServerCatalogs(t *testing.T) map[string]serverCatalog {
	t.Helper()
	out := map[string]serverCatalog{}
	entries, err := fs.ReadDir(NativeTools(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := fs.ReadFile(NativeTools(), e.Name()+"/tool.yaml")
		if err != nil {
			continue
		}
		c := serverCatalog{tools: map[string]bool{}, scopes: map[string]bool{}}
		for _, m := range toolNameRe.FindAllStringSubmatch(string(body), -1) {
			c.tools[m[1]] = true
		}
		for _, m := range toolScopeRe.FindAllStringSubmatch(string(body), -1) {
			c.scopes[m[1]] = true
		}
		for _, name := range plannedFlowTools[e.Name()] {
			c.tools[name] = true
		}
		for _, sc := range plannedFlowScopes[e.Name()] {
			c.scopes[sc] = true
		}
		out[e.Name()] = c
	}
	return out
}

func flowAgents(t *testing.T) map[string]flowAgent {
	t.Helper()
	dirs, err := fs.Glob(Workshop(), "agents/thulu-*")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]flowAgent{}
	for _, dir := range dirs {
		body, err := fs.ReadFile(Workshop(), dir+"/agent.json")
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		var a flowAgent
		if err := json.Unmarshal(body, &a); err != nil {
			t.Fatalf("%s/agent.json: %v", dir, err)
		}
		out[dir] = a
	}
	if len(out) == 0 {
		t.Fatal("no thulu-* agents in the Workshop; the guard is not looking at anything")
	}
	return out
}

func TestFlowAgentGrantsNameRealTools(t *testing.T) {
	servers := nativeServerCatalogs(t)
	if !servers["octopus-api"].tools["task_get"] || !servers["thulu-github"].tools["run_dispatch"] ||
		!servers["thulu-github"].scopes["read"] {
		t.Fatal("tool.yaml parsing found no tools; the guard is not looking at anything")
	}
	for dir, a := range flowAgents(t) {
		for stage, grants := range a.Definition.MCPByStage {
			for _, g := range grants {
				server, list, scoped := strings.Cut(g, "@")
				c, ok := servers[server]
				if !ok {
					t.Errorf("%s %s: %q grants an unknown server", dir, stage, g)
					continue
				}
				if !scoped && (server == "octopus-api" || server == "thulu-github") {
					t.Errorf("%s %s: %q is a bare grant; name the scope and tools", dir, stage, g)
				}
				for _, name := range strings.Split(list, "+") {
					switch {
					case name == "":
					case server == "octopus-api" && name == "write":
						t.Errorf("%s %s: octopus-api@write brings task_delete and project_delete; list the tools", dir, stage)
					case c.scopes[name]:
					case !c.tools[name]:
						t.Errorf("%s %s: %s has no tool or scope %q", dir, stage, server, name)
					}
				}
			}
		}
	}
}

func TestFlowAgentsNeverMergeOrApproveDeploys(t *testing.T) {
	for dir, a := range flowAgents(t) {
		for stage, grants := range a.Definition.MCPByStage {
			for _, g := range grants {
				server, list, _ := strings.Cut(g, "@")
				if server != "thulu-github" {
					continue
				}
				tools := strings.Split(list, "+")
				for _, banned := range []string{"pr_merge", "deployment_approve", "deploy"} {
					if slices.Contains(tools, banned) {
						t.Errorf("%s %s: %q grants %s", dir, stage, g, banned)
					}
				}
				if slices.Contains(tools, "run_dispatch") && path.Base(dir) != "thulu-release" {
					t.Errorf("%s %s: only thulu-release dispatches workflows, got %q", dir, stage, g)
				}
			}
		}
	}
}

func TestFlowAgentsShipTheirPromptsAndSkills(t *testing.T) {
	for dir, a := range flowAgents(t) {
		if _, err := fs.Stat(Workshop(), dir+"/prompts/main.md"); err != nil {
			t.Errorf("%s: no prompts/main.md", dir)
		}
		for _, stage := range a.Definition.ValidStages {
			if _, err := fs.Stat(Workshop(), dir+"/prompts/"+stage+".md"); err != nil {
				t.Errorf("%s: valid stage %q has no prompts/%s.md", dir, stage, stage)
			}
			if _, ok := a.Definition.MCPByStage[stage]; !ok {
				t.Errorf("%s: valid stage %q has no mcp_by_stage entry", dir, stage)
			}
		}
		for stage, skills := range a.Definition.SkillsByStage {
			for _, sk := range skills {
				if _, err := fs.Stat(Workshop(), "skills/"+sk+"/prompt.md"); err != nil {
					t.Errorf("%s %s: skill %q is not in the Workshop", dir, stage, sk)
				}
			}
		}
	}
}

// Every agent a Workshop flow puts in a column with agent_verdict names where verdict.md goes and spells out
// its front matter. Without it the task blocks: on a real task the tester, whose prompts had the format
// but no place, wrote "verdict: kick_back" in its report instead.
func TestFlowVerdictAgentsSpellOutVerdictFile(t *testing.T) {
	var flow struct {
		Stages []struct {
			ID           string `json:"id"`
			DefaultAgent string `json:"default_agent"`
			AgentVerdict bool   `json:"agent_verdict"`
		} `json:"stages"`
		Types []struct {
			Agents map[string]string `json:"agents"`
		} `json:"task_types"`
	}
	files, err := fs.Glob(Workshop(), "flows/*/flow.json")
	if err != nil {
		t.Fatal(err)
	}
	pairs := map[[2]string]bool{}
	for _, f := range files {
		flow.Stages, flow.Types = nil, nil
		body, err := fs.ReadFile(Workshop(), f)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &flow); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		verdict := map[string]bool{}
		for _, st := range flow.Stages {
			verdict[st.ID] = st.AgentVerdict
			if st.AgentVerdict && st.DefaultAgent != "" {
				pairs[[2]string{st.DefaultAgent, st.ID}] = true
			}
		}
		for _, tt := range flow.Types {
			for stage, agent := range tt.Agents {
				if verdict[stage] {
					pairs[[2]string{agent, stage}] = true
				}
			}
		}
	}
	if len(pairs) < 5 {
		t.Fatalf("found %d agent_verdict columns with an agent; the guard is not looking at anything", len(pairs))
	}
	place := regexp.MustCompile("`verdict\\.md` in\\s+the\\s+output dir|`verdict\\.md` in the output\\s+dir")
	for pair := range pairs {
		agent, stage := pair[0], pair[1]
		var text strings.Builder
		for _, f := range []string{"main.md", stage + ".md"} {
			b, err := fs.ReadFile(Workshop(), "agents/"+agent+"/prompts/"+f)
			if err != nil {
				t.Fatalf("%s: %v", agent, err)
			}
			text.Write(b)
		}
		s := text.String()
		if !place.MatchString(s) {
			t.Errorf("%s in %s: the prompts never say verdict.md goes in the output dir", agent, stage)
		}
		if !strings.Contains(s, "---\nverdict: ") {
			t.Errorf("%s in %s: the prompts do not spell out verdict.md's front matter", agent, stage)
		}
		if !strings.Contains(s, "does not count") && !strings.Contains(s, "is not read") {
			t.Errorf("%s in %s: the prompts do not say a verdict outside verdict.md is ignored", agent, stage)
		}
	}
}

// Stage agents hand off through verdict.md and the report, which the next brief carries; comment.md
// is only a mention's or a continuation's reply. No shipped prompt or skill asks a stage run for it,
// and no auditor prompt sends its fixes through the discussion.
func TestNoStagePromptAsksForCommentMD(t *testing.T) {
	asks := regexp.MustCompile("- `comment\\.md`:|then `comment\\.md`|`comment\\.md` or `task_add_comment`|" +
		"auditor-comment\\.md` in the discussion|reads (it|them) in the discussion")
	for name, fsys := range map[string]fs.FS{"workshop": Workshop(), "system-skills": SystemSkills()} {
		err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || path.Ext(p) != ".md" {
				return err
			}
			body, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			if m := asks.FindString(string(body)); m != "" {
				t.Errorf("%s/%s asks for an end-of-run comment: %q", name, p, m)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Per-task column skips: the refiner writes them in flags.yaml and gets no task_skip_add (a second
// path would race the flags.yaml rewrite at the end of its run), and every later flow agent can
// remove one on the stage it works, since its prompt tells it to.
func TestFlowAgentsSkipGrants(t *testing.T) {
	agents := flowAgents(t)
	octopusTools := func(dir, stage string) []string {
		for _, g := range agents[dir].Definition.MCPByStage[stage] {
			if server, list, _ := strings.Cut(g, "@"); server == "octopus-api" {
				return strings.Split(list, "+")
			}
		}
		return nil
	}
	if tools := octopusTools("agents/thulu-refiner", "refinement"); len(tools) == 0 || slices.Contains(tools, "task_skip_add") {
		t.Errorf("thulu-refiner refinement grants %v: it needs octopus-api and writes skips through flags.yaml, not task_skip_add", tools)
	}
	later := map[string]string{
		"thulu-test-designer": "test_design", "thulu-developer": "in_progress", "thulu-verifier": "verification",
		"thulu-reviewer": "ai_review", "thulu-agentic-tester": "testing", "thulu-readiness": "readiness",
	}
	for name, stage := range later {
		dir := "agents/" + name
		if _, ok := agents[dir]; !ok {
			t.Errorf("%s is not in the Workshop", name)
			continue
		}
		if !slices.Contains(octopusTools(dir, stage), "task_skip_remove") {
			t.Errorf("%s %s: no task_skip_remove grant, but its prompt tells it to remove skips", name, stage)
		}
		body, err := fs.ReadFile(Workshop(), dir+"/prompts/"+stage+".md")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "`task_skip_remove`") {
			t.Errorf("%s %s: the prompt never says when to remove a skip", name, stage)
		}
		for g := range agents[dir].Definition.MCPByStage {
			if slices.Contains(octopusTools(dir, g), "task_skip_add") {
				t.Errorf("%s %s: only the refinement stage adds skips; task_skip_add is refused here", name, g)
			}
		}
	}
	body, err := fs.ReadFile(Workshop(), "agents/thulu-refiner/prompts/refinement.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "skip:") || !strings.Contains(string(body), "reason:") {
		t.Error("the refiner's prompt does not show the skip key of flags.yaml with a reason")
	}
}
