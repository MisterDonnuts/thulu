// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/MisterDonnuts/thulu/internal/kubeapi"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Set by internal/sandbox/kube.go; the value is the sanitized sandbox-root task id.
const LabelSandbox = kubeapi.LabelSandbox

func PodFinishedAt(pod *corev1.Pod) time.Time {
	if _, done := terminalExitCode(pod); !done {
		return time.Time{}
	}
	var at time.Time
	for i := range pod.Status.ContainerStatuses {
		if term := pod.Status.ContainerStatuses[i].State.Terminated; term != nil && term.FinishedAt.After(at) {
			at = term.FinishedAt.Time
		}
	}
	if !at.IsZero() {
		return at
	}
	for i := range pod.Status.Conditions {
		if t := pod.Status.Conditions[i].LastTransitionTime.Time; t.After(at) {
			at = t
		}
	}
	if !at.IsZero() {
		return at
	}
	return pod.CreationTimestamp.Time
}

type ExitSettled func(ctx context.Context, pod ExitedPod) bool

// Chat pods are skipped: the chat runtime owns them. settled must hold back pods whose exit a
// restart lost,
// because Reconcile replays that exit from the pod itself.
func (r *Runtime) SweepExitedPods(ctx context.Context, olderThan time.Duration, settled ExitSettled) (int, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	deleted := 0
	for _, ns := range namespaces {
		list, err := r.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: LabelManagedBy + "=" + managedByValue,
		})
		if err != nil {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_list_pods_failed", "ns", ns, "err", err))
			continue
		}
		for i := range list.Items {
			pod := &list.Items[i]
			if pod.Labels[LabelStage] == chatStageLabel || pod.DeletionTimestamp != nil {
				continue
			}
			code, done := terminalExitCode(pod)
			if !done || PodFinishedAt(pod).After(cutoff) {
				continue
			}
			exited := ExitedPod{
				Name:       pod.Name,
				TaskID:     pod.Labels[LabelTaskID],
				InstanceID: pod.Annotations[AnnotationInstanceID],
				AgentType:  pod.Annotations[AnnotationAgentType],
				ExitCode:   code,
			}
			if !settled(ctx, exited) {
				continue
			}
			if err := r.DeletePod(ctx, pod.Name, ns); err != nil {
				log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_failed", "err", err))
				continue
			}
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_deleted_pod",
				"ns", ns, "pod", pod.Name, "age", time.Since(PodFinishedAt(pod)).Round(time.Second)))
			deleted++
		}
	}
	return deleted, nil
}

// SweepStrandedPods deletes agent pods that can never start: Pending for longer than olderThan
// and mounting a PersistentVolumeClaim that no longer exists (the task's worktree claim went with
// its teardown while the pod was still waiting). SweepExitedPods never sees them, since they never
// ran. A pod Pending for lack of room or quota keeps its claims and is left alone. Chat pods are
// the chat runtime's.
func (r *Runtime) SweepStrandedPods(ctx context.Context, olderThan time.Duration) (int, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	deleted := 0
	for _, ns := range namespaces {
		list, err := r.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: LabelManagedBy + "=" + managedByValue,
		})
		if err != nil {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_list_pods_failed", "ns", ns, "err", err))
			continue
		}
		for i := range list.Items {
			pod := &list.Items[i]
			if pod.Labels[LabelStage] == chatStageLabel || pod.DeletionTimestamp != nil ||
				pod.Status.Phase != corev1.PodPending || pod.CreationTimestamp.After(cutoff) {
				continue
			}
			claim := r.missingClaim(ctx, ns, pod)
			if claim == "" {
				continue
			}
			if err := r.DeletePod(ctx, pod.Name, ns); err != nil {
				log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_failed", "err", err))
				continue
			}
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_deleted_stranded_pod",
				"ns", ns, "pod", pod.Name, "claim", claim, "age", time.Since(pod.CreationTimestamp.Time).Round(time.Minute)))
			deleted++
		}
	}
	return deleted, nil
}

// missingClaim is the first claim pod mounts that the API answers NotFound for; "" when every
// claim exists or one could not be read.
func (r *Runtime) missingClaim(ctx context.Context, ns string, pod *corev1.Pod) string {
	for i := range pod.Spec.Volumes {
		v := &pod.Spec.Volumes[i]
		if v.PersistentVolumeClaim == nil {
			continue
		}
		_, err := r.client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, v.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return v.PersistentVolumeClaim.ClaimName
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

// Assumes the daemon creates nothing for a tenant outside its namespaces; anything created
// elsewhere must be deleted here too.
func (r *Runtime) TeardownTenant(ctx context.Context, workspaceID string) error {
	tenant := sanitizeDNS1123(workspaceID)
	if tenant == "" {
		return apperr.Invalid("kuberuntime.errors.teardown_workspace_required")
	}
	names := map[string]bool{r.NamespaceFor(workspaceID): true}
	list, err := r.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s,%s=%s", LabelPlane, planeValue, LabelTenant, tenant),
	})
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.list_tenant_namespaces", "tenant", tenant)
	}
	for i := range list.Items {
		names[list.Items[i].Name] = true
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	var errs []error
	for _, name := range sorted {
		if err := r.deleteTenantNamespace(ctx, name, tenant); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Runtime) deleteTenantNamespace(ctx context.Context, name, tenant string) error {
	nsClient := r.client.CoreV1().Namespaces()
	ns, err := nsClient.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.get_namespace", "name", name)
	}
	if name == r.cfg.Isolation.ControlPlaneNamespace ||
		ns.Labels[LabelPlane] != planeValue || ns.Labels[LabelTenant] != tenant {
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.teardown_namespace_not_owned",
			"tenant", tenant, "name", name, "plane_label", LabelPlane, "plane", planeValue, "tenant_label", LabelTenant))
		return nil
	}
	if ns.DeletionTimestamp != nil {
		return nil
	}
	// The UID precondition pins the delete to the namespace inspected above; one recreated under
	// the same name fails with Conflict.
	uid := ns.UID
	err = nsClient.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.delete_namespace", "name", name)
	}
	log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.teardown_namespace_deleted", "tenant", tenant, "name", name))
	return nil
}
