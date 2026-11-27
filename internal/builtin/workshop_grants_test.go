package builtin

import (
	"encoding/json"
	"io/fs"
	"path"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A Workshop agent's grants are strings the loader accepts whatever they name: a tool that does not
// exist, or one renamed later, silently grants nothing and the agent finds out mid-run. This test
// resolves every grant against the shipped tool.yaml files and every system_skills_off entry against
// the system skills.

type toolDescriptor struct {
	Tools []struct {
		Name  string `yaml:"name"`
		Scope string `yaml:"scope"`
	} `yaml:"tools"`
}

type workshopAgent struct {
	Definition struct {
		MCPByStage      map[string][]string `json:"mcp_by_stage"`
		SystemSkillsOff map[string][]string `json:"system_skills_off"`
	} `json:"definition"`
}

// Grant problems for one mcp_by_stage entry: "server", "server@scope" or "server@a+b+c".
func grantProblems(entry string, servers map[string]toolDescriptor) []string {
	name, list, scoped := strings.Cut(strings.TrimSpace(entry), "@")
	desc, ok := servers[name]
	if !ok {
		return []string{"unknown MCP server " + name}
	}
	if !scoped {
		return nil
	}
	tools := map[string]bool{}
	scopes := map[string]bool{}
	for _, t := range desc.Tools {
		tools[t.Name] = true
		if t.Scope != "" {
			scopes[t.Scope] = true
		}
	}
	var out []string
	for _, item := range strings.Split(list, "+") {
		switch {
		case item == "":
			out = append(out, "empty item in "+entry)
		case name == "octopus-api" && item == "write":
			// The bare write scope brings task_delete, agent_stop and project_delete.
			out = append(out, "octopus-api@write grants every write tool; name them instead")
		case tools[item], scopes[item]:
		default:
			out = append(out, name+" has no tool or scope "+item)
		}
	}
	return out
}

func nativeServers(t *testing.T) map[string]toolDescriptor {
	t.Helper()
	servers := map[string]toolDescriptor{}
	files, err := fs.Glob(NativeTools(), "*/tool.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		body, err := fs.ReadFile(NativeTools(), f)
		if err != nil {
			t.Fatal(err)
		}
		var d toolDescriptor
		if err := yaml.Unmarshal(body, &d); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		servers[path.Dir(f)] = d
	}
	return servers
}

func TestWorkshopGrantsNameRealTools(t *testing.T) {
	servers := nativeServers(t)
	files, err := fs.Glob(Workshop(), "agents/*/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Workshop agents found")
	}
	for _, f := range files {
		body, err := fs.ReadFile(Workshop(), f)
		if err != nil {
			t.Fatal(err)
		}
		var a workshopAgent
		if err := json.Unmarshal(body, &a); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for stage, entries := range a.Definition.MCPByStage {
			for _, e := range entries {
				for _, p := range grantProblems(e, servers) {
					t.Errorf("%s, stage %s: %s", f, stage, p)
				}
			}
		}
		for stage, skills := range a.Definition.SystemSkillsOff {
			for _, s := range skills {
				if _, err := fs.Stat(SystemSkills(), s+"/prompt.md"); err != nil {
					t.Errorf("%s, stage %s: system_skills_off names %s, which is not a system skill", f, stage, s)
				}
			}
		}
	}
}

func TestGrantProblemsCatchesBadGrants(t *testing.T) {
	servers := map[string]toolDescriptor{"octopus-api": {Tools: []struct {
		Name  string `yaml:"name"`
		Scope string `yaml:"scope"`
	}{{Name: "task_get", Scope: "read"}, {Name: "task_delete", Scope: "write"}}}}
	for entry, want := range map[string]int{
		"octopus-api":                   0,
		"octopus-api@read+task_get":     0,
		"octopus-api@read+task_gett":    1,
		"octopus-api@write":             1,
		"thulu-nowhere":                 1,
		"octopus-api@read++task_delete": 1,
	} {
		if got := len(grantProblems(entry, servers)); got != want {
			t.Errorf("%s: %d problems, want %d", entry, got, want)
		}
	}
}
