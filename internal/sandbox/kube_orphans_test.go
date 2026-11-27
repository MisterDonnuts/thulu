//go:build !foss

package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func newKubeManager(t *testing.T, k *Kube) (*Manager, *Store) {
	t.Helper()
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(st, sett, k, ManagerConfig{}), st
}

// A daemon that restarted has no record of the pod and must List to find it. When that List fails,
// the old Remove answered nil, Destroy deleted the row, and nothing ever looked at the pod again.
func TestKubeRemoveReportsAFailedLookupAndDestroyKeepsTheRow(t *testing.T) {
	ctx := context.Background()
	client := uidClient()
	mgr, st := newKubeManager(t, NewKube(client, KubeConfig{Image: "img"}))
	res, err := mgr.Acquire(ctx, "t1", "dev", AcquireDockerOpts{WorkspaceID: "acme"})
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewKube(client, KubeConfig{Image: "img"})
	sett, err := settings.NewStore(st.db)
	if err != nil {
		t.Fatal(err)
	}
	mgr2 := NewManager(st, sett, restarted, ManagerConfig{})
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})

	if err := restarted.Remove(ctx, res.ContainerID); err == nil {
		t.Fatal("Remove answered nil for a pod it could not look up")
	}
	if err := mgr2.Destroy(ctx, "t1"); err == nil {
		t.Fatal("Destroy reported success while the pod is still there")
	}
	sb, err := st.Get(ctx, "t1")
	if err != nil {
		t.Fatalf("Destroy deleted the row of a pod it did not remove: %v", err)
	}
	if sb.Status != StatusEvicted || sb.ContainerID != res.ContainerID {
		t.Fatalf("row %+v: want evicted with its pod, for the GC to retry", sb)
	}
	if _, err := client.CoreV1().Pods("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{}); err != nil {
		t.Fatalf("pod: %v", err)
	}
}

// The GC removes a sandbox pod no live row records once it is older than the grace, keeps the
// pod of a live row, and keeps a pod young enough to be a create in flight.
func TestKubeOrphanSweepRemovesSandboxPodsWithoutALiveRow(t *testing.T) {
	ctx := context.Background()
	client := uidClient()
	k := NewKube(client, KubeConfig{Image: "img"})
	mgr, _ := newKubeManager(t, k)
	owned, err := mgr.Acquire(ctx, "owned", "dev", AcquireDockerOpts{WorkspaceID: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gone", "young"} {
		if _, err := k.Create(ctx, CreateOpts{TaskID: id, WorkspaceID: "acme"}); err != nil {
			t.Fatal(err)
		}
	}
	pods := client.CoreV1().Pods("tenant-acme")
	for name, created := range map[string]time.Time{
		owned.ContainerID:       time.Now().Add(-time.Hour),
		sandboxPodName("gone"):  time.Now().Add(-time.Hour),
		sandboxPodName("young"): time.Now(),
	} {
		pod, err := pods.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pod.CreationTimestamp = metav1.NewTime(created)
		if _, err := pods.Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	if got := mgr.ReconcileOrphans(ctx); got != 1 {
		t.Fatalf("removed %d sandbox pods, want 1", got)
	}
	if _, err := pods.Get(ctx, sandboxPodName("gone"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the orphan sandbox pod is still there (err=%v)", err)
	}
	for _, keep := range []string{owned.ContainerID, sandboxPodName("young")} {
		pod, err := pods.Get(ctx, keep, metav1.GetOptions{})
		if err != nil {
			t.Errorf("sandbox pod %s was removed: %v", keep, err)
			continue
		}
		if d := pod.Spec.ActiveDeadlineSeconds; d == nil || *d != int64(SandboxPodDeadline/time.Second) {
			t.Errorf("pod %s activeDeadlineSeconds = %v, want the 24 h backstop", keep, d)
		}
	}
}
