//go:build !foss

package sandbox

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubeCreatePodSpec(t *testing.T) {
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "ghcr.io/x/octopus-runtime:latest", RuntimeClass: "kata-clh"})
	ctx := context.Background()

	res, err := k.Create(ctx, CreateOpts{TaskID: "Task_123", WorkspaceID: "acme"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.ContainerID == "" {
		t.Fatal("Create returned empty handle")
	}

	pod, err := client.CoreV1().Pods("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "kata-clh" {
		t.Errorf("runtimeClassName = %v, want kata-clh", pod.Spec.RuntimeClassName)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("must not automount the service-account token")
	}
	c := pod.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Capabilities == nil ||
		len(c.SecurityContext.Capabilities.Drop) == 0 {
		t.Error("sandbox container must drop capabilities")
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("must forbid privilege escalation")
	}
	var tokenRef *corev1.SecretKeySelector
	for _, e := range c.Env {
		if e.Name == "OCTOPUS_EXEC_TOKEN" {
			if e.Value != "" {
				t.Error("exec token must not be a literal in the pod spec")
			}
			if e.ValueFrom != nil {
				tokenRef = e.ValueFrom.SecretKeyRef
			}
		}
	}
	if tokenRef == nil {
		t.Fatal("exec token env must reference the pod's Secret")
	}
	secret, err := client.CoreV1().Secrets("tenant-acme").Get(ctx, tokenRef.Name, metav1.GetOptions{})
	if err != nil || len(secret.Data[tokenRef.Key]) == 0 {
		t.Fatalf("env secret %s missing its token (err=%v)", tokenRef.Name, err)
	}
	psc := pod.Spec.SecurityContext
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
		t.Error("sandbox must run as non-root uid 1000")
	}
	if c.Resources.Limits.Memory().IsZero() || c.Resources.Limits.Cpu().IsZero() ||
		c.Resources.Requests.Memory().IsZero() || c.Resources.Requests.Cpu().IsZero() {
		t.Errorf("sandbox needs cpu/memory requests and limits, got %+v", c.Resources)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 8587 {
		t.Errorf("exec port = %+v, want 8587", c.Ports)
	}
	if len(c.Command) != 0 || len(c.Args) != 0 {
		t.Errorf("command %q args %q, want both unset so the image's tini stays PID 1", c.Command, c.Args)
	}
	if pod.Spec.ShareProcessNamespace != nil && *pod.Spec.ShareProcessNamespace {
		t.Error("the sandbox must not share its process namespace")
	}

	if _, err := client.CoreV1().Services("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{}); err != nil {
		t.Errorf("sandbox Service not created: %v", err)
	}

	pvcName := WorktreePVCName("Task_123")
	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant-acme").Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("worktree PVC not created: %v", err)
	}
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("worktree PVC must be ReadWriteOnce (co-locates the pods), got %v", pvc.Spec.AccessModes)
	}
	var mountsWorktree bool
	for _, vm := range c.VolumeMounts {
		if vm.MountPath == "/workspace/projects" {
			mountsWorktree = true
		}
	}
	if !mountsWorktree {
		t.Error("sandbox container must mount the worktree at /workspace/projects (path parity with the agent)")
	}
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.FSGroup == nil ||
		*pod.Spec.SecurityContext.FSGroup != 1000 {
		t.Error("sandbox pod must set fsGroup=1000 so the non-root user can write the PVC")
	}
}

func TestKubeAliveAndRemove(t *testing.T) {
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img"})
	ctx := context.Background()

	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, _ := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{})
	pod.Status.Phase = corev1.PodRunning
	_, _ = client.CoreV1().Pods("tenant-x").Update(ctx, pod, metav1.UpdateOptions{})

	if !k.Alive(ctx, res.ContainerID) {
		t.Error("Alive = false for a running sandbox")
	}
	if err := k.Remove(ctx, res.ContainerID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if k.Alive(ctx, res.ContainerID) {
		t.Error("Alive = true after Remove")
	}
	if err := k.Remove(ctx, res.ContainerID); err != nil {
		t.Errorf("Remove must be idempotent, got %v", err)
	}
	if _, err := k.Exec(ctx, res.ContainerID, ExecOpts{Command: "true"}); err == nil {
		t.Error("Exec on removed sandbox should error (no token)")
	}
}

func TestKubeCreateReplacesDeadPod(t *testing.T) {
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img"})
	ctx := context.Background()

	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, _ := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{})
	deadToken := k.execTokenOf(ctx, pod)
	pod.Status.Phase = corev1.PodFailed
	pod.Status.Reason = "Evicted"
	_, _ = client.CoreV1().Pods("tenant-x").Update(ctx, pod, metav1.UpdateOptions{})

	k2 := NewKube(client, KubeConfig{Image: "img"})
	res2, err := k2.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create over an evicted pod: %v", err)
	}
	fresh, err := client.CoreV1().Pods("tenant-x").Get(ctx, res2.ContainerID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("replacement pod missing: %v", err)
	}
	if fresh.Status.Phase == corev1.PodFailed || k2.execTokenOf(ctx, fresh) == deadToken {
		t.Error("Create returned the dead pod instead of replacing it")
	}
	if k2.execTokenOf(ctx, fresh) == "" {
		t.Error("replacement pod has no exec token")
	}
}

func TestKubeCreateAdoptsLivePod(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()
	k := NewKube(client, KubeConfig{Image: "img"})
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, _ := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{})
	pod.Status.Phase = corev1.PodRunning
	_, _ = client.CoreV1().Pods("tenant-x").Update(ctx, pod, metav1.UpdateOptions{})
	liveUID, liveToken := pod.UID, k.execTokenOf(ctx, pod)

	k2 := NewKube(client, KubeConfig{Image: "img"})
	if _, err := k2.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"}); err != nil {
		t.Fatalf("Create over a live pod: %v", err)
	}
	same, err := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{})
	if err != nil || same.UID != liveUID {
		t.Fatalf("live pod was replaced instead of adopted (err=%v)", err)
	}
	k2.mu.Lock()
	rec := k2.pods[res.ContainerID]
	k2.mu.Unlock()
	if rec.token != liveToken {
		t.Errorf("adopted token = %q, want the pod's own %q", rec.token, liveToken)
	}
}

// A live pod an older release made carries other label keys; nothing selects it, so it is
// replaced rather than adopted.
func TestKubeCreateReplacesLivePodWithoutCurrentLabels(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()
	k := NewKube(client, KubeConfig{Image: "img"})
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, _ := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{})
	pod.Status.Phase = corev1.PodRunning
	pod.Labels = map[string]string{"example.com/sandbox": "t1"}
	_, _ = client.CoreV1().Pods("tenant-x").Update(ctx, pod, metav1.UpdateOptions{})
	oldToken := k.execTokenOf(ctx, pod)

	k2 := NewKube(client, KubeConfig{Image: "img"})
	if _, err := k2.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"}); err != nil {
		t.Fatalf("Create over an unlabeled pod: %v", err)
	}
	fresh, err := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("replacement pod missing: %v", err)
	}
	if fresh.Labels[sandboxLabel] != "t1" || k2.execTokenOf(ctx, fresh) == oldToken {
		t.Errorf("the unlabeled pod was adopted: labels %v", fresh.Labels)
	}
}

func TestKubeRehydratesAfterRestart(t *testing.T) {
	client := fake.NewClientset()
	ctx := context.Background()
	res, err := NewKube(client, KubeConfig{Image: "img"}).
		Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pod, _ := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{})
	pod.Status.Phase = corev1.PodRunning
	_, _ = client.CoreV1().Pods("tenant-x").Update(ctx, pod, metav1.UpdateOptions{})

	k2 := NewKube(client, KubeConfig{Image: "img"})
	if !k2.Alive(ctx, res.ContainerID) {
		t.Error("Alive = false after restart; the pod is running")
	}
	if err := k2.Remove(ctx, res.ContainerID); err != nil {
		t.Fatalf("Remove after restart: %v", err)
	}
	if _, err := client.CoreV1().Pods("tenant-x").Get(ctx, res.ContainerID, metav1.GetOptions{}); err == nil {
		t.Error("Remove after restart left the pod behind")
	}
}

func TestKubeSandboxEphemeralStorage(t *testing.T) {
	k := NewKube(nil, KubeConfig{Image: "img"})
	res := k.resourcesFor(CreateOpts{})
	if got := res.Requests.StorageEphemeral().String(); got != "1Gi" {
		t.Errorf("default ephemeral request = %s", got)
	}
	if got := res.Limits.StorageEphemeral().String(); got != "20Gi" {
		t.Errorf("default ephemeral limit = %s", got)
	}

	env := map[string]string{
		EnvSandboxEphemeralRequest: "2Gi",
		EnvSandboxEphemeralLimit:   "5Gi",
	}
	k = NewKube(nil, KubeConfigFromEnv(func(key string) string { return env[key] }))
	res = k.resourcesFor(CreateOpts{})
	if got := res.Limits.StorageEphemeral().String(); got != "5Gi" {
		t.Errorf("ephemeral limit from env = %s", got)
	}

	k = NewKube(nil, KubeConfig{EphemeralRequest: "50Gi", EphemeralLimit: "4Gi"})
	res = k.resourcesFor(CreateOpts{})
	if got := res.Requests.StorageEphemeral().String(); got != "4Gi" {
		t.Errorf("clamped ephemeral request = %s, want 4Gi", got)
	}
}
