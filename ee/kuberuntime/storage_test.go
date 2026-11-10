// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes/fake"
)

func quantity(t *testing.T, l corev1.ResourceList, name corev1.ResourceName) string {
	t.Helper()
	q, ok := l[name]
	if !ok {
		t.Fatalf("%s missing from %v", name, l)
	}
	return q.String()
}

func TestBuildPodResourceDefaults(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RunAsNonRoot: true})
	c := rt.buildPod(baseSpec()).Spec.Containers[0]
	want := map[corev1.ResourceName][2]string{
		corev1.ResourceMemory:           {"512Mi", "6Gi"},
		corev1.ResourceCPU:              {"250m", "2"},
		corev1.ResourceEphemeralStorage: {"1Gi", "20Gi"},
	}
	for name, w := range want {
		if got := quantity(t, c.Resources.Requests, name); got != w[0] {
			t.Errorf("%s request = %s, want %s", name, got, w[0])
		}
		if got := quantity(t, c.Resources.Limits, name); got != w[1] {
			t.Errorf("%s limit = %s, want %s", name, got, w[1])
		}
	}
}

func TestConfigFromEnvSizing(t *testing.T) {
	env := map[string]string{
		EnvAgentMemoryRequest:    "1Gi",
		EnvAgentMemoryLimit:      "4Gi",
		EnvAgentCPURequest:       "500m",
		EnvAgentCPULimit:         "1",
		EnvAgentEphemeralRequest: "2Gi",
		EnvAgentEphemeralLimit:   "8Gi",
		EnvEmptyDirSizeLimit:     "3Gi",
		EnvAllowNoRuntimeClass:   "1",
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	rt, _ := newFakeRuntime(t, cfg)
	spec := baseSpec()
	spec.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://d/git/t/app"}}
	pod := rt.buildPod(spec)
	c := pod.Spec.Containers[0]
	if got := quantity(t, c.Resources.Limits, corev1.ResourceMemory); got != "4Gi" {
		t.Errorf("memory limit = %s", got)
	}
	if got := quantity(t, c.Resources.Requests, corev1.ResourceCPU); got != "500m" {
		t.Errorf("cpu request = %s", got)
	}
	if got := quantity(t, c.Resources.Limits, corev1.ResourceEphemeralStorage); got != "8Gi" {
		t.Errorf("ephemeral limit = %s", got)
	}
	ed := pod.Spec.Volumes[0].EmptyDir
	if ed == nil || ed.SizeLimit == nil || ed.SizeLimit.String() != "3Gi" {
		t.Errorf("emptyDir = %+v, want sizeLimit 3Gi", ed)
	}
}

func TestWorktreeEmptyDirHasDefaultSizeLimit(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RunAsNonRoot: true})
	spec := baseSpec()
	spec.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://d/git/t/app"}}
	ed := rt.buildPod(spec).Spec.Volumes[0].EmptyDir
	if ed == nil || ed.SizeLimit == nil || ed.SizeLimit.Cmp(resource.MustParse("10Gi")) != 0 {
		t.Fatalf("emptyDir = %+v, want sizeLimit 10Gi", ed)
	}
}

func TestNewRejectsBadStorageQuantities(t *testing.T) {
	for _, cfg := range []Config{
		{EphemeralLimit: "lots"},
		{EphemeralRequest: "one gig"},
		{EmptyDirSizeLimit: "ten gigs"},
	} {
		if _, err := NewWithClient(fake.NewClientset(), cfg); err == nil {
			t.Errorf("config %+v accepted", cfg)
		}
	}
}

func TestBuildPodActiveDeadline(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RunAsNonRoot: true})
	if d := rt.buildPod(baseSpec()).Spec.ActiveDeadlineSeconds; d != nil {
		t.Errorf("deadline set without asking: %d", *d)
	}
	spec := baseSpec()
	spec.ActiveDeadlineSeconds = 3660
	d := rt.buildPod(spec).Spec.ActiveDeadlineSeconds
	if d == nil || *d != 3660 {
		t.Fatalf("activeDeadlineSeconds = %v, want 3660", d)
	}
}
