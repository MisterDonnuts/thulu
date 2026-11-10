// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/catrust"
	"github.com/MisterDonnuts/thulu/internal/proxyenv"
)

const (
	EnvInCluster           = "OCTOPUS_KUBE_INCLUSTER"
	EnvKubeconfig          = "OCTOPUS_KUBECONFIG"
	EnvContext             = "OCTOPUS_KUBE_CONTEXT"
	EnvRuntimeClass        = "OCTOPUS_RUNTIME_CLASS"
	EnvAllowNoRuntimeClass = "OCTOPUS_KUBE_ALLOW_NO_RUNTIMECLASS"

	EnvControlPlaneNamespace   = "OCTOPUS_CONTROL_PLANE_NAMESPACE"
	EnvPodNamespace            = "POD_NAMESPACE"
	EnvControlPlanePorts       = "OCTOPUS_TENANT_CONTROL_PLANE_PORTS"
	EnvControlPlanePodSelector = "OCTOPUS_CONTROL_PLANE_POD_SELECTOR"
	EnvIngressPorts            = "OCTOPUS_TENANT_INGRESS_PORTS"
	EnvPodSecurity             = "OCTOPUS_TENANT_POD_SECURITY"
	EnvDNSNamespace            = "OCTOPUS_TENANT_DNS_NAMESPACE"
	EnvEgressMode              = "OCTOPUS_TENANT_EGRESS_MODE"
	EnvEgressAllow             = "OCTOPUS_TENANT_EGRESS_ALLOW_CIDRS"
	EnvPrivateCIDRs            = "OCTOPUS_TENANT_PRIVATE_CIDRS"
	EnvQuota                   = "OCTOPUS_TENANT_QUOTA"
	EnvQuotaPods               = "OCTOPUS_TENANT_QUOTA_PODS"
	EnvQuotaCPURequests        = "OCTOPUS_TENANT_QUOTA_CPU_REQUESTS"
	EnvQuotaMemoryRequests     = "OCTOPUS_TENANT_QUOTA_MEMORY_REQUESTS"
	EnvQuotaCPU                = "OCTOPUS_TENANT_QUOTA_CPU"
	EnvQuotaMemory             = "OCTOPUS_TENANT_QUOTA_MEMORY"
	EnvQuotaStorage            = "OCTOPUS_TENANT_QUOTA_STORAGE"
	EnvLimitMaxCPU             = "OCTOPUS_TENANT_LIMIT_MAX_CPU"
	EnvLimitMaxMemory          = "OCTOPUS_TENANT_LIMIT_MAX_MEMORY"
)

const serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

var readFile = os.ReadFile

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	env := func(k string) string { return strings.TrimSpace(getenv(k)) }
	agentImages := agentimage.FromEnv(getenv)
	cfg := Config{
		InCluster:           strings.EqualFold(env(EnvInCluster), "true"),
		KubeconfigPath:      env(EnvKubeconfig),
		Context:             env(EnvContext),
		Image:               agentImages.Override,
		EngineImages:        agentImages.ByEngine,
		RuntimeClassName:    env(EnvRuntimeClass),
		AllowNoRuntimeClass: truthy(env(EnvAllowNoRuntimeClass)),
		RunAsNonRoot:        true,
		ForwardEnv:          proxyenv.FromEnv(getenv),
	}
	applySizingEnv(&cfg, env)

	iso := &cfg.Isolation
	iso.PodSecurity = strings.ToLower(env(EnvPodSecurity))
	iso.DNSNamespace = env(EnvDNSNamespace)
	iso.EgressMode = strings.ToLower(env(EnvEgressMode))
	iso.ControlPlaneNamespace = controlPlaneNamespace(env, cfg.InCluster)
	var err error
	if v := env(EnvControlPlanePorts); v != "" {
		if iso.ControlPlanePorts, err = ParsePorts(v); err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvControlPlanePorts, err)
		}
	}
	if v := env(EnvIngressPorts); v != "" {
		if iso.IngressPorts, err = ParsePorts(v); err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvIngressPorts, err)
		}
	}
	if v := env(EnvControlPlanePodSelector); v != "" {
		if iso.ControlPlanePodSelector, err = ParseLabels(v); err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvControlPlanePodSelector, err)
		}
	}
	if iso.EgressAllow, err = ParseEgressAllow(env(EnvEgressAllow)); err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvEgressAllow, err)
	}
	if iso.EgressMode == EgressProxyOnly {
		iso.EgressAllow = append(iso.EgressAllow, proxyAllow(cfg.ForwardEnv)...)
	}
	if v := env(EnvPrivateCIDRs); v != "" {
		iso.PrivateCIDRs = splitList(v)
	}
	if iso.Quota, err = quotaFromEnv(env); err != nil {
		return Config{}, err
	}

	cfg.TenantSecrets = TenantSecrets{
		ClusterRole:    env(EnvTenantSecretsRole),
		ServiceAccount: env(EnvServiceAccount),
		Namespace:      ownNamespace(env, cfg.InCluster),
	}
	if verr := cfg.TenantSecrets.validate(); verr != nil {
		return Config{}, verr
	}
	if cfg.Browser, err = browserrun.ConfigFromEnv(getenv); err != nil {
		return Config{}, err
	}

	cfg.ImageBuild = imageBuildFromEnv(env)

	path := env(catrust.BundleEnv)
	if path != "" {
		if cfg.CABundle, err = catrust.LoadFile(path); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func quotaFromEnv(env func(string) string) (TenantQuota, error) {
	q := TenantQuota{
		Pods:           env(EnvQuotaPods),
		CPURequests:    env(EnvQuotaCPURequests),
		MemoryRequests: env(EnvQuotaMemoryRequests),
		CPU:            env(EnvQuotaCPU),
		Memory:         env(EnvQuotaMemory),
		Storage:        env(EnvQuotaStorage),
		MaxCPU:         env(EnvLimitMaxCPU),
		MaxMemory:      env(EnvLimitMaxMemory),
	}
	if off := strings.ToLower(env(EnvQuota)); off == "off" || off == "0" || off == "false" || off == "disabled" {
		q.Disabled = true
	}
	cacheSize, cacheOn, err := ParseCacheSize(env(EnvCacheSize))
	if err != nil {
		return TenantQuota{}, err
	}
	if cacheOn {
		q.CacheStorage = cacheSize.String()
	}
	return q, nil
}

// A NetworkPolicy cannot name a host, so only IP proxies are allowed here; hostname proxies go in
// OCTOPUS_TENANT_EGRESS_ALLOW_CIDRS.
func proxyAllow(forward map[string]string) []EgressAllow {
	seen := map[EgressAllow]bool{}
	var out []EgressAllow
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		u, err := url.Parse(forward[k])
		if err != nil || u.Host == "" {
			continue
		}
		ip := net.ParseIP(u.Hostname())
		if ip == nil {
			continue
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		p, err := strconv.ParseInt(port, 10, 32)
		if err != nil || p <= 0 || p > 65535 {
			continue
		}
		bits := "/32"
		if ip.To4() == nil {
			bits = "/128"
		}
		a := EgressAllow{CIDR: ip.String() + bits, Port: int32(p)}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

func controlPlaneNamespace(env func(string) string, inCluster bool) string {
	if ns := env(EnvControlPlaneNamespace); ns != "" {
		return ns
	}
	return ownNamespace(env, inCluster)
}

func ownNamespace(env func(string) string, inCluster bool) string {
	if ns := env(EnvPodNamespace); ns != "" {
		return ns
	}
	if inCluster {
		if b, err := readFile(serviceAccountNamespaceFile); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
