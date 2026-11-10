//go:build !foss

package sandbox

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func uidClient() *fake.Clientset {
	client := fake.NewClientset()
	n := 0
	client.PrependReactor("create", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if obj, ok := a.(k8stesting.CreateAction).GetObject().(metav1.Object); ok && obj.GetUID() == "" {
			n++
			obj.SetUID(k8stypes.UID(fmt.Sprintf("%s-uid-%d", obj.GetName(), n)))
		}
		return false, nil, nil
	})
	return client
}

func TestKubeServiceIsOwnedByItsPodAndLabeled(t *testing.T) {
	client := uidClient()
	k := NewKube(client, KubeConfig{Image: "img"})
	ctx := context.Background()
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "acme"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, _ := client.CoreV1().Pods("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{})
	svc, err := client.CoreV1().Services("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	if !ownedBy(svc.OwnerReferences, pod.UID) {
		t.Errorf("service owners = %+v, want the sandbox pod %s", svc.OwnerReferences, pod.UID)
	}
	for key, want := range map[string]string{
		kuberuntime.LabelManagedBy: "octopus", kuberuntime.LabelPlane: "data",
		kuberuntime.LabelTaskID: "t1", kuberuntime.LabelWorkspace: "acme", sandboxLabel: "t1",
	} {
		if svc.Labels[key] != want {
			t.Errorf("service label %s = %q, want %q", key, svc.Labels[key], want)
		}
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, WorktreePVCName("t1"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if pvc.Labels[kuberuntime.LabelTaskID] != "t1" || pvc.Labels[kuberuntime.LabelWorkspace] != "acme" ||
		kuberuntime.ClaimOwnerID(pvc) != "t1" {
		t.Errorf("claim labels = %v, annotations = %v", pvc.Labels, pvc.Annotations)
	}
}

func TestKubeCreateReplacesForeignService(t *testing.T) {
	client := uidClient()
	ctx := context.Background()
	stale := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: sandboxPodName("t1"), Namespace: "tenant-acme"}}
	if _, err := client.CoreV1().Services("tenant-acme").Create(ctx, stale, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	k := NewKube(client, KubeConfig{Image: "img"})
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "acme"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, _ := client.CoreV1().Pods("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{})
	svc, _ := client.CoreV1().Services("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{})
	if !ownedBy(svc.OwnerReferences, pod.UID) {
		t.Errorf("stale service kept: owners %+v", svc.OwnerReferences)
	}
}

func setPhase(t *testing.T, client *fake.Clientset, ns, name string, phase corev1.PodPhase, finished time.Time) {
	t.Helper()
	ctx := context.Background()
	pod, err := client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = phase
	if !finished.IsZero() {
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: sandboxContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 137, FinishedAt: metav1.NewTime(finished),
			}},
		}}
	}
	if _, err := client.CoreV1().Pods(ns).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestKubeSweepTerminated(t *testing.T) {
	client := uidClient()
	k := NewKube(client, KubeConfig{Image: "img"})
	ctx := context.Background()
	for _, id := range []string{"dead", "fresh", "live"} {
		if _, err := k.Create(ctx, CreateOpts{TaskID: id, WorkspaceID: "acme"}); err != nil {
			t.Fatal(err)
		}
	}
	setPhase(t, client, "tenant-acme", sandboxPodName("dead"), corev1.PodFailed, time.Now().Add(-7*time.Hour))
	setPhase(t, client, "tenant-acme", sandboxPodName("fresh"), corev1.PodFailed, time.Now().Add(-time.Hour))
	setPhase(t, client, "tenant-acme", sandboxPodName("live"), corev1.PodRunning, time.Time{})

	n, err := k.SweepTerminated(ctx, 6*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("SweepTerminated = %d, %v; want 1", n, err)
	}
	dead := sandboxPodName("dead")
	if _, err := client.CoreV1().Pods("tenant-acme").Get(ctx, dead, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("the long-dead sandbox pod is still there")
	}
	if _, err := client.CoreV1().Services("tenant-acme").Get(ctx, dead, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("the swept pod's Service is still there")
	}
	if _, err := client.CoreV1().Secrets("tenant-acme").Get(ctx, kuberuntime.EnvSecretName(dead), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("the swept pod's env Secret is still there")
	}
	for _, keep := range []string{"fresh", "live"} {
		if _, err := client.CoreV1().Pods("tenant-acme").Get(ctx, sandboxPodName(keep), metav1.GetOptions{}); err != nil {
			t.Errorf("sandbox %s was swept: %v", keep, err)
		}
	}
}

func TestManagerReleaseOnKubeDropsTheWorktreeClaim(t *testing.T) {
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatalf("settings.NewStore: %v", err)
	}
	client := uidClient()
	k := NewKube(client, KubeConfig{Image: "img"})
	mgr := NewManager(st, sett, k, ManagerConfig{})
	ctx := context.Background()

	res, err := mgr.Acquire(ctx, "t1", "dev", AcquireDockerOpts{WorkspaceID: "acme"})
	if err != nil || res.ContainerID == "" {
		t.Fatalf("Acquire: %+v, %v", res, err)
	}
	claim := WorktreePVCName("t1")
	if err := k.Remove(ctx, res.ContainerID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, claim, metav1.GetOptions{}); err != nil {
		t.Fatalf("Remove alone must keep the claim: %v", err)
	}

	if err := mgr.Release(ctx, "t1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, claim, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("Release left the worktree claim behind (err=%v)", err)
	}
	if _, err := client.CoreV1().Pods("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("Release left the sandbox pod behind")
	}
	if err := kuberuntime.EnsureWorktreeClaim(ctx, client, kuberuntime.WorktreeClaim{
		Namespace: "tenant-acme", Name: WorktreePVCName("t2"), OwnerID: "t2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Release(ctx, "t2"); err != nil {
		t.Fatalf("Release without a sandbox: %v", err)
	}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, WorktreePVCName("t2"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("Release of a sandbox-less task left its claim behind")
	}
}
