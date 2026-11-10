// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"log"
	"maps"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Tenant policies deny pod-to-pod traffic, so each sandbox gets two pod-owned policies opening its
// app ports to its own task group's pods only.

const LabelSandboxRoot = "thulu.dev/sandbox-root"

const (
	previewPolicyPrefix        = "octopus-preview-"
	previewClientsPolicyPrefix = "octopus-preview-clients-"
)

// Must match the app.kubernetes.io/name of the shared thulu-browser Deployment in the Helm chart
// (templates/_mcp.tpl).
const sharedBrowserName = "thulu-browser"

type SandboxPreview struct {
	Pod        *corev1.Pod
	RootTaskID string
	Ports      []int32
}

func PreviewPolicyNames(podName string) []string {
	return []string{previewPolicyPrefix + podName, previewClientsPolicyPrefix + podName}
}

func (r *Runtime) EnsureSandboxPreview(ctx context.Context, p SandboxPreview) error {
	if len(p.Ports) == 0 {
		DeleteSandboxPreview(ctx, r.client, p.Pod.Namespace, p.Pod.Name)
		return nil
	}
	for _, want := range r.sandboxPreviewPolicies(p) {
		if err := putOwnedPolicy(ctx, r.client, p.Pod.Namespace, want, p.Pod.UID); err != nil {
			return err
		}
	}
	return nil
}

// A shared browser serves every workspace, so any agent that drives it reaches every sandbox's app
// ports; only per-run mode keeps tenants apart.
func (r *Runtime) sharedBrowserPeer() *networkingv1.NetworkPolicyPeer {
	iso := r.cfg.Isolation
	if r.cfg.Browser.PerRun() || iso.ControlPlaneNamespace == "" {
		return nil
	}
	sel := maps.Clone(iso.ControlPlanePodSelector)
	if sel == nil {
		sel = map[string]string{}
	}
	sel["app.kubernetes.io/name"] = sharedBrowserName
	return &networkingv1.NetworkPolicyPeer{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{labelMetadataName: iso.ControlPlaneNamespace}},
		PodSelector:       &metav1.LabelSelector{MatchLabels: sel},
	}
}

func (r *Runtime) sandboxPreviewPolicies(p SandboxPreview) []*networkingv1.NetworkPolicy {
	tcp := corev1.ProtocolTCP
	ports := make([]networkingv1.NetworkPolicyPort, 0, len(p.Ports))
	for _, n := range p.Ports {
		pp := intstr.FromInt32(n)
		ports = append(ports, networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &pp})
	}
	sandboxSel := metav1.LabelSelector{MatchLabels: map[string]string{LabelSandbox: p.Pod.Labels[LabelSandbox]}}
	groupSel := metav1.LabelSelector{MatchLabels: map[string]string{LabelSandboxRoot: sanitizeDNS1123(p.RootTaskID)}}

	from := []networkingv1.NetworkPolicyPeer{{PodSelector: groupSel.DeepCopy()}}
	if peer := r.sharedBrowserPeer(); peer != nil {
		from = append(from, *peer)
	}
	labels := managedLabels()
	labels[LabelSandbox] = p.Pod.Labels[LabelSandbox]
	owner := []metav1.OwnerReference{{
		APIVersion: "v1", Kind: "Pod", Name: p.Pod.Name, UID: p.Pod.UID, //i18n:ignore Kubernetes object kind
	}}
	names := PreviewPolicyNames(p.Pod.Name)
	return []*networkingv1.NetworkPolicy{{
		ObjectMeta: metav1.ObjectMeta{Name: names[0], Labels: labels, OwnerReferences: owner},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: sandboxSel,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: from, Ports: ports}},
		},
	}, {
		ObjectMeta: metav1.ObjectMeta{Name: names[1], Labels: maps.Clone(labels), OwnerReferences: owner},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: groupSel,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To:    []networkingv1.NetworkPolicyPeer{{PodSelector: sandboxSel.DeepCopy()}},
				Ports: ports,
			}},
		},
	}}
}

// A policy owned by another (replaced) pod is recreated, not updated: in place it would keep that
// owner and be garbage-collected with it.
func putOwnedPolicy(ctx context.Context, client kubernetes.Interface, ns string, want *networkingv1.NetworkPolicy, owner types.UID) error {
	cl := client.NetworkingV1().NetworkPolicies(ns)
	existing, err := ownedPolicy(ctx, client, ns, want.Name, owner)
	if err != nil {
		return err
	}
	if existing == nil {
		if _, err := cl.Create(ctx, want, metav1.CreateOptions{}); err != nil {
			return apperr.Wrap(err, "kuberuntime.errors.create_network_policy", "ns", ns, "name", want.Name)
		}
		return nil
	}
	labelsChanged := mergeLabels(&existing.ObjectMeta, want.Labels)
	if !labelsChanged && apiequality.Semantic.DeepEqual(existing.Spec, want.Spec) {
		return nil
	}
	existing.Spec = want.Spec
	if _, err := cl.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.update_network_policy", "ns", ns, "name", want.Name)
	}
	return nil
}

func ownedPolicy(ctx context.Context, client kubernetes.Interface, ns, name string, owner types.UID) (*networkingv1.NetworkPolicy, error) {
	cl := client.NetworkingV1().NetworkPolicies(ns)
	existing, err := cl.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, apperr.Wrap(err, "kuberuntime.errors.get_network_policy", "ns", ns, "name", name)
	case ownedByUID(existing.OwnerReferences, owner):
		return existing, nil
	}
	uid := existing.UID
	err = cl.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, apperr.Wrap(err, "kuberuntime.errors.delete_network_policy", "ns", ns, "name", name)
	}
	return nil, nil
}

func DeleteSandboxPreview(ctx context.Context, client kubernetes.Interface, ns, podName string) {
	for _, name := range PreviewPolicyNames(podName) {
		err := client.NetworkingV1().NetworkPolicies(ns).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.delete_preview_policy", "ns", ns, "name", name, "err", err))
		}
	}
}

func ownedByUID(refs []metav1.OwnerReference, uid types.UID) bool {
	if uid == "" {
		return false
	}
	for _, r := range refs {
		if r.UID == uid {
			return true
		}
	}
	return false
}
