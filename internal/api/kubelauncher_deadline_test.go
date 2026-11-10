//go:build !foss

package api

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestKubeLauncher_DeadlineAndKill(t *testing.T) {
	agentsDir := seedContractAgent(t)
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{Image: "ghcr.io/example/octopus-agent:latest"})
	if err != nil {
		t.Fatal(err)
	}
	l := newKubeLauncher(context.Background(), rt, t.TempDir(), "", types.AllWorkspaces(types.NewCatalog(agentsDir)))
	l.runDeadline = func(ws string) time.Duration {
		if ws == "ws-dl" {
			return 60 * time.Minute
		}
		return 10 * time.Minute
	}
	drv := &kubeContractDriver{rt: rt, client: client, agentsDir: agentsDir}
	tk := &task.Task{ID: "task-dl", Stage: types.StageInProgress, WorkspaceID: "ws-dl"}
	env := map[string]string{"INSTANCE_ID": "coder-task-dl-in_progress-1"}

	cid, _, _, err := l.LaunchForStage(t.Context(), tk, types.StageInProgress, drv.AgentType(), env, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor("ws-dl")
	pod, err := client.CoreV1().Pods(ns).Get(t.Context(), cid, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d := pod.Spec.ActiveDeadlineSeconds; d == nil || *d != 61*60 {
		t.Fatalf("activeDeadlineSeconds = %v, want 3660", d)
	}

	if err := l.KillRun(t.Context(), cid); err != nil {
		t.Fatalf("KillRun: %v", err)
	}
	if _, err := client.CoreV1().Pods(ns).Get(t.Context(), cid, metav1.GetOptions{}); err == nil {
		t.Fatal("pod still there after KillRun")
	}
	if err := l.KillRun(t.Context(), "someone-elses-pod"); err == nil {
		t.Fatal("KillRun accepted an untracked run")
	}
}

func TestPodDeadlineSeconds(t *testing.T) {
	if got := podDeadlineSeconds(0); got != 0 {
		t.Errorf("no deadline = %d, want 0", got)
	}
	if got := podDeadlineSeconds(30 * time.Minute); got != 31*60 {
		t.Errorf("30m = %d, want %d", got, 31*60)
	}
}
