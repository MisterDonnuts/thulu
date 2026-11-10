// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"os"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const itestWorkspace = "itest-kuberuntime"

func deleteNamespaceAndWait(ctx context.Context, t *testing.T, rt *Runtime, ns string) {
	t.Helper()
	err := rt.client.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("delete namespace %s: %v", ns, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		_, err := rt.client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("namespace %s still terminating after 90s", ns)
}

func waitExit(ctx context.Context, t *testing.T, rt *Runtime, pod, ns string, timeout time.Duration) int {
	t.Helper()
	got := make(chan int, 1)
	rt.WatchExit(ctx, pod, ns, func(code int) { got <- code })
	select {
	case code := <-got:
		return code
	case <-time.After(timeout):
		t.Fatalf("pod %s/%s did not terminate within %s", ns, pod, timeout)
		return 0
	}
}

func TestIntegrationKubeRuntime(t *testing.T) {
	kubeContext := os.Getenv("OCTOPUS_TEST_KUBECONTEXT")
	if kubeContext == "" {
		t.Skip("OCTOPUS_TEST_KUBECONTEXT not set — skipping live-cluster integration test")
	}

	rt, err := New(Config{
		Context:             kubeContext,
		AllowNoRuntimeClass: true,
		Image:               "busybox:stable",
		Isolation:           TenantIsolation{PodSecurity: PodSecurityBaseline},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	ns := rt.NamespaceFor(itestWorkspace)

	deleteNamespaceAndWait(ctx, t, rt, ns)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		deleteNamespaceAndWait(cleanupCtx, t, rt, ns)
	})

	if err := rt.EnsureTenantNamespace(ctx, itestWorkspace); err != nil {
		t.Fatalf("EnsureTenantNamespace: %v", err)
	}
	if err := rt.EnsureTenantNamespace(ctx, itestWorkspace); err != nil {
		t.Fatalf("EnsureTenantNamespace (second call, idempotency): %v", err)
	}
	nsObj, err := rt.client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if nsObj.Labels[LabelTenant] != itestWorkspace || nsObj.Labels[LabelPlane] != "data" {
		t.Errorf("namespace labels = %v, want tenant=%s plane=data", nsObj.Labels, itestWorkspace)
	}

	pod1, err := rt.LaunchPod(ctx, LaunchSpec{
		WorkspaceID: itestWorkspace,
		TaskID:      "itest-task-7",
		Stage:       "in_progress",
		InstanceID:  "itest-agent-exit7",
		Command:     []string{"sh", "-c", "echo hi; exit 7"},
		Env:         map[string]string{"TASK_ID": "itest-task-7"},
	})
	if err != nil {
		t.Fatalf("LaunchPod (exit 7): %v", err)
	}
	if !rt.PodAlive(ctx, pod1, ns) {
		t.Error("PodAlive must be true right after launch (pending/running)")
	}
	if code := waitExit(ctx, t, rt, pod1, ns, 3*time.Minute); code != 7 {
		t.Errorf("pod1 exit code = %d, want 7", code)
	}
	if rt.PodAlive(ctx, pod1, ns) {
		t.Error("PodAlive must be false after the pod terminated")
	}

	pod2, err := rt.LaunchPod(ctx, LaunchSpec{
		WorkspaceID: itestWorkspace,
		TaskID:      "itest-task-0",
		Stage:       "review",
		InstanceID:  "itest-agent-exit0",
		Command:     []string{"sh", "-c", "echo bye"},
		Env:         map[string]string{"TASK_ID": "itest-task-0"},
	})
	if err != nil {
		t.Fatalf("LaunchPod (exit 0): %v", err)
	}
	if code := waitExit(ctx, t, rt, pod2, ns, 3*time.Minute); code != 0 {
		t.Errorf("pod2 exit code = %d, want 0", code)
	}

	exited, err := rt.ListExited(ctx, ns)
	if err != nil {
		t.Fatalf("ListExited: %v", err)
	}
	byName := map[string]ExitedPod{}
	for _, e := range exited {
		byName[e.Name] = e
	}
	if len(exited) != 2 {
		t.Errorf("ListExited returned %d pods, want 2: %+v", len(exited), exited)
	}
	if e := byName[pod1]; e.ExitCode != 7 || e.TaskID != "itest-task-7" {
		t.Errorf("ListExited[%s] = %+v, want exit 7 / task itest-task-7", pod1, e)
	}
	if e := byName[pod2]; e.ExitCode != 0 || e.TaskID != "itest-task-0" {
		t.Errorf("ListExited[%s] = %+v, want exit 0 / task itest-task-0", pod2, e)
	}

	if err := rt.DeletePod(ctx, pod1, ns); err != nil {
		t.Fatalf("DeletePod: %v", err)
	}
	if err := rt.DeletePod(ctx, pod1, ns); err != nil {
		t.Errorf("DeletePod (second call, idempotency): %v", err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		if !rt.PodAlive(ctx, pod1, ns) {
			if _, err := rt.client.CoreV1().Pods(ns).Get(ctx, pod1, metav1.GetOptions{}); apierrors.IsNotFound(err) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pod %s still present 60s after delete", pod1)
		}
		time.Sleep(time.Second)
	}
	exited, err = rt.ListExited(ctx, ns)
	if err != nil {
		t.Fatalf("ListExited after delete: %v", err)
	}
	if len(exited) != 1 || exited[0].Name != pod2 {
		t.Errorf("ListExited after delete = %+v, want only %s", exited, pod2)
	}
}
