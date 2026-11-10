// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"log"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

func (r *Runtime) CheckOwnPodSelector(ctx context.Context, podName string) {
	iso := r.cfg.Isolation
	if iso.ControlPlaneNamespace == "" || podName == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, nodeListTimeout)
	defer cancel()
	pod, err := r.client.CoreV1().Pods(iso.ControlPlaneNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return
	}
	keys := make([]string, 0, len(iso.ControlPlanePodSelector))
	for k := range iso.ControlPlanePodSelector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		want := iso.ControlPlanePodSelector[k]
		if got, ok := pod.Labels[k]; !ok || got != want {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.own_pod_selector_mismatch",
				"pod", iso.ControlPlaneNamespace+"/"+podName, "label", k+"="+want, "env", EnvControlPlanePodSelector))
			return
		}
	}
}
