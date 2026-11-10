// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func tenantNS(t *testing.T, client *fake.Clientset, name, tenant string) {
	t.Helper()
	labels := map[string]string{LabelPlane: planeValue}
	if tenant != "" {
		labels[LabelTenant] = tenant
	}
	_, err := client.CoreV1().Namespaces().Create(context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create namespace %s: %v", name, err)
	}
}

func claimPod(t *testing.T, client *fake.Clientset, ns, name, claim string, phase corev1.PodPhase) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name:         "workspace",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}},
		}}},
		Status: corev1.PodStatus{Phase: phase},
	}
	if _, err := client.CoreV1().Pods(ns).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod %s: %v", name, err)
	}
}

func claimExists(t *testing.T, client *fake.Clientset, ns, name string) bool {
	t.Helper()
	_, err := client.CoreV1().PersistentVolumeClaims(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get claim %s/%s: %v", ns, name, err)
	}
	return err == nil
}

func TestEnsureWorktreeClaimLabelsTheOwner(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()
	c := WorktreeClaim{Namespace: "tenant-acme", Name: "octopus-wt-root1", OwnerID: "Root1", WorkspaceID: "acme"}
	for range 2 {
		if err := EnsureWorktreeClaim(ctx, client, c); err != nil {
			t.Fatalf("EnsureWorktreeClaim: %v", err)
		}
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, "octopus-wt-root1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get claim: %v", err)
	}
	want := map[string]string{LabelPlane: "data", LabelManagedBy: "octopus", LabelTaskID: "root1", LabelWorkspace: "acme"}
	for k, v := range want {
		if pvc.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, pvc.Labels[k], v)
		}
	}
	if got := ClaimOwnerID(pvc); got != "Root1" {
		t.Errorf("ClaimOwnerID = %q, want the exact id Root1", got)
	}
	if q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; q.String() != "10Gi" {
		t.Errorf("size = %s, want the 10Gi default", q.String())
	}
}

func TestEnsureWorktreeClaimRecreatesATerminatingClaim(t *testing.T) {
	prevPoll := claimTerminatingPoll
	claimTerminatingPoll = 5 * time.Millisecond
	t.Cleanup(func() { claimTerminatingPoll = prevPoll })

	client := fake.NewClientset()
	ctx := context.Background()
	now := metav1.Now()
	old := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "octopus-wt-t1", Namespace: "tenant-acme", DeletionTimestamp: &now,
		Finalizers: []string{"kubernetes.io/pvc-protection"},
	}}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = client.CoreV1().PersistentVolumeClaims("tenant-acme").Delete(ctx, "octopus-wt-t1", metav1.DeleteOptions{})
	}()
	if err := EnsureWorktreeClaim(ctx, client, WorktreeClaim{Namespace: "tenant-acme", Name: "octopus-wt-t1"}); err != nil {
		t.Fatalf("EnsureWorktreeClaim: %v", err)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, "octopus-wt-t1", metav1.GetOptions{})
	if err != nil || pvc.DeletionTimestamp != nil {
		t.Fatalf("want a fresh claim, got %+v (err %v)", pvc, err)
	}
	if pvc.Labels[LabelTaskID] != "t1" {
		t.Errorf("owner from the name: task-id label = %q, want t1", pvc.Labels[LabelTaskID])
	}
}

func TestLaunchPodLabelsClaimWithSandboxRoot(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{Image: "img", AllowNoRuntimeClass: true})
	ctx := context.Background()
	spec := LaunchSpec{
		WorkspaceID: "acme", TaskID: "child1", InstanceID: "a-task-child1-dev-1",
		WorktreePVC: "octopus-wt-root1", WorktreeOwnerID: "root1",
	}
	if _, err := rt.LaunchPod(ctx, spec); err != nil {
		t.Fatalf("LaunchPod: %v", err)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, "octopus-wt-root1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get claim: %v", err)
	}
	if ClaimOwnerID(pvc) != "root1" || pvc.Labels[LabelTaskID] != "root1" {
		t.Errorf("claim owner = %q / label %q, want the sandbox root, not the subtask",
			ClaimOwnerID(pvc), pvc.Labels[LabelTaskID])
	}
}

func TestReleaseWorktreeClaimsKeepsMountedClaim(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()
	for _, c := range []WorktreeClaim{
		{Namespace: "tenant-acme", Name: "octopus-wt-t1", OwnerID: "t1"},
		{Namespace: "tenant-acme", Name: "octopus-wt-t2", OwnerID: "t2"},
	} {
		if err := EnsureWorktreeClaim(ctx, client, c); err != nil {
			t.Fatal(err)
		}
	}
	claimPod(t, client, "tenant-acme", "agent-1", "octopus-wt-t1", corev1.PodRunning)

	n, err := ReleaseWorktreeClaims(ctx, client, "t1")
	if err != nil || n != 0 || !claimExists(t, client, "tenant-acme", "octopus-wt-t1") {
		t.Fatalf("a claim a running pod mounts must stay (deleted=%d err=%v)", n, err)
	}

	if err := client.CoreV1().Pods("tenant-acme").Delete(ctx, "agent-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	claimPod(t, client, "tenant-acme", "agent-2", "octopus-wt-t1", corev1.PodSucceeded)
	if n, err := ReleaseWorktreeClaims(ctx, client, "t1"); err != nil || n != 1 {
		t.Fatalf("ReleaseWorktreeClaims = %d, %v; want 1 deleted", n, err)
	}
	if claimExists(t, client, "tenant-acme", "octopus-wt-t1") {
		t.Error("t1's claim still there")
	}
	if !claimExists(t, client, "tenant-acme", "octopus-wt-t2") {
		t.Error("t2's claim was deleted by t1's release")
	}
}

func TestSweepWorktreeClaims(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	pvcs := client.CoreV1().PersistentVolumeClaims("tenant-acme")
	for _, c := range []WorktreeClaim{
		{Namespace: "tenant-acme", Name: "octopus-wt-done1", OwnerID: "done1"},
		{Namespace: "tenant-acme", Name: "octopus-wt-busy1", OwnerID: "busy1"},
		{Namespace: "tenant-acme", Name: "octopus-wt-live1", OwnerID: "live1"},
	} {
		if err := EnsureWorktreeClaim(ctx, client, c); err != nil {
			t.Fatal(err)
		}
	}
	unlabeled := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "octopus-wt-unlabeled1", Labels: map[string]string{LabelPlane: planeValue},
	}}
	other := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "data-db", Labels: map[string]string{LabelPlane: planeValue},
	}}
	for _, p := range []*corev1.PersistentVolumeClaim{unlabeled, other} {
		if _, err := pvcs.Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	claimPod(t, client, "tenant-acme", "sandbox-busy1", "octopus-wt-busy1", corev1.PodPending)

	var asked []string
	releasable := func(_ context.Context, owner string) (bool, error) {
		asked = append(asked, owner)
		switch owner {
		case "live1":
			return false, nil
		case "data-db":
			t.Error("asked about a claim that is not a worktree claim")
		}
		return true, nil
	}
	n, err := rt.SweepWorktreeClaims(ctx, releasable)
	if err != nil {
		t.Fatalf("SweepWorktreeClaims: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d claims, want 2 (done1 + unlabeled1); asked about %v", n, asked)
	}
	for name, want := range map[string]bool{
		"octopus-wt-done1": false, "octopus-wt-unlabeled1": false,
		"octopus-wt-busy1": true,
		"octopus-wt-live1": true,
		"data-db":          true,
	} {
		if got := claimExists(t, client, "tenant-acme", name); got != want {
			t.Errorf("claim %s exists = %v, want %v", name, got, want)
		}
	}
}

func TestSweepWorktreeClaimsSkipsOnCallbackError(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	if err := EnsureWorktreeClaim(ctx, client, WorktreeClaim{Namespace: "tenant-acme", Name: "octopus-wt-t1"}); err != nil {
		t.Fatal(err)
	}
	n, err := rt.SweepWorktreeClaims(ctx, func(context.Context, string) (bool, error) {
		return true, errors.New("db down")
	})
	if err != nil || n != 0 || !claimExists(t, client, "tenant-acme", "octopus-wt-t1") {
		t.Fatalf("an unanswerable owner must keep its claim (deleted=%d err=%v)", n, err)
	}
}

func agentPod(t *testing.T, client *fake.Clientset, ns, name, stage string, finished time.Time) {
	t.Helper()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels:      map[string]string{LabelManagedBy: managedByValue, LabelStage: stage, LabelTaskID: "t1"},
			Annotations: map[string]string{AnnotationInstanceID: name + "-instance"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if !finished.IsZero() {
		pod.Status.Phase = corev1.PodSucceeded
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: agentContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 0, FinishedAt: metav1.NewTime(finished),
			}},
		}}
	}
	if _, err := client.CoreV1().Pods(ns).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: EnvSecretName(name), Namespace: ns}}
	if _, err := client.CoreV1().Secrets(ns).Create(context.Background(), secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestSweepExitedPods(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	old := time.Now().Add(-3 * time.Hour)
	agentPod(t, client, "tenant-acme", "done-old", "dev", old)
	agentPod(t, client, "tenant-acme", "unrecorded-old", "dev", old)
	agentPod(t, client, "tenant-acme", "done-recent", "dev", time.Now().Add(-10*time.Minute))
	agentPod(t, client, "tenant-acme", "running", "dev", time.Time{})
	agentPod(t, client, "tenant-acme", "chat-old", "chat", old)

	settled := func(_ context.Context, p ExitedPod) bool {
		if p.Name == "running" || p.Name == "done-recent" || p.Name == "chat-old" {
			t.Errorf("settled asked about %s, which the sweep must skip on its own", p.Name)
		}
		return p.InstanceID != "unrecorded-old-instance"
	}
	n, err := rt.SweepExitedPods(ctx, 2*time.Hour, settled)
	if err != nil || n != 1 {
		t.Fatalf("SweepExitedPods = %d, %v; want 1", n, err)
	}
	pods := client.CoreV1().Pods("tenant-acme")
	if _, err := pods.Get(ctx, "done-old", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("the settled pod past the grace is still there")
	}
	if _, err := client.CoreV1().Secrets("tenant-acme").Get(ctx, EnvSecretName("done-old"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("the swept pod's env Secret is still there")
	}
	for _, keep := range []string{"unrecorded-old", "done-recent", "running", "chat-old"} {
		if _, err := pods.Get(ctx, keep, metav1.GetOptions{}); err != nil {
			t.Errorf("pod %s was swept: %v", keep, err)
		}
	}
}

func TestPodFinishedAt(t *testing.T) {
	created := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	later := metav1.NewTime(created.Add(time.Hour))
	evicted := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: created},
		Status: corev1.PodStatus{
			Phase:      corev1.PodFailed,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, LastTransitionTime: later}},
		},
	}
	if got := PodFinishedAt(evicted); !got.Equal(later.Time) {
		t.Errorf("evicted pod finished at %v, want its last condition change %v", got, later.Time)
	}
	running := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if got := PodFinishedAt(running); !got.IsZero() {
		t.Errorf("running pod finished at %v, want zero", got)
	}
}

func TestTeardownTenant(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{Isolation: TenantIsolation{ControlPlaneNamespace: "tenant-ops"}})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	tenantNS(t, client, "sandboxes-acme", "acme")
	tenantNS(t, client, "tenant-globex", "globex")
	tenantNS(t, client, "tenant-ops", "ops")
	tenantNS(t, client, "octopus-sandboxes", "")
	if _, err := client.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-handmade"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := rt.TeardownTenant(ctx, "acme"); err != nil {
			t.Fatalf("TeardownTenant: %v", err)
		}
	}
	for _, gone := range []string{"tenant-acme", "sandboxes-acme"} {
		if _, err := client.CoreV1().Namespaces().Get(ctx, gone, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("namespace %s survived the teardown", gone)
		}
	}
	for _, ws := range []string{"handmade", "ops"} {
		if err := rt.TeardownTenant(ctx, ws); err != nil {
			t.Fatalf("TeardownTenant(%s): %v", ws, err)
		}
	}
	for _, keep := range []string{"tenant-globex", "tenant-ops", "octopus-sandboxes", "tenant-handmade"} {
		if _, err := client.CoreV1().Namespaces().Get(ctx, keep, metav1.GetOptions{}); err != nil {
			t.Errorf("namespace %s was deleted: %v", keep, err)
		}
	}
	if err := rt.TeardownTenant(ctx, "  "); err == nil {
		t.Error("an empty workspace id must be refused")
	}
}

func TestSweepStrandedPods(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	pending := func(name, stage, claim string, created metav1.Time) {
		t.Helper()
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "tenant-acme", CreationTimestamp: created,
				Labels: map[string]string{LabelManagedBy: managedByValue, LabelStage: stage, LabelTaskID: "t1"},
			},
			Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
				Name:         "workspace",
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}},
			}}},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		}
		if _, err := client.CoreV1().Pods("tenant-acme").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "octopus-wt-live", Namespace: "tenant-acme"}}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Create(ctx, claim, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pending("stranded", "in-progress", "octopus-wt-gone", old)
	pending("waiting-for-room", "in-progress", "octopus-wt-live", old)
	pending("just-created", "in-progress", "octopus-wt-gone", metav1.Now())
	pending("chat", "chat", "octopus-wt-gone", old)

	n, err := rt.SweepStrandedPods(ctx, 10*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("SweepStrandedPods = %d, %v; want 1", n, err)
	}
	pods := client.CoreV1().Pods("tenant-acme")
	if _, err := pods.Get(ctx, "stranded", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Error("the pod on a missing claim is still there")
	}
	for _, keep := range []string{"waiting-for-room", "just-created", "chat"} {
		if _, err := pods.Get(ctx, keep, metav1.GetOptions{}); err != nil {
			t.Errorf("pod %s was swept: %v", keep, err)
		}
	}
}
