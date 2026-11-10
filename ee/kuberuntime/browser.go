// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/catrust"
)

const browserContainerName = "thulu-browser"

// Must match USER 1000:1000 in tools/thulu-browser/Dockerfile; pinned so RunAsNonRoot admission can
// verify it.
const browserUser = int64(1000)

const (
	browserTmpVolume    = "browser-tmp"
	browserSharedVolume = "browser-shared"
	browserShmVolume    = "browser-shm"
)

// The runtime's 64Mi /dev/shm default crashes Chromium tabs on real pages; the tmpfs counts against
// the memory limit.
var browserShmSize = resource.MustParse("1Gi")

var browserScratchSize = resource.MustParse("1Gi")

func browserResources(cfg browserrun.Config) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    cfg.CPURequest,
			corev1.ResourceMemory: cfg.MemoryRequest,
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    cfg.CPULimit,
			corev1.ResourceMemory: cfg.MemoryLimit,
		},
	}
}

// A browser over the tenant LimitRange max would make every agent pod granted the browser fail
// admission.
func (iso *TenantIsolation) validateBrowser(cfg browserrun.Config) error {
	if iso.Quota.Disabled {
		return nil
	}
	maxCPU, err := resource.ParseQuantity(iso.Quota.MaxCPU)
	if err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "kuberuntime.errors.limit_max_cpu", "value", iso.Quota.MaxCPU)
	}
	maxMem, err := resource.ParseQuantity(iso.Quota.MaxMemory)
	if err != nil {
		return apperr.WrapKind(apperr.KindInvalid, err, "kuberuntime.errors.limit_max_memory", "value", iso.Quota.MaxMemory)
	}
	if cfg.CPULimit.Cmp(maxCPU) > 0 {
		return apperr.Invalid("kuberuntime.errors.browser_cpu_over_max",
			"limit", cfg.CPULimit.String(), "max", maxCPU.String(), "env", browserrun.EnvCPULimit)
	}
	if cfg.MemoryLimit.Cmp(maxMem) > 0 {
		return apperr.Invalid("kuberuntime.errors.browser_memory_over_max",
			"limit", cfg.MemoryLimit.String(), "max", maxMem.String(), "env", browserrun.EnvMemoryLimit)
	}
	return nil
}

// Native sidecar (initContainer, restartPolicy Always) so the pod ends with the agent;
// terminalExitCode skips
// InitContainerStatuses, so the run's exit code stays the agent's. mcpTools pins granted tools
// server-side, where the agent cannot widen them.
func (r *Runtime) browserSidecar(secretName, mcpTools string) (corev1.Container, []corev1.Volume) {
	cfg := r.cfg.Browser
	always := corev1.ContainerRestartPolicyAlways

	literal := cfg.ServerEnv(mcpTools)
	keys := make([]string, 0, len(literal))
	for k := range literal {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]corev1.EnvVar, 0, len(keys)+3)
	for _, k := range keys {
		env = append(env, corev1.EnvVar{Name: k, Value: literal[k]})
	}
	fromSecret := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
		}}}
	}
	env = append(env,
		fromSecret(browserrun.DaemonURLEnv, browserrun.DaemonURLEnv),
		fromSecret(browserrun.RequiredKeyEnv, browserrun.RunKeyEnv),
	)

	mounts := []corev1.VolumeMount{
		{Name: browserTmpVolume, MountPath: "/tmp"},
		{Name: browserSharedVolume, MountPath: "/agents-shared"},
		{Name: browserShmVolume, MountPath: "/dev/shm"},
	}
	if r.CABundleConfigured() {
		// The CA volume itself is added by buildPod for the agent.
		mounts = append(mounts, caVolumeMount())
		env = append(env, corev1.EnvVar{Name: "NODE_EXTRA_CA_CERTS", Value: catrust.ContainerPath})
	}

	scratch := browserScratchSize
	shm := browserShmSize
	volumes := []corev1.Volume{
		{Name: browserTmpVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &scratch}}},
		{Name: browserSharedVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &scratch}}},
		{Name: browserShmVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			Medium: corev1.StorageMediumMemory, SizeLimit: &shm,
		}}},
	}

	return corev1.Container{
		Name:            browserContainerName,
		SecurityContext: browserSecurityContext(),
		Image:           cfg.Image,
		ImagePullPolicy: agentPullPolicy,
		RestartPolicy:   &always,
		Env:             env,
		Resources:       browserResources(cfg),
		VolumeMounts:    mounts,
	}, volumes
}

// Full restricted profile on this container: it runs code the agent sends. Chromium needs
// --no-sandbox
// (its sandbox needs user namespaces this profile forbids), so the boundary is the pod's
// RuntimeClass.
func browserSecurityContext() *corev1.SecurityContext {
	nonRoot, readOnly, noEscalation := true, true, false
	uid := browserUser
	return &corev1.SecurityContext{
		RunAsNonRoot:             &nonRoot,
		RunAsUser:                &uid,
		RunAsGroup:               &uid,
		AllowPrivilegeEscalation: &noEscalation,
		ReadOnlyRootFilesystem:   &readOnly,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func browserJobVolumes() ([]corev1.VolumeMount, []corev1.Volume) {
	scratch, shm := browserScratchSize, browserShmSize
	mounts := []corev1.VolumeMount{
		{Name: browserTmpVolume, MountPath: "/tmp"},
		{Name: browserShmVolume, MountPath: "/dev/shm"},
	}
	volumes := []corev1.Volume{
		{Name: browserTmpVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &scratch}}},
		{Name: browserShmVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			Medium: corev1.StorageMediumMemory, SizeLimit: &shm,
		}}},
	}
	return mounts, volumes
}
