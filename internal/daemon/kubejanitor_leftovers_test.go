//go:build !foss

package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

// The minute sweep deletes the run of a deleted task, the pod no node took past its deadline (with
// a note on its task), and the env Secret whose pod is gone; the live run of a live task stays.
func TestKubeJanitorSweepLeftovers(t *testing.T) {
	ctx := context.Background()
	tasks, err := task.NewStore(testpg.New(t), filepath.Join(t.TempDir(), "att"))
	if err != nil {
		t.Fatal(err)
	}
	live := &task.Task{Title: "live", Stage: "in_progress", WorkspaceID: "ws1"}
	if err := tasks.Create(ctx, live); err != nil {
		t.Fatal(err)
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
	old := metav1.NewTime(time.Now().Add(-5 * time.Hour))
	pod := func(name, instance string, phase corev1.PodPhase, node string) {
		t.Helper()
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns, CreationTimestamp: old,
				Labels:      map[string]string{kuberuntime.LabelManagedBy: "octopus"},
				Annotations: map[string]string{kuberuntime.AnnotationInstanceID: instance, kuberuntime.AnnotationAgentType: "coder"},
			},
			Spec:   corev1.PodSpec{NodeName: node},
			Status: corev1.PodStatus{Phase: phase},
		}
		if _, err := client.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	pod("deleted-task-run", "coder-task-deadbeef-in_progress-1700000000", corev1.PodRunning, "n1")
	pod("live-run", "coder-task-"+live.ID+"-in_progress-1700000001", corev1.PodRunning, "n1")
	pod("never-scheduled", "coder-task-"+live.ID+"-in_progress-1700000002", corev1.PodPending, "")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "gone-pod-env", Namespace: ns, CreationTimestamp: old,
		Labels: map[string]string{kuberuntime.LabelManagedBy: "octopus"},
	}}
	if _, err := client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	newKubeJanitor(rt, nil, tasks, nil).SweepLeftovers(ctx)

	pods := client.CoreV1().Pods(ns)
	for _, gone := range []string{"deleted-task-run", "never-scheduled"} {
		if _, err := pods.Get(ctx, gone, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("pod %s is still there (err=%v)", gone, err)
		}
	}
	if _, err := pods.Get(ctx, "live-run", metav1.GetOptions{}); err != nil {
		t.Errorf("the live task's run was deleted: %v", err)
	}
	if _, err := client.CoreV1().Secrets(ns).Get(ctx, "gone-pod-env", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the env secret of a gone pod is still there (err=%v)", err)
	}
	comments, err := tasks.ListComments(ctx, live.ID)
	if err != nil {
		t.Fatal(err)
	}
	noted := false
	for i := range comments {
		noted = noted || strings.Contains(comments[i].Body, "never-scheduled")
	}
	if !noted {
		t.Errorf("no note on the task about its unscheduled pod; comments %+v", comments)
	}
}

// Pod ends come in bursts; the Reconcile they ask for runs once per burst, not once per pod.
func TestCoalescedFoldsABurstIntoFewRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var runs atomic.Int32
	release := make(chan struct{})
	trigger := coalesced(ctx, time.Millisecond, func(context.Context) {
		runs.Add(1)
		<-release
	})
	trigger()
	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for range 50 {
		trigger()
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
	if got := runs.Load(); got != 2 {
		t.Fatalf("fn ran %d times for one call and a burst of 50, want 2", got)
	}
}
