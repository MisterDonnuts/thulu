// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
)

func newFakeRuntime(t *testing.T, cfg Config) (*Runtime, *fake.Clientset) {
	t.Helper()
	client := fake.NewClientset()
	rt, err := NewWithClient(client, cfg)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	rt.pollInterval = 5 * time.Millisecond
	return rt, client
}

func TestNamespaceFor(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	cases := map[string]string{
		"acme":              "tenant-acme",
		"WS_42":             "tenant-ws-42",
		"--weird..chars--":  "tenant-weird--chars",
		"MiXeD-Case-Te日ant": "tenant-mixed-case-te-ant",
	}
	for in, want := range cases {
		if got := rt.NamespaceFor(in); got != want {
			t.Errorf("NamespaceFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNamespaceForCustomTemplate(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{NamespaceTemplate: "octo-%s-agents"})
	if got := rt.NamespaceFor("acme"); got != "octo-acme-agents" {
		t.Errorf("custom template: got %q", got)
	}
}

func TestNewWithClientRejectsBadTemplate(t *testing.T) {
	if _, err := NewWithClient(fake.NewClientset(), Config{NamespaceTemplate: "no-placeholder"}); err == nil {
		t.Fatal("expected error for template without placeholder")
	}
}

func TestNewWithClientRejectsBadQuantity(t *testing.T) {
	if _, err := NewWithClient(fake.NewClientset(), Config{MemoryLimit: "six gigs"}); err == nil {
		t.Fatal("expected error for unparseable memory limit")
	}
}

func TestSanitizeDNS1123(t *testing.T) {
	cases := map[string]string{
		"simple":         "simple",
		"Upper_Case.ID":  "upper-case-id",
		"-trim--edges-":  "trim--edges",
		"":               "",
		"agent-task-abc": "agent-task-abc",
	}
	for in, want := range cases {
		if got := sanitizeDNS1123(in); got != want {
			t.Errorf("sanitizeDNS1123(%q) = %q, want %q", in, got, want)
		}
	}
	long := sanitizeDNS1123("a0123456789012345678901234567890123456789012345678901234567890123456789")
	if len(long) > 63 {
		t.Errorf("sanitized name exceeds 63 chars: %d", len(long))
	}
}

func TestEnsureTenantNamespaceIdempotent(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatalf("second ensure (idempotency): %v", err)
	}
	ns, err := client.CoreV1().Namespaces().Get(ctx, "tenant-acme", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if ns.Labels[LabelTenant] != "acme" || ns.Labels[LabelPlane] != "data" {
		t.Errorf("namespace labels = %v, want tenant=acme plane=data", ns.Labels)
	}
}

func baseSpec() LaunchSpec {
	return LaunchSpec{
		WorkspaceID: "acme",
		TaskID:      "task1234",
		Stage:       "in_progress",
		InstanceID:  "coder-task-task1234-in-progress-99",
		Image:       "octopus/agent:test",
		Env:         map[string]string{"TASK_ID": "task1234", "OCTOPUS_API_KEY": "sk-scoped"},
		Labels:      map[string]string{"custom": "yes", LabelManagedBy: "spoofed"},
	}
}

func TestBuildPodWorktreeHydration(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RunAsNonRoot: true})
	spec := baseSpec()
	spec.Worktrees = []WorktreeMount{
		{
			Name: "webshop", CloneURL: "http://daemon:8585/git/task1234/webshop", Branch: "octo/task1234",
			ReadOnly: false, CredentialEnv: "OCTOPUS_API_KEY",
		},
	}
	pod := rt.buildPod(spec)

	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].EmptyDir == nil {
		t.Fatalf("want 1 emptyDir volume, got %+v", pod.Spec.Volumes)
	}
	volName := pod.Spec.Volumes[0].Name

	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("want 1 initContainer, got %d", len(pod.Spec.InitContainers))
	}
	ic := pod.Spec.InitContainers[0]
	cmd := strings.Join(ic.Command, " ")
	if !strings.Contains(cmd, " clone ") || !strings.Contains(cmd, spec.Worktrees[0].CloneURL) {
		t.Errorf("initContainer command missing clone: %q", cmd)
	}
	if !strings.Contains(cmd, "octo/task1234") {
		t.Errorf("initContainer command missing branch: %q", cmd)
	}
	if strings.Contains(cmd, "sk-scoped") {
		t.Errorf("run key leaked into the clone command: %q", cmd)
	}
	if !strings.Contains(cmd, "${OCTOPUS_API_KEY}") || !strings.Contains(cmd, "config credential.helper") {
		t.Errorf("clone must authenticate through an env-reading credential helper: %q", cmd)
	}
	if ic.SecurityContext == nil || ic.SecurityContext.Capabilities == nil ||
		len(ic.SecurityContext.Capabilities.Drop) == 0 {
		t.Errorf("initContainer must drop capabilities (PSA restricted): %+v", ic.SecurityContext)
	}
	if len(ic.VolumeMounts) != 1 || ic.VolumeMounts[0].Name != volName {
		t.Errorf("initContainer must mount the worktree volume rw, got %+v", ic.VolumeMounts)
	}

	agent := pod.Spec.Containers[0]
	if len(agent.VolumeMounts) != 1 || agent.VolumeMounts[0].Name != volName ||
		agent.VolumeMounts[0].MountPath != "/workspace/projects/webshop" {
		t.Errorf("agent must mount worktree at project path, got %+v", agent.VolumeMounts)
	}
}

func TestBuildPodPullsLocalImages(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RunAsNonRoot: true})
	spec := baseSpec()
	spec.Image = "octopus-docker-agent:latest"
	spec.Worktrees = []WorktreeMount{
		{Name: "webshop", CloneURL: "http://daemon:8585/git/task1234/webshop", Branch: "octo/task1234"},
	}
	pod := rt.buildPod(spec)

	if len(pod.Spec.InitContainers) == 0 {
		t.Fatal("expected at least one initContainer to check")
	}
	for _, c := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		if c.ImagePullPolicy != corev1.PullIfNotPresent {
			t.Errorf("container %q has ImagePullPolicy %q, want %q — a :latest tag would otherwise default to Always and never resolve locally",
				c.Name, c.ImagePullPolicy, corev1.PullIfNotPresent)
		}
	}
}

func TestBuildPodSharedWorktreePVC(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	spec := baseSpec()
	spec.WorktreePVC = "octopus-wt-task1234"
	spec.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://x/app", Branch: "octo/task1234"}}
	pod := rt.buildPod(spec)

	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].PersistentVolumeClaim == nil ||
		pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "octopus-wt-task1234" {
		t.Fatalf("agent must mount the shared worktree PVC, got %+v", pod.Spec.Volumes)
	}

	agent := pod.Spec.Containers[0]
	if len(agent.VolumeMounts) != 1 || agent.VolumeMounts[0].MountPath != "/workspace/projects" {
		t.Errorf("agent must mount the PVC at /workspace/projects, got %+v", agent.VolumeMounts)
	}

	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("want 1 clone initContainer hydrating the PVC, got %d", len(pod.Spec.InitContainers))
	}
	ic := pod.Spec.InitContainers[0]
	cmd := strings.Join(ic.Command, " ")
	if !strings.Contains(cmd, " clone ") || !strings.Contains(cmd, "http://x/app") {
		t.Errorf("clone initContainer command missing clone: %q", cmd)
	}
	if !strings.Contains(cmd, "already hydrated") {
		t.Errorf("clone must be idempotent (relaunch/subtask share the PVC): %q", cmd)
	}
	if !strings.Contains(cmd, "remote set-url origin") {
		t.Errorf("idempotent path must scrub credentials from origin: %q", cmd)
	}
	if len(ic.VolumeMounts) != 1 || ic.VolumeMounts[0].MountPath != "/workspace/projects" {
		t.Errorf("clone init must mount the PVC at the projects root, got %+v", ic.VolumeMounts)
	}
	if ic.SecurityContext == nil || ic.SecurityContext.RunAsUser == nil || *ic.SecurityContext.RunAsUser != 1000 {
		t.Errorf("loader must pin a numeric non-root uid (RunAsNonRoot verification), got %+v", ic.SecurityContext)
	}

	if pod.Spec.SecurityContext.FSGroup == nil || *pod.Spec.SecurityContext.FSGroup != 1000 {
		t.Errorf("pod with volumes must set fsGroup=1000, got %+v", pod.Spec.SecurityContext.FSGroup)
	}
}

func TestLaunchPodEnsuresWorktreePVC(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	spec := baseSpec()
	spec.WorktreePVC = "octopus-wt-task1234"
	spec.Worktrees = []WorktreeMount{{Name: "app", CloneURL: "http://x/app"}}
	if _, err := rt.LaunchPod(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor(spec.WorkspaceID)
	pvc, err := client.CoreV1().PersistentVolumeClaims(ns).Get(context.Background(), "octopus-wt-task1234", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("LaunchPod must create the worktree PVC (the sandbox is acquired lazily, so nobody else does): %v", err)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("worktree PVC must be RWO (co-locates agent+sandbox on one node), got %+v", pvc.Spec.AccessModes)
	}
	spec.InstanceID = "coder-task-task1234-in-progress-100"
	if _, err := rt.LaunchPod(context.Background(), spec); err != nil {
		t.Fatalf("second launch against an existing PVC must succeed: %v", err)
	}
}

func TestBuildPodNoWorktreesStaysZeroVolume(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	pod := rt.buildPod(baseSpec())
	if len(pod.Spec.Volumes) != 0 || len(pod.Spec.InitContainers) != 0 {
		t.Errorf("no-worktree pod must stay zero-volume: volumes=%d init=%d",
			len(pod.Spec.Volumes), len(pod.Spec.InitContainers))
	}
}

func TestBuildPodSecurityInvariants(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RuntimeClassName: "gvisor", RunAsNonRoot: true})
	pod := rt.buildPod(baseSpec())

	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("automountServiceAccountToken must be false")
	}
	if len(pod.Spec.Volumes) != 0 {
		t.Errorf("pod must mount no volumes (no hostPath, no SA token), got %d", len(pod.Spec.Volumes))
	}
	if len(pod.Spec.Containers) != 1 || len(pod.Spec.Containers[0].VolumeMounts) != 0 {
		t.Error("container must have no volume mounts")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %v, want Never", pod.Spec.RestartPolicy)
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "gvisor" {
		t.Error("runtimeClassName must be gvisor when configured")
	}
	if pod.Spec.SecurityContext.RunAsNonRoot == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Error("runAsNonRoot must be set when configured")
	}
	cs := pod.Spec.Containers[0].SecurityContext
	if cs.AllowPrivilegeEscalation == nil || *cs.AllowPrivilegeEscalation {
		t.Error("allowPrivilegeEscalation must be false")
	}
	if cs.Capabilities == nil || len(cs.Capabilities.Drop) != 1 || cs.Capabilities.Drop[0] != "ALL" {
		t.Error("capabilities must drop ALL")
	}
	if pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds != defaultGracePeriod {
		t.Error("terminationGracePeriodSeconds must default to a small value")
	}
}

func TestBuildPodDefaultsAndLabels(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	pod := rt.buildPod(baseSpec())

	if pod.Spec.RuntimeClassName != nil {
		t.Errorf("runtimeClassName must be unset (cluster default) when not configured, got %q", *pod.Spec.RuntimeClassName)
	}
	if pod.Spec.SecurityContext.RunAsNonRoot != nil {
		t.Error("runAsNonRoot must be unset when not configured (test images may run as root)")
	}
	wantLabels := map[string]string{
		LabelTaskID:    "task1234",
		LabelWorkspace: "acme",
		LabelStage:     "in-progress",
		LabelManagedBy: "octopus",
		"custom":       "yes",
	}
	for k, want := range wantLabels {
		if got := pod.Labels[k]; got != want {
			t.Errorf("label %s = %q, want %q", k, got, want)
		}
	}
	if pod.Name != "coder-task-task1234-in-progress-99" {
		t.Errorf("pod name = %q", pod.Name)
	}

	mem := pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
	if mem.String() != "6Gi" {
		t.Errorf("memory limit = %s, want 6Gi", mem.String())
	}

	env := pod.Spec.Containers[0].Env
	if len(env) != 2 || env[0].Name != "OCTOPUS_API_KEY" || env[1].Name != "TASK_ID" {
		t.Errorf("env = %v, want sorted OCTOPUS_API_KEY,TASK_ID", env)
	}
	for _, e := range env {
		if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil ||
			e.ValueFrom.SecretKeyRef.Name != EnvSecretName(pod.Name) || e.ValueFrom.SecretKeyRef.Key != e.Name {
			t.Errorf("env %s must be a secretKeyRef into %s, got %+v", e.Name, EnvSecretName(pod.Name), e)
		}
	}
}

func TestLaunchPodValidation(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if _, err := rt.LaunchPod(ctx, LaunchSpec{TaskID: "t", InstanceID: "i", Image: "img"}); err == nil {
		t.Error("expected error for missing WorkspaceID")
	}
	spec := baseSpec()
	spec.Image = ""
	if _, err := rt.LaunchPod(ctx, spec); err == nil {
		t.Error("expected error when no image in spec and no default configured")
	}
}

func TestLaunchPodUsesDefaultImage(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{Image: "octopus/agent:default"})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	spec := baseSpec()
	spec.Image = ""
	name, err := rt.LaunchPod(ctx, spec)
	if err != nil {
		t.Fatalf("LaunchPod: %v", err)
	}
	pod, err := client.CoreV1().Pods("tenant-acme").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if pod.Spec.Containers[0].Image != "octopus/agent:default" {
		t.Errorf("image = %q, want config default", pod.Spec.Containers[0].Image)
	}
}

func TestLaunchPodPicksTheEngineImage(t *testing.T) {
	cfg, err := ConfigFromEnv(func(k string) string {
		return map[string]string{
			"OCTOPUS_AGENT_CLAUDE_CODE_IMAGE": "octopus/agent-claude-code:1",
			"OCTOPUS_AGENT_CODEX_IMAGE":       "octopus/agent-codex:1",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, client := newFakeRuntime(t, cfg)
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ engine, want string }{
		{"codex", "octopus/agent-codex:1"},
		{"", "octopus/agent-claude-code:1"},
	} {
		spec := baseSpec()
		spec.Image, spec.Engine = "", tc.engine
		spec.InstanceID += string(rune('a' + i))
		name, err := rt.LaunchPod(ctx, spec)
		if err != nil {
			t.Fatalf("LaunchPod(%q): %v", tc.engine, err)
		}
		pod, err := client.CoreV1().Pods("tenant-acme").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := pod.Spec.Containers[0].Image; got != tc.want {
			t.Errorf("engine %q: image = %q, want %q", tc.engine, got, tc.want)
		}
	}
	spec := baseSpec()
	spec.Image, spec.Engine = "", "opencode"
	if _, err := rt.LaunchPod(ctx, spec); err == nil || !strings.Contains(err.Error(), "OCTOPUS_AGENT_OPENCODE_IMAGE") {
		t.Errorf("no opencode image: err = %v, want it to name the variable", err)
	}
}

func markTerminated(t *testing.T, client *fake.Clientset, ns, name string, exitCode int32) {
	t.Helper()
	ctx := context.Background()
	pod, err := client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod for status update: %v", err)
	}
	phase := corev1.PodSucceeded
	if exitCode != 0 {
		phase = corev1.PodFailed
	}
	pod.Status = corev1.PodStatus{
		Phase: phase,
		ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "agent",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exitCode}},
		}},
	}
	if _, err := client.CoreV1().Pods(ns).UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update pod status: %v", err)
	}
}

func TestWatchExitDeliversExitCode(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	name, err := rt.LaunchPod(ctx, baseSpec())
	if err != nil {
		t.Fatalf("LaunchPod: %v", err)
	}
	ns := rt.NamespaceFor("acme")

	if !rt.PodAlive(ctx, name, ns) {
		t.Error("PodAlive must be true before termination")
	}

	got := make(chan int, 1)
	rt.WatchExit(ctx, name, ns, func(code int) { got <- code })
	markTerminated(t, client, ns, name, 7)

	select {
	case code := <-got:
		if code != 7 {
			t.Errorf("exit code = %d, want 7", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchExit never delivered the exit code")
	}
	if rt.PodAlive(ctx, name, ns) {
		t.Error("PodAlive must be false after termination")
	}
}

func TestWatchExitPodDeleted(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	name, err := rt.LaunchPod(ctx, baseSpec())
	if err != nil {
		t.Fatalf("LaunchPod: %v", err)
	}
	ns := rt.NamespaceFor("acme")

	got := make(chan int, 1)
	rt.WatchExit(ctx, name, ns, func(code int) { got <- code })
	if err := client.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete pod: %v", err)
	}

	select {
	case code := <-got:
		if code != ExitDeleted {
			t.Errorf("exit code = %d, want %d for deleted pod", code, ExitDeleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchExit never reported the deleted pod")
	}
}

func TestListExited(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor("acme")

	specA := baseSpec()
	specA.InstanceID = "pod-a"
	specA.TaskID = "taska"
	nameA, err := rt.LaunchPod(ctx, specA)
	if err != nil {
		t.Fatal(err)
	}
	specB := baseSpec()
	specB.InstanceID = "pod-b"
	specB.TaskID = "taskb"
	nameB, err := rt.LaunchPod(ctx, specB)
	if err != nil {
		t.Fatal(err)
	}
	specC := baseSpec()
	specC.InstanceID = "pod-still-running"
	if _, err := rt.LaunchPod(ctx, specC); err != nil {
		t.Fatal(err)
	}

	markTerminated(t, client, ns, nameA, 7)
	markTerminated(t, client, ns, nameB, 0)

	exited, err := rt.ListExited(ctx, ns)
	if err != nil {
		t.Fatalf("ListExited: %v", err)
	}
	if len(exited) != 2 {
		t.Fatalf("ListExited returned %d pods, want 2 (running pod must be excluded): %+v", len(exited), exited)
	}
	byName := map[string]ExitedPod{}
	for _, e := range exited {
		byName[e.Name] = e
	}
	if e := byName[nameA]; e.ExitCode != 7 || e.TaskID != "taska" {
		t.Errorf("pod-a = %+v, want exit 7 task taska", e)
	}
	if e := byName[nameB]; e.ExitCode != 0 || e.TaskID != "taskb" {
		t.Errorf("pod-b = %+v, want exit 0 task taskb", e)
	}
}

func TestDeletePodIdempotent(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	name, err := rt.LaunchPod(ctx, baseSpec())
	if err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor("acme")
	if err := rt.DeletePod(ctx, name, ns); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := rt.DeletePod(ctx, name, ns); err != nil {
		t.Fatalf("second delete (idempotency): %v", err)
	}
	if rt.PodAlive(ctx, name, ns) {
		t.Error("PodAlive must be false after delete")
	}
}

func TestListPodsAllCoversRunningAndExited(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor("acme")

	live := baseSpec()
	live.InstanceID = "coder-task-task1234-in-progress-1"
	live.Annotations = map[string]string{AnnotationInstanceID: live.InstanceID, AnnotationAgentType: "developer"}
	liveName, err := rt.LaunchPod(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	done := baseSpec()
	done.InstanceID = "tester-task-task1234-testing-2"
	done.Stage = "testing"
	done.Annotations = map[string]string{AnnotationInstanceID: done.InstanceID, AnnotationAgentType: "tester"}
	doneName, err := rt.LaunchPod(ctx, done)
	if err != nil {
		t.Fatal(err)
	}
	markTerminated(t, client, ns, doneName, 3)

	pods, err := rt.ListPodsAll(ctx)
	if err != nil {
		t.Fatalf("ListPodsAll: %v", err)
	}
	byName := map[string]RunningPod{}
	for _, p := range pods {
		byName[p.Name] = p
	}
	if len(byName) != 2 {
		t.Fatalf("ListPodsAll returned %d pods, want 2: %+v", len(byName), pods)
	}
	if p := byName[liveName]; !p.Running || p.ExitCode != nil ||
		p.AgentType != "developer" || p.TaskID != "task1234" || p.Namespace != ns {
		t.Errorf("live pod = %+v, want running developer on task1234 in %s", p, ns)
	}
	if p := byName[doneName]; p.Running || p.ExitCode == nil || *p.ExitCode != 3 || p.AgentType != "tester" {
		t.Errorf("exited pod = %+v, want terminated tester with exit 3", p)
	}
	if byName[liveName].InstanceID != live.InstanceID {
		t.Errorf("instance id = %q, want the unsanitized %q", byName[liveName].InstanceID, live.InstanceID)
	}
}

func TestFindPodAcrossNamespaces(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	ctx := context.Background()
	for _, ws := range []string{"acme", "globex"} {
		if err := rt.EnsureTenantNamespace(ctx, ws); err != nil {
			t.Fatal(err)
		}
	}
	spec := baseSpec()
	spec.WorkspaceID = "globex"
	name, err := rt.LaunchPod(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := rt.FindPod(ctx, name)
	if !ok {
		t.Fatalf("FindPod(%s) not found; it lives in a tenant namespace the caller doesn't know", name)
	}
	if p.Namespace != rt.NamespaceFor("globex") {
		t.Errorf("namespace = %q, want %q", p.Namespace, rt.NamespaceFor("globex"))
	}
	if _, ok := rt.FindPod(ctx, "no-such-pod"); ok {
		t.Error("FindPod must report not-found for an unknown pod")
	}
}

func TestPodExitedReportsTheContainerThatEndedThePod(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	ctx := context.Background()
	ns := "tenant-acme"
	put := func(name string, status corev1.PodStatus) {
		t.Helper()
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Status: status}
		if _, err := client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	terminated := func(code int32, reason string) corev1.ContainerState {
		return corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: reason}}
	}
	put("running", corev1.PodStatus{Phase: corev1.PodRunning})
	put("agent-died", corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{
		{Name: "agent", State: terminated(91, "Error")},
	}})
	put("clone-failed", corev1.PodStatus{
		Phase: corev1.PodFailed,
		InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "clone-api", State: terminated(0, "Completed")},
			{Name: "clone-web", State: terminated(128, "Error")},
		},
		ContainerStatuses: []corev1.ContainerStatus{
			{Name: "agent", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
		},
	})
	put("evicted", corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"})

	if exit, done := rt.PodExited(ctx, "running", ns); done {
		t.Errorf("running pod reported exited: %+v", exit)
	}
	cases := map[string]PodExit{
		"agent-died":   {Container: "agent", ExitCode: 91, Reason: "Error"},
		"clone-failed": {Container: "clone-web", ExitCode: 128, Reason: "Error"},
		"evicted":      {ExitCode: ExitDeleted, Reason: "Evicted"},
		"never-there":  {ExitCode: ExitDeleted, Reason: "NotFound"},
	}
	for name, want := range cases {
		got, done := rt.PodExited(ctx, name, ns)
		if !done || got != want {
			t.Errorf("%s: PodExited = %+v, %v; want %+v, true", name, got, done, want)
		}
	}
}

// Agent and browser containers must keep the image entrypoint so tini stays PID 1 (reaps orphans,
// forwards SIGTERM); a command: replaces it.
func TestBuildPodKeepsTheImageInit(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{RunAsNonRoot: true, Browser: browserConfig(t, browserrun.ModePerRun, nil)})
	spec := baseSpec()
	spec.Browser = true
	pod := rt.buildPod(spec)

	agent := pod.Spec.Containers[0]
	if len(agent.Command) != 0 || len(agent.Args) != 0 {
		t.Errorf("agent command %q args %q, want both unset so the image's tini stays PID 1", agent.Command, agent.Args)
	}
	b := findBrowser(pod)
	if b == nil {
		t.Fatal("no browser sidecar")
	}
	if len(b.Command) != 0 || len(b.Args) != 0 {
		t.Errorf("browser command %q args %q, want both unset", b.Command, b.Args)
	}
	if pod.Spec.ShareProcessNamespace != nil && *pod.Spec.ShareProcessNamespace {
		t.Error("an agent pod must not share its process namespace")
	}
}

func TestBuildPodBrowserJobGetsTheBrowserIsolation(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{Browser: browserrun.Config{MemoryLimit: resource.MustParse("2Gi")}})
	spec := baseSpec()
	spec.BrowserJob = true
	pod := rt.buildPod(spec)
	c := pod.Spec.Containers[0]
	cs := c.SecurityContext
	if cs.ReadOnlyRootFilesystem == nil || !*cs.ReadOnlyRootFilesystem {
		t.Error("a browser job must run on a read-only root")
	}
	if cs.RunAsUser == nil || *cs.RunAsUser != browserUser {
		t.Errorf("runAsUser = %v, want %d", cs.RunAsUser, browserUser)
	}
	if pod.Spec.SecurityContext.FSGroup == nil || *pod.Spec.SecurityContext.FSGroup != browserUser {
		t.Error("the scratch volumes must belong to the browser's group")
	}
	mounted := map[string]bool{}
	for _, m := range c.VolumeMounts {
		mounted[m.MountPath] = true
	}
	if !mounted["/tmp"] || !mounted["/dev/shm"] {
		t.Errorf("browser job mounts = %v, want /tmp and /dev/shm", c.VolumeMounts)
	}
	for _, v := range pod.Spec.Volumes {
		if v.HostPath != nil {
			t.Errorf("volume %s is a hostPath", v.Name)
		}
	}
	if c.Resources.Limits.Memory().String() != "2Gi" {
		t.Errorf("memory limit = %s, want the browser's 2Gi", c.Resources.Limits.Memory())
	}
}

func TestBuildPodSandboxRootForEvidenceRunner(t *testing.T) {
	rt, _ := newFakeRuntime(t, Config{})
	spec := baseSpec()
	spec.TaskID = "ev-b1"
	spec.BrowserJob = true
	spec.SandboxRoot = "evidence:b1"
	pod := rt.buildPod(spec)
	want := sanitizeDNS1123("evidence:b1")
	if got := pod.Labels[LabelSandboxRoot]; got != want || got != "evidence-b1" {
		t.Fatalf("%s = %q, want %q", LabelSandboxRoot, got, want)
	}
	if got := pod.Labels[LabelTaskID]; got != "ev-b1" {
		t.Errorf("%s = %q, the task label keeps the runner's own id", LabelTaskID, got)
	}
	for _, np := range rt.sandboxPreviewPolicies(SandboxPreview{Pod: previewSandboxPod("uid-1"), RootTaskID: "evidence:b1", Ports: []int32{3000}}) {
		sel := np.Spec.PodSelector.MatchLabels
		if len(np.Spec.Ingress) == 1 {
			sel = np.Spec.Ingress[0].From[0].PodSelector.MatchLabels
		}
		if sel[LabelSandboxRoot] != want {
			t.Errorf("policy %s selects %v, not the runner's label %q", np.Name, sel, want)
		}
	}
	spec.WorktreeOwnerID = "parent-1"
	if got := rt.buildPod(spec).Labels[LabelSandboxRoot]; got != want {
		t.Errorf("SandboxRoot must win over WorktreeOwnerID: %q", got)
	}
}

func TestLaunchPodMarksEmptyDirWorktreesSafe(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{})
	spec := baseSpec()
	spec.Env = map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.pager", "GIT_CONFIG_VALUE_0": "cat"}
	spec.Worktrees = []WorktreeMount{{Name: "SHOP", CloneURL: "http://x/SHOP", ReadOnly: true}}
	name, err := rt.LaunchPod(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ns := rt.NamespaceFor(spec.WorkspaceID)
	sec, err := client.CoreV1().Secrets(ns).Get(context.Background(), EnvSecretName(name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("env secret: %v", err)
	}
	want := map[string]string{
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "core.pager",
		"GIT_CONFIG_VALUE_0": "cat",
		"GIT_CONFIG_KEY_1":   "safe.directory",
		"GIT_CONFIG_VALUE_1": "/workspace/projects/SHOP",
	}
	for k, v := range want {
		if got := string(sec.Data[k]); got != v {
			t.Errorf("env %s = %q, want %q", k, got, v)
		}
	}
	if spec.Env["GIT_CONFIG_COUNT"] != "1" {
		t.Errorf("the caller's env was modified: %v", spec.Env)
	}
	pod, err := client.CoreV1().Pods(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var clone corev1.Container
	for _, c := range pod.Spec.InitContainers {
		if strings.HasPrefix(c.Name, "clone-") {
			clone = c
		}
	}
	if !hasEnvRef(clone.Env, "GIT_CONFIG_VALUE_1") || !hasEnvRef(pod.Spec.Containers[0].Env, "GIT_CONFIG_VALUE_1") {
		t.Errorf("safe.directory must reach the clone and the agent; clone env %v", clone.Env)
	}

	spec.InstanceID = "coder-task-task1234-in-progress-101"
	spec.WorktreePVC = "octopus-wt-task1234"
	spec.Env = map[string]string{}
	name, err = rt.LaunchPod(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	sec, err = client.CoreV1().Secrets(ns).Get(context.Background(), EnvSecretName(name), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sec.Data["GIT_CONFIG_COUNT"]; ok {
		t.Errorf("a claim worktree got safe.directory entries: %v", sec.Data)
	}
}

func hasEnvRef(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}
