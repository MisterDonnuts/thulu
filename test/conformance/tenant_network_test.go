//go:build !foss

package conformance

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
)

func TestTenantNetworkPolicy(t *testing.T) {
	cs, cfg := requireCluster(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	iso, rt := daemonRuntime(t, cs)
	nsA := ensureDaemonTenant(ctx, t, cs, rt, "conformance-a")
	nsB := ensureDaemonTenant(ctx, t, cs, rt, "conformance-b")

	attacker := runProbe(ctx, t, cs, nsA, "attacker", nil, "sleep 600")
	victim := runProbe(ctx, t, cs, nsB, "victim", nil,
		"httpd -p 8587 -h /tmp && httpd -p 8080 -h /tmp && sleep 600")
	victimIP := podIP(ctx, t, cs, nsB, victim)
	for _, port := range []string{"8587", "8080"} {
		if _, err := podExec(ctx, cs, cfg, nsB, victim, ncProbe("127.0.0.1", port)); err != nil {
			t.Fatalf("victim is not listening on %s; the cross-tenant probe would prove nothing: %v", port, err)
		}
	}

	mustBlock := func(t *testing.T, ns, pod, host, port string) {
		t.Helper()
		out, err := podExec(ctx, cs, cfg, ns, pod, ncProbe(host, port))
		if err == nil {
			t.Fatalf("tenant pod in %s REACHED %s — the tenant NetworkPolicy does not hold; output=%q",
				ns, net.JoinHostPort(host, port), out)
		}
		t.Logf("blocked as required: %s", net.JoinHostPort(host, port))
	}

	t.Run("node addresses", func(t *testing.T) {
		nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil || len(nodes.Items) == 0 {
			t.Fatalf("list nodes: %v", err)
		}
		for _, n := range nodes.Items {
			for _, a := range n.Status.Addresses {
				if a.Type != corev1.NodeInternalIP && a.Type != corev1.NodeExternalIP {
					continue
				}
				if ip := net.ParseIP(a.Address); ip == nil || ip.To4() == nil {
					continue
				}
				for _, port := range []string{"6443", "10250", "22", "443"} {
					mustBlock(t, nsA, attacker, a.Address, port)
				}
			}
		}
	})

	t.Run("other tenant", func(t *testing.T) {
		mustBlock(t, nsA, attacker, victimIP, "8587")
		mustBlock(t, nsA, attacker, victimIP, "8080")
	})

	t.Run("control-plane pods other than the daemon", func(t *testing.T) {
		if _, err := cs.CoreV1().Namespaces().Get(ctx, iso.ControlPlaneNamespace, metav1.GetOptions{}); err != nil {
			t.Skipf("control-plane namespace %q: %v", iso.ControlPlaneNamespace, err)
		}
		impostor := runProbe(ctx, t, cs, iso.ControlPlaneNamespace, "conformance-not-daemon",
			map[string]string{"app.kubernetes.io/name": "conformance-probe"}, "sleep 600")
		mustBlock(t, iso.ControlPlaneNamespace, impostor, victimIP, "8587")
	})

	t.Run("daemon reaches the exec agent", func(t *testing.T) {
		// Without this positive check, a selector that matches no pod passes every block above
		// while exec agents are unreachable.
		if _, err := cs.CoreV1().Namespaces().Get(ctx, iso.ControlPlaneNamespace, metav1.GetOptions{}); err != nil {
			t.Skipf("control-plane namespace %q: %v", iso.ControlPlaneNamespace, err)
		}
		sel := labels.SelectorFromSet(iso.ControlPlanePodSelector).String()
		pods, err := cs.CoreV1().Pods(iso.ControlPlaneNamespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			t.Fatalf("list daemon pods (%s): %v", sel, err)
		}
		var daemon *corev1.Pod
		for i := range pods.Items {
			if pods.Items[i].Status.Phase == corev1.PodRunning {
				daemon = &pods.Items[i]
				break
			}
		}
		if daemon == nil {
			t.Fatalf("no running pod in %s matches %s (%s): the tenant policy admits no daemon",
				iso.ControlPlaneNamespace, sel, kuberuntime.EnvControlPlanePodSelector)
		}
		url := "http://" + net.JoinHostPort(victimIP, "8587") + "/"
		out, err := podExecIn(ctx, cs, cfg, iso.ControlPlaneNamespace, daemon.Name, daemon.Spec.Containers[0].Name,
			[]string{"curl", "-sS", "-o", "/dev/null", "--max-time", "4", url})
		if err != nil {
			t.Fatalf("daemon pod %s cannot reach a tenant exec agent at %s; output=%q err=%v", daemon.Name, url, out, err)
		}
	})

	t.Run("cloud metadata", func(t *testing.T) {
		mustBlock(t, nsA, attacker, "169.254.169.254", "80")
	})

	t.Run("postgres", func(t *testing.T) {
		addr := os.Getenv("OCTOPUS_CONFORMANCE_POSTGRES_ADDR")
		if addr == "" {
			t.Skip("OCTOPUS_CONFORMANCE_POSTGRES_ADDR not set")
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("OCTOPUS_CONFORMANCE_POSTGRES_ADDR=%q: %v", addr, err)
		}
		mustBlock(t, nsA, attacker, host, port)
	})

	t.Run("public 443", func(t *testing.T) {
		if iso.EgressMode != kuberuntime.EgressOpen {
			t.Skipf("egress mode %q: public 443 goes through the proxy", iso.EgressMode)
		}
		addr := os.Getenv("OCTOPUS_CONFORMANCE_PUBLIC_ADDR")
		if addr == "" {
			addr = "1.1.1.1:443"
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("OCTOPUS_CONFORMANCE_PUBLIC_ADDR=%q: %v", addr, err)
		}
		// Without this the gate would pass on a CNI that drops everything.
		if out, err := podExec(ctx, cs, cfg, nsA, attacker, ncProbe(host, port)); err != nil {
			t.Fatalf("tenant pod cannot reach %s — open egress is broken; output=%q err=%v", addr, out, err)
		}
	})
}

func daemonRuntime(t *testing.T, cs kubernetes.Interface) (kuberuntime.TenantIsolation, *kuberuntime.Runtime) {
	t.Helper()
	cfg, err := kuberuntime.ConfigFromEnv(func(k string) string {
		v := os.Getenv(k)
		if k == kuberuntime.EnvControlPlaneNamespace && v == "" && os.Getenv(kuberuntime.EnvPodNamespace) == "" {
			v = "octopus"
		}
		return v
	})
	if err != nil {
		t.Fatalf("kube runtime config: %v", err)
	}
	rt, err := kuberuntime.NewWithClient(cs, cfg)
	if err != nil {
		t.Fatalf("kube runtime: %v", err)
	}
	return rt.Isolation(), rt
}

func ensureDaemonTenant(ctx context.Context, t *testing.T, cs kubernetes.Interface, rt *kuberuntime.Runtime, workspace string) string {
	t.Helper()
	if err := rt.EnsureTenantNamespace(ctx, workspace); err != nil {
		t.Fatalf("ensure tenant %s: %v", workspace, err)
	}
	ns := rt.NamespaceFor(workspace)
	t.Cleanup(func() {
		err := cs.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Logf("delete %s: %v", ns, err)
		}
	})
	return ns
}

func runProbe(ctx context.Context, t *testing.T, cs kubernetes.Interface, ns, name string, labels map[string]string, script string) string {
	t.Helper()
	image := os.Getenv("OCTOPUS_CONFORMANCE_PROBE_IMAGE")
	if image == "" {
		image = "busybox:1.36"
	}
	rc := sandboxRuntimeClass()
	nonRoot, noEsc := true, false
	uid := int64(1000)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: corev1.PodSpec{
			RuntimeClassName:              &rc,
			RestartPolicy:                 corev1.RestartPolicyNever,
			AutomountServiceAccountToken:  &noEsc,
			TerminationGracePeriodSeconds: new(int64),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &nonRoot,
				RunAsUser:      &uid,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   image,
				Command: []string{"sh", "-c", script},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &noEsc,
					RunAsNonRoot:             &nonRoot,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	if _, err := cs.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create probe pod %s/%s: %v", ns, name, err)
	}
	t.Cleanup(func() {
		_ = cs.CoreV1().Pods(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		p, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil && p.Status.Phase == corev1.PodRunning {
			return name
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("probe pod %s/%s never reached Running", ns, name)
	return name
}

func podIP(ctx context.Context, t *testing.T, cs kubernetes.Interface, ns, name string) string {
	t.Helper()
	p, err := cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil || p.Status.PodIP == "" {
		t.Fatalf("pod IP of %s/%s: %v", ns, name, err)
	}
	return p.Status.PodIP
}

// A refused connection also fails, so the victim's listeners are checked from inside before the
// cross-tenant probes.
func ncProbe(host, port string) []string {
	return []string{"nc", "-z", "-w", "4", strings.Trim(host, "[]"), port}
}
