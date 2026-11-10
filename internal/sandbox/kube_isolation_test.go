//go:build !foss

package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/catrust"
)

type recordingProvisioner struct {
	calls [][2]string
	err   error
}

func (p *recordingProvisioner) EnsureNamespace(_ context.Context, name, tenant string) error {
	p.calls = append(p.calls, [2]string{name, tenant})
	return p.err
}

func sandboxContainer(t *testing.T, pod *corev1.Pod) corev1.Container {
	t.Helper()
	for _, c := range pod.Spec.Containers {
		if c.Name == sandboxContainerName {
			return c
		}
	}
	t.Fatal("sandbox container missing")
	return corev1.Container{}
}

func TestKubeProvisionsNamespaceAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	prov := &recordingProvisioner{}
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img", Provisioner: prov})
	if _, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "Acme"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(prov.calls) != 1 || prov.calls[0] != [2]string{"tenant-acme", "acme"} {
		t.Errorf("provisioner calls = %v, want [[tenant-acme acme]]", prov.calls)
	}

	prov.err = errors.New("forbidden")
	if _, err := k.Create(ctx, CreateOpts{TaskID: "t2", WorkspaceID: "acme"}); err == nil {
		t.Fatal("Create must fail when the namespace cannot be provisioned")
	}
	if _, err := client.CoreV1().Pods("tenant-acme").Get(ctx, sandboxPodName("t2"), metav1.GetOptions{}); err == nil {
		t.Error("no pod may be scheduled into an unprovisioned namespace")
	}
}

func TestKubeEnvLivesInSecret(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img", Env: map[string]string{
		"HTTPS_PROXY": "http://user:pw@proxy:3128",
	}})
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, _ := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{})
	c := sandboxContainer(t, pod)
	for _, e := range c.Env {
		if strings.Contains(e.Value, "pw@") {
			t.Errorf("env %s carries a credential literal in the pod spec", e.Name)
		}
	}
	secret, err := client.CoreV1().Secrets("tenant-x").Get(ctx, kuberuntime.EnvSecretName(res.ContainerID), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("env secret: %v", err)
	}
	if string(secret.Data["HTTPS_PROXY"]) != "http://user:pw@proxy:3128" {
		t.Errorf("proxy missing from the env secret: %v", secret.Data)
	}

	if err := k.Remove(ctx, res.ContainerID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := client.CoreV1().Secrets("tenant-x").Get(ctx, secret.Name, metav1.GetOptions{}); err == nil {
		t.Error("Remove must delete the env secret")
	}
}

func TestKubeAdoptsLegacyLiteralTokenPod(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img"})
	legacy := k.buildSandboxPod(sandboxPodName("t1"), CreateOpts{TaskID: "t1"})
	legacy.Namespace = "tenant-x"
	legacy.Spec.Containers[0].Env = []corev1.EnvVar{{Name: execAgentAuthEnv, Value: "legacy-token"}}
	legacy.Status.Phase = corev1.PodRunning
	if _, err := client.CoreV1().Pods("tenant-x").Create(ctx, legacy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	k.mu.Lock()
	rec := k.pods[sandboxPodName("t1")]
	k.mu.Unlock()
	if rec.token != "legacy-token" {
		t.Errorf("adopted token = %q, want the legacy literal", rec.token)
	}
}

func TestKubeMemoryCapOverridesLimit(t *testing.T) {
	k := NewKube(fake.NewClientset(), KubeConfig{Image: "img", MemoryRequest: "8Gi", CPURequest: "bogus"})
	res := k.resourcesFor(CreateOpts{MemoryBytes: 1 << 30})
	if got := res.Limits.Memory(); got.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Errorf("memory limit = %s, want the manager's 1Gi cap", got)
	}
	if got := res.Requests.Memory(); got.Cmp(*res.Limits.Memory()) != 0 {
		t.Errorf("memory request %s above its limit must be clamped to it", got)
	}
	if got := res.Requests.Cpu(); got.Cmp(resource.MustParse("250m")) != 0 {
		t.Errorf("a malformed cpu request must fall back to 250m, got %s", got)
	}
	def := k.resourcesFor(CreateOpts{})
	if got := def.Limits.Memory(); got.Cmp(resource.MustParse("6Gi")) != 0 {
		t.Errorf("default memory limit = %s, want 6Gi", got)
	}
}

func TestKubeMountsCABundle(t *testing.T) {
	k := NewKube(fake.NewClientset(), KubeConfig{Image: "img", CABundle: true})
	pod := k.buildSandboxPod("sandbox-t1", CreateOpts{TaskID: "t1"})
	c := sandboxContainer(t, pod)
	got := map[string]string{}
	for _, m := range c.VolumeMounts {
		got[m.MountPath] = m.SubPath
	}
	if got[catrust.ContainerPath] != catrust.ConfigMapCAKey || got[catrust.ProfilePath] != catrust.ConfigMapProfileKey {
		t.Errorf("CA mounts = %v", got)
	}
	var hasVol bool
	for _, v := range pod.Spec.Volumes {
		if v.ConfigMap != nil && v.ConfigMap.Name == catrust.ConfigMapName {
			hasVol = true
		}
	}
	if !hasVol {
		t.Error("CA ConfigMap volume missing")
	}

	plain := NewKube(fake.NewClientset(), KubeConfig{Image: "img"}).buildSandboxPod("sandbox-t1", CreateOpts{TaskID: "t1"})
	if len(plain.Spec.Volumes) != 1 {
		t.Errorf("without a CA bundle only the worktree volume is mounted, got %d", len(plain.Spec.Volumes))
	}
}

func TestKubeConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"OCTOPUS_SANDBOX_IMAGE":       "octopus-sandbox:1",
		"OCTOPUS_RUNTIME_CLASS":       "kata-clh",
		"OCTOPUS_SANDBOX_CPU_LIMIT":   "3",
		"OCTOPUS_EXTRA_CA_BUNDLE":     "/etc/ssl/corp.pem",
		"https_proxy":                 "http://proxy:3128",
		"OCTOPUS_SANDBOX_NAMESPACE":   "sbx",
		"OCTOPUS_SANDBOX_CPU_REQUEST": "500m",
	}
	cfg := KubeConfigFromEnv(func(k string) string { return env[k] })
	if cfg.Image != "octopus-sandbox:1" || cfg.RuntimeClass != "kata-clh" || cfg.Namespace != "sbx" ||
		cfg.CPULimit != "3" || cfg.CPURequest != "500m" || !cfg.CABundle {
		t.Errorf("KubeConfigFromEnv = %+v", cfg)
	}
	if cfg.Env["HTTPS_PROXY"] != "http://proxy:3128" {
		t.Errorf("proxy env not forwarded: %v", cfg.Env)
	}
}
