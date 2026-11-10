// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/MisterDonnuts/thulu/internal/logx"
)

// A shared informer, not a bare watch: it relists when the watch expires, so a missed pod end still
// arrives.
func (r *Runtime) WatchPodEnds(ctx context.Context, ended func(namespace string)) {
	factory := informers.NewSharedInformerFactoryWithOptions(r.client, 0,
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.LabelSelector = LabelManagedBy + "=" + managedByValue
		}))
	inf := factory.Core().V1().Pods().Informer()
	_, err := inf.AddEventHandler(podEndHandler(ended))
	if err != nil {
		logx.Warn(err, "kuberuntime.log.watch_pod_ends")
		return
	}
	factory.Start(ctx.Done())
	<-ctx.Done()
	factory.Shutdown()
}

func podEndHandler(ended func(namespace string)) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj any) {
			before, ok1 := oldObj.(*corev1.Pod)
			after, ok2 := newObj.(*corev1.Pod)
			if ok1 && ok2 && !podTerminal(before) && podTerminal(after) {
				ended(after.Namespace)
			}
		},
		DeleteFunc: func(obj any) {
			if tomb, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tomb.Obj
			}
			if pod, ok := obj.(*corev1.Pod); ok && !podTerminal(pod) {
				ended(pod.Namespace)
			}
		},
	}
}

func podTerminal(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}
