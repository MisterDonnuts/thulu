// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseCacheSize(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: "20Gi"},
		{in: "  ", want: "20Gi"},
		{in: "50Gi", want: "50Gi"},
		{in: "512Mi", want: "512Mi"},
		{in: "off", want: ""},
		{in: "OFF", want: ""},
		{in: "0", want: ""},
		{in: "0Gi", want: ""},
		{in: "false", want: ""},
		{in: "disabled", want: ""},
		{in: "twenty gigs", wantErr: true},
		{in: "-5Gi", wantErr: true},
	} {
		size, on, err := ParseCacheSize(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseCacheSize(%q) = %v, %v; want an error", tc.in, size.String(), on)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCacheSize(%q): %v", tc.in, err)
			continue
		}
		if tc.want == "" {
			if on {
				t.Errorf("ParseCacheSize(%q) left the cache on (%s)", tc.in, size.String())
			}
			continue
		}
		if !on || size.Cmp(resource.MustParse(tc.want)) != 0 {
			t.Errorf("ParseCacheSize(%q) = %s, on=%v; want %s", tc.in, size.String(), on, tc.want)
		}
	}
}

func TestEnsureCacheClaim(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()
	c := CacheClaim{Namespace: "tenant-acme", WorkspaceID: "Acme_1", Size: resource.MustParse("30Gi")}
	if err := EnsureCacheClaim(ctx, client, c); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCacheClaim(ctx, client, c); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, CacheClaimName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("30Gi")) != 0 {
		t.Errorf("size = %s, want 30Gi", got.String())
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("access modes = %v, want RWO", pvc.Spec.AccessModes)
	}
	for k, want := range map[string]string{
		LabelPlane: planeValue, LabelManagedBy: managedByValue, LabelCache: "true", LabelWorkspace: "acme-1",
	} {
		if pvc.Labels[k] != want {
			t.Errorf("label %s = %q, want %q", k, pvc.Labels[k], want)
		}
	}
	if _, ok := pvc.Labels[LabelTaskID]; ok {
		t.Errorf("cache claim must not carry a task label: %v", pvc.Labels)
	}

	if got := buildCacheClaim(CacheClaim{Namespace: "x"}).Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse(DefaultCacheSize)) != 0 {
		t.Errorf("default size = %s, want %s", got.String(), DefaultCacheSize)
	}
}

func TestCacheClaimSurvivesTaskCleanup(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	tenantNS(t, client, "tenant-acme", "acme")
	if err := EnsureCacheClaim(ctx, client, CacheClaim{Namespace: "tenant-acme", WorkspaceID: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureWorktreeClaim(ctx, client, WorktreeClaim{Namespace: "tenant-acme", Name: "octopus-wt-t1", OwnerID: "t1", WorkspaceID: "acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseWorktreeClaims(ctx, client, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.SweepWorktreeClaims(ctx, func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if claimExists(t, client, "tenant-acme", "octopus-wt-t1") {
		t.Error("the task's worktree claim should be gone")
	}
	if !claimExists(t, client, "tenant-acme", CacheClaimName) {
		t.Error("task cleanup deleted the workspace's cache claim")
	}
}

func TestQuotaStorageAddsCache(t *testing.T) {
	for _, tc := range []struct {
		q    TenantQuota
		want string
	}{
		{TenantQuota{Storage: "500Gi", CacheStorage: "20Gi"}, "520Gi"},
		{TenantQuota{Storage: "60Gi", CacheStorage: "20Gi"}, "80Gi"},
		{TenantQuota{Storage: "30Gi"}, "30Gi"},
	} {
		got := tc.q.storageHard()
		if got.Cmp(resource.MustParse(tc.want)) != 0 {
			t.Errorf("storageHard(%+v) = %s, want %s", tc.q, got.String(), tc.want)
		}
	}

	rt, client := newFakeRuntime(t, Config{Isolation: TenantIsolation{Quota: TenantQuota{Storage: "60Gi", CacheStorage: "20Gi"}}})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	quota, err := client.CoreV1().ResourceQuotas("tenant-acme").Get(ctx, QuotaName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := quota.Spec.Hard[corev1.ResourceRequestsStorage]; got.Cmp(resource.MustParse("80Gi")) != 0 {
		t.Errorf("requests.storage = %s, want 80Gi (60Gi of worktrees + the 20Gi cache)", got.String())
	}
}

func TestConfigFromEnvCacheSize(t *testing.T) {
	env := map[string]string{EnvRuntimeClass: "kata-clh"}
	get := func(k string) string { return env[k] }

	cfg, err := ConfigFromEnv(get)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Isolation.Quota.CacheStorage != DefaultCacheSize {
		t.Errorf("default cache storage = %q, want %s", cfg.Isolation.Quota.CacheStorage, DefaultCacheSize)
	}

	env[EnvCacheSize] = "off"
	if cfg, err = ConfigFromEnv(get); err != nil || cfg.Isolation.Quota.CacheStorage != "" {
		t.Errorf("cache off: storage = %q, err = %v; want none", cfg.Isolation.Quota.CacheStorage, err)
	}

	env[EnvCacheSize] = "lots"
	if _, err := ConfigFromEnv(get); err == nil {
		t.Error("a malformed cache size must fail the config")
	}

	if err := (TenantQuota{
		Pods: "1", CPU: "1", Memory: "1Gi", Storage: "1Gi", MaxCPU: "1", MaxMemory: "1Gi", CacheStorage: "lots",
	}).validate(corev1.ResourceRequirements{}); err == nil {
		t.Error("validate must reject a malformed cache size")
	}
}
