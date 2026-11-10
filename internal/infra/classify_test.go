package infra

import (
	"testing"

	"github.com/docker/docker/api/types"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/dockerx"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
)

func TestClassifyDocker(t *testing.T) {
	stack := func(svc string) map[string]string {
		return map[string]string{labelComposeProject: "deploy", labelComposeService: svc}
	}
	cases := []struct {
		name   string
		labels map[string]string
		state  string
		want   Kind
	}{
		{"agent run", map[string]string{agent.LabelManaged: "true", agent.LabelType: "coder"}, "running", KindAgent},
		{"sandbox", map[string]string{sandbox.SandboxLabel: "true"}, "running", KindSandbox},
		{"chat", map[string]string{agent.LabelChat: "true"}, "running", KindChat},
		{"daemon-made MCP server", map[string]string{"octopus.mcp": "true"}, "running", KindMCP},
		{"per-run browser", map[string]string{"thulu.browser": "true", "thulu.browser.agent": "a9a9a9a9a9a9",
			agent.LabelTask: "t1", agent.LabelWorkspaceID: "ws1"}, "running", KindBrowser},
		{"production daemon", stack("octopus"), "running", KindDaemon},
		{"dev daemon", stack("api"), "running", KindDaemon},
		{"postgres", stack("pg"), "running", KindPostgres},
		{"caddy", stack("caddy"), "running", KindWeb},
		{"compose MCP server", stack("thulu-github"), "running", KindMCP},
		{"another stack service", stack("llm-gateway"), "running", KindPlatform},
		{"someone else's compose project", map[string]string{labelComposeProject: "blog", labelComposeService: "pg"}, "running", KindOther},
		{"a bare container", nil, "exited", KindOther},
	}
	for _, c := range cases {
		ctr := &types.Container{Labels: c.labels, State: c.state}
		if got := classifyDocker(ctr, []string{"deploy"}); got != c.want {
			t.Errorf("%s: kind = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestClassifyDockerOtherInstall(t *testing.T) {
	dockerx.SetInstallID("this-install")
	t.Cleanup(func() { dockerx.SetInstallID("") })
	other := &types.Container{Labels: map[string]string{sandbox.SandboxLabel: "true", dockerx.InstallLabel: "other-install"}}
	if got := classifyDocker(other, []string{"deploy"}); got != KindOtherInstall {
		t.Errorf("another install's sandbox = %q, want %q", got, KindOtherInstall)
	}
	mine := &types.Container{Labels: map[string]string{sandbox.SandboxLabel: "true", dockerx.InstallLabel: "this-install"}}
	if got := classifyDocker(mine, []string{"deploy"}); got != KindSandbox {
		t.Errorf("this install's sandbox = %q, want %q", got, KindSandbox)
	}
}

func TestDockerAttribution(t *testing.T) {
	var c Container
	dockerAttribution(&c, map[string]string{
		agent.LabelWorkspaceID: "ws1", agent.LabelTask: "t9", agent.LabelType: "reviewer", agent.LabelStage: "review",
	})
	if c.WorkspaceID != "ws1" || c.TaskID != "t9" || c.Agent != "reviewer" || c.Stage != "review" {
		t.Errorf("agent attribution = %+v", c)
	}
	var s Container
	dockerAttribution(&s, map[string]string{sandbox.SandboxTaskLabel: "t3", labelComposeService: "pg"})
	if s.TaskID != "t3" || s.Service != "pg" {
		t.Errorf("sandbox attribution = %+v", s)
	}
}

func TestClassifyPod(t *testing.T) {
	const cp = "octopus"
	managed := map[string]string{kubeapi.LabelManagedBy: "octopus", kubeapi.LabelStage: "build"}
	cases := []struct {
		name, ns, pod string
		labels        map[string]string
		tenant        bool
		want          Kind
	}{
		{"agent pod", "tenant-ws1", "run-abc", managed, true, KindAgent},
		{"chat pod", "tenant-ws1", "chat-x", map[string]string{kubeapi.LabelManagedBy: "octopus", kubeapi.LabelStage: "chat"}, true, KindChat},
		{"sandbox pod", "tenant-ws1", "sbx", map[string]string{kubeapi.LabelSandbox: "t1", kubeapi.LabelManagedBy: "octopus"}, true, KindSandbox},
		{"MCP deployment", cp, "thulu-github-7d9", map[string]string{labelPartOf: partOfMCP}, false, KindMCP},
		{"cluster DNS", "kube-system", "coredns-1", nil, false, KindSystem},
		{"daemon", cp, "octopus-5f6c-abcde", map[string]string{labelAppName: "octopus"}, false, KindDaemon},
		{"postgres", cp, "octopus-postgres-0", map[string]string{labelAppName: "octopus"}, false, KindPostgres},
		{"gateway", cp, "octopus-gateway-1", map[string]string{labelAppName: "octopus"}, false, KindPlatform},
		{"unrelated namespace", "monitoring", "grafana-0", nil, false, KindOther},
	}
	for _, c := range cases {
		if got := classifyPod(c.ns, c.labels, c.pod, cp, c.tenant); got != c.want {
			t.Errorf("%s: kind = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDockerAttributionInstance(t *testing.T) {
	labeled := Container{Kind: KindAgent, Name: "dev-task-t1-in_progress-1"}
	dockerAttribution(&labeled, map[string]string{agent.LabelInstance: "dev-task-t1-in_progress-2"})
	if labeled.Instance != "dev-task-t1-in_progress-2" {
		t.Errorf("labeled instance = %q", labeled.Instance)
	}
	named := Container{Kind: KindAgent, Name: "dev-task-t1-in_progress-1"}
	dockerAttribution(&named, map[string]string{agent.LabelTask: "t1"})
	if named.Instance != "dev-task-t1-in_progress-1" {
		t.Errorf("unlabeled instance = %q, want the container name", named.Instance)
	}
	sb := Container{Kind: KindSandbox, Name: "octopus-sandbox-t1"}
	dockerAttribution(&sb, map[string]string{agent.LabelInstance: "x"})
	if sb.Instance != "" {
		t.Errorf("sandbox instance = %q, want none", sb.Instance)
	}
}

func TestCollectorForget(t *testing.T) {
	c := New(Config{})
	c.containers = []Container{{ID: "a"}, {ID: "b"}}
	c.Forget("a")
	if got := c.Containers(); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("after Forget(a): %+v", got)
	}
}
