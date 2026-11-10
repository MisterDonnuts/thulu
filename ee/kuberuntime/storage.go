// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	EnvAgentMemoryRequest    = "OCTOPUS_KUBE_AGENT_MEMORY_REQUEST"
	EnvAgentMemoryLimit      = "OCTOPUS_KUBE_AGENT_MEMORY_LIMIT"
	EnvAgentCPURequest       = "OCTOPUS_KUBE_AGENT_CPU_REQUEST"
	EnvAgentCPULimit         = "OCTOPUS_KUBE_AGENT_CPU_LIMIT"
	EnvAgentEphemeralRequest = "OCTOPUS_KUBE_AGENT_EPHEMERAL_REQUEST"
	EnvAgentEphemeralLimit   = "OCTOPUS_KUBE_AGENT_EPHEMERAL_LIMIT"
	EnvEmptyDirSizeLimit     = "OCTOPUS_KUBE_EMPTYDIR_SIZE_LIMIT"
)

// Without an ephemeral-storage limit one pod can fill the node disk and DiskPressure evicts every
// tenant's pods; with it only that pod is evicted.
const (
	defaultEphemeralRequest  = "1Gi"
	defaultEphemeralLimit    = "20Gi"
	defaultEmptyDirSizeLimit = "10Gi"
)

func applySizingEnv(cfg *Config, env func(string) string) {
	cfg.MemoryRequest = env(EnvAgentMemoryRequest)
	cfg.MemoryLimit = env(EnvAgentMemoryLimit)
	cfg.CPURequest = env(EnvAgentCPURequest)
	cfg.CPULimit = env(EnvAgentCPULimit)
	cfg.EphemeralRequest = env(EnvAgentEphemeralRequest)
	cfg.EphemeralLimit = env(EnvAgentEphemeralLimit)
	cfg.EmptyDirSizeLimit = env(EnvEmptyDirSizeLimit)
}

func parseEmptyDirSizeLimit(v string) (resource.Quantity, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		v = defaultEmptyDirSizeLimit
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return resource.Quantity{}, apperr.WrapKind(apperr.KindInvalid, err, "kuberuntime.errors.parse_emptydir_size_limit", "value", v)
	}
	return q, nil
}

func (r *Runtime) worktreeEmptyDir() *corev1.EmptyDirVolumeSource {
	size := r.emptyDirSize.DeepCopy()
	if size.IsZero() {
		size = resource.MustParse(defaultEmptyDirSizeLimit)
	}
	return &corev1.EmptyDirVolumeSource{SizeLimit: &size}
}
