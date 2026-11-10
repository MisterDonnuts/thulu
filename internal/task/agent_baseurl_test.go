package task

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestSandboxMCPURL(t *testing.T) {
	got := sandboxMCPURL("http://octopus.octopus.svc:8585", "abc123", types.StageInProgress)
	want := "http://octopus.octopus.svc:8585/sandbox/sse?task_id=abc123&stage=in_progress"
	if got != want {
		t.Errorf("sandboxMCPURL = %q, want %q", got, want)
	}
}

func TestBuildStageEnv_BaseURLPropagates(t *testing.T) {
	tk := &Task{WorkspaceID: testWS, ID: "abc123", Stage: types.StageInProgress}
	at := &types.AgentType{Name: "dev", ValidStages: []types.Stage{types.StageInProgress}}

	docker := buildStageEnv(tk, tk.Stage, "dev", "dev-task-abc123-in_progress-1", defaultAgentBaseURL, at, "")
	if docker["OCTOPUS_API"] != defaultAgentBaseURL {
		t.Errorf("OCTOPUS_API = %q, want %q", docker["OCTOPUS_API"], defaultAgentBaseURL)
	}
	if docker["MCP_THULU_SANDBOX_URL"] != sandboxMCPURL(defaultAgentBaseURL, "abc123", types.StageInProgress) {
		t.Errorf("MCP_THULU_SANDBOX_URL = %q", docker["MCP_THULU_SANDBOX_URL"])
	}

	const svc = "http://octopus.octopus-system.svc.cluster.local:8585"
	k8s := buildStageEnv(tk, tk.Stage, "dev", "dev-task-abc123-in_progress-1", svc, at, "")
	if k8s["OCTOPUS_API"] != svc {
		t.Errorf("OCTOPUS_API = %q, want %q", k8s["OCTOPUS_API"], svc)
	}
	if want := svc + "/sandbox/sse?task_id=abc123&stage=in_progress"; k8s["MCP_THULU_SANDBOX_URL"] != want {
		t.Errorf("MCP_THULU_SANDBOX_URL = %q, want %q", k8s["MCP_THULU_SANDBOX_URL"], want)
	}
}

func TestBuildMentionEnv_ExactVarSetNoAuditorLeak(t *testing.T) {
	tk := &Task{WorkspaceID: testWS,
		ID:         "abc123",
		Stage:      types.StageInProgress,
		ProjectIDs: []string{"p1"},
		BranchName: "task-abc123",
	}
	at := &types.AgentType{
		Name:          "dev",
		Model:         "opus",
		Effort:        "high",
		SkillsByStage: map[string][]string{string(types.StageMention): {"s1"}},
		MCPByStage:    map[string][]string{string(types.StageMention): {"m1"}},
		Auditor: &types.AuditorSpec{
			Stages:        []string{string(types.StageMention), string(types.StageInProgress)},
			TurnsByStage:  map[string]string{string(types.StageMention): "5"},
			ModelByStage:  map[string]string{string(types.StageMention): "haiku"},
			SkillsByStage: map[string][]string{string(types.StageMention): {"audit"}},
		},
	}
	c := &Comment{ID: "c1", Body: "hola @dev"}
	env := buildMentionEnv(tk, "dev", "dev-mention-abc123-c1-1", defaultAgentBaseURL, "es", at, c)

	want := []string{
		"TASK_ID", "OCTOPUS_API", "AGENT_NAME", "INSTANCE_ID", "PROJECT_NAME",
		"STAGE", "MCP_THULU_SANDBOX_URL", "MENTION_COMMENT_ID", "MENTION_COMMENT_BODY", "THREAD_ID",
		"PROJECT_ID", "MAX_TURNS", "SKILLS", "SKILLS_MODE", "MCP_TOOLS",
		"BRANCH_NAME", "MODEL", "EFFORT", "ENGINE", "WORKSPACE_ID", "OCTOPUS_LOCALE",
	}
	wantSet := map[string]bool{}
	for _, k := range want {
		wantSet[k] = true
		if _, ok := env[k]; !ok {
			t.Errorf("missing env var %s", k)
		}
	}
	for k := range env {
		if !wantSet[k] {
			t.Errorf("unexpected env var %s=%q (auditor config leaking into mention?)", k, env[k])
		}
	}
	if env["STAGE"] != string(types.StageMention) {
		t.Errorf("STAGE = %q, want %q", env["STAGE"], types.StageMention)
	}
	if env["MCP_THULU_SANDBOX_URL"] != sandboxMCPURL(defaultAgentBaseURL, "abc123", types.StageMention) {
		t.Errorf("MCP_THULU_SANDBOX_URL = %q, want mention-stage URL", env["MCP_THULU_SANDBOX_URL"])
	}
}

func TestWithAgentBaseURL(t *testing.T) {
	d := &Dispatcher{agentBaseURL: defaultAgentBaseURL}

	d.WithAgentBaseURL("")
	if d.agentBaseURL != defaultAgentBaseURL {
		t.Errorf("empty override changed base to %q", d.agentBaseURL)
	}

	d.WithAgentBaseURL("http://svc:8585")
	if d.agentBaseURL != "http://svc:8585" {
		t.Errorf("override = %q, want http://svc:8585", d.agentBaseURL)
	}
}
