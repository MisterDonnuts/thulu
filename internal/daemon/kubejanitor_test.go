//go:build !foss

package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	dbpkg "github.com/MisterDonnuts/thulu/internal/db"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
	"github.com/MisterDonnuts/thulu/internal/types"
)

// A group whose tasks all reached a stage with release_resources has no more use for its claim,
// even though the task is not done.
func TestKubeJanitorReleasesAGroupInAReleaseStage(t *testing.T) {
	ctx := context.Background()
	db := testpg.New(t)
	tasks, err := task.NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("task.NewStore: %v", err)
	}
	tasks.WithStages(stagepkg.NewSnapshot([]types.StageInfo{
		{ID: "backlog", Label: "Backlog", Position: 10, Initial: true},
		{ID: "in_progress", Label: "In progress", Position: 20},
		{ID: "release", Label: "Release", Position: 30, ReleaseResources: true},
		{ID: "done", Label: "Done", Position: 40, Terminal: true},
	}))
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{AllowNoRuntimeClass: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.EnsureTenantNamespace(ctx, "ws1"); err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor("ws1")
	create := func(tk *task.Task) *task.Task {
		t.Helper()
		tk.WorkspaceID = "ws1"
		if err := tasks.Create(ctx, tk); err != nil {
			t.Fatalf("create task: %v", err)
		}
		return tk
	}
	released := create(&task.Task{Title: "released", Stage: "release"})
	waiting := create(&task.Task{Title: "parent", Stage: "release"})
	create(&task.Task{Title: "child still coding", Stage: "in_progress", ParentID: waiting.ID})
	// Its release agent is at work: the sweep leaves the group alone until the run ends.
	releasing := create(&task.Task{Title: "release agent running", Stage: "release", Status: task.StatusRunning})

	claim := func(owner string) string { return kuberuntime.WorktreeClaimPrefix + owner }
	for _, owner := range []string{released.ID, waiting.ID, releasing.ID} {
		if err := kuberuntime.EnsureWorktreeClaim(ctx, client, kuberuntime.WorktreeClaim{
			Namespace: ns, Name: claim(owner), OwnerID: owner, WorkspaceID: "ws1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	newKubeJanitor(rt, nil, tasks, &stubRuns{}).Sweep(ctx)

	for owner, want := range map[string]bool{released.ID: false, waiting.ID: true, releasing.ID: true} {
		_, err := client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim(owner), metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		if got := err == nil; got != want {
			t.Errorf("claim of %s exists = %v, want %v", owner, got, want)
		}
	}
}

type stubRuns struct {
	reconciles int
	recorded   map[string]bool
}

func (s *stubRuns) Reconcile(context.Context) { s.reconciles++ }
func (s *stubRuns) ExitSettled(_ context.Context, id string) bool {
	return s.recorded[id]
}

func TestKubeJanitorDefaultsMatchTheDockerJanitor(t *testing.T) {
	k, d := newKubeJanitor(nil, nil, nil, nil), newJanitor(nil, nil, nil, nil, "")
	if k.interval != d.interval || k.agentExitedTTL != d.agentExitedTTL ||
		k.sandboxExitedTTL != d.sandboxExitedTTL || k.failedTaskSandboxTTL != d.failedTaskSandboxTTL {
		t.Errorf("kube janitor %+v drifted from the Docker janitor's thresholds", k)
	}
}

func TestKubeJanitorSweep(t *testing.T) {
	ctx := context.Background()
	db := testpg.New(t)
	tasks, err := task.NewStore(db, filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatalf("task.NewStore: %v", err)
	}
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{AllowNoRuntimeClass: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.EnsureTenantNamespace(ctx, "ws1"); err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor("ws1")

	create := func(tk *task.Task) *task.Task {
		t.Helper()
		tk.WorkspaceID = "ws1"
		if err := tasks.Create(ctx, tk); err != nil {
			t.Fatalf("create task: %v", err)
		}
		return tk
	}
	setStatus := func(id string, s task.RunStatus, age time.Duration) {
		t.Helper()
		if _, err := tasks.Update(ctx, id, task.Patch{Status: &s}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, dbpkg.Rebind(`UPDATE tasks SET updated_at = ? WHERE id = ?`),
			time.Now().Add(-age).UTC(), id); err != nil {
			t.Fatal(err)
		}
	}
	done := create(&task.Task{Title: "done", Stage: "done"})
	live := create(&task.Task{Title: "live", Stage: "in_progress"})
	failedOld := create(&task.Task{Title: "failed long ago", Stage: "in_progress"})
	setStatus(failedOld.ID, task.StatusFailed, 7*time.Hour)
	failedNew := create(&task.Task{Title: "failed just now", Stage: "in_progress"})
	setStatus(failedNew.ID, task.StatusFailed, time.Hour)
	parent := create(&task.Task{Title: "parent", Stage: "in_progress"})
	setStatus(parent.ID, task.StatusFailed, 7*time.Hour)
	create(&task.Task{Title: "child", Stage: "in_progress", ParentID: parent.ID})

	claim := func(owner string) string {
		return kuberuntime.WorktreeClaimPrefix + strings.ReplaceAll(owner, ":", "-")
	}
	owners := []string{done.ID, live.ID, failedOld.ID, failedNew.ID, parent.ID, "deletedtask", "auto:busy", "auto:gone"}
	for _, owner := range owners {
		if err := kuberuntime.EnsureWorktreeClaim(ctx, client, kuberuntime.WorktreeClaim{
			Namespace: ns, Name: claim(owner), OwnerID: owner, WorkspaceID: "ws1",
		}); err != nil {
			t.Fatal(err)
		}
	}
	sandboxPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "sandbox-auto-busy", Namespace: ns, Labels: map[string]string{
			kuberuntime.LabelPlane: "data", kuberuntime.LabelSandbox: "auto-busy",
		}},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim("auto:busy")},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if _, err := client.CoreV1().Pods(ns).Create(ctx, sandboxPod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"recorded", "unrecorded"} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns,
				Labels:      map[string]string{kuberuntime.LabelManagedBy: "octopus"},
				Annotations: map[string]string{kuberuntime.AnnotationInstanceID: name},
			},
			Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					FinishedAt: metav1.NewTime(time.Now().Add(-3 * time.Hour)),
				}},
			}}},
		}
		if _, err := client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	runs := &stubRuns{recorded: map[string]bool{"recorded": true}}
	newKubeJanitor(rt, nil, tasks, runs).Sweep(ctx)

	if runs.reconciles != 1 {
		t.Errorf("Reconcile ran %d times, want once per sweep", runs.reconciles)
	}
	exists := func(get func() error) bool {
		err := get()
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}
	for owner, want := range map[string]bool{
		done.ID: false, "deletedtask": false, failedOld.ID: false, "auto:gone": false,
		live.ID: true, failedNew.ID: true, parent.ID: true, "auto:busy": true,
	} {
		got := exists(func() error {
			_, err := client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, claim(owner), metav1.GetOptions{})
			return err
		})
		if got != want {
			t.Errorf("claim of %s exists = %v, want %v", owner, got, want)
		}
	}
	for pod, want := range map[string]bool{"recorded": false, "unrecorded": true} {
		got := exists(func() error {
			_, err := client.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
			return err
		})
		if got != want {
			t.Errorf("pod %s exists = %v, want %v", pod, got, want)
		}
	}
}
