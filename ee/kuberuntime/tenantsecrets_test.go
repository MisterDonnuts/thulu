// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var testTenantSecrets = TenantSecrets{ClusterRole: "octopus-tenant-secrets", ServiceAccount: "octopus", Namespace: "octopus"}

func allowAfter(client *fake.Clientset, n int32) *atomic.Int32 {
	var calls atomic.Int32
	client.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		review := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview).DeepCopy()
		review.Status.Allowed = calls.Add(1) >= n
		return true, review, nil
	})
	return &calls
}

func TestEnsureNamespaceBindsTenantSecretsFirst(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{TenantSecrets: testTenantSecrets, CABundle: []byte(testCAPEMValid(t))})
	reviews := allowAfter(client, 2)
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	rb, err := client.RbacV1().RoleBindings("tenant-acme").Get(ctx, TenantSecretsBindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rb.RoleRef.Kind != "ClusterRole" || rb.RoleRef.Name != "octopus-tenant-secrets" || rb.RoleRef.APIGroup != rbacv1.GroupName {
		t.Errorf("roleRef = %+v", rb.RoleRef)
	}
	if len(rb.Subjects) != 1 || rb.Subjects[0].Kind != "ServiceAccount" ||
		rb.Subjects[0].Name != "octopus" || rb.Subjects[0].Namespace != "octopus" {
		t.Errorf("subjects = %+v", rb.Subjects)
	}
	if reviews.Load() != 2 {
		t.Errorf("waited for %d access reviews, want 2 (until the binding took effect)", reviews.Load())
	}
	bindAt, cmAt := -1, -1
	for i, a := range client.Actions() {
		if a.GetVerb() != "create" || a.GetNamespace() != "tenant-acme" {
			continue
		}
		switch a.GetResource().Resource {
		case "rolebindings":
			bindAt = i
		case "configmaps":
			cmAt = i
		}
	}
	if bindAt < 0 || cmAt < 0 || bindAt > cmAt {
		t.Errorf("rolebinding created at action %d, CA ConfigMap at %d; want the binding first", bindAt, cmAt)
	}

	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if reviews.Load() != 2 {
		t.Errorf("an existing binding must not trigger access reviews, got %d", reviews.Load())
	}
}

func TestTenantSecretsBindingRepairsSubjects(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{TenantSecrets: testTenantSecrets})
	allowAfter(client, 1)
	ctx := context.Background()
	stale := rt.tenantSecretsBinding()
	stale.Subjects = append(stale.Subjects, rbacv1.Subject{Kind: "ServiceAccount", Name: "intruder", Namespace: "tenant-acme"})
	if _, err := client.RbacV1().RoleBindings("tenant-acme").Create(ctx, stale, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.ensureTenantSecretsBinding(ctx, "tenant-acme"); err != nil {
		t.Fatal(err)
	}
	rb, _ := client.RbacV1().RoleBindings("tenant-acme").Get(ctx, TenantSecretsBindingName, metav1.GetOptions{})
	if len(rb.Subjects) != 1 || rb.Subjects[0].Name != "octopus" {
		t.Errorf("extra subjects survived: %+v", rb.Subjects)
	}
}

func TestTenantSecretsBindingRefusesForeignRole(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{TenantSecrets: testTenantSecrets})
	ctx := context.Background()
	other := rt.tenantSecretsBinding()
	other.RoleRef.Name = "cluster-admin"
	if _, err := client.RbacV1().RoleBindings("tenant-acme").Create(ctx, other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	err := rt.ensureTenantSecretsBinding(ctx, "tenant-acme")
	if err == nil || !strings.Contains(err.Error(), "cluster-admin") {
		t.Fatalf("a binding to another role must fail the launch, got %v", err)
	}
}

func TestTenantSecretsUnsetSkipsBinding(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	list, err := client.RbacV1().RoleBindings("tenant-acme").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Errorf("no ClusterRole configured, yet %d rolebindings were created", len(list.Items))
	}
}

func TestTenantSecretsNeedsServiceAccount(t *testing.T) {
	_, err := NewWithClient(fake.NewClientset(), Config{TenantSecrets: TenantSecrets{ClusterRole: "x"}})
	if err == nil || !strings.Contains(err.Error(), EnvServiceAccount) {
		t.Fatalf("want an error naming %s, got %v", EnvServiceAccount, err)
	}
}

func TestConfigFromEnvTenantSecrets(t *testing.T) {
	env := map[string]string{
		EnvTenantSecretsRole: "rel-octopus-tenant-secrets",
		EnvServiceAccount:    "rel-octopus",
		EnvPodNamespace:      "octopus",
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := TenantSecrets{ClusterRole: "rel-octopus-tenant-secrets", ServiceAccount: "rel-octopus", Namespace: "octopus"}
	if cfg.TenantSecrets != want {
		t.Errorf("TenantSecrets = %+v, want %+v", cfg.TenantSecrets, want)
	}
	delete(env, EnvServiceAccount)
	if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Error("a ClusterRole without the ServiceAccount must be a config error")
	}
}
