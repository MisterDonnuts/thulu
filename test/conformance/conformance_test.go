package conformance

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

const probeNamespace = "tenant-conformance"

func sandboxRuntimeClass() string {
	if rc := os.Getenv("OCTOPUS_CONFORMANCE_RUNTIMECLASS"); rc != "" {
		return rc
	}
	return "gvisor"
}

func requireCluster(t *testing.T) (kubernetes.Interface, *rest.Config) {
	t.Helper()
	kubeconfig := os.Getenv("OCTOPUS_CONFORMANCE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("OCTOPUS_CONFORMANCE_KUBECONFIG not set — skipping cluster conformance gate (run `make k8s-dev up`)")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("build kube config: %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	return cs, cfg
}

// thulu.dev/plane=data is the label the deploy/k8s-data-plane ValidatingAdmissionPolicy binds on.
func ensureTenantNS(t *testing.T, cs kubernetes.Interface) string {
	t.Helper()
	ctx := context.Background()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: probeNamespace,
			Labels: map[string]string{
				"thulu.dev/plane":                    "data",
				"pod-security.kubernetes.io/enforce": "restricted",
			},
		},
	}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create tenant namespace: %v", err)
	}
	// Shared and not deleted per test, to avoid a Terminating-namespace race between tests.
	return probeNamespace
}

func probePod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: probeNamespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c", "sleep 1"},
			}},
		},
	}
}

func TestVAP_RejectsPodWithoutSandboxRuntime(t *testing.T) {
	cs, _ := requireCluster(t)
	ensureTenantNS(t, cs)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pod := probePod("no-runtimeclass")
	_, err := cs.CoreV1().Pods(probeNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err == nil {
		_ = cs.CoreV1().Pods(probeNamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		t.Fatal("pod without a sandboxed runtimeClassName was ADMITTED — the VAP is not enforcing")
	}
	if !apierrors.IsForbidden(err) && !strings.Contains(err.Error(), "runtimeClassName") {
		t.Errorf("expected a forbidden/runtimeClass admission denial, got: %v", err)
	}
}

func TestVAP_RejectsPrivilegedContainer(t *testing.T) {
	cs, _ := requireCluster(t)
	ensureTenantNS(t, cs)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	priv := true
	rc := sandboxRuntimeClass()
	pod := probePod("privileged")
	pod.Spec.RuntimeClassName = &rc
	pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: &priv}

	_, err := cs.CoreV1().Pods(probeNamespace).Create(ctx, pod, metav1.CreateOptions{})
	if err == nil {
		_ = cs.CoreV1().Pods(probeNamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		t.Fatal("privileged container was ADMITTED — isolation controls are not enforcing")
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("expected forbidden admission denial for privileged pod, got: %v", err)
	}
}

func TestNetworkIsolationProbes(t *testing.T) {
	cs, cfg := requireCluster(t)
	ensureTenantNS(t, cs)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	applyDenyAllEgress(t, cs)
	pod := createRunningProbePod(t, cs, "netprobe")

	forbidden := []struct {
		name, target string
	}{
		{"cloud metadata endpoint", "http://169.254.169.254/"},
		{"arbitrary public IP", "http://1.1.1.1/"},
	}
	for _, f := range forbidden {
		t.Run(f.name, func(t *testing.T) {
			out, err := podExec(ctx, cs, cfg, probeNamespace, pod,
				[]string{"wget", "-T", "4", "-q", "-O", "-", f.target})
			if err == nil {
				t.Fatalf("sandbox REACHED %s (%s) — egress is NOT locked down; output=%q", f.name, f.target, out)
			}
			t.Logf("blocked as required: %s (%v)", f.target, err)
		})
	}
}

func TestSandboxRuntimeContainment(t *testing.T) {
	cs, cfg := requireCluster(t)
	ensureTenantNS(t, cs)
	pod := createRunningProbePod(t, cs, "runtime-containment")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := podExec(ctx, cs, cfg, probeNamespace, pod, []string{"cat", "/proc/version"})
	if err != nil {
		t.Fatalf("exec /proc/version: %v", err)
	}

	rc := sandboxRuntimeClass()
	if strings.Contains(rc, "gvisor") {
		if !strings.Contains(strings.ToLower(out), "gvisor") {
			t.Fatalf("pod is NOT running under gVisor — the sandboxed runtime is not isolating the kernel; /proc/version=%q", out)
		}
		t.Logf("contained under the gVisor userspace kernel: %s", strings.TrimSpace(out))
		return
	}

	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		t.Fatalf("list nodes for kernel comparison: %v", err)
	}
	for _, n := range nodes.Items {
		if hostKernel := n.Status.NodeInfo.KernelVersion; strings.Contains(out, hostKernel) {
			t.Fatalf("pod sees the HOST kernel (%s) — %s is not providing a guest kernel; /proc/version=%q",
				hostKernel, rc, out)
		}
	}
	t.Logf("contained under a guest kernel distinct from every node (%s): %s", rc, strings.TrimSpace(out))
}

func applyDenyAllEgress(t *testing.T, cs kubernetes.Interface) {
	t.Helper()
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "deny-all-egress", Namespace: probeNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		},
	}
	_, err := cs.NetworkingV1().NetworkPolicies(probeNamespace).Create(context.Background(), np, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("apply deny-all egress: %v", err)
	}
}

func createRunningProbePod(t *testing.T, cs kubernetes.Interface, name string) string {
	t.Helper()
	ctx := context.Background()
	rc := sandboxRuntimeClass()
	nonRoot, noEsc := true, false
	uid := int64(1000)
	pod := probePod(name)
	pod.Spec.RuntimeClassName = &rc
	pod.Spec.Containers[0].Command = []string{"sh", "-c", "sleep 300"}
	pod.Spec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot:   &nonRoot,
		RunAsUser:      &uid,
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noEsc,
		RunAsNonRoot:             &nonRoot,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	if _, err := cs.CoreV1().Pods(probeNamespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create probe pod: %v", err)
	}
	t.Cleanup(func() {
		_ = cs.CoreV1().Pods(probeNamespace).Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		p, err := cs.CoreV1().Pods(probeNamespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil && p.Status.Phase == corev1.PodRunning {
			return name
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("probe pod %s never reached Running", name)
	return name
}

func podExec(ctx context.Context, cs kubernetes.Interface, cfg *rest.Config, ns, pod string, cmd []string) (string, error) {
	return podExecIn(ctx, cs, cfg, ns, pod, "probe", cmd)
}

func podExecIn(ctx context.Context, cs kubernetes.Interface, cfg *rest.Config, ns, pod, container string, cmd []string) (string, error) {
	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").Name(pod).Namespace(ns).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &buf, Stderr: &buf})
	return buf.String(), err
}
