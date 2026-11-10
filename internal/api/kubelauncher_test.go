//go:build !foss

package api

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/task/launchercontract"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestKubeLauncher_Contract(t *testing.T) {
	agentsDir := seedContractAgent(t)
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{
		Image:        "ghcr.io/example/octopus-agent:latest",
		RunAsNonRoot: false,
	})
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}

	l := newKubeLauncher(context.Background(), rt, t.TempDir(), "", types.AllWorkspaces(types.NewCatalog(agentsDir)))
	drv := &kubeContractDriver{rt: rt, client: client, agentsDir: agentsDir}
	launchercontract.RunSuite(t, l, drv)
}

func TestKubeLauncher_WorktreeMounts(t *testing.T) {
	l := newKubeLauncher(context.Background(), nil, "", "http://octopus.ns.svc:8585/git/", nil)
	tk := &task.Task{ID: "task42", BranchName: "octo/task42", WorktreePaths: map[string]string{"p1": "/data/wt"}}

	bare := newKubeLauncher(context.Background(), nil, "", "", nil)
	if got := bare.worktreeMounts(tk, map[string]string{"app": "/x"}, false, ""); got != nil {
		t.Errorf("no git base must yield no worktrees, got %+v", got)
	}

	mounts := l.worktreeMounts(tk, map[string]string{"webshop": "/data/wt"}, true, "")
	if len(mounts) != 1 {
		t.Fatalf("want 1 worktree mount, got %d", len(mounts))
	}
	m := mounts[0]
	if m.Name != "webshop" || m.Branch != "octo/task42" || !m.ReadOnly {
		t.Errorf("mount fields wrong: %+v", m)
	}
	if m.CloneURL != "http://octopus.ns.svc:8585/git/task42/webshop" {
		t.Errorf("clone URL = %q (trailing slash must be trimmed, task+project appended)", m.CloneURL)
	}

	checkout := l.worktreeMounts(tk, map[string]string{"api": "/data/projects/api"}, true, "")
	if len(checkout) != 1 || checkout[0].Branch != "" {
		t.Errorf("checkout mount = %+v, want the default branch", checkout)
	}

	if m.CredentialEnv != "" {
		t.Errorf("no task key → anonymous clone, got credential env %q", m.CredentialEnv)
	}

	withKey := l.worktreeMounts(tk, map[string]string{"webshop": "/data/wt"}, false, "sk-task-key/+odd")
	if len(withKey) != 1 {
		t.Fatalf("want 1 worktree mount, got %d", len(withKey))
	}
	u, err := url.Parse(withKey[0].CloneURL)
	if err != nil {
		t.Fatalf("clone URL must stay parseable: %v", err)
	}
	if u.User != nil {
		t.Errorf("clone URL must carry no credentials, got user %q", u.User)
	}
	if withKey[0].CredentialEnv != "OCTOPUS_API_KEY" {
		t.Errorf("credential env = %q, want OCTOPUS_API_KEY", withKey[0].CredentialEnv)
	}
	if u.Host != "octopus.ns.svc:8585" || u.Path != "/git/task42/webshop" {
		t.Errorf("clone URL host/path wrong: %s", withKey[0].CloneURL)
	}
}

func TestKubeLauncher_CapturesLogsOnExit(t *testing.T) {
	agentsDir := seedContractAgent(t)
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{
		Image:        "ghcr.io/example/octopus-agent:latest",
		RunAsNonRoot: false,
	})
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}

	type capture struct {
		a    *agent.Agent
		logs string
	}
	got := make(chan capture, 1)
	l := newKubeLauncher(context.Background(), rt, t.TempDir(), "", types.AllWorkspaces(types.NewCatalog(agentsDir)))
	l.WithLogSink(func(_ context.Context, a *agent.Agent, logs string) { got <- capture{a, logs} })

	drv := &kubeContractDriver{rt: rt, client: client, agentsDir: agentsDir}
	at := drv.AgentType()
	env := map[string]string{"INSTANCE_ID": "refiner-task-abc-refinement-1"}
	tk := &task.Task{
		ID: "task-abc", Stage: types.StageInProgress,
		WorkspaceID: "ws-contract", ProjectIDs: []string{"proj-1"},
	}

	cid, _, _, err := l.LaunchForStage(context.Background(), tk, types.StageInProgress, at, env, nil, nil, false)
	if err != nil {
		t.Fatalf("LaunchForStage: %v", err)
	}
	l.OnExit(t.Context(), cid, func(int, string, string) {})
	drv.ForceExit(cid, 0)

	select {
	case c := <-got:
		if c.a.ID != cid {
			t.Errorf("sink agent.ID = %q, want pod name %q", c.a.ID, cid)
		}
		if c.a.TaskID != "task-abc" {
			t.Errorf("sink agent.TaskID = %q, want task-abc", c.a.TaskID)
		}
		if c.a.Stage != string(types.StageInProgress) {
			t.Errorf("sink agent.Stage = %q", c.a.Stage)
		}
		if c.a.ProjectID != "proj-1" {
			t.Errorf("sink agent.ProjectID = %q, want proj-1 (not the workspace id)", c.a.ProjectID)
		}
		if c.a.StartedAt.IsZero() {
			t.Error("sink agent.StartedAt is zero — launch time not captured")
		}
		if c.logs == "" {
			t.Error("sink got empty logs")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("log sink was not called within 5s of pod exit")
	}
}

func TestKubeLauncher_ScanSkipsWatchedPods(t *testing.T) {
	agentsDir := seedContractAgent(t)
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{Image: "ghcr.io/example/octopus-agent:latest"})
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	base, cancel := context.WithCancel(context.Background())
	cancel()
	l := newKubeLauncher(base, rt, t.TempDir(), "", types.AllWorkspaces(types.NewCatalog(agentsDir)))
	drv := &kubeContractDriver{rt: rt, client: client, agentsDir: agentsDir}
	at := drv.AgentType()
	tk := &task.Task{ID: "task-abc", Stage: types.StageInProgress, WorkspaceID: "ws-contract"}

	launch := func(instance string) string {
		t.Helper()
		env := map[string]string{"INSTANCE_ID": instance}
		cid, _, _, err := l.LaunchForStage(context.Background(), tk, types.StageInProgress, at, env, nil, nil, false)
		if err != nil {
			t.Fatalf("LaunchForStage: %v", err)
		}
		return cid
	}
	watched := launch("contract-agent-task-abc-in_progress-1")
	l.OnExit(t.Context(), watched, func(int, string, string) {})
	orphan := launch("contract-agent-task-abc-in_progress-2")
	drv.ForceExit(watched, 0)
	drv.ForceExit(orphan, 0)

	exited, err := l.ScanExitedTaskAgents(t.Context())
	if err != nil {
		t.Fatalf("ScanExitedTaskAgents: %v", err)
	}
	if len(exited) != 1 || exited[0].ContainerID != orphan {
		t.Errorf("scan = %+v, want only the unwatched pod %s", exited, orphan)
	}
}

type kubeContractDriver struct {
	rt        *kuberuntime.Runtime
	client    *fake.Clientset
	agentsDir string
}

func (d *kubeContractDriver) AgentType() *types.AgentType {
	return &types.AgentType{
		Name:        "contract-agent",
		Dir:         filepath.Join(d.agentsDir, "agents", "contract-agent"),
		ValidStages: []types.Stage{types.StageInProgress, types.StageMention},
	}
}

func (d *kubeContractDriver) ForceExit(containerID string, code int) {
	ns := d.rt.NamespaceFor("ws-contract")
	ctx := context.Background()
	pod, err := d.client.CoreV1().Pods(ns).Get(ctx, containerID, metav1.GetOptions{})
	if err != nil {
		return
	}
	pod.Status.Phase = corev1.PodFailed
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "agent",
		State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: int32(code)},
		},
	}}
	_, _ = d.client.CoreV1().Pods(ns).Update(ctx, pod, metav1.UpdateOptions{})
}

func seedContractAgent(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "agents", "contract-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	compose := `services:
  contract-agent:
    image: ghcr.io/example/octopus-agent:latest
    labels:
      thulu.agent: "true"
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return base
}

func TestKubeLauncherResolvesAgentsThroughTheSourceNotTheDirectory(t *testing.T) {
	stored := []types.AgentType{{Name: "probe", Type: "probe", ValidStages: []types.Stage{"in_progress"}}}

	l := newKubeLauncher(context.Background(), nil, t.TempDir(), "",
		types.AllWorkspaces(staticSource(stored)))

	got := l.FindAgentType("ws1", "probe")
	if got == nil {
		t.Fatal("probe not found: the launcher is still reading the agents directory")
	}
	if got.Name != "probe" {
		t.Fatalf("resolved %q, want probe", got.Name)
	}
	if l.FindAgentType("ws1", "nonexistent") != nil {
		t.Fatal("resolved an agent that is in neither the source nor the directory")
	}
}
