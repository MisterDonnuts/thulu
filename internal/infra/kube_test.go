package infra

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/internal/kubeapi"
)

const (
	nodeMetricsJSON = `{"items":[{"metadata":{"name":"node-1"},"usage":{"cpu":"1500m","memory":"4Gi"}}]}`
	podMetricsJSON  = `{"items":[
	  {"metadata":{"name":"run-1","namespace":"tenant-ws1"},"containers":[
	    {"usage":{"cpu":"250m","memory":"100Mi"}},{"usage":{"cpu":"250m","memory":"28Mi"}}]},
	  {"metadata":{"name":"octopus-abc","namespace":"octopus"},"containers":[{"usage":{"cpu":"50m","memory":"64Mi"}}]}]}`
)

func TestParseUsage(t *testing.T) {
	nodes, err := parseUsage([]byte(nodeMetricsJSON), false)
	if err != nil {
		t.Fatal(err)
	}
	if nodes["node-1"] != (cpuMem{cpu: 1500, mem: 4 << 30}) {
		t.Errorf("node usage = %+v", nodes)
	}
	pods, err := parseUsage([]byte(podMetricsJSON), true)
	if err != nil {
		t.Fatal(err)
	}
	if pods["tenant-ws1/run-1"] != (cpuMem{cpu: 500, mem: 128 << 20}) {
		t.Errorf("pod usage sums its containers: %+v", pods["tenant-ws1/run-1"])
	}
	if _, err := parseUsage([]byte("<html>"), true); err == nil {
		t.Error("a non-JSON body parsed")
	}
}

func kubeFixture() *fake.Clientset {
	q := resource.MustParse
	return fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "octopus"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-ws1", Labels: map[string]string{
			kubeapi.LabelPlane: "data", kubeapi.LabelTenant: "ws1"}}},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
			Status: corev1.NodeStatus{
				Capacity:    corev1.ResourceList{corev1.ResourceCPU: q("8"), corev1.ResourceMemory: q("32Gi"), corev1.ResourcePods: q("110")},
				Allocatable: corev1.ResourceList{corev1.ResourceCPU: q("7800m"), corev1.ResourceMemory: q("31Gi")},
				Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				NodeInfo:    corev1.NodeSystemInfo{KubeletVersion: "v1.31.4+k3s1"},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "tenant-ws1", UID: "u1",
				Labels: map[string]string{kubeapi.LabelManagedBy: "octopus", kubeapi.LabelStage: "build",
					kubeapi.LabelTaskID: "t1"},
				Annotations: map[string]string{kubeapi.AnnotationAgentType: "coder"}},
			Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{
				Image: "octopus-built-coder:latest",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: q("500m"), corev1.ResourceMemory: q("512Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: q("2"), corev1.ResourceMemory: q("2Gi")},
				}}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "octopus-abc", Namespace: "octopus", UID: "u2",
				Labels: map[string]string{labelAppName: "octopus"}},
			Spec:   corev1.PodSpec{NodeName: "node-1"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "run-0", Namespace: "tenant-ws1", UID: "u3",
				Labels: map[string]string{kubeapi.LabelManagedBy: "octopus"}},
			Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: q("4")}}}}},
			Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
		},
		&corev1.ResourceQuota{
			ObjectMeta: metav1.ObjectMeta{Name: "tenant", Namespace: "tenant-ws1"},
			Status: corev1.ResourceQuotaStatus{
				Hard: corev1.ResourceList{corev1.ResourceLimitsCPU: q("4")},
				Used: corev1.ResourceList{corev1.ResourceLimitsCPU: q("2")},
			},
		},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "octopus"}},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "tenant-ws1"},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound,
				Capacity: corev1.ResourceList{corev1.ResourceStorage: q("10Gi")}},
		},
	)
}

func TestSampleKube(t *testing.T) {
	metrics := func(_ context.Context, path string) ([]byte, error) {
		if path == metricsNodesPath {
			return []byte(nodeMetricsJSON), nil
		}
		return []byte(podMetricsJSON), nil
	}
	info, pods, probs := sampleKube(context.Background(), KubeSource{
		Client: kubeFixture(), Metrics: metrics, ControlPlaneNamespace: "octopus", RuntimeClass: "kata",
	})
	if len(probs) != 0 {
		t.Fatalf("problems = %+v", probs)
	}
	if !info.MetricsAvailable || info.RuntimeClass != "kata" {
		t.Errorf("info = %+v", info)
	}
	if info.Pods.Total != 3 || info.Pods.Running != 2 || info.Pods.Succeeded != 1 {
		t.Errorf("phases = %+v", info.Pods)
	}
	if len(info.Nodes) != 1 {
		t.Fatalf("nodes = %+v", info.Nodes)
	}
	n := info.Nodes[0]
	if n.CPUCapacityMilli != 8000 || n.CPUUsageMilli != 1500 || n.MemUsage != 4<<30 {
		t.Errorf("node = %+v", n)
	}
	if n.CPURequestsMilli != 500 || n.MemRequests != 512<<20 {
		t.Errorf("node requests = %d mCPU / %d B, want only the running pod's", n.CPURequestsMilli, n.MemRequests)
	}
	if len(info.Quotas) != 1 || info.Quotas[0].WorkspaceID != "ws1" || info.Quotas[0].Used["limits.cpu"] != "2" {
		t.Errorf("quotas = %+v, want the tenant's only", info.Quotas)
	}
	if len(info.PVCs) != 1 || info.PVCs[0].Capacity != 10<<30 || info.PVCs[0].WorkspaceID != "ws1" {
		t.Errorf("pvcs = %+v", info.PVCs)
	}

	byName := map[string]Container{}
	for _, p := range pods {
		byName[p.Name] = p
	}
	run := byName["run-1"]
	if run.Kind != KindAgent || run.WorkspaceID != "ws1" || run.Agent != "coder" || run.TaskID != "t1" ||
		!run.HasUsage || !near(run.CPUCores, 0.5) || run.MemBytes != 128<<20 ||
		run.CPURequestMilli != 500 || run.CPULimitMilli != 2000 || run.MemLimit != 2<<30 {
		t.Errorf("agent pod = %+v", run)
	}
	if d := byName["octopus-abc"]; d.Kind != KindDaemon || !near(d.CPUCores, 0.05) {
		t.Errorf("daemon pod = %+v", d)
	}
}

func TestSampleKubeWithoutMetricsServer(t *testing.T) {
	metrics := func(context.Context, string) ([]byte, error) {
		return nil, errors.New("the server could not find the requested resource")
	}
	info, pods, probs := sampleKube(context.Background(), KubeSource{Client: kubeFixture(), Metrics: metrics, ControlPlaneNamespace: "octopus"})
	if info.MetricsAvailable {
		t.Error("metrics reported available")
	}
	if len(probs) != 1 || probs[0].Source != "metrics" {
		t.Errorf("problems = %+v, want one metrics problem", probs)
	}
	if len(pods) != 3 || len(info.Nodes) != 1 {
		t.Errorf("pods %d nodes %d", len(pods), len(info.Nodes))
	}
	for _, p := range pods {
		if p.HasUsage {
			t.Errorf("%s has usage without metrics-server", p.Name)
		}
	}
}

func TestPodContainerInstance(t *testing.T) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "dev-task-t1", Namespace: "tenant-ws1",
		Labels:      map[string]string{kubeapi.LabelManagedBy: "octopus", kubeapi.LabelTaskID: "t1"},
		Annotations: map[string]string{kubeapi.AnnotationInstanceID: "dev-task-t1-in_progress-9"},
	}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	c := podContainer(p, "octopus", map[string]string{"tenant-ws1": "ws1"})
	if c.Kind != KindAgent || c.Instance != "dev-task-t1-in_progress-9" || !c.IsRunning() {
		t.Errorf("agent pod = kind %s, instance %q, running %v", c.Kind, c.Instance, c.IsRunning())
	}
}
