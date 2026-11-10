// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	intstr "k8s.io/apimachinery/pkg/util/intstr"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Chat pods are reached through a per-pod Service, never `pods/exec`: the daemon's ClusterRole
// withholds it.

const agentContainerName = "agent"

const chatStageLabel = "chat"

type ChatPodRef struct {
	Name      string
	Namespace string
}

func (r *Runtime) ListChatPods(ctx context.Context) ([]ChatPodRef, error) {
	sel := fmt.Sprintf("%s=%s,%s=%s", LabelManagedBy, managedByValue, LabelStage, chatStageLabel)
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	var out []ChatPodRef
	for _, ns := range namespaces {
		pods, err := r.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			return nil, apperr.Wrap(err, "kuberuntime.errors.list_chat_pods", "ns", ns)
		}
		for i := range pods.Items {
			out = append(out, ChatPodRef{Name: pods.Items[i].Name, Namespace: ns})
		}
	}
	return out, nil
}

// The daemon dials http://<pod>.<ns>.svc:<port>, so the Service name must equal the pod name.
// Owned by the pod so GC removes it even if explicit teardown is missed.
func (r *Runtime) EnsureChatExecService(ctx context.Context, podName, namespace string, port int32) error {
	pod, err := r.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.get_chat_pod", "ns", namespace, "pod", podName)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   podName,
			Labels: map[string]string{LabelPlane: planeValue, LabelManagedBy: managedByValue, LabelStage: chatStageLabel},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "Pod", //i18n:ignore Kubernetes object kind
				Name:       pod.Name,
				UID:        pod.UID,
			}},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				LabelTaskID: sanitizeDNS1123(podName),
				LabelStage:  chatStageLabel,
			},
			Ports: []corev1.ServicePort{{
				Name:       "exec",
				Port:       port,
				TargetPort: intstr.FromInt32(port),
			}},
		},
	}
	_, err = r.client.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.create_chat_exec_service", "ns", namespace, "pod", podName)
	}
	return nil
}

func (r *Runtime) DeleteService(ctx context.Context, name, namespace string) error {
	err := r.client.CoreV1().Services(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.delete_service", "ns", namespace, "name", name)
	}
	return nil
}
