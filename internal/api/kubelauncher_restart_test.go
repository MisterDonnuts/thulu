//go:build !foss

package api

import (
	"context"
	"slices"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func restartRig(t *testing.T) (*kuberuntime.Runtime, *fake.Clientset, *kubeContractDriver, string) {
	t.Helper()
	agentsDir := seedContractAgent(t)
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{Image: "ghcr.io/example/octopus-agent:latest"})
	if err != nil {
		t.Fatal(err)
	}
	return rt, client, &kubeContractDriver{rt: rt, client: client, agentsDir: agentsDir}, agentsDir
}

func launchContractRun(t *testing.T, l *kubeLauncher, drv *kubeContractDriver, taskID, instance string) string {
	t.Helper()
	tk := &task.Task{ID: taskID, Stage: types.StageInProgress, WorkspaceID: "ws-contract"}
	env := map[string]string{"INSTANCE_ID": instance, task.EnvLaunchKind: string(task.LaunchContinuation)}
	cid, _, _, err := l.LaunchForStage(t.Context(), tk, types.StageInProgress, drv.AgentType(), env, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return cid
}

// A restarted daemon has an empty byPod. Before, ContainerAlive said false for every run still
// going, KillRun answered NotFound and no exit callback could be armed: the run went on unwatched
// until its deadline. Now the launcher finds the pod by name and adopts it.
func TestKubeLauncherAfterARestartFindsItsLiveRuns(t *testing.T) {
	rt, client, drv, agentsDir := restartRig(t)
	agents := types.AllWorkspaces(types.NewCatalog(agentsDir))
	before := newKubeLauncher(context.Background(), rt, t.TempDir(), "", agents)
	watched := launchContractRun(t, before, drv, "abc123", "coder-task-abc123-in_progress-1700000001")
	killed := launchContractRun(t, before, drv, "def456", "coder-task-def456-in_progress-1700000002")

	after := newKubeLauncher(context.Background(), rt, t.TempDir(), "", agents)
	live, err := after.ScanLiveTaskAgents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range live {
		names = append(names, r.ContainerID)
		if r.LaunchKind != task.LaunchContinuation {
			t.Errorf("live run %+v: want its launch kind", r)
		}
	}
	slices.Sort(names)
	if want := []string{watched, killed}; !slices.Equal(names, sortedCopy(want)) {
		t.Fatalf("live runs %v, want %v", names, want)
	}
	if !after.ContainerAlive(watched) {
		t.Fatal("ContainerAlive said false for a run the restart left running")
	}

	exited := make(chan int, 1)
	after.OnExit(t.Context(), watched, func(code int, _, _ string) { exited <- code })
	drv.ForceExit(watched, 7)
	select {
	case code := <-exited:
		if code != 7 {
			t.Fatalf("exit code %d, want 7", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the re-armed exit callback never fired")
	}

	fresh := newKubeLauncher(context.Background(), rt, t.TempDir(), "", agents)
	if err := fresh.KillRun(t.Context(), killed); err != nil {
		t.Fatalf("KillRun of a run this daemon never launched: %v", err)
	}
	ns := rt.NamespaceFor("ws-contract")
	if _, err := client.CoreV1().Pods(ns).Get(t.Context(), killed, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("KillRun left the pod (err=%v)", err)
	}
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// Deleting a task deletes its run pods by label, without the launcher's memory, and marks each
// stopped first so its exit reads as a stop.
func TestRemoveTaskPodsDeletesTheTasksPods(t *testing.T) {
	rt, client, drv, agentsDir := restartRig(t)
	l := newKubeLauncher(context.Background(), rt, t.TempDir(), "", types.AllWorkspaces(types.NewCatalog(agentsDir)))
	mine := launchContractRun(t, l, drv, "abc123", "coder-task-abc123-in_progress-1700000001")
	other := launchContractRun(t, l, drv, "def456", "coder-task-def456-in_progress-1700000002")

	var stopped []string
	tk := &task.Task{ID: "abc123", WorkspaceID: "ws-contract"}
	if n := removeTaskPods(t.Context(), rt, tk, func(id, _ string) { stopped = append(stopped, id) }); n != 1 {
		t.Fatalf("removed %d pods, want 1", n)
	}
	if !slices.Equal(stopped, []string{mine}) {
		t.Fatalf("marked stopped %v, want %v", stopped, mine)
	}
	pods := client.CoreV1().Pods(rt.NamespaceFor("ws-contract"))
	if _, err := pods.Get(t.Context(), mine, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the task's pod is still there (err=%v)", err)
	}
	if _, err := pods.Get(t.Context(), other, metav1.GetOptions{}); err != nil {
		t.Fatalf("another task's pod went: %v", err)
	}
}
