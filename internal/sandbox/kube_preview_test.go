//go:build !foss

package sandbox

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

type svcPort struct {
	name  string
	port  int32
	proto corev1.Protocol
}

func servicePorts(svc *corev1.Service) []svcPort {
	var out []svcPort
	for _, p := range svc.Spec.Ports {
		out = append(out, svcPort{p.Name, p.Port, p.Protocol})
	}
	return out
}

func TestKubeExposesAppPorts(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{
		Isolation: kuberuntime.TenantIsolation{ControlPlaneNamespace: "octopus"},
		Browser:   browserrun.Config{Mode: browserrun.ModePerRun},
	})
	if err != nil {
		t.Fatal(err)
	}
	k := NewKube(client, KubeConfig{Image: "img", Provisioner: rt})
	res, err := k.Create(ctx, CreateOpts{TaskID: "Task_1", WorkspaceID: "acme", ExposePorts: []int{3000, 8587, 5173}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc, err := client.CoreV1().Services("tenant-acme").Get(ctx, res.ContainerID, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []svcPort{{"exec", 8587, ""}, {"app-3000", 3000, corev1.ProtocolTCP}, {"app-5173", 5173, corev1.ProtocolTCP}}
	if got := servicePorts(svc); !reflect.DeepEqual(got, want) {
		t.Errorf("service ports = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(res.HostPorts, map[int]int{3000: 3000, 5173: 5173}) {
		t.Errorf("HostPorts = %v, want each app port mapped to itself", res.HostPorts)
	}

	names := kuberuntime.PreviewPolicyNames(res.ContainerID)
	in, err := client.NetworkingV1().NetworkPolicies("tenant-acme").Get(ctx, names[0], metav1.GetOptions{})
	if err != nil {
		t.Fatalf("preview ingress policy: %v", err)
	}
	if got := in.Spec.Ingress[0].From[0].PodSelector.MatchLabels; !reflect.DeepEqual(got, map[string]string{kuberuntime.LabelSandboxRoot: "task-1"}) {
		t.Errorf("app ports open to %v, want the task's own pods", got)
	}
	var ports []int32
	for _, p := range in.Spec.Ingress[0].Ports {
		ports = append(ports, p.Port.IntVal)
	}
	if !reflect.DeepEqual(ports, []int32{3000, 5173}) {
		t.Errorf("policy ports = %v, want the app ports without the exec port", ports)
	}

	if url, ok := k.ClusterAddress(ctx, res.ContainerID, 3000); !ok || url != "http://sandbox-task-1.tenant-acme.svc:3000" {
		t.Errorf("ClusterAddress = %q, %v", url, ok)
	}

	if err := k.Remove(ctx, res.ContainerID); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err := client.NetworkingV1().NetworkPolicies("tenant-acme").Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("%s survives Remove (err=%v)", name, err)
		}
	}
}

func TestKubeEnsureServiceAddsAppPorts(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img"})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "sandbox-t1", Namespace: "tenant-acme", UID: "pod-uid",
		Labels: map[string]string{sandboxLabel: "t1"},
	}}
	old := k.buildSandboxService(pod, CreateOpts{TaskID: "t1"}, nil)
	old.Namespace = pod.Namespace
	if _, err := client.CoreV1().Services(pod.Namespace).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := k.ensureService(ctx, pod, CreateOpts{TaskID: "t1"}, []int32{3000}); err != nil {
		t.Fatal(err)
	}
	svc, _ := client.CoreV1().Services(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	want := []svcPort{{"exec", 8587, ""}, {"app-3000", 3000, corev1.ProtocolTCP}}
	if got := servicePorts(svc); !reflect.DeepEqual(got, want) {
		t.Errorf("service ports = %v, want %v", got, want)
	}
}

func TestManagerPreviewURLOnKube(t *testing.T) {
	ctx := context.Background()
	db := testpg.New(t)
	st, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	sett, err := settings.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	_ = sett.PutJSON(ctx, settings.SandboxAcquireBlockMsKey, 200)
	k := NewKube(fake.NewClientset(), KubeConfig{Image: "img"})
	m := NewManager(st, sett, k, ManagerConfig{AppPorts: []int{3000, 4200}})
	if _, err := m.Acquire(ctx, "task-kp", "in_progress", AcquireDockerOpts{WorkspaceID: "acme"}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	info, err := m.PreviewURL(ctx, "task-kp", 4200)
	if err != nil {
		t.Fatalf("PreviewURL: %v", err)
	}
	if info.URL != "http://sandbox-task-kp.tenant-acme.svc:4200" || info.HumanURL != "" || info.Note == "" || info.Port != 4200 {
		t.Errorf("PreviewURL = %+v", info)
	}
	if _, err := m.PreviewURL(ctx, "task-kp", 5173); err == nil {
		t.Error("a port outside the sandbox's list must be refused")
	}
}
