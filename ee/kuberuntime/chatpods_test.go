// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListChatPodsFiltersByStageLabel(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()

	ns := "octopus-tenant-ws1"
	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{LabelPlane: planeValue}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	createPod(t, client, ns, "chat-abc", map[string]string{LabelManagedBy: managedByValue, LabelStage: chatStageLabel})
	createPod(t, client, ns, "task-xyz", map[string]string{LabelManagedBy: managedByValue, LabelStage: "in_progress"})

	refs, err := rt.ListChatPods(ctx)
	if err != nil {
		t.Fatalf("ListChatPods: %v", err)
	}
	if len(refs) != 1 || refs[0].Name != "chat-abc" || refs[0].Namespace != ns {
		t.Fatalf("want one chat pod chat-abc in %s, got %+v", ns, refs)
	}
}

func TestEnsureChatExecServiceIsIdempotentAndSelectsPod(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	ns := "octopus-tenant-ws1"

	createPod(t, client, ns, "chat-abc", map[string]string{LabelManagedBy: managedByValue, LabelStage: chatStageLabel})

	if err := rt.EnsureChatExecService(ctx, "chat-abc", ns, 8587); err != nil {
		t.Fatalf("EnsureChatExecService: %v", err)
	}
	if err := rt.EnsureChatExecService(ctx, "chat-abc", ns, 8587); err != nil {
		t.Fatalf("EnsureChatExecService (2nd): %v", err)
	}

	svc, err := client.CoreV1().Services(ns).Get(ctx, "chat-abc", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if svc.Spec.Selector[LabelTaskID] != "chat-abc" || svc.Spec.Selector[LabelStage] != chatStageLabel {
		t.Fatalf("service selector = %v, want task-id=chat-abc stage=chat", svc.Spec.Selector)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8587 {
		t.Fatalf("service ports = %v, want one port 8587", svc.Spec.Ports)
	}

	if err := rt.DeleteService(ctx, "chat-abc", ns); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	if err := rt.DeleteService(ctx, "chat-abc", ns); err != nil {
		t.Fatalf("DeleteService (missing): %v", err)
	}
}

func createPod(t *testing.T, client *fake.Clientset, ns, name string, labels map[string]string) {
	t.Helper()
	if _, err := client.CoreV1().Pods(ns).Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod %s: %v", name, err)
	}
}
