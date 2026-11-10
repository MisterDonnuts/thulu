package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/project"
)

func TestAutomationPortsStayInsideTheWorkspace(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	projectID, taskID := newProjectWithTask(t, h, "tenant-a")
	env, err := srv.projectStore.CreateEnvironment(t.Context(), projectID, project.EnvironmentInput{
		Name: "prod", Provider: "aws",
	})
	if err != nil {
		t.Fatalf("create environment: %v", err)
	}
	deps := srv.AutomationDeps()

	isNotFound := func(err error) bool { return err != nil && apperr.KindOf(err) == apperr.KindNotFound }

	if _, err := deps.Tasks.CommentTask(t.Context(), "ws-other", taskID, "hola"); !isNotFound(err) {
		t.Errorf("comment from another workspace: err = %v, want not found", err)
	}
	if _, err := deps.Tasks.MentionAgent(t.Context(), "ws-other", taskID, "dev", "hola"); !isNotFound(err) {
		t.Errorf("mention from another workspace: err = %v, want not found", err)
	}
	comments, err := srv.taskStore.ListComments(t.Context(), taskID)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("a refused recipe left %d comments on the task", len(comments))
	}
	if _, err := deps.Tasks.CommentTask(t.Context(), "ws-test", taskID, "hola"); err != nil {
		t.Errorf("comment from the task's own workspace: %v", err)
	}

	if _, _, err := deps.Metrics.Aggregate(t.Context(), "ws-other", env.ID, "error_rate", time.Minute, "avg"); !isNotFound(err) {
		t.Errorf("metric from another workspace: err = %v, want not found", err)
	}
	if _, _, err := deps.Metrics.Aggregate(t.Context(), "ws-test", "no-such-env", "error_rate", time.Minute, "avg"); !isNotFound(err) {
		t.Errorf("metric of a missing environment: err = %v, want not found", err)
	}
	if _, ok, err := deps.Metrics.Aggregate(t.Context(), "ws-test", env.ID, "error_rate", time.Minute, "avg"); err != nil || ok {
		t.Errorf("metric from the environment's own workspace = (ok %v, err %v), want no data and no error", ok, err)
	}
}

func TestMentionAgentRefusesAnAgentWithoutTheMentionStage(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	_, taskID := newProjectWithTask(t, h, "mention-stage")
	agentDir := filepath.Join(srv.agentsDir, "agents", "builder")
	if err := os.MkdirAll(filepath.Join(agentDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "prompts", "main.md"), []byte("# Builder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  agent:\n    image: octopus-docker\n    labels:\n      thulu.agent: \"true\"\n" +
		"      thulu.type: \"builder\"\n      thulu.stages: \"development\"\n"
	if err := os.WriteFile(filepath.Join(agentDir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	title, err := srv.AutomationDeps().Tasks.MentionAgent(t.Context(), "ws-test", taskID, "builder", "hola")
	if err == nil || apperr.KindOf(err) != apperr.KindConflict {
		t.Fatalf("mention of an agent without the mention stage: err = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "mention") {
		t.Errorf("error %q does not say why", err)
	}
	if title == "" {
		t.Error("the refusal still names the task, so the step can say which")
	}
	comments, err := srv.taskStore.ListComments(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Errorf("a refused mention left %d comments on the task", len(comments))
	}

	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{
		"title":             "staged",
		"stage_assignments": map[string]string{"backlog": "builder"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d %s", w.Code, w.Body.String())
	}
	var staged struct {
		ID    string `json:"id"`
		Stage string `json:"stage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Stage != "backlog" {
		t.Fatalf("tasks start in %q here; the assignment above names backlog", staged.Stage)
	}
	agent, answers, err := srv.AutomationDeps().Tasks.StageAgent(t.Context(), "ws-test", staged.ID)
	if err != nil || agent != "builder" || answers {
		t.Errorf("StageAgent = %q, %v, %v; want builder, not answering: it does not declare mention", agent, answers, err)
	}
}

type recordingCaller struct {
	url, key string
	calls    int
}

func (c *recordingCaller) Call(_ context.Context, sseURL, _ string, _ map[string]any, apiKey string) (map[string]any, error) {
	c.url, c.key = sseURL, apiKey
	c.calls++
	return map[string]any{}, nil
}

func TestAutomationMCPFollowsTheRegistration(t *testing.T) {
	t.Parallel()
	p := newProxyRig(t, "automcp@test.com")
	p.register(t, mcp.Registration{Name: "thulu-github", URL: p.url + "/gh/sse", Transport: mcp.TransportSSE, ForwardsCredential: true})
	p.register(t, mcp.Registration{Name: "docs", URL: p.url + "/docs/sse", Transport: mcp.TransportSSE})
	p.register(t, mcp.Registration{Name: "cloud", URL: p.url + "/mcp", Transport: mcp.TransportHTTP})
	p.register(t, mcp.Registration{Name: "jira", URL: p.url + "/jira/sse", Transport: mcp.TransportSSE,
		Auth: mcp.AuthCredential, CredentialRef: "jira"})
	caller := &recordingCaller{}
	a := &automationMCP{s: p.srv, caller: caller}
	ctx := t.Context()

	if _, err := a.CallTool(ctx, "thulu-github", "pr_view", nil, "oct_auto"); err != nil || caller.key != "oct_auto" || caller.url != p.url+"/gh/sse" {
		t.Errorf("forwarding server: url %q key %q, %v", caller.url, caller.key, err)
	}
	if _, err := a.CallTool(ctx, "docs", "search", nil, "oct_auto"); err != nil || caller.key != "" {
		t.Errorf("a server not marked forwards_credential got key %q (%v)", caller.key, err)
	}
	calls := caller.calls
	for _, name := range []string{"cloud", "jira", "nope"} {
		if _, err := a.CallTool(ctx, name, "x", nil, "oct_auto"); err == nil {
			t.Errorf("%s: call accepted", name)
		}
	}
	if caller.calls != calls {
		t.Error("a refused server was called")
	}
}
