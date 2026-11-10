// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/MisterDonnuts/thulu/internal/netguard"
)

func addNode(t *testing.T, client *fake.Clientset, name string, addrs ...corev1.NodeAddress) {
	t.Helper()
	if _, err := client.CoreV1().Nodes().Create(context.Background(), &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Addresses: addrs},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func worldExcept(t *testing.T, p *networkingv1.NetworkPolicy) []string {
	t.Helper()
	w := egressRules(p, func(peer networkingv1.NetworkPolicyPeer) bool {
		return peer.IPBlock != nil && peer.IPBlock.CIDR == "0.0.0.0/0"
	})
	if len(w) != 1 {
		t.Fatalf("want one world rule, got %+v", p.Spec.Egress)
	}
	return w[0].To[0].IPBlock.Except
}

func TestTenantEgressExcludesNodeAddresses(t *testing.T) {
	t.Cleanup(func() { netguard.SetDiscovered(netguardSource, nil) })
	rt, client := newFakeRuntime(t, Config{Isolation: TenantIsolation{ControlPlaneNamespace: "octopus"}})
	addNode(t, client, "node-a",
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "203.0.113.20"},
		corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.21"},
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "2001:db8:1:2::2"},
		corev1.NodeAddress{Type: corev1.NodeHostName, Address: "node-a"},
	)
	addNode(t, client, "worker",
		corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.4"},
	)
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	got := worldExcept(t, getPolicy(t, client, "tenant-acme", PolicyTenantAllow))
	want := []string{"10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16",
		"203.0.113.20/32", "203.0.113.21/32"}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("except = %v\nwant     %v", got, want)
	}

	if err := netguard.EgressPolicy().Check(netip.MustParseAddr("203.0.113.20")); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("netguard not told about the node address: %v", err)
	}

	n, _ := client.CoreV1().Nodes().Get(ctx, "node-a", metav1.GetOptions{})
	n.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "203.0.113.99"}}
	if _, err := client.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	got = worldExcept(t, getPolicy(t, client, "tenant-acme", PolicyTenantAllow))
	if !slices.Contains(got, "203.0.113.99/32") || slices.Contains(got, "203.0.113.20/32") {
		t.Errorf("except after the address change = %v", got)
	}
}

func TestTenantEgressNodeListFailureFallsBack(t *testing.T) {
	t.Cleanup(func() { netguard.SetDiscovered(netguardSource, nil) })
	rt, client := newFakeRuntime(t, Config{})
	deny := false
	client.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		if deny {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", nil)
		}
		return false, nil, nil
	})
	addNode(t, client, "node-a", corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.20"})
	ctx := context.Background()

	deny = true
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatalf("a failed node listing must not fail the launch: %v", err)
	}
	if got := worldExcept(t, getPolicy(t, client, "tenant-acme", PolicyTenantAllow)); slices.Contains(got, "203.0.113.20/32") {
		t.Errorf("no listing ever succeeded, yet except = %v", got)
	}

	deny = false
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	deny = true
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if got := worldExcept(t, getPolicy(t, client, "tenant-acme", PolicyTenantAllow)); !slices.Contains(got, "203.0.113.20/32") {
		t.Errorf("a failed listing dropped the known node address: except = %v", got)
	}
}

func TestTenantIngressOnlyDaemonPodsOnExecPort(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{Isolation: TenantIsolation{ControlPlaneNamespace: "octopus"}})
	p := rt.tenantAllowPolicy()
	if len(p.Spec.Ingress) != 1 {
		t.Fatalf("want one ingress rule, got %+v", p.Spec.Ingress)
	}
	rule := p.Spec.Ingress[0]
	if len(rule.From) != 1 {
		t.Fatalf("want one peer, got %+v", rule.From)
	}
	peer := rule.From[0]
	if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels[labelMetadataName] != "octopus" {
		t.Errorf("peer must be pinned to the control-plane namespace: %+v", peer)
	}
	if peer.PodSelector == nil || !reflect.DeepEqual(peer.PodSelector.MatchLabels, map[string]string{"app.kubernetes.io/name": "octopus"}) {
		t.Errorf("peer must select the daemon's pods only: %+v", peer.PodSelector)
	}
	if len(rule.Ports) != 1 || rule.Ports[0].Port.IntValue() != 8587 || *rule.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Errorf("ingress must be TCP 8587 only: %+v", rule.Ports)
	}

	rt, _ = newFakeRuntime(t, Config{Isolation: TenantIsolation{
		ControlPlaneNamespace:   "octopus",
		ControlPlanePodSelector: map[string]string{"app.kubernetes.io/name": "octopus", "app.kubernetes.io/instance": "prod"},
		IngressPorts:            []int32{8587, 9000},
	}})
	rule = rt.tenantAllowPolicy().Spec.Ingress[0]
	if len(rule.From[0].PodSelector.MatchLabels) != 2 || !containsAll(portsIn(rule), 8587, 9000) {
		t.Errorf("configured selector/ports ignored: %+v", rule)
	}
}

func portsIn(r networkingv1.NetworkPolicyIngressRule) []int {
	var out []int
	for _, p := range r.Ports {
		out = append(out, p.Port.IntValue())
	}
	return out
}

func TestConfigFromEnvIngress(t *testing.T) {
	env := map[string]string{
		EnvRuntimeClass:            "kata-clh",
		EnvPodNamespace:            "octopus",
		EnvControlPlanePodSelector: "app.kubernetes.io/name=octopus, app.kubernetes.io/instance=octopus",
		EnvIngressPorts:            "8587",
	}
	cfg, err := ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Isolation.ControlPlanePodSelector; len(got) != 2 || got["app.kubernetes.io/instance"] != "octopus" {
		t.Errorf("selector = %v", got)
	}
	if got := cfg.Isolation.IngressPorts; len(got) != 1 || got[0] != 8587 {
		t.Errorf("ingress ports = %v", got)
	}
	env[EnvControlPlanePodSelector] = "justakey"
	if _, err := ConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Error("a selector entry without = must fail config")
	}
}
