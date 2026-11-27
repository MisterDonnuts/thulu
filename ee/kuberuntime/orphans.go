// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// DeleteTaskPods deletes the pods a task's runs started in the tenant namespace: stage runs,
// auditors, mentions and subtasks, found by the task label, never by a launcher's memory. Chat pods
// are the chat runtime's. stop sees each pod before its delete and answers false for one that is
// not the task's (the label is a lossy sanitization of the id).
func (r *Runtime) DeleteTaskPods(ctx context.Context, workspaceID, taskID string, stop func(RunningPod) bool) (int, error) {
	if taskID == "" {
		return 0, nil
	}
	ns := r.NamespaceFor(workspaceID)
	list, err := r.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s,%s=%s", LabelManagedBy, managedByValue, LabelTaskID, sanitizeDNS1123(taskID)),
	})
	if err != nil {
		return 0, apperr.Wrap(err, "kuberuntime.errors.list_pods", "ns", ns)
	}
	deleted := 0
	var errs []error
	for i := range list.Items {
		pod := &list.Items[i]
		if pod.Labels[LabelStage] == chatStageLabel || pod.DeletionTimestamp != nil {
			continue
		}
		info := podInfo(pod)
		if stop != nil && !stop(info) {
			continue
		}
		if err := r.DeletePod(ctx, pod.Name, ns); err != nil {
			errs = append(errs, err)
			continue
		}
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.task_pod_deleted",
			"ns", ns, "pod", pod.Name, "task", taskID, "instance", info.InstanceID, "phase", pod.Status.Phase))
		deleted++
	}
	return deleted, errors.Join(errs...)
}

// SweepOrphanAgentPods deletes the agent pods still running that orphan claims, once they are
// older than grace: a run of a task deleted while a teardown failed half way, or by an older daemon.
// Chat pods are the chat runtime's.
func (r *Runtime) SweepOrphanAgentPods(ctx context.Context, grace time.Duration, orphan func(context.Context, RunningPod) bool) (int, error) {
	return r.sweepPods(ctx, func(ctx context.Context, pod *corev1.Pod) (string, bool) {
		info := podInfo(pod)
		if !info.Running || time.Since(pod.CreationTimestamp.Time) < grace || !orphan(ctx, info) {
			return "", false
		}
		return "kuberuntime.log.orphan_agent_pod_deleted", true
	}, nil)
}

// SweepUnscheduledPods deletes the agent pods no node ever took after their run deadline plus
// grace. activeDeadlineSeconds counts from the start a node records, so such a pod would wait
// forever, holding quota and a Secret with credentials. A pod without a deadline gets
// defaultDeadline. gone sees each deleted pod, for a note on its task.
func (r *Runtime) SweepUnscheduledPods(ctx context.Context, defaultDeadline, grace time.Duration, gone func(RunningPod)) (int, error) {
	return r.sweepPods(ctx, func(_ context.Context, pod *corev1.Pod) (string, bool) {
		if pod.Spec.NodeName != "" || pod.Status.Phase != corev1.PodPending {
			return "", false
		}
		limit := defaultDeadline
		if d := pod.Spec.ActiveDeadlineSeconds; d != nil && *d > 0 {
			limit = time.Duration(*d) * time.Second
		}
		if time.Since(pod.CreationTimestamp.Time) < limit+grace {
			return "", false
		}
		return "kuberuntime.log.unscheduled_pod_deleted", true
	}, gone)
}

// sweepPods deletes, in every managed namespace, the agent pods pick chooses, and logs each with the
// key pick returns. Chat pods and pods already being deleted are skipped.
func (r *Runtime) sweepPods(ctx context.Context, pick func(context.Context, *corev1.Pod) (string, bool), deleted func(RunningPod)) (int, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
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
			key, ok := pick(ctx, pod)
			if !ok {
				continue
			}
			if err := r.DeletePod(ctx, pod.Name, ns); err != nil {
				log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.sweep_failed", "err", err))
				continue
			}
			info := podInfo(pod)
			log.Printf("[kuberuntime] %s", i18n.Log(key, "ns", ns, "pod", pod.Name, "task", info.TaskID,
				"instance", info.InstanceID, "age", time.Since(pod.CreationTimestamp.Time).Round(time.Second)))
			if deleted != nil {
				deleted(info)
			}
			n++
		}
	}
	return n, nil
}

// envSecretSelectors pick the per-pod env Secrets: a run's or a chat's carries its pod's managed-by
// label, a sandbox's its pod's plane label.
var envSecretSelectors = []string{LabelManagedBy + "=" + managedByValue, LabelPlane + "=" + planeValue}

// SweepOrphanEnvSecrets deletes the per-pod env Secrets (API key, LLM credentials, proxy) whose pod
// is gone, once they are older than minAge: a daemon that died between the Secret and the pod, a
// rollback delete that failed, a pod deleted by hand. A Secret whose pod lives but which never got
// its owner reference gets it now, so the pod's deletion takes it along.
func (r *Runtime) SweepOrphanEnvSecrets(ctx context.Context, minAge time.Duration) (int, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return 0, err
	}
	deleted := 0
	var errs []error
	for _, ns := range namespaces {
		seen := map[string]bool{}
		for _, sel := range envSecretSelectors {
			list, err := r.client.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
			if err != nil {
				errs = append(errs, apperr.Wrap(err, "kuberuntime.errors.list_secrets", "ns", ns))
				continue
			}
			for i := range list.Items {
				s := &list.Items[i]
				if seen[s.Name] {
					continue
				}
				seen[s.Name] = true
				if r.settleEnvSecret(ctx, ns, s, minAge) {
					deleted++
				}
			}
		}
	}
	return deleted, errors.Join(errs...)
}

func (r *Runtime) settleEnvSecret(ctx context.Context, ns string, s *corev1.Secret, minAge time.Duration) bool {
	podName, ok := strings.CutSuffix(s.Name, "-env")
	if !ok || podName == "" || s.DeletionTimestamp != nil || time.Since(s.CreationTimestamp.Time) < minAge {
		return false
	}
	pod, err := r.client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	switch {
	case err == nil:
		if !ownedByUID(s.OwnerReferences, pod.UID) {
			r.ownSecretByPod(ctx, ns, s, pod)
		}
		return false
	case !apierrors.IsNotFound(err):
		return false
	}
	err = r.client.CoreV1().Secrets(ns).Delete(ctx, s.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &s.UID},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.delete_env_secret_failed", "ns", ns, "secret", s.Name, "err", err))
		return false
	}
	log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.orphan_env_secret_deleted",
		"ns", ns, "secret", s.Name, "age", time.Since(s.CreationTimestamp.Time).Round(time.Second)))
	return true
}
