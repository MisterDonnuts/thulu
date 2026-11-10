// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/MisterDonnuts/thulu/internal/catrust"
)

func testCAPEMValid(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Thulu Test Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func getPolicy(t *testing.T, client *fake.Clientset, ns, name string) *networkingv1.NetworkPolicy {
	t.Helper()
	p, err := client.NetworkingV1().NetworkPolicies(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get network policy %s/%s: %v", ns, name, err)
	}
	return p
}

func TestEnsureTenantNamespaceAppliesIsolation(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{Isolation: TenantIsolation{ControlPlaneNamespace: "octopus"}})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, "tenant-acme", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"pod-security.kubernetes.io/enforce": "restricted",
		"pod-security.kubernetes.io/audit":   "restricted",
		"pod-security.kubernetes.io/warn":    "restricted",
		LabelTenant:                          "acme",
		LabelPlane:                           "data",
	} {
		if ns.Labels[k] != want {
			t.Errorf("namespace label %s = %q, want %q", k, ns.Labels[k], want)
		}
	}

	deny := getPolicy(t, client, "tenant-acme", PolicyDefaultDeny)
	if len(deny.Spec.PolicyTypes) != 2 || len(deny.Spec.Ingress) != 0 || len(deny.Spec.Egress) != 0 ||
		len(deny.Spec.PodSelector.MatchLabels) != 0 {
		t.Errorf("default-deny must select every pod and allow nothing: %+v", deny.Spec)
	}
	getPolicy(t, client, "tenant-acme", PolicyTenantAllow)

	quota, err := client.CoreV1().ResourceQuotas("tenant-acme").Get(ctx, QuotaName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("resource quota: %v", err)
	}
	if q := quota.Spec.Hard[corev1.ResourcePods]; q.String() != defaultQuotaPods {
		t.Errorf("quota pods = %s, want %s", q.String(), defaultQuotaPods)
	}
	lr, err := client.CoreV1().LimitRanges("tenant-acme").Get(ctx, LimitRangeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("limit range: %v", err)
	}
	if m := lr.Spec.Limits[0].Max[corev1.ResourceMemory]; m.String() != defaultLimitMaxMemory {
		t.Errorf("limit range max memory = %s", m.String())
	}
	if _, err := client.CoreV1().ConfigMaps("tenant-acme").Get(ctx, catrust.ConfigMapName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("CA ConfigMap must not exist without a bundle, err=%v", err)
	}
}

func TestEnsureNamespaceRepairsDrift(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-acme", Labels: map[string]string{"team": "keep-me"}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NetworkingV1().NetworkPolicies("tenant-acme").Create(ctx, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: PolicyTenantAllow},
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{}},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	ns, _ := client.CoreV1().Namespaces().Get(ctx, "tenant-acme", metav1.GetOptions{})
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" || ns.Labels["team"] != "keep-me" {
		t.Errorf("adopted namespace must gain PSA labels and keep foreign ones: %v", ns.Labels)
	}
	allow := getPolicy(t, client, "tenant-acme", PolicyTenantAllow)
	for _, rule := range allow.Spec.Egress {
		if len(rule.To) == 0 {
			t.Fatalf("the widened allow-all egress rule survived reconcile: %+v", allow.Spec.Egress)
		}
	}
}

func TestEnsureNamespaceFailsClosedWhenLabelsCannotBeApplied(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "tenant-acme"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("update", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "tenant-acme", nil)
	})
	err := rt.EnsureTenantNamespace(ctx, "acme")
	if err == nil || !strings.Contains(err.Error(), "update") {
		t.Fatalf("a namespace the daemon cannot label must fail the launch with an RBAC hint, got %v", err)
	}
}

func egressRules(p *networkingv1.NetworkPolicy, pred func(networkingv1.NetworkPolicyPeer) bool) []networkingv1.NetworkPolicyEgressRule {
	var out []networkingv1.NetworkPolicyEgressRule
	for _, r := range p.Spec.Egress {
		if len(r.To) > 0 && pred(r.To[0]) {
			out = append(out, r)
		}
	}
	return out
}

func ports(r networkingv1.NetworkPolicyEgressRule) []int {
	var out []int
	for _, p := range r.Ports {
		out = append(out, p.Port.IntValue())
	}
	return out
}

func TestTenantAllowPolicyOpenMode(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{Isolation: TenantIsolation{
		ControlPlaneNamespace: "octopus",
		EgressAllow:           []EgressAllow{{CIDR: "10.20.0.0/16", Port: 443}, {CIDR: "10.30.1.5/32"}},
	}})
	p := rt.tenantAllowPolicy()

	if len(p.Spec.Ingress) != 1 || p.Spec.Ingress[0].From[0].NamespaceSelector.MatchLabels[labelMetadataName] != "octopus" {
		t.Errorf("ingress must come only from the control plane: %+v", p.Spec.Ingress)
	}
	cp := egressRules(p, func(peer networkingv1.NetworkPolicyPeer) bool {
		return peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels[labelMetadataName] == "octopus"
	})
	if len(cp) != 1 || !containsAll(ports(cp[0]), 8585, 8586, 3000) {
		t.Errorf("control-plane egress must allow 8585, 8586 and 3000: %+v", cp)
	}
	dns := egressRules(p, func(peer networkingv1.NetworkPolicyPeer) bool {
		return peer.NamespaceSelector != nil && peer.NamespaceSelector.MatchLabels[labelMetadataName] == "kube-system"
	})
	if len(dns) != 1 || !containsAll(ports(dns[0]), 53) {
		t.Errorf("DNS egress missing: %+v", dns)
	}
	world := egressRules(p, func(peer networkingv1.NetworkPolicyPeer) bool {
		return peer.IPBlock != nil && peer.IPBlock.CIDR == "0.0.0.0/0"
	})
	if len(world) != 1 || !containsAll(ports(world[0]), 443) || len(ports(world[0])) != 1 {
		t.Fatalf("open mode must allow 443 to the world and nothing else: %+v", world)
	}
	except := strings.Join(world[0].To[0].IPBlock.Except, ",")
	for _, c := range []string{"169.254.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		if !strings.Contains(except, c) {
			t.Errorf("world rule must exclude %s, except=%s", c, except)
		}
	}
	allow := egressRules(p, func(peer networkingv1.NetworkPolicyPeer) bool {
		return peer.IPBlock != nil && peer.IPBlock.CIDR != "0.0.0.0/0"
	})
	if len(allow) != 2 || !containsAll(ports(allow[0]), 443) || len(allow[1].Ports) != 0 {
		t.Errorf("allowlist rules wrong: %+v", allow)
	}
}

func TestTenantAllowPolicyProxyOnlyDropsWorld(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{Isolation: TenantIsolation{
		ControlPlaneNamespace: "octopus",
		EgressMode:            EgressProxyOnly,
		EgressAllow:           []EgressAllow{{CIDR: "10.9.8.7/32", Port: 3128}},
	}})
	p := rt.tenantAllowPolicy()
	if w := egressRules(p, func(peer networkingv1.NetworkPolicyPeer) bool {
		return peer.IPBlock != nil && peer.IPBlock.CIDR == "0.0.0.0/0"
	}); len(w) != 0 {
		t.Errorf("proxy-only must not allow the world: %+v", w)
	}
	proxy := egressRules(p, func(peer networkingv1.NetworkPolicyPeer) bool {
		return peer.IPBlock != nil && peer.IPBlock.CIDR == "10.9.8.7/32"
	})
	if len(proxy) != 1 || !containsAll(ports(proxy[0]), 3128) {
		t.Errorf("proxy-only must allow the configured proxy: %+v", proxy)
	}
}

func TestTenantAllowPolicyWithoutControlPlaneDeniesIngress(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	p := rt.tenantAllowPolicy()
	if len(p.Spec.Ingress) != 0 {
		t.Errorf("no control-plane namespace → no ingress at all: %+v", p.Spec.Ingress)
	}
}

func containsAll(have []int, want ...int) bool {
	set := map[int]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func TestParseEgressAllow(t *testing.T) {
	got, err := ParseEgressAllow("10.20.0.0/16:443, 10.30.1.5:3128; fd00:1::/64:8443\n192.168.5.5 2001:db8::1")
	if err != nil {
		t.Fatal(err)
	}
	want := []EgressAllow{
		{CIDR: "10.20.0.0/16", Port: 443},
		{CIDR: "10.30.1.5/32", Port: 3128},
		{CIDR: "fd00:1::/64", Port: 8443},
		{CIDR: "192.168.5.5/32"},
		{CIDR: "2001:db8::1/128"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, bad := range []string{"not-an-ip", "10.0.0.0/8:99999", "10.0.0.0/33"} {
		if _, err := ParseEgressAllow(bad); err == nil {
			t.Errorf("ParseEgressAllow(%q) must fail", bad)
		}
	}
}

func TestIsolationValidation(t *testing.T) {
	cases := map[string]Config{
		"bad psa":         {Isolation: TenantIsolation{PodSecurity: "privileged"}},
		"bad mode":        {Isolation: TenantIsolation{EgressMode: "anything"}},
		"bad cidr":        {Isolation: TenantIsolation{PrivateCIDRs: []string{"10.0.0.0/99"}}},
		"agent above max": {MemoryLimit: "16Gi"},
		"bad quota":       {Isolation: TenantIsolation{Quota: TenantQuota{Pods: "lots"}}},
		"bad selector":    {Isolation: TenantIsolation{ControlPlanePodSelector: map[string]string{"app": "no spaces allowed"}}},
		"bad ingress":     {Isolation: TenantIsolation{IngressPorts: []int32{70000}}},
	}
	for name, cfg := range cases {
		if _, err := NewWithClient(fake.NewClientset(), cfg); err == nil {
			t.Errorf("%s: NewWithClient must reject the config", name)
		}
	}
	rt, client := newFakeRuntime(t, Config{MemoryLimit: "16Gi", Isolation: TenantIsolation{Quota: TenantQuota{Disabled: true}}})
	if err := rt.EnsureTenantNamespace(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().ResourceQuotas("tenant-acme").Get(context.Background(), QuotaName, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("disabled quota must not be created, err=%v", err)
	}
}

func TestRuntimeClassRequired(t *testing.T) {
	if _, err := New(Config{}); err == nil || !strings.Contains(err.Error(), EnvRuntimeClass) {
		t.Fatalf("New without a RuntimeClass must refuse to start naming %s, got %v", EnvRuntimeClass, err)
	}
	if err := checkRuntimeClass(Config{AllowNoRuntimeClass: true}); err != nil {
		t.Errorf("explicit dev opt-in must be accepted: %v", err)
	}
	if err := checkRuntimeClass(Config{RuntimeClassName: "kata-clh"}); err != nil {
		t.Errorf("a RuntimeClass must be accepted: %v", err)
	}
}

func TestProxyOnlyNeedsTheProxy(t *testing.T) {
	iso := TenantIsolation{EgressMode: EgressProxyOnly}
	iso.applyDefaults()
	err := iso.validate(corev1.ResourceRequirements{})
	if err == nil || !strings.Contains(err.Error(), EnvEgressAllow) {
		t.Fatalf("proxy-only with no allowlist must fail naming %s, got %v", EnvEgressAllow, err)
	}

	env := map[string]string{
		EnvEgressMode:          EgressProxyOnly,
		"HTTPS_PROXY":          "http://10.0.5.20:3128",
		"http_proxy":           "http://proxy.corp:8080",
		EnvAllowNoRuntimeClass: "1",
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := []EgressAllow{{CIDR: "10.0.5.20/32", Port: 3128}}
	if !reflect.DeepEqual(cfg.Isolation.EgressAllow, want) {
		t.Errorf("EgressAllow = %+v, want %+v", cfg.Isolation.EgressAllow, want)
	}
	env[EnvEgressMode] = EgressOpen
	cfg, _ = ConfigFromEnv(func(k string) string { return env[k] })
	if len(cfg.Isolation.EgressAllow) != 0 {
		t.Errorf("open mode must not grow the allowlist: %+v", cfg.Isolation.EgressAllow)
	}
}

func TestConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, []byte(testCAPEMValid(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		EnvInCluster:           "true",
		EnvRuntimeClass:        "kata-clh",
		EnvPodNamespace:        "octopus",
		EnvEgressMode:          "Proxy-Only",
		EnvEgressAllow:         "10.9.8.7:3128",
		EnvControlPlanePorts:   "8585 8586",
		EnvQuota:               "off",
		EnvPodSecurity:         "baseline",
		catrust.BundleEnv:      caPath,
		EnvAllowNoRuntimeClass: "",
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.InCluster || cfg.RuntimeClassName != "kata-clh" || !cfg.RunAsNonRoot {
		t.Errorf("cluster fields wrong: %+v", cfg)
	}
	iso := cfg.Isolation
	if iso.ControlPlaneNamespace != "octopus" || iso.EgressMode != EgressProxyOnly || iso.PodSecurity != "baseline" ||
		!iso.Quota.Disabled || len(iso.EgressAllow) != 1 || iso.EgressAllow[0].Port != 3128 ||
		len(iso.ControlPlanePorts) != 2 {
		t.Errorf("isolation fields wrong: %+v", iso)
	}
	if len(cfg.CABundle) == 0 {
		t.Error("CA bundle must be loaded from OCTOPUS_EXTRA_CA_BUNDLE")
	}

	env[EnvControlPlaneNamespace] = "control"
	cfg, _ = ConfigFromEnv(func(k string) string { return env[k] })
	if cfg.Isolation.ControlPlaneNamespace != "control" {
		t.Errorf("explicit control-plane namespace ignored: %q", cfg.Isolation.ControlPlaneNamespace)
	}

	env[catrust.BundleEnv] = filepath.Join(dir, "missing.pem")
	if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Error("a missing CA bundle must fail config")
	}
}

func TestCABundleSyncedAndMounted(t *testing.T) {
	ca := []byte(testCAPEMValid(t))
	rt, client := newFakeRuntime(t, Config{CABundle: ca, RunAsNonRoot: true})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	cm, err := client.CoreV1().ConfigMaps("tenant-acme").Get(ctx, catrust.ConfigMapName, metav1.GetOptions{})
	if err != nil || cm.Data[catrust.ConfigMapCAKey] != string(ca) {
		t.Fatalf("CA ConfigMap missing or wrong: %v %+v", err, cm)
	}
	spec := baseSpec()
	spec.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://x/app"}}
	pod := rt.buildPod(spec)
	found := false
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.MountPath == catrust.ContainerDir && m.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Errorf("agent must mount the CA read-only at %s: %+v", catrust.ContainerDir, pod.Spec.Containers[0].VolumeMounts)
	}
	for _, e := range pod.Spec.InitContainers[0].Env {
		if e.Name == "GIT_SSL_CAINFO" && e.Value == catrust.ContainerPath {
			return
		}
	}
	t.Error("loader must trust the CA for its clone from the daemon")
}

func stampPodUIDs(client *fake.Clientset) {
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if pod, ok := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod); ok {
			pod.UID = "uid-" + k8stypes.UID(pod.Name)
		}
		return false, nil, nil
	})
}

func TestLaunchPodStoresEnvInOwnedSecret(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	stampPodUIDs(client)
	ctx := context.Background()
	spec := baseSpec()
	name, err := rt.LaunchPod(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor(spec.WorkspaceID)
	sec, err := client.CoreV1().Secrets(ns).Get(ctx, EnvSecretName(name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("env secret: %v", err)
	}
	if string(sec.Data["OCTOPUS_API_KEY"]) != "sk-scoped" {
		t.Errorf("secret must carry the env values: %v", sec.Data)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Kind != "Pod" || sec.OwnerReferences[0].Name != name {
		t.Errorf("secret must be owned by its pod for GC: %+v", sec.OwnerReferences)
	}
	pod, _ := client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	for _, c := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		for _, e := range c.Env {
			if strings.Contains(e.Value, "sk-scoped") {
				t.Errorf("container %s carries the key as a literal", c.Name)
			}
		}
	}
	if err := rt.DeletePod(ctx, name, ns); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Secrets(ns).Get(ctx, EnvSecretName(name), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("DeletePod must delete the env secret, err=%v", err)
	}
}

func TestLaunchPodForwardsProxyEnv(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{ForwardEnv: map[string]string{
		"HTTPS_PROXY": "http://u:pw@proxy:3128", "OCTOPUS_API_KEY": "must-not-win",
	}})
	ctx := context.Background()
	spec := baseSpec()
	name, err := rt.LaunchPod(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := client.CoreV1().Secrets(rt.NamespaceFor(spec.WorkspaceID)).Get(ctx, EnvSecretName(name), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(sec.Data["HTTPS_PROXY"]) != "http://u:pw@proxy:3128" {
		t.Errorf("proxy env not forwarded into the secret: %v", sec.Data)
	}
	if string(sec.Data["OCTOPUS_API_KEY"]) != "sk-scoped" {
		t.Error("a launch's own env must win over forwarded env")
	}
	if _, ok := spec.Env["HTTPS_PROXY"]; ok {
		t.Error("the caller's env map must not be mutated")
	}
	cfg, err := ConfigFromEnv(func(k string) string {
		return map[string]string{"https_proxy": "http://p:1", EnvAllowNoRuntimeClass: "1"}[k]
	})
	if err != nil || cfg.ForwardEnv["HTTPS_PROXY"] != "http://p:1" {
		t.Errorf("ConfigFromEnv must pick up the proxy: %v %v", cfg.ForwardEnv, err)
	}
}

func TestLaunchPodRollsBackSecretOnPodFailure(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	client.PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(os.ErrInvalid)
	})
	spec := baseSpec()
	if _, err := rt.LaunchPod(context.Background(), spec); err == nil {
		t.Fatal("expected pod create failure")
	}
	ns := rt.NamespaceFor(spec.WorkspaceID)
	list, _ := client.CoreV1().Secrets(ns).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Errorf("a failed launch must not leave its env secret behind: %d left", len(list.Items))
	}
}

func TestLaunchPodResolvesRegistry(t *testing.T) {
	t.Setenv("OCTOPUS_IMAGE_REGISTRY", "registry.corp/octo")
	rt, client := newFakeRuntime(t, Config{})
	spec := baseSpec()
	spec.Image = "octopus-docker-agent:latest"
	name, err := rt.LaunchPod(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	pod, _ := client.CoreV1().Pods(rt.NamespaceFor(spec.WorkspaceID)).Get(context.Background(), name, metav1.GetOptions{})
	if got := pod.Spec.Containers[0].Image; got != "registry.corp/octo/octopus-docker-agent:latest" {
		t.Errorf("image = %q, want the registry-prefixed ref", got)
	}
}
