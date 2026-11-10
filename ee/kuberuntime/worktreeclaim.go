// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

const WorktreeClaimPrefix = "octopus-wt-"

// The thulu.dev/task-id label holds the sanitized id; this annotation holds the exact one the
// sweep asks about.
const AnnotationOwnerID = "thulu.dev/owner-id"

const defaultWorktreeClaimSize = "10Gi"

// A terminating claim goes only after the last pod using it is gone, and a Kata pod is slow to
// unwind.
var (
	claimTerminatingTimeout = 2 * time.Minute
	claimTerminatingPoll    = 500 * time.Millisecond
)

type WorktreeClaim struct {
	Namespace   string
	Name        string
	Size        string
	OwnerID     string
	WorkspaceID string
}

// A claim without AnnotationOwnerID carries only the sanitized id, in its name.
func ClaimOwnerID(pvc *corev1.PersistentVolumeClaim) string {
	if id := pvc.Annotations[AnnotationOwnerID]; id != "" {
		return id
	}
	return strings.TrimPrefix(pvc.Name, WorktreeClaimPrefix)
}

// A claim without these labels carries only thulu.dev/plane; only the sweep finds it.
func WorktreeClaimSelector(ownerID string) string {
	return fmt.Sprintf("%s=%s,%s=%s", LabelManagedBy, managedByValue, LabelTaskID, sanitizeDNS1123(ownerID))
}

func buildWorktreeClaim(c WorktreeClaim) *corev1.PersistentVolumeClaim {
	size, err := resource.ParseQuantity(c.Size)
	if err != nil {
		size = resource.MustParse(defaultWorktreeClaimSize)
	}
	owner := c.OwnerID
	if owner == "" {
		owner = strings.TrimPrefix(c.Name, WorktreeClaimPrefix)
	}
	labels := map[string]string{
		LabelPlane:     planeValue,
		LabelManagedBy: managedByValue,
		LabelTaskID:    sanitizeDNS1123(owner),
	}
	if ws := sanitizeDNS1123(c.WorkspaceID); ws != "" {
		labels[LabelWorkspace] = ws
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        c.Name,
			Labels:      labels,
			Annotations: map[string]string{AnnotationOwnerID: owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}},
		},
	}
}

// A terminating claim (a release raced a relaunch) must be waited out and recreated: a pod created
// against it stays Pending forever.
func EnsureWorktreeClaim(ctx context.Context, client kubernetes.Interface, c WorktreeClaim) error {
	return ensureClaim(ctx, client, c.Namespace, buildWorktreeClaim(c), claimErrKeys{
		create:      "kuberuntime.errors.create_worktree_claim",
		get:         "kuberuntime.errors.get_worktree_claim",
		terminating: "kuberuntime.errors.worktree_claim_terminating",
	})
}

type claimErrKeys struct{ create, get, terminating string }

func ensureClaim(ctx context.Context, client kubernetes.Interface, ns string, want *corev1.PersistentVolumeClaim, keys claimErrKeys) error {
	cl := client.CoreV1().PersistentVolumeClaims(ns)
	deadline := time.Now().Add(claimTerminatingTimeout)
	for {
		_, err := cl.Create(ctx, want, metav1.CreateOptions{})
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return apperr.Wrap(err, keys.create, "ns", ns, "name", want.Name)
		}
		existing, err := cl.Get(ctx, want.Name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return apperr.Wrap(err, keys.get, "ns", ns, "name", want.Name)
		case existing.DeletionTimestamp == nil:
			return nil
		}
		if time.Now().After(deadline) {
			return apperr.Timeout(keys.terminating, "ns", ns, "name", want.Name, "timeout", claimTerminatingTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(claimTerminatingPoll):
		}
	}
}

// Quota refusals fail the launch: the sandbox side would be refused too, so the pod would wait
// forever.
// Other errors are only logged; the pod stays Pending until the sandbox side creates the claim.
func (r *Runtime) ensureWorktreeClaim(ctx context.Context, ns string, spec LaunchSpec) error {
	owner := spec.WorktreeOwnerID
	if owner == "" {
		owner = spec.TaskID
	}
	err := EnsureWorktreeClaim(ctx, r.client, WorktreeClaim{
		Namespace:   ns,
		Name:        spec.WorktreePVC,
		OwnerID:     owner,
		WorkspaceID: spec.WorkspaceID,
	})
	if quotaErr := QuotaRefusal(err); quotaErr != nil {
		return quotaErr
	}
	if err != nil {
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.ensure_worktree_claim_failed", "ns", ns, "name", spec.WorktreePVC, "err", err))
	}
	return nil
}

// Pods being deleted are excluded on purpose: the pvc-protection finalizer keeps a claim they still
// mount until they are gone.
func claimsInUse(ctx context.Context, client kubernetes.Interface, ns string) (map[string]bool, error) {
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.list_pods", "ns", ns)
	}
	inUse := map[string]bool{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil ||
			pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		for j := range pod.Spec.Volumes {
			if pvc := pod.Spec.Volumes[j].PersistentVolumeClaim; pvc != nil {
				inUse[pvc.ClaimName] = true
			}
		}
	}
	return inUse, nil
}

func DeleteWorktreeClaim(ctx context.Context, client kubernetes.Interface, ns, name string) (bool, error) {
	inUse, err := claimsInUse(ctx, client, ns)
	if err != nil {
		return false, err
	}
	if inUse[name] {
		return false, nil
	}
	return deleteClaim(ctx, client, ns, name)
}

func deleteClaim(ctx context.Context, client kubernetes.Interface, ns, name string) (bool, error) {
	err := client.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, apperr.Wrap(err, "kuberuntime.errors.delete_worktree_claim", "ns", ns, "name", name)
	}
	return true, nil
}

func ReleaseWorktreeClaims(ctx context.Context, client kubernetes.Interface, ownerID string) (int, error) {
	if sanitizeDNS1123(ownerID) == "" {
		return 0, nil
	}
	list, err := client.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{
		LabelSelector: WorktreeClaimSelector(ownerID),
	})
	if err != nil {
		return 0, apperr.Wrap(err, "kuberuntime.errors.list_worktree_claims", "owner", ownerID)
	}
	deleted := 0
	for i := range list.Items {
		pvc := &list.Items[i]
		if ClaimOwnerID(pvc) != ownerID || pvc.DeletionTimestamp != nil {
			continue
		}
		ok, err := DeleteWorktreeClaim(ctx, client, pvc.Namespace, pvc.Name)
		if err != nil {
			return deleted, err
		}
		if ok {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.worktree_claim_released",
				"ns", pvc.Namespace, "name", pvc.Name, "task", ownerID))
			deleted++
		}
	}
	return deleted, nil
}

// ownerID is the sandbox-root task id: the answer must cover the whole subtask group sharing the
// claim.
type ClaimReleasable func(ctx context.Context, ownerID string) (bool, error)

func (r *Runtime) SweepWorktreeClaims(ctx context.Context, releasable ClaimReleasable) (int, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, ns := range namespaces {
		deleted += r.sweepClaimsIn(ctx, ns, releasable)
	}
	return deleted, nil
}

func (r *Runtime) sweepClaimsIn(ctx context.Context, ns string, releasable ClaimReleasable) int {
	// Select on plane, not managed-by: some claims carry only the plane label. The name prefix
	// filters out other claims.
	list, err := r.client.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
		LabelSelector: LabelPlane + "=" + planeValue,
	})
	if err != nil {
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_list_claims_failed", "ns", ns, "err", err))
		return 0
	}
	var inUse map[string]bool
	deleted := 0
	for i := range list.Items {
		pvc := &list.Items[i]
		if !strings.HasPrefix(pvc.Name, WorktreeClaimPrefix) || pvc.DeletionTimestamp != nil {
			continue
		}
		owner := ClaimOwnerID(pvc)
		ok, err := releasable(ctx, owner)
		if err != nil {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_claim_failed", "ns", ns, "name", pvc.Name, "task", owner, "err", err))
			continue
		}
		if !ok {
			continue
		}
		if inUse == nil {
			if inUse, err = claimsInUse(ctx, r.client, ns); err != nil {
				log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_failed", "err", err))
				return deleted
			}
		}
		if inUse[pvc.Name] {
			continue
		}
		gone, err := deleteClaim(ctx, r.client, ns, pvc.Name)
		if err != nil {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_failed", "err", err))
			continue
		}
		if gone {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_claim_deleted", "ns", ns, "name", pvc.Name, "task", owner))
			deleted++
		}
	}
	return deleted
}
