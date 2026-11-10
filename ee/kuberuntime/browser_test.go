// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
)

func browserConfig(t *testing.T, mode browserrun.Mode, extra map[string]string) browserrun.Config {
	t.Helper()
	cfg, err := browserrun.ConfigFromEnv(func(k string) string {
		if k == browserrun.EnvMode {
			return string(mode)
		}
		return extra[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func findBrowser(pod *corev1.Pod) *corev1.Container {
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == browserContainerName {
			return &pod.Spec.InitContainers[i]
		}
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == browserContainerName {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

func TestBrowserSidecarOnlyWhenGrantedAndPerRun(t *testing.T) {
	perRun, _ := newFakeRuntime(t, Config{RunAsNonRoot: true, Browser: browserConfig(t, browserrun.ModePerRun, nil)})
	shared, _ := newFakeRuntime(t, Config{RunAsNonRoot: true, Browser: browserConfig(t, browserrun.ModeShared, nil)})

	spec := baseSpec()
	if findBrowser(perRun.buildPod(spec)) != nil {
		t.Error("a run without the grant got a browser")
	}
	spec.Browser = true
	if findBrowser(shared.buildPod(spec)) != nil {
		t.Error("shared mode put a browser in the agent pod")
	}
	pod := perRun.buildPod(spec)
	if findBrowser(pod) == nil {
		t.Fatal("a granted per-run launch has no browser")
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != agentContainerName {
		t.Errorf("the browser must not be a regular container (the pod would outlive the agent): %v", pod.Spec.Containers)
	}
}

func TestBrowserSidecarShape(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{
		RunAsNonRoot: true,
		Browser: browserConfig(t, browserrun.ModePerRun, map[string]string{
			browserrun.EnvImage: "registry.example/browser:9", browserrun.EnvPort: "3999",
		}),
	})
	spec := baseSpec()
	spec.Browser = true
	pod := rt.buildPod(spec)
	b := findBrowser(pod)

	if b.RestartPolicy == nil || *b.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Error("not a native sidecar: restartPolicy Always is what lets the pod end with the agent")
	}
	if pod.Spec.InitContainers[len(pod.Spec.InitContainers)-1].Name != browserContainerName {
		t.Error("the sidecar must come after the worktree loaders")
	}
	if b.Image != "registry.example/browser:9" || b.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("image %q pull %q", b.Image, b.ImagePullPolicy)
	}

	sc := b.SecurityContext
	switch {
	case sc == nil:
		t.Fatal("no securityContext")
	case sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot:
		t.Error("runAsNonRoot not set")
	case sc.RunAsUser == nil || *sc.RunAsUser == 0:
		t.Error("runAsUser must be a non-zero uid")
	case sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation:
		t.Error("privilege escalation allowed")
	case sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) != 0:
		t.Errorf("capabilities %+v, want drop ALL and nothing added", sc.Capabilities)
	case sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault:
		t.Error("seccomp is not RuntimeDefault")
	case sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem:
		t.Error("root filesystem is writable")
	case sc.Privileged != nil && *sc.Privileged:
		t.Error("privileged")
	}

	res := b.Resources
	if q := res.Requests[corev1.ResourceCPU]; q.String() != "500m" {
		t.Errorf("cpu request %s", q.String())
	}
	if q := res.Requests[corev1.ResourceMemory]; q.String() != "1Gi" {
		t.Errorf("memory request %s", q.String())
	}
	if q := res.Limits[corev1.ResourceCPU]; q.String() != "1" {
		t.Errorf("cpu limit %s", q.String())
	}
	if q := res.Limits[corev1.ResourceMemory]; q.String() != "1536Mi" {
		t.Errorf("memory limit %s", q.String())
	}

	env := map[string]corev1.EnvVar{}
	for _, e := range b.Env {
		env[e.Name] = e
	}
	if env["LISTEN_HOST"].Value != "127.0.0.1" || env["PORT"].Value != "3999" || env["HOME"].Value != "/tmp" {
		t.Errorf("literal env %v", b.Env)
	}
	secret := EnvSecretName(pod.Name)
	for name, key := range map[string]string{"MCP_REQUIRED_KEY": "OCTOPUS_API_KEY", "OCTOPUS_API": "OCTOPUS_API"} {
		ref := env[name].ValueFrom
		if env[name].Value != "" || ref == nil || ref.SecretKeyRef == nil ||
			ref.SecretKeyRef.Name != secret || ref.SecretKeyRef.Key != key {
			t.Errorf("%s must come from key %s of %s, got %+v", name, key, secret, env[name])
		}
	}
	if len(b.Env) != 5 {
		t.Errorf("browser env has %d entries, want 5: %v", len(b.Env), b.Env)
	}
	if len(b.Ports) != 0 {
		t.Errorf("the browser publishes ports %v; it listens on loopback only", b.Ports)
	}

	mounts := map[string]string{}
	for _, m := range b.VolumeMounts {
		mounts[m.MountPath] = m.Name
	}
	vols := map[string]corev1.Volume{}
	for _, v := range pod.Spec.Volumes {
		vols[v.Name] = v
	}
	for _, path := range []string{"/tmp", "/agents-shared", "/dev/shm"} {
		v, ok := vols[mounts[path]]
		if !ok || v.EmptyDir == nil || v.EmptyDir.SizeLimit == nil {
			t.Errorf("%s is not a bounded emptyDir: %+v", path, v)
		}
	}
	if vols[mounts["/dev/shm"]].EmptyDir.Medium != corev1.StorageMediumMemory {
		t.Error("/dev/shm is not memory-backed")
	}
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.Name == mounts["/tmp"] || m.Name == mounts["/agents-shared"] {
			t.Errorf("the agent mounts the browser's scratch volume %s", m.Name)
		}
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("service account token mounted")
	}
}

func TestBrowserSidecarPinsTheGrantedTools(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{
		RunAsNonRoot: true,
		Browser:      browserConfig(t, browserrun.ModePerRun, nil),
	})
	spec := baseSpec()
	spec.Browser = true
	spec.Env = map[string]string{"MCP_TOOLS": "octopus-api,thulu-browser@screenshot+get_text", "OCTOPUS_API_KEY": "k"}
	b := findBrowser(rt.buildPod(spec))
	got := ""
	for _, e := range b.Env {
		if e.Name == browserrun.GrantedToolsEnv {
			got = e.Value
		}
	}
	if got != "screenshot,get_text" {
		t.Errorf("%s = %q, want the stage's grant", browserrun.GrantedToolsEnv, got)
	}
}

func TestBrowserSidecarTrustsTheOperatorCA(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{
		RunAsNonRoot: true, CABundle: []byte("-----BEGIN CERTIFICATE-----\n"),
		Browser: browserConfig(t, browserrun.ModePerRun, nil),
	})
	spec := baseSpec()
	spec.Browser = true
	b := findBrowser(rt.buildPod(spec))
	found := false
	for _, e := range b.Env {
		if e.Name == "NODE_EXTRA_CA_CERTS" && e.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("NODE_EXTRA_CA_CERTS missing with a CA bundle configured")
	}
}

func TestLaunchPodStoresTheKeyTheBrowserReads(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{RunAsNonRoot: true, Browser: browserConfig(t, browserrun.ModePerRun, nil)})
	spec := baseSpec()
	spec.Browser = true
	spec.Env["OCTOPUS_API"] = "http://octopus.octopus.svc:8585"
	name, err := rt.LaunchPod(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor(spec.WorkspaceID)
	secret, err := client.CoreV1().Secrets(ns).Get(context.Background(), EnvSecretName(name), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"OCTOPUS_API_KEY", "OCTOPUS_API"} {
		if len(secret.Data[key]) == 0 {
			t.Errorf("the browser reads %s from the Secret, which lacks it", key)
		}
	}
}

func TestNewRefusesABrowserOverTheTenantMax(t *testing.T) {
	cfg := Config{Browser: browserConfig(t, browserrun.ModePerRun, map[string]string{browserrun.EnvMemoryLimit: "16Gi"})}
	if _, err := NewWithClient(fake.NewClientset(), cfg); err == nil {
		t.Error("accepted a per-run browser memory limit above the LimitRange max")
	}
	cfg = Config{Browser: browserConfig(t, browserrun.ModePerRun, map[string]string{browserrun.EnvCPULimit: "8"})}
	if _, err := NewWithClient(fake.NewClientset(), cfg); err == nil {
		t.Error("accepted a per-run browser cpu limit above the LimitRange max")
	}
	cfg = Config{Browser: browserConfig(t, browserrun.ModeShared, map[string]string{browserrun.EnvCPULimit: "8"})}
	if _, err := NewWithClient(fake.NewClientset(), cfg); err != nil {
		t.Errorf("shared mode refused over a sidecar it never starts: %v", err)
	}
}

func TestSidecarStatusDoesNotDecideTheExitCode(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodSucceeded,
		InitContainerStatuses: []corev1.ContainerStatus{{
			Name:  browserContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143}},
		}},
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  agentContainerName,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		}},
	}}
	if code, done := terminalExitCode(pod); !done || code != 0 {
		t.Errorf("exit code %d done %v, want the agent's 0", code, done)
	}
}
