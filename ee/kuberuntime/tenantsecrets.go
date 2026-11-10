// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"log"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// The daemon must not get Secret access cluster-wide (kube-system included). It binds the chart's
// tenant-secrets ClusterRole to itself per namespace;
// RBAC (bind with resourceNames) and the chart's admission policy limit that RoleBinding to this
// role, this SA, and tenant-*/sandbox namespaces.

const TenantSecretsBindingName = "octopus-daemon-tenant"

const (
	EnvTenantSecretsRole = "OCTOPUS_TENANT_SECRETS_CLUSTERROLE"
	EnvServiceAccount    = "OCTOPUS_SERVICE_ACCOUNT"
)

type TenantSecrets struct {
	ClusterRole    string
	ServiceAccount string
	Namespace      string
}

func (ts TenantSecrets) validate() error {
	if ts.ClusterRole == "" {
		return nil
	}
	if ts.ServiceAccount == "" || ts.Namespace == "" {
		return apperr.Invalid("kuberuntime.errors.tenant_secrets_needs_sa",
			"role_env", EnvTenantSecretsRole, "sa_env", EnvServiceAccount, "ns_env", EnvPodNamespace)
	}
	return nil
}

// A new RoleBinding takes time to reach the API server's authorizer cache; without this wait the
// Secret create that follows is refused.
const (
	bindingSettleTimeout = 10 * time.Second
	bindingSettlePoll    = 250 * time.Millisecond
)

func (r *Runtime) tenantSecretsBinding() *rbacv1.RoleBinding {
	ts := r.cfg.TenantSecrets
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: TenantSecretsBindingName, Labels: managedLabels()},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     ts.ClusterRole,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      ts.ServiceAccount,
			Namespace: ts.Namespace,
		}},
	}
}

// Must run before anything writes a Secret or ConfigMap in ns.
func (r *Runtime) ensureTenantSecretsBinding(ctx context.Context, ns string) error {
	if r.cfg.TenantSecrets.ClusterRole == "" {
		r.noBindingOnce.Do(func() {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.tenant_secrets_unbound", "role_env", EnvTenantSecretsRole))
		})
		return nil
	}
	want := r.tenantSecretsBinding()
	cl := r.client.RbacV1().RoleBindings(ns)
	existing, err := cl.Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cl.Create(ctx, want, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		if err != nil {
			return apperr.Wrap(err, "kuberuntime.errors.bind_tenant_secrets", "role", want.RoleRef.Name, "ns", ns)
		}
		r.waitSecretAccess(ctx, ns)
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.get_rolebinding", "ns", ns, "name", want.Name)
	}
	if existing.RoleRef != want.RoleRef {
		// roleRef is immutable and the daemon has no delete on rolebindings.
		return apperr.Conflict("kuberuntime.errors.rolebinding_wrong_role",
			"ns", ns, "name", want.Name, "kind", existing.RoleRef.Kind, "role", existing.RoleRef.Name, "want", want.RoleRef.Name)
	}
	labelsChanged := mergeLabels(&existing.ObjectMeta, want.Labels)
	if !labelsChanged && apiequality.Semantic.DeepEqual(existing.Subjects, want.Subjects) {
		return nil
	}
	existing.Subjects = want.Subjects
	if _, err := cl.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.update_rolebinding", "ns", ns, "name", want.Name)
	}
	return nil
}

func (r *Runtime) waitSecretAccess(ctx context.Context, ns string) {
	deadline := time.Now().Add(bindingSettleTimeout)
	interval := min(r.pollInterval, bindingSettlePoll)
	for {
		review, err := r.client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
			&authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: ns, Verb: "create", Resource: "secrets"},
			}}, metav1.CreateOptions{})
		if err != nil {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.check_secret_access_failed", "ns", ns, "err", err))
			return
		}
		if review.Status.Allowed {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.secret_access_not_effective",
				"ns", ns, "name", TenantSecretsBindingName, "timeout", bindingSettleTimeout))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
