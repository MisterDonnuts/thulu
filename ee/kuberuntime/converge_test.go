// Copyright (c) 2026 Mister.D. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A label key no release of the daemon reads, standing in for the ones an older release wrote.
const foreignLabel = "example.com/plane"

func TestReconcileTenantNamespacesRelabelsWhatExists(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	core := client.CoreV1()
	if _, err := core.Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "tenant-acme", Labels: map[string]string{foreignLabel: "data"},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{WorktreeClaimPrefix + "root1", CacheClaimName, "someone-elses"} {
		if _, err := core.PersistentVolumeClaims("tenant-acme").Create(ctx, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{foreignLabel: "data"}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	if got := rt.ReconcileTenantNamespaces(ctx, []string{"acme", "beta"}); got != 1 {
		t.Fatalf("reconciled %d namespaces, want 1", got)
	}

	ns, err := core.Namespaces().Get(ctx, "tenant-acme", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Labels[LabelPlane] != planeValue || ns.Labels[LabelTenant] != "acme" || ns.Labels[foreignLabel] != "data" {
		t.Fatalf("namespace labels = %v", ns.Labels)
	}
	if _, err := core.Namespaces().Get(ctx, "tenant-beta", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a workspace without a namespace got one: %v", err)
	}
	for name, want := range map[string]string{WorktreeClaimPrefix + "root1": planeValue, CacheClaimName: planeValue, "someone-elses": ""} {
		pvc, err := core.PersistentVolumeClaims("tenant-acme").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if pvc.Labels[LabelPlane] != want {
			t.Errorf("claim %s plane label = %q, want %q", name, pvc.Labels[LabelPlane], want)
		}
	}
	if pvc, _ := core.PersistentVolumeClaims("tenant-acme").Get(ctx, CacheClaimName, metav1.GetOptions{}); pvc.Labels[LabelWorkspace] != "acme" {
		t.Errorf("cache claim workspace label = %v", pvc.Labels)
	}
}

func TestSweepUnlabeledPodsOnlyTakesTheDaemonsEndedOnes(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	runPod := func(name string, phase corev1.PodPhase, labels map[string]string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Env: []corev1.EnvVar{{
				Name: "X", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: EnvSecretName(name)}, Key: "X",
				}},
			}}}}},
			Status: corev1.PodStatus{Phase: phase},
		}
	}
	sandbox := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: SandboxPodPrefix + "t1"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: WorktreeClaimPrefix + "t1"},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	handMade := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "debug"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	handSandbox := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: SandboxPodPrefix + "mine"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	pods := []*corev1.Pod{
		runPod("ended-run", corev1.PodSucceeded, map[string]string{foreignLabel: "x"}),
		runPod("live-run", corev1.PodRunning, nil),
		runPod("current-run", corev1.PodFailed, map[string]string{LabelManagedBy: managedByValue}),
		sandbox, handMade, handSandbox,
	}
	for _, p := range pods {
		if _, err := client.CoreV1().Pods("tenant-acme").Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := rt.SweepUnlabeledPods(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d pods, want 2", n)
	}
	for name, gone := range map[string]bool{
		"ended-run": true, SandboxPodPrefix + "t1": true,
		"live-run": false, "current-run": false, "debug": false, SandboxPodPrefix + "mine": false,
	} {
		_, err := client.CoreV1().Pods("tenant-acme").Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) != gone {
			t.Errorf("pod %s: gone = %v, want %v", name, apierrors.IsNotFound(err), gone)
		}
	}
}
