// Copyright (c) 2026 Mister.D. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"log"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Name prefix of the sandbox pods internal/sandbox creates.
const SandboxPodPrefix = "sandbox-"

// Brings every tenant namespace that already exists to the current labels, policies and claim
// labels, so the sweeps (which select on LabelPlane) and the admission policy see what an older
// release created. Works from names only. A workspace without a namespace is left to its first
// launch.
func (r *Runtime) ReconcileTenantNamespaces(ctx context.Context, workspaceIDs []string) int {
	done := 0
	for _, ws := range workspaceIDs {
		tenant := sanitizeDNS1123(ws)
		if tenant == "" {
			continue
		}
		ns := r.NamespaceFor(ws)
		if _, err := r.client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.reconcile_namespace_failed", "ns", ns, "err", err))
			}
			continue
		}
		if err := r.EnsureNamespace(ctx, ns, tenant); err != nil {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.reconcile_namespace_failed", "ns", ns, "err", err))
			continue
		}
		r.labelClaims(ctx, ns, ws)
		done++
	}
	return done
}

// Claims are found by name: the worktree prefix and the cache claim name are the daemon's own.
func (r *Runtime) labelClaims(ctx context.Context, ns, workspaceID string) {
	cl := r.client.CoreV1().PersistentVolumeClaims(ns)
	list, err := cl.List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_list_claims_failed", "ns", ns, "err", err))
		return
	}
	for i := range list.Items {
		pvc := &list.Items[i]
		if pvc.DeletionTimestamp != nil || pvc.Labels[LabelPlane] == planeValue {
			continue
		}
		var want *corev1.PersistentVolumeClaim
		switch {
		case strings.HasPrefix(pvc.Name, WorktreeClaimPrefix):
			want = buildWorktreeClaim(WorktreeClaim{Name: pvc.Name, OwnerID: ClaimOwnerID(pvc), WorkspaceID: workspaceID})
		case pvc.Name == CacheClaimName:
			want = buildCacheClaim(CacheClaim{Namespace: ns, WorkspaceID: workspaceID})
		default:
			continue
		}
		mergeLabels(&pvc.ObjectMeta, want.Labels)
		if pvc.Annotations == nil {
			pvc.Annotations = map[string]string{}
		}
		for k, v := range want.Annotations {
			if _, ok := pvc.Annotations[k]; !ok {
				pvc.Annotations[k] = v
			}
		}
		if _, err := cl.Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.label_claim_failed", "ns", ns, "name", pvc.Name, "err", err))
		}
	}
}

// Every pod the daemon creates in a tenant namespace carries LabelManagedBy (runs, chats) or
// LabelPlane (sandboxes). One with neither, built the way only the daemon builds pods
// (madeByDaemon), was made by a release that used other label keys, and no sweep or lookup finds
// it any more. It is removed once it cannot be doing work: when it has ended, or when it is a
// sandbox (the next acquire recreates it by name). A run still going is left until it ends.
func (r *Runtime) SweepUnlabeledPods(ctx context.Context) (int, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, ns := range namespaces {
		list, err := r.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_unlabeled_failed", "ns", ns, "err", err))
			continue
		}
		for i := range list.Items {
			pod := &list.Items[i]
			if pod.Labels[LabelManagedBy] != "" || pod.Labels[LabelPlane] != "" || pod.DeletionTimestamp != nil ||
				!madeByDaemon(pod) {
				continue
			}
			_, ended := terminalExitCode(pod)
			if !ended && !strings.HasPrefix(pod.Name, SandboxPodPrefix) {
				continue
			}
			err := r.client.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_unlabeled_failed", "ns", ns, "err", err))
				continue
			}
			r.deleteEnvSecret(ctx, ns, pod.Name)
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.unlabeled_pod_removed", "ns", ns, "name", pod.Name, "phase", pod.Status.Phase))
			deleted++
		}
	}
	return deleted, nil
}

// Runs and chats read their env from a Secret named after the pod (EnvSecretName); sandboxes are
// named SandboxPodPrefix+task and mount the task's worktree claim. Anything else in the namespace,
// such as a pod an operator started by hand, is not the daemon's to delete.
func madeByDaemon(pod *corev1.Pod) bool {
	if strings.HasPrefix(pod.Name, SandboxPodPrefix) {
		for i := range pod.Spec.Volumes {
			pvc := pod.Spec.Volumes[i].PersistentVolumeClaim
			if pvc != nil && strings.HasPrefix(pvc.ClaimName, WorktreeClaimPrefix) {
				return true
			}
		}
	}
	return readsEnvSecret(pod.Spec.InitContainers, EnvSecretName(pod.Name)) ||
		readsEnvSecret(pod.Spec.Containers, EnvSecretName(pod.Name))
}

func readsEnvSecret(containers []corev1.Container, secret string) bool {
	for i := range containers {
		for _, e := range containers[i].Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == secret {
				return true
			}
		}
	}
	return false
}
