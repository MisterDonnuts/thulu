// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type testPod struct {
	name, task, stage, instance string
	phase                       corev1.PodPhase
	node                        string
	created                     time.Time
	deadline                    int64
}

func makePod(t *testing.T, client *fake.Clientset, ns string, p testPod) {
	t.Helper()
	if p.phase == "" {
		p.phase = corev1.PodRunning
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: p.name, Namespace: ns, CreationTimestamp: metav1.NewTime(p.created),
			Labels:      map[string]string{LabelManagedBy: managedByValue, LabelTaskID: p.task, LabelStage: p.stage},
			Annotations: map[string]string{AnnotationInstanceID: p.instance},
		},
		Spec:   corev1.PodSpec{NodeName: p.node},
		Status: corev1.PodStatus{Phase: p.phase},
	}
	if p.deadline > 0 {
		pod.Spec.ActiveDeadlineSeconds = &p.deadline
	}
	if _, err := client.CoreV1().Pods(ns).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod %s: %v", p.name, err)
	}
}

func podNames(t *testing.T, client *fake.Clientset, ns string) []string {
	t.Helper()
	list, err := client.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	slices.Sort(out)
	return out
}

// A task's teardown finds its pods by label, whoever launched them, and leaves its chats and the
// pods of other tasks.
func TestDeleteTaskPodsDeletesOnlyTheTasksRunPods(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	ns := rt.NamespaceFor("acme")
	tenantNS(t, client, ns, "acme")
	now := time.Now()
	for _, p := range []testPod{
		{name: "run", task: "t1", stage: "in-progress", instance: "coder-t1-in_progress-1"},
		{name: "mention", task: "t1", stage: "mention", instance: "mention-t1-1"},
		{name: "chat", task: "t1", stage: chatStageLabel, instance: "chat-1"},
		{name: "other", task: "t2", stage: "in-progress", instance: "coder-t2-in_progress-1"},
	} {
		p.created = now
		makePod(t, client, ns, p)
	}
	var stopped []string
	n, err := rt.DeleteTaskPods(ctx, "acme", "t1", func(p RunningPod) bool {
		stopped = append(stopped, p.Name)
		return true
	})
	if err != nil || n != 2 {
		t.Fatalf("DeleteTaskPods = %d, %v; want 2", n, err)
	}
	slices.Sort(stopped)
	if !slices.Equal(stopped, []string{"mention", "run"}) {
		t.Fatalf("stop saw %v", stopped)
	}
	if got := podNames(t, client, ns); !slices.Equal(got, []string{"chat", "other"}) {
		t.Fatalf("pods left %v, want the chat and the other task's run", got)
	}
}

func TestSweepOrphanAgentPodsDeletesRunsOfGoneTasksAfterTheGrace(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	old := time.Now().Add(-time.Hour)
	for _, p := range []testPod{
		{name: "gone-old", instance: "gone", created: old},
		{name: "gone-young", instance: "gone", created: time.Now()},
		{name: "gone-exited", instance: "gone", created: old, phase: corev1.PodSucceeded},
		{name: "kept", instance: "kept", created: old},
		{name: "chat", instance: "gone", created: old, stage: chatStageLabel},
	} {
		makePod(t, client, "tenant-acme", p)
	}
	n, err := rt.SweepOrphanAgentPods(ctx, 2*time.Minute, func(_ context.Context, p RunningPod) bool {
		return p.InstanceID == "gone"
	})
	if err != nil || n != 1 {
		t.Fatalf("SweepOrphanAgentPods = %d, %v; want 1", n, err)
	}
	if got := podNames(t, client, "tenant-acme"); !slices.Equal(got, []string{"chat", "gone-exited", "gone-young", "kept"}) {
		t.Fatalf("pods left %v", got)
	}
}

// activeDeadlineSeconds never starts for a pod no node took; the sweep deletes it after its deadline
// plus the grace and says which one it took.
func TestSweepUnscheduledPods(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	for _, p := range []testPod{
		{name: "stuck", instance: "i-stuck", phase: corev1.PodPending, created: time.Now().Add(-2 * time.Hour), deadline: 3600},
		{name: "stuck-default", instance: "i-def", phase: corev1.PodPending, created: time.Now().Add(-5 * time.Hour)},
		{name: "young", instance: "i-young", phase: corev1.PodPending, created: time.Now().Add(-30 * time.Minute), deadline: 3600},
		{name: "on-a-node", instance: "i-node", phase: corev1.PodPending, node: "n1", created: time.Now().Add(-5 * time.Hour), deadline: 3600},
	} {
		makePod(t, client, "tenant-acme", p)
	}
	var gone []string
	n, err := rt.SweepUnscheduledPods(ctx, 4*time.Hour, 10*time.Minute, func(p RunningPod) { gone = append(gone, p.InstanceID) })
	if err != nil || n != 2 {
		t.Fatalf("SweepUnscheduledPods = %d, %v; want 2", n, err)
	}
	slices.Sort(gone)
	if !slices.Equal(gone, []string{"i-def", "i-stuck"}) {
		t.Fatalf("reported %v", gone)
	}
	if got := podNames(t, client, "tenant-acme"); !slices.Equal(got, []string{"on-a-node", "young"}) {
		t.Fatalf("pods left %v", got)
	}
}

// An env Secret whose pod is gone holds a run's credentials for nothing: the sweep deletes it once it
// is older than the launch window, and keeps one whose pod lives.
func TestSweepOrphanEnvSecrets(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	makePod(t, client, "tenant-acme", testPod{name: "live", created: time.Now()})
	secret := func(name string, labels map[string]string, created time.Time) {
		t.Helper()
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "tenant-acme", Labels: labels, CreationTimestamp: metav1.NewTime(created),
		}}
		if _, err := client.CoreV1().Secrets("tenant-acme").Create(ctx, s, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	run := map[string]string{LabelManagedBy: managedByValue}
	sandbox := map[string]string{LabelPlane: planeValue, LabelSandbox: "t1"}
	secret("gone-run-env", run, old)
	secret("sandbox-gone-env", sandbox, old)
	secret("live-env", run, old)
	secret("launching-env", run, time.Now())
	secret("registry", run, old)
	secret("foreign-env", nil, old)

	n, err := rt.SweepOrphanEnvSecrets(ctx, 10*time.Minute)
	if err != nil || n != 2 {
		t.Fatalf("SweepOrphanEnvSecrets = %d, %v; want 2", n, err)
	}
	for _, name := range []string{"gone-run-env", "sandbox-gone-env"} {
		if _, err := client.CoreV1().Secrets("tenant-acme").Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("orphan secret %s is still there (err=%v)", name, err)
		}
	}
	for _, name := range []string{"live-env", "launching-env", "registry", "foreign-env"} {
		if _, err := client.CoreV1().Secrets("tenant-acme").Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Errorf("secret %s was deleted: %v", name, err)
		}
	}
}
