// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const itestSecretsWorkspace = "itest-secrets"

func TestIntegrationDaemonTenantSecrets(t *testing.T) {
	kubeContext := os.Getenv("OCTOPUS_TEST_KUBECONTEXT")
	saRef := os.Getenv("OCTOPUS_TEST_DAEMON_SA")
	role := os.Getenv("OCTOPUS_TEST_TENANT_SECRETS_ROLE")
	if kubeContext == "" || saRef == "" || role == "" {
		t.Skip("OCTOPUS_TEST_KUBECONTEXT / OCTOPUS_TEST_DAEMON_SA / OCTOPUS_TEST_TENANT_SECRETS_ROLE not set")
	}
	saNS, saName, ok := strings.Cut(saRef, ":")
	if !ok {
		t.Fatalf("OCTOPUS_TEST_DAEMON_SA = %q, want namespace:name", saRef)
	}
	runtimeClass := os.Getenv("OCTOPUS_TEST_RUNTIME_CLASS")
	if runtimeClass == "" {
		runtimeClass = "kata-clh"
	}

	adminCfg := Config{Context: kubeContext, AllowNoRuntimeClass: true}
	admin, err := New(adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := newRESTConfig(adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	rc = rest.CopyConfig(rc)
	rc.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:" + saNS + ":" + saName,
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:" + saNS, "system:authenticated"},
	}
	daemonClient, err := kubernetes.NewForConfig(rc)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewWithClient(daemonClient, Config{
		RuntimeClassName: runtimeClass,
		Image:            "busybox:stable",
		// busybox runs as root, which PSA "restricted" rejects at the kubelet.
		Isolation:     TenantIsolation{PodSecurity: PodSecurityBaseline},
		TenantSecrets: TenantSecrets{ClusterRole: role, ServiceAccount: saName, Namespace: saNS},
		CABundle:      []byte(testCAPEMValid(t)),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	ns := daemon.NamespaceFor(itestSecretsWorkspace)
	deleteNamespaceAndWait(ctx, t, admin, ns)
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cc()
		deleteNamespaceAndWait(c, t, admin, ns)
	})

	if err := daemon.EnsureTenantNamespace(ctx, itestSecretsWorkspace); err != nil {
		t.Fatalf("EnsureTenantNamespace as the daemon: %v", err)
	}
	pod, err := daemon.LaunchPod(ctx, LaunchSpec{
		WorkspaceID: itestSecretsWorkspace,
		TaskID:      "itest-secrets",
		Stage:       "in_progress",
		InstanceID:  "itest-secrets-env",
		Command:     []string{"sh", "-c", `test "$OCTOPUS_ITEST" = from-secret`},
		Env:         map[string]string{"OCTOPUS_ITEST": "from-secret"},
	})
	if err != nil {
		t.Fatalf("LaunchPod as the daemon: %v", err)
	}
	if code := waitExit(ctx, t, daemon, pod, ns, 3*time.Minute); code != 0 {
		t.Errorf("pod could not read its env Secret (exit %d)", code)
	}

	dc := daemonClient
	forbidden := func(what string, err error) {
		t.Helper()
		if !apierrors.IsForbidden(err) {
			t.Errorf("%s: want forbidden, got %v", what, err)
			return
		}
		t.Logf("%s: refused: %v", what, err)
	}
	_, err = dc.CoreV1().Secrets("kube-system").List(ctx, metav1.ListOptions{})
	forbidden("list secrets in kube-system", err)
	_, err = dc.CoreV1().Secrets(saNS).List(ctx, metav1.ListOptions{})
	forbidden("list secrets in the daemon's own namespace", err)
	_, err = dc.CoreV1().Secrets("kube-system").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "planted"},
	}, metav1.CreateOptions{})
	forbidden("create a secret in kube-system", err)

	bindTo := func(name, roleName string, subject rbacv1.Subject) error {
		_, err := dc.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName},
			Subjects:   []rbacv1.Subject{subject},
		}, metav1.CreateOptions{})
		return err
	}
	self := rbacv1.Subject{Kind: "ServiceAccount", Name: saName, Namespace: saNS}
	forbidden("bind cluster-admin to itself", bindTo("escalate", "cluster-admin", self))
	forbidden("bind the tenant-secrets role to another SA",
		bindTo("lend", role, rbacv1.Subject{Kind: "ServiceAccount", Name: "default", Namespace: ns}))
	_, err = dc.RbacV1().RoleBindings("kube-system").Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: TenantSecretsBindingName},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
		Subjects:   []rbacv1.Subject{self},
	}, metav1.CreateOptions{})
	forbidden("bind the tenant-secrets role in kube-system", err)

	thief := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "thief"},
		Spec: corev1.PodSpec{
			RuntimeClassName:             &runtimeClass,
			AutomountServiceAccountToken: new(bool),
			Containers: []corev1.Container{{
				Name: "c", Image: "busybox:stable",
				Env: []corev1.EnvVar{{Name: "X", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: EnvSecretName(pod)}, Key: "OCTOPUS_ITEST",
				}}}},
			}},
		},
	}
	_, err = dc.CoreV1().Pods(ns).Create(ctx, thief, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	forbidden("pod reading another pod's env Secret", err)
	thief.Name = "thief2"
	thief.Spec.Containers[0].Env = nil
	thief.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: "thief2-env"},
	}}}
	_, err = dc.CoreV1().Pods(ns).Create(ctx, thief, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	forbidden("pod with envFrom secretRef", err)
}
