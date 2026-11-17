// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
)

// MeteredPods lists every pod in the tenant namespaces with the time its containers ran: agents,
// auditors, chats and Evidence runners (launched here) and sandboxes (launched by internal/sandbox).
// The workspace comes from the pod's label, or from the namespace's tenant label for pods without one.
func (r *Runtime) MeteredPods(ctx context.Context) ([]entitlements.PodSample, error) {
	namespaces, err := r.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: LabelPlane + "=" + planeValue,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.list_tenant_namespaces_all")
	}
	var out []entitlements.PodSample
	for i := range namespaces.Items {
		ns := &namespaces.Items[i]
		pods, err := r.client.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, apperr.Wrap(err, "kuberuntime.errors.list_pods", "ns", ns.Name)
		}
		for j := range pods.Items {
			if s, ok := podSample(&pods.Items[j], ns.Labels[LabelTenant]); ok {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// Init containers (the worktree clone) and the time before the first container starts (scheduling,
// image pull) are not billed.
func podSample(pod *corev1.Pod, tenant string) (entitlements.PodSample, bool) {
	ws := pod.Labels[LabelWorkspace]
	if ws == "" {
		ws = tenant
	}
	if ws == "" || pod.UID == "" {
		return entitlements.PodSample{}, false
	}
	var start, end time.Time
	allDone := len(pod.Status.ContainerStatuses) > 0
	for i := range pod.Status.ContainerStatuses {
		st := pod.Status.ContainerStatuses[i].State
		switch {
		case st.Running != nil:
			allDone = false
			start = earliest(start, st.Running.StartedAt.Time)
		case st.Terminated != nil:
			start = earliest(start, st.Terminated.StartedAt.Time)
			if st.Terminated.FinishedAt.After(end) {
				end = st.Terminated.FinishedAt.Time
			}
		default:
			allDone = false
		}
	}
	if start.IsZero() {
		return entitlements.PodSample{}, false
	}
	if !allDone {
		end = time.Time{}
	}
	return entitlements.PodSample{
		Key:         string(pod.UID),
		WorkspaceID: ws,
		Sandbox:     pod.Labels[LabelSandbox] != "",
		StartedAt:   start,
		EndedAt:     end,
	}, true
}

func earliest(a, b time.Time) time.Time {
	if b.IsZero() {
		return a
	}
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}
