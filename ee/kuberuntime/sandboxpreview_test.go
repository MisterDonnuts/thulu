// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
)

func previewSandboxPod(uid string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "sandbox-root-1", Namespace: "tenant-acme", UID: types.UID(uid),
		Labels: map[string]string{LabelSandbox: "root-1", LabelPlane: "data"},
	}}
}

func policyPorts(ps []networkingv1.NetworkPolicyPort) []int32 {
	var out []int32
	for _, p := range ps {
		if p.Protocol == nil || *p.Protocol != corev1.ProtocolTCP {
			return nil
		}
		out = append(out, p.Port.IntVal)
	}
	return out
}

func TestEnsureSandboxPreviewPerRun(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{
		Isolation: TenantIsolation{ControlPlaneNamespace: "octopus"},
		Browser:   browserrun.Config{Mode: browserrun.ModePerRun},
	})
	ctx := context.Background()
	pod := previewSandboxPod("uid-1")
	if err := rt.EnsureSandboxPreview(ctx, SandboxPreview{Pod: pod, RootTaskID: "Root_1", Ports: []int32{3000, 5173}}); err != nil {
		t.Fatal(err)
	}
	names := PreviewPolicyNames(pod.Name)

	in := getPolicy(t, client, "tenant-acme", names[0])
	if !reflect.DeepEqual(in.Spec.PodSelector.MatchLabels, map[string]string{LabelSandbox: "root-1"}) {
		t.Errorf("ingress policy selects %v, want the sandbox pod", in.Spec.PodSelector.MatchLabels)
	}
	if !reflect.DeepEqual(in.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}) {
		t.Errorf("ingress policy types = %v", in.Spec.PolicyTypes)
	}
	if len(in.Spec.Ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(in.Spec.Ingress))
	}
	rule := in.Spec.Ingress[0]
	if !reflect.DeepEqual(policyPorts(rule.Ports), []int32{3000, 5173}) {
		t.Errorf("ingress ports = %+v, want TCP 3000 and 5173", rule.Ports)
	}
	if len(rule.From) != 1 || rule.From[0].NamespaceSelector != nil || rule.From[0].PodSelector == nil ||
		!reflect.DeepEqual(rule.From[0].PodSelector.MatchLabels, map[string]string{LabelSandboxRoot: "root-1"}) {
		t.Errorf("ingress must come from the task group's pods in the namespace only: %+v", rule.From)
	}
	if len(in.OwnerReferences) != 1 || in.OwnerReferences[0].UID != pod.UID || in.OwnerReferences[0].Kind != "Pod" {
		t.Errorf("policy must be owned by the sandbox pod: %+v", in.OwnerReferences)
	}

	out := getPolicy(t, client, "tenant-acme", names[1])
	if !reflect.DeepEqual(out.Spec.PodSelector.MatchLabels, map[string]string{LabelSandboxRoot: "root-1"}) {
		t.Errorf("egress policy selects %v, want the task group's pods", out.Spec.PodSelector.MatchLabels)
	}
	if !reflect.DeepEqual(out.Spec.PolicyTypes, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}) {
		t.Errorf("egress policy types = %v", out.Spec.PolicyTypes)
	}
	if len(out.Spec.Egress) != 1 || len(out.Spec.Egress[0].To) != 1 ||
		out.Spec.Egress[0].To[0].NamespaceSelector != nil || out.Spec.Egress[0].To[0].IPBlock != nil ||
		!reflect.DeepEqual(out.Spec.Egress[0].To[0].PodSelector.MatchLabels, map[string]string{LabelSandbox: "root-1"}) {
		t.Fatalf("egress must reach the sandbox pod only: %+v", out.Spec.Egress)
	}
	if !reflect.DeepEqual(policyPorts(out.Spec.Egress[0].Ports), []int32{3000, 5173}) {
		t.Errorf("egress ports = %+v", out.Spec.Egress[0].Ports)
	}

	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	allow := getPolicy(t, client, "tenant-acme", PolicyTenantAllow)
	if len(allow.Spec.Ingress) != 1 || !reflect.DeepEqual(policyPorts(allow.Spec.Ingress[0].Ports), []int32{8587}) {
		t.Errorf("tenant ingress changed: %+v", allow.Spec.Ingress)
	}
}

func TestEnsureSandboxPreviewSharedBrowser(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{
		Isolation: TenantIsolation{
			ControlPlaneNamespace:   "octopus",
			ControlPlanePodSelector: map[string]string{"app.kubernetes.io/name": "octopus", "app.kubernetes.io/instance": "prod"},
		},
		Browser: browserrun.Config{Mode: browserrun.ModeShared},
	})
	ctx := context.Background()
	pod := previewSandboxPod("uid-1")
	if err := rt.EnsureSandboxPreview(ctx, SandboxPreview{Pod: pod, RootTaskID: "root-1", Ports: []int32{3000}}); err != nil {
		t.Fatal(err)
	}
	from := getPolicy(t, client, "tenant-acme", PreviewPolicyNames(pod.Name)[0]).Spec.Ingress[0].From
	if len(from) != 2 {
		t.Fatalf("peers = %+v, want the task group and the shared browser", from)
	}
	browser := from[1]
	if browser.NamespaceSelector == nil || browser.PodSelector == nil ||
		!reflect.DeepEqual(browser.NamespaceSelector.MatchLabels, map[string]string{labelMetadataName: "octopus"}) ||
		!reflect.DeepEqual(browser.PodSelector.MatchLabels, map[string]string{
			"app.kubernetes.io/name": "thulu-browser", "app.kubernetes.io/instance": "prod",
		}) {
		t.Errorf("shared browser peer = %+v", browser)
	}

	rt.cfg.Isolation.ControlPlaneNamespace = ""
	if err := rt.EnsureSandboxPreview(ctx, SandboxPreview{Pod: pod, RootTaskID: "root-1", Ports: []int32{3000}}); err != nil {
		t.Fatal(err)
	}
	if from := getPolicy(t, client, "tenant-acme", PreviewPolicyNames(pod.Name)[0]).Spec.Ingress[0].From; len(from) != 1 {
		t.Errorf("peers without a control plane = %+v, want the task group only", from)
	}
}

func TestEnsureSandboxPreviewReplacesAndDeletes(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{Browser: browserrun.Config{Mode: browserrun.ModePerRun}})
	ctx := context.Background()
	if err := rt.EnsureSandboxPreview(ctx, SandboxPreview{Pod: previewSandboxPod("old"), RootTaskID: "root-1", Ports: []int32{3000}}); err != nil {
		t.Fatal(err)
	}
	pod := previewSandboxPod("new")
	if err := rt.EnsureSandboxPreview(ctx, SandboxPreview{Pod: pod, RootTaskID: "root-1", Ports: []int32{3000, 8080}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range PreviewPolicyNames(pod.Name) {
		p := getPolicy(t, client, "tenant-acme", name)
		if len(p.OwnerReferences) != 1 || p.OwnerReferences[0].UID != "new" {
			t.Errorf("%s owners = %+v, want the new pod", name, p.OwnerReferences)
		}
	}
	if got := policyPorts(getPolicy(t, client, "tenant-acme", PreviewPolicyNames(pod.Name)[0]).Spec.Ingress[0].Ports); !reflect.DeepEqual(got, []int32{3000, 8080}) {
		t.Errorf("ports after update = %v", got)
	}

	if err := rt.EnsureSandboxPreview(ctx, SandboxPreview{Pod: pod, RootTaskID: "root-1"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range PreviewPolicyNames(pod.Name) {
		if _, err := client.NetworkingV1().NetworkPolicies("tenant-acme").Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("%s still exists with no ports (err=%v)", name, err)
		}
	}
}

func TestBuildPodLabelsSandboxRoot(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	spec := LaunchSpec{WorkspaceID: "acme", TaskID: "Sub_1", InstanceID: "i-1"}
	if got := rt.buildPod(spec).Labels[LabelSandboxRoot]; got != "sub-1" {
		t.Errorf("own task: %s = %q, want sub-1", LabelSandboxRoot, got)
	}
	spec.WorktreeOwnerID = "Parent_1"
	if got := rt.buildPod(spec).Labels[LabelSandboxRoot]; got != "parent-1" {
		t.Errorf("subtask: %s = %q, want parent-1", LabelSandboxRoot, got)
	}
}
