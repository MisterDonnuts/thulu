// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func running(at time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{State: corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(at)}}}
}

func done(from, to time.Time) corev1.ContainerStatus {
	return corev1.ContainerStatus{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		StartedAt: metav1.NewTime(from), FinishedAt: metav1.NewTime(to)}}}
}

func meteredPod(uid string, labels map[string]string, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: uid, UID: types.UID(uid), Labels: labels},
		Status:     corev1.PodStatus{ContainerStatuses: statuses},
	}
}

func TestPodSample(t *testing.T) {
	t0 := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	ws := map[string]string{LabelWorkspace: "acme"}

	s, ok := podSample(meteredPod("a", ws, running(t0.Add(time.Minute)), running(t0)), "")
	if !ok || !s.StartedAt.Equal(t0) || !s.EndedAt.IsZero() || s.WorkspaceID != "acme" || s.Key != "a" {
		t.Fatalf("running pod = %+v ok=%v", s, ok)
	}

	// One container done and the other still going: the pod is still running.
	s, _ = podSample(meteredPod("b", ws, done(t0, t0.Add(time.Hour)), running(t0)), "")
	if !s.EndedAt.IsZero() {
		t.Fatalf("half-finished pod ended at %s", s.EndedAt)
	}

	s, _ = podSample(meteredPod("c", ws, done(t0, t0.Add(time.Hour)), done(t0, t0.Add(2*time.Hour))), "")
	if !s.EndedAt.Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("finished pod ended at %s, want the last container's end", s.EndedAt)
	}

	// Still pulling its image: nothing to bill yet.
	waiting := corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}}
	if _, ok = podSample(meteredPod("d", ws, waiting), ""); ok {
		t.Fatal("a pod that never started was sampled")
	}

	s, ok = podSample(meteredPod("e", map[string]string{LabelSandbox: "sb1"}, running(t0)), "tenant-ws")
	if !ok || !s.Sandbox || s.WorkspaceID != "tenant-ws" {
		t.Fatalf("sandbox without a workspace label = %+v ok=%v", s, ok)
	}

	if _, ok = podSample(meteredPod("f", nil, running(t0)), ""); ok {
		t.Fatal("a pod with no workspace was sampled")
	}
}

func TestMeteredPods_OnlyTenantNamespaces(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	if _, err := client.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "octopus"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	for ns, uid := range map[string]string{"tenant-acme": "agent-1", "octopus": "daemon-1"} {
		p := meteredPod(uid, nil, running(t0))
		p.Namespace = ns
		if _, err := client.CoreV1().Pods(ns).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rt := &Runtime{client: client}
	got, err := rt.MeteredPods(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "agent-1" || got[0].WorkspaceID != "acme" {
		t.Fatalf("metered = %+v, want only the tenant's agent", got)
	}
}
