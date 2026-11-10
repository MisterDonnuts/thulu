// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/MisterDonnuts/thulu/internal/kubeapi"
)

const (
	CacheClaimName   = kubeapi.CacheClaimName
	EnvCacheSize     = kubeapi.EnvCacheSize
	DefaultCacheSize = kubeapi.DefaultCacheSize
)

const LabelCache = "thulu.dev/cache"

func ParseCacheSize(v string) (resource.Quantity, bool, error) { return kubeapi.ParseCacheSize(v) }

type CacheClaim struct {
	Namespace   string
	WorkspaceID string
	Size        resource.Quantity
}

func buildCacheClaim(c CacheClaim) *corev1.PersistentVolumeClaim {
	size := c.Size.DeepCopy()
	if size.IsZero() {
		size = resource.MustParse(DefaultCacheSize)
	}
	labels := map[string]string{
		LabelPlane:     planeValue,
		LabelManagedBy: managedByValue,
		LabelCache:     "true",
	}
	if ws := sanitizeDNS1123(c.WorkspaceID); ws != "" {
		labels[LabelWorkspace] = ws
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: CacheClaimName, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			// RWO only works on a single node; multi-node clusters need a ReadWriteMany class or
			// sandboxes on other nodes wait on attach forever.
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}},
		},
	}
}

// Keep an existing claim as is: a PVC's request can only grow, and only on a storage class that
// allows expansion.
func EnsureCacheClaim(ctx context.Context, client kubernetes.Interface, c CacheClaim) error {
	return ensureClaim(ctx, client, c.Namespace, buildCacheClaim(c), claimErrKeys{
		create:      "kuberuntime.errors.create_cache_claim",
		get:         "kuberuntime.errors.get_cache_claim",
		terminating: "kuberuntime.errors.cache_claim_terminating",
	})
}
