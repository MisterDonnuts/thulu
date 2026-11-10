// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"log"
	"maps"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/catrust"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Mirrors deploy/k8s-data-plane/tenant-namespace.template.yaml; the octopus- name prefix keeps a
// pipeline
// applying that template from fighting the daemon over the same objects.

const (
	PodSecurityRestricted = "restricted"
	PodSecurityBaseline   = "baseline"
)

const (
	EgressOpen      = "open"
	EgressProxyOnly = "proxy-only"
)

const (
	PolicyDefaultDeny = "octopus-default-deny"
	PolicyTenantAllow = "octopus-tenant-allow"
	QuotaName         = "octopus-tenant-quota"
	LimitRangeName    = "octopus-tenant-limits"
)

const labelMetadataName = "kubernetes.io/metadata.name"

// Always excluded: it holds the cloud metadata endpoint (169.254.169.254), the classic pivot to
// node credentials.
const linkLocalCIDR = "169.254.0.0/16"

var (
	defaultControlPlanePorts = []int32{8585, 8586, 3000}
	// The in-pod exec agent port of sandbox and chat pods (cmd/sandbox-execagent).
	defaultIngressPorts = []int32{8587}
	// The daemon pod's name label in the Helm chart (octopus.selectorLabels); MCP pods carry their
	// own name there, so they do not match.
	defaultControlPlanePodSelector = map[string]string{"app.kubernetes.io/name": "octopus"}
	// 100.64.0.0/10 (CGNAT) is used for pod ranges by several CNIs and clouds.
	defaultPrivateCIDRs = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
)

const (
	defaultDNSNamespace        = "kube-system"
	defaultQuotaPods           = "20"
	defaultQuotaCPURequests    = "20"
	defaultQuotaMemoryRequests = "64Gi"
	defaultQuotaMemory         = "128Gi"
	defaultQuotaStorage        = "500Gi"
	defaultLimitMaxCPU         = "4"
	defaultLimitMaxMemory      = "8Gi"
)

const QuotaOff = "off"

type TenantIsolation struct {
	PodSecurity string

	ControlPlaneNamespace string
	// Defaults: 8585 daemon API + git, 8586 LLM gateway, 3000 MCP servers
	// (the chart's MCP Services, deploy/helm/octopus/templates/mcp.yaml).
	ControlPlanePorts       []int32
	ControlPlanePodSelector map[string]string
	IngressPorts            []int32
	DNSNamespace            string

	EgressMode   string
	EgressAllow  []EgressAllow
	PrivateCIDRs []string

	Quota TenantQuota
}

type EgressAllow struct {
	CIDR string
	Port int32
}

// The quota budgets requests: summed limits (each Kata microVM's size) would refuse pods on an idle
// node.
// CPU limits are not summed by default since CPU is compressible; limits.memory stays as the
// overcommit ceiling.
type TenantQuota struct {
	Disabled       bool
	Pods           string
	CPURequests    string
	MemoryRequests string
	CPU            string
	Memory         string
	Storage        string
	CacheStorage   string
	// Must be at least the agent and sandbox container limits, or those pods are rejected.
	MaxCPU    string
	MaxMemory string
}

func (iso *TenantIsolation) applyDefaults() {
	if iso.PodSecurity == "" {
		iso.PodSecurity = PodSecurityRestricted
	}
	if len(iso.ControlPlanePorts) == 0 {
		iso.ControlPlanePorts = append([]int32(nil), defaultControlPlanePorts...)
	}
	if len(iso.IngressPorts) == 0 {
		iso.IngressPorts = append([]int32(nil), defaultIngressPorts...)
	}
	if len(iso.ControlPlanePodSelector) == 0 {
		iso.ControlPlanePodSelector = maps.Clone(defaultControlPlanePodSelector)
	}
	if iso.DNSNamespace == "" {
		iso.DNSNamespace = defaultDNSNamespace
	}
	if iso.EgressMode == "" {
		iso.EgressMode = EgressOpen
	}
	if len(iso.PrivateCIDRs) == 0 {
		iso.PrivateCIDRs = append([]string(nil), defaultPrivateCIDRs...)
	}
	q := &iso.Quota
	for _, f := range []struct {
		field *string
		def   string
	}{
		{&q.Pods, defaultQuotaPods}, {&q.CPURequests, defaultQuotaCPURequests},
		{&q.MemoryRequests, defaultQuotaMemoryRequests}, {&q.Memory, defaultQuotaMemory},
		{&q.Storage, defaultQuotaStorage}, {&q.MaxCPU, defaultLimitMaxCPU}, {&q.MaxMemory, defaultLimitMaxMemory},
	} {
		if *f.field == "" {
			*f.field = f.def
		}
	}
}

func (iso *TenantIsolation) validate(agent corev1.ResourceRequirements) error {
	switch iso.PodSecurity {
	case PodSecurityRestricted, PodSecurityBaseline:
	default:
		return apperr.Invalid("kuberuntime.errors.pod_security_level",
			"level", iso.PodSecurity, "restricted", PodSecurityRestricted, "baseline", PodSecurityBaseline)
	}
	switch iso.EgressMode {
	case EgressOpen:
	case EgressProxyOnly:
		if len(iso.EgressAllow) == 0 {
			return apperr.Invalid("kuberuntime.errors.proxy_only_needs_allow", "mode", EgressProxyOnly, "env", EnvEgressAllow)
		}
	default:
		return apperr.Invalid("kuberuntime.errors.egress_mode", "mode", iso.EgressMode, "open", EgressOpen, "proxy_only", EgressProxyOnly)
	}
	for _, c := range iso.PrivateCIDRs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			return apperr.WrapKind(apperr.KindInvalid, err, "kuberuntime.errors.private_cidr", "cidr", c)
		}
	}
	for _, a := range iso.EgressAllow {
		if _, _, err := net.ParseCIDR(a.CIDR); err != nil {
			return apperr.WrapKind(apperr.KindInvalid, err, "kuberuntime.errors.egress_allow_cidr", "cidr", a.CIDR)
		}
	}
	for _, p := range append(slices.Clone(iso.ControlPlanePorts), iso.IngressPorts...) {
		if p <= 0 || p > 65535 {
			return apperr.Invalid("kuberuntime.errors.control_plane_port_range", "port", p)
		}
	}
	if err := validatePodSelector(iso.ControlPlanePodSelector); err != nil {
		return err
	}
	return iso.Quota.validate(agent)
}

func (q TenantQuota) validate(agent corev1.ResourceRequirements) error {
	if q.Disabled {
		return nil
	}
	for key, v := range map[string]string{
		"kuberuntime.errors.quota_pods": q.Pods, "kuberuntime.errors.quota_storage": q.Storage,
		"kuberuntime.errors.quota_cpu_requests": q.CPURequests, "kuberuntime.errors.quota_memory_requests": q.MemoryRequests,
		"kuberuntime.errors.limit_max_cpu": q.MaxCPU, "kuberuntime.errors.limit_max_memory": q.MaxMemory,
	} {
		if _, err := resource.ParseQuantity(v); err != nil {
			return apperr.WrapKind(apperr.KindInvalid, err, key, "value", v)
		}
	}
	for key, v := range map[string]string{
		"kuberuntime.errors.quota_cpu": q.CPU, "kuberuntime.errors.quota_memory": q.Memory,
	} {
		if quotaLineOff(v) {
			continue
		}
		if _, err := resource.ParseQuantity(v); err != nil {
			return apperr.WrapKind(apperr.KindInvalid, err, key, "value", v)
		}
	}
	if q.CacheStorage != "" {
		if _, err := resource.ParseQuantity(q.CacheStorage); err != nil {
			return apperr.WrapKind(apperr.KindInvalid, err, "kuberuntime.errors.cache_size", "value", q.CacheStorage, "env", EnvCacheSize)
		}
	}
	maxCPU := resource.MustParse(q.MaxCPU)
	maxMem := resource.MustParse(q.MaxMemory)
	if lim := agent.Limits[corev1.ResourceCPU]; lim.Cmp(maxCPU) > 0 {
		return apperr.Invalid("kuberuntime.errors.agent_cpu_over_max", "limit", lim.String(), "max", maxCPU.String())
	}
	if lim := agent.Limits[corev1.ResourceMemory]; lim.Cmp(maxMem) > 0 {
		return apperr.Invalid("kuberuntime.errors.agent_memory_over_max", "limit", lim.String(), "max", maxMem.String())
	}
	for _, c := range []struct {
		have  resource.Quantity
		total string
		res   corev1.ResourceName
		env   string
	}{
		{agent.Requests[corev1.ResourceCPU], q.CPURequests, corev1.ResourceRequestsCPU, EnvQuotaCPURequests},
		{agent.Requests[corev1.ResourceMemory], q.MemoryRequests, corev1.ResourceRequestsMemory, EnvQuotaMemoryRequests},
		{agent.Limits[corev1.ResourceCPU], q.CPU, corev1.ResourceLimitsCPU, EnvQuotaCPU},
		{agent.Limits[corev1.ResourceMemory], q.Memory, corev1.ResourceLimitsMemory, EnvQuotaMemory},
	} {
		if quotaLineOff(c.total) {
			continue
		}
		if total := resource.MustParse(c.total); c.have.Cmp(total) > 0 {
			return apperr.Invalid("kuberuntime.errors.agent_over_quota",
				"resource", string(c.res), "value", c.have.String(), "total", total.String(), "env", c.env)
		}
	}
	return nil
}

func quotaLineOff(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || strings.EqualFold(v, QuotaOff)
}

func (q TenantQuota) hard() corev1.ResourceList {
	h := corev1.ResourceList{
		corev1.ResourcePods:            resource.MustParse(q.Pods),
		corev1.ResourceRequestsCPU:     resource.MustParse(q.CPURequests),
		corev1.ResourceRequestsMemory:  resource.MustParse(q.MemoryRequests),
		corev1.ResourceRequestsStorage: q.storageHard(),
	}
	if !quotaLineOff(q.CPU) {
		h[corev1.ResourceLimitsCPU] = resource.MustParse(q.CPU)
	}
	if !quotaLineOff(q.Memory) {
		h[corev1.ResourceLimitsMemory] = resource.MustParse(q.Memory)
	}
	return h
}

func ParseEgressAllow(s string) ([]EgressAllow, error) {
	var out []EgressAllow
	for _, raw := range splitList(s) {
		entry, port, err := splitCIDRPort(raw)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, apperr.Invalid("kuberuntime.errors.egress_entry_not_ip", "entry", raw)
			}
			if ip.To4() != nil {
				entry += "/32"
			} else {
				entry += "/128"
			}
		}
		if _, _, perr := net.ParseCIDR(entry); perr != nil {
			return nil, apperr.WrapKind(apperr.KindInvalid, perr, "kuberuntime.errors.egress_entry", "entry", raw)
		}
		out = append(out, EgressAllow{CIDR: entry, Port: port})
	}
	return out, nil
}

func splitCIDRPort(raw string) (string, int32, error) {
	host, portStr := raw, ""
	if slash := strings.IndexByte(raw, '/'); slash >= 0 {
		if colon := strings.IndexByte(raw[slash:], ':'); colon >= 0 {
			host, portStr = raw[:slash+colon], raw[slash+colon+1:]
		}
	} else if strings.Count(raw, ":") == 1 {
		host, portStr, _ = strings.Cut(raw, ":")
	}
	if portStr == "" {
		return host, 0, nil
	}
	p, err := strconv.ParseInt(portStr, 10, 32)
	if err != nil || p <= 0 || p > 65535 {
		return "", 0, apperr.Invalid("kuberuntime.errors.egress_entry_bad_port", "entry", raw, "port", portStr)
	}
	return host, int32(p), nil
}

func ParseLabels(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range splitList(s) {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			return nil, apperr.Invalid("kuberuntime.errors.bad_label", "entry", f)
		}
		out[k] = v
	}
	return out, nil
}

func ParsePorts(s string) ([]int32, error) {
	var out []int32
	for _, f := range splitList(s) {
		p, err := strconv.ParseInt(f, 10, 32)
		if err != nil || p <= 0 || p > 65535 {
			return nil, apperr.Invalid("kuberuntime.errors.bad_port", "port", f)
		}
		out = append(out, int32(p))
	}
	return out, nil
}

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
}

// Fails closed: never launch into a namespace whose isolation could not be applied.
func (r *Runtime) EnsureNamespace(ctx context.Context, name, tenant string) error {
	if err := r.ensureNamespaceObject(ctx, name, tenant); err != nil {
		return err
	}
	// Must run first: the CA ConfigMap and the pod's env Secret can only be written once this
	// binding exists.
	if err := r.ensureTenantSecretsBinding(ctx, name); err != nil {
		return err
	}
	r.RefreshNodeAddrs(ctx)
	if err := r.ensureNetworkPolicies(ctx, name); err != nil {
		return err
	}
	if err := r.ensureQuota(ctx, name); err != nil {
		return err
	}
	return r.ensureCABundle(ctx, name)
}

func (r *Runtime) namespaceLabels(tenant string) map[string]string {
	level := r.cfg.Isolation.PodSecurity
	labels := map[string]string{
		LabelPlane:                                   planeValue,
		"pod-security.kubernetes.io/enforce":         level,
		"pod-security.kubernetes.io/enforce-version": "latest",
		"pod-security.kubernetes.io/audit":           level,
		"pod-security.kubernetes.io/warn":            level,
	}
	if tenant != "" {
		labels[LabelTenant] = tenant
	}
	return labels
}

func (r *Runtime) ensureNamespaceObject(ctx context.Context, name, tenant string) error {
	want := r.namespaceLabels(tenant)
	nsClient := r.client.CoreV1().Namespaces()
	existing, err := nsClient.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, cerr := nsClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: want}}, metav1.CreateOptions{})
		if cerr == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(cerr) {
			return apperr.Wrap(cerr, "kuberuntime.errors.create_namespace", "name", name)
		}
		existing, err = nsClient.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.get_namespace", "name", name)
	}
	if !mergeLabels(&existing.ObjectMeta, want) {
		return nil
	}
	if _, err := nsClient.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsForbidden(err) {
			return apperr.Wrap(err, "kuberuntime.errors.label_namespace_forbidden", "name", name)
		}
		return apperr.Wrap(err, "kuberuntime.errors.label_namespace", "name", name)
	}
	log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.namespace_adopted", "name", name))
	return nil
}

func mergeLabels(meta *metav1.ObjectMeta, want map[string]string) bool {
	changed := false
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}
	for k, v := range want {
		if meta.Labels[k] != v {
			meta.Labels[k] = v
			changed = true
		}
	}
	return changed
}

func managedLabels() map[string]string {
	return map[string]string{LabelManagedBy: managedByValue, LabelPlane: planeValue}
}

func (r *Runtime) ensureNetworkPolicies(ctx context.Context, ns string) error {
	for _, want := range []*networkingv1.NetworkPolicy{r.defaultDenyPolicy(), r.tenantAllowPolicy()} {
		if err := r.upsertNetworkPolicy(ctx, ns, want); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) upsertNetworkPolicy(ctx context.Context, ns string, want *networkingv1.NetworkPolicy) error {
	cl := r.client.NetworkingV1().NetworkPolicies(ns)
	existing, err := cl.Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = cl.Create(ctx, want, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return apperr.Wrap(err, "kuberuntime.errors.create_network_policy", "ns", ns, "name", want.Name)
		}
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.get_network_policy", "ns", ns, "name", want.Name)
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

func (r *Runtime) defaultDenyPolicy() *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: PolicyDefaultDeny, Labels: managedLabels()},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		},
	}
}

func (r *Runtime) tenantAllowPolicy() *networkingv1.NetworkPolicy {
	iso := r.cfg.Isolation
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	port := func(proto corev1.Protocol, p int32) networkingv1.NetworkPolicyPort {
		pp := intstr.FromInt32(p)
		return networkingv1.NetworkPolicyPort{Protocol: &proto, Port: &pp}
	}
	nsPeer := func(name string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{labelMetadataName: name},
		}}
	}

	var ingress []networkingv1.NetworkPolicyIngressRule
	egress := []networkingv1.NetworkPolicyEgressRule{{
		To:    []networkingv1.NetworkPolicyPeer{nsPeer(iso.DNSNamespace)},
		Ports: []networkingv1.NetworkPolicyPort{port(udp, 53), port(tcp, 53)},
	}}
	if iso.ControlPlaneNamespace != "" {
		// Namespace and pod selector in one peer: only the daemon's pods may dial tenant pods, not
		// MCP servers or anything else in that namespace.
		daemon := nsPeer(iso.ControlPlaneNamespace)
		daemon.PodSelector = &metav1.LabelSelector{MatchLabels: maps.Clone(iso.ControlPlanePodSelector)}
		inPorts := make([]networkingv1.NetworkPolicyPort, 0, len(iso.IngressPorts))
		for _, p := range iso.IngressPorts {
			inPorts = append(inPorts, port(tcp, p))
		}
		ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
			From:  []networkingv1.NetworkPolicyPeer{daemon},
			Ports: inPorts,
		})
		cpPorts := make([]networkingv1.NetworkPolicyPort, 0, len(iso.ControlPlanePorts))
		for _, p := range iso.ControlPlanePorts {
			cpPorts = append(cpPorts, port(tcp, p))
		}
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{nsPeer(iso.ControlPlaneNamespace)},
			Ports: cpPorts,
		})
	}
	if iso.EgressMode == EgressOpen {
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
				CIDR:   "0.0.0.0/0",
				Except: openEgressExcept(iso.PrivateCIDRs, r.nodeAddrs()),
			}}},
			Ports: []networkingv1.NetworkPolicyPort{port(tcp, 443)},
		})
	}
	for _, a := range iso.EgressAllow {
		rule := networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: a.CIDR}}},
		}
		if a.Port > 0 {
			rule.Ports = []networkingv1.NetworkPolicyPort{port(tcp, a.Port)}
		}
		egress = append(egress, rule)
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: PolicyTenantAllow, Labels: managedLabels()},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     ingress,
			Egress:      egress,
		},
	}
}

// IPv4 only: an except must sit inside the rule's CIDR, and no rule admits IPv6 to the world.
// Sorted so the spec is stable across reconciles.
func openEgressExcept(private []string, nodes []netip.Addr) []string {
	set := map[string]bool{linkLocalCIDR: true}
	var ranges []netip.Prefix
	for _, c := range append([]string{linkLocalCIDR}, private...) {
		if ip, _, err := net.ParseCIDR(c); err == nil && ip.To4() != nil {
			set[c] = true
			if pfx, perr := netip.ParsePrefix(c); perr == nil {
				ranges = append(ranges, pfx.Masked())
			}
		}
	}
	for _, a := range nodes {
		if !a.Is4() || slices.ContainsFunc(ranges, func(p netip.Prefix) bool { return p.Contains(a) }) {
			continue
		}
		set[netip.PrefixFrom(a, 32).String()] = true
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func (q TenantQuota) storageHard() resource.Quantity {
	total := resource.MustParse(q.Storage)
	if q.CacheStorage != "" {
		total.Add(resource.MustParse(q.CacheStorage))
	}
	return total
}

func (r *Runtime) ensureQuota(ctx context.Context, ns string) error {
	if r.cfg.Isolation.Quota.Disabled {
		return nil
	}
	q := r.cfg.Isolation.Quota
	wantQuota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: QuotaName, Labels: managedLabels()},
		Spec:       corev1.ResourceQuotaSpec{Hard: q.hard()},
	}
	if err := r.upsertQuota(ctx, ns, wantQuota); err != nil {
		return err
	}
	wantLR := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: LimitRangeName, Labels: managedLabels()},
		Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type:           corev1.LimitTypeContainer,
			Default:        r.resources.Limits.DeepCopy(),
			DefaultRequest: r.resources.Requests.DeepCopy(),
			Max: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(q.MaxCPU),
				corev1.ResourceMemory: resource.MustParse(q.MaxMemory),
			},
		}}},
	}
	return r.upsertLimitRange(ctx, ns, wantLR)
}

func (r *Runtime) upsertQuota(ctx context.Context, ns string, want *corev1.ResourceQuota) error {
	cl := r.client.CoreV1().ResourceQuotas(ns)
	existing, err := cl.Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = cl.Create(ctx, want, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return apperr.Wrap(err, "kuberuntime.errors.create_resource_quota", "ns", ns, "name", want.Name)
		}
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.get_resource_quota", "ns", ns, "name", want.Name)
	}
	labelsChanged := mergeLabels(&existing.ObjectMeta, want.Labels)
	if !labelsChanged && apiequality.Semantic.DeepEqual(existing.Spec, want.Spec) {
		return nil
	}
	existing.Spec = want.Spec
	if _, err := cl.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.update_resource_quota", "ns", ns, "name", want.Name)
	}
	return nil
}

func (r *Runtime) upsertLimitRange(ctx context.Context, ns string, want *corev1.LimitRange) error {
	cl := r.client.CoreV1().LimitRanges(ns)
	existing, err := cl.Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = cl.Create(ctx, want, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return apperr.Wrap(err, "kuberuntime.errors.create_limit_range", "ns", ns, "name", want.Name)
		}
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.get_limit_range", "ns", ns, "name", want.Name)
	}
	labelsChanged := mergeLabels(&existing.ObjectMeta, want.Labels)
	if !labelsChanged && apiequality.Semantic.DeepEqual(existing.Spec, want.Spec) {
		return nil
	}
	existing.Spec = want.Spec
	if _, err := cl.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.update_limit_range", "ns", ns, "name", want.Name)
	}
	return nil
}

func (r *Runtime) ensureCABundle(ctx context.Context, ns string) error {
	if len(r.cfg.CABundle) == 0 {
		return nil
	}
	want := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: catrust.ConfigMapName, Labels: managedLabels()},
		Data: map[string]string{
			catrust.ConfigMapCAKey:      string(r.cfg.CABundle),
			catrust.ConfigMapProfileKey: catrust.ProfileScript,
		},
	}
	cl := r.client.CoreV1().ConfigMaps(ns)
	existing, err := cl.Get(ctx, want.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err = cl.Create(ctx, want, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return apperr.Wrap(err, "kuberuntime.errors.create_ca_bundle", "ns", ns, "name", want.Name)
		}
		return nil
	}
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.get_ca_bundle", "ns", ns, "name", want.Name)
	}
	labelsChanged := mergeLabels(&existing.ObjectMeta, want.Labels)
	if !labelsChanged && apiequality.Semantic.DeepEqual(existing.Data, want.Data) {
		return nil
	}
	existing.Data = want.Data
	if _, err := cl.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return apperr.Wrap(err, "kuberuntime.errors.update_ca_bundle", "ns", ns, "name", want.Name)
	}
	return nil
}

func (r *Runtime) CABundleConfigured() bool { return len(r.cfg.CABundle) > 0 }

func validatePodSelector(sel map[string]string) error {
	for k, v := range sel {
		errs := validation.IsQualifiedName(k)
		if len(errs) == 0 {
			errs = validation.IsValidLabelValue(v)
		}
		if len(errs) > 0 {
			return apperr.Invalid("kuberuntime.errors.pod_selector_label",
				"label", k+"="+v, "env", EnvControlPlanePodSelector, "why", strings.Join(errs, "; "))
		}
	}
	return nil
}

// Shares slices and maps with the runtime config; callers must not mutate them.
func (r *Runtime) Isolation() TenantIsolation {
	return r.cfg.Isolation
}
