package mcp

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func mcpPod(name, podName string, phase corev1.PodPhase, ready bool, restarts int32) corev1.Pod {
	start := metav1.NewTime(time.Now().Add(-time.Hour))
	readyStatus := corev1.ConditionFalse
	if ready {
		readyStatus = corev1.ConditionTrue
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "octopus",
			Labels: map[string]string{
				"app.kubernetes.io/name":    name,
				"app.kubernetes.io/part-of": "octopus-mcp",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  name,
				Image: name + ":local",
				Ports: []corev1.ContainerPort{{ContainerPort: 3000}},
			}},
		},
		Status: corev1.PodStatus{
			Phase:     phase,
			StartTime: &start,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: readyStatus},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: name, RestartCount: restarts},
			},
		},
	}
}

func TestK8sManagerListRunning(t *testing.T) {
	other := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "postgres-x", Namespace: "octopus"}}
	client := fake.NewClientset(
		ptr(mcpPod("thulu-browser", "thulu-browser-1", corev1.PodRunning, true, 2)),
		ptr(mcpPod("octopus-api", "octopus-api-1", corev1.PodRunning, true, 0)),
		&other,
	)
	m := NewK8sManager(client, "octopus")

	got, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 MCPs, got %d: %+v", len(got), got)
	}
	// Sorted by name.
	if got[0].Name != "octopus-api" {
		t.Fatalf("want octopus-api first, got %q", got[0].Name)
	}
	b := got[1]
	if b.Name != "thulu-browser" {
		t.Fatalf("want thulu-browser second, got %q", b.Name)
	}
	if b.Status != "running" || b.Health != "healthy" {
		t.Errorf("status/health = %q/%q, want running/healthy", b.Status, b.Health)
	}
	if b.RestartCount != 2 {
		t.Errorf("restartCount = %d, want 2", b.RestartCount)
	}
	if b.Image != "thulu-browser:local" {
		t.Errorf("image = %q, want thulu-browser:local", b.Image)
	}
	if b.ContainerPort != 3000 {
		t.Errorf("containerPort = %d, want 3000", b.ContainerPort)
	}
	if b.StartedAt == "" {
		t.Errorf("startedAt empty, want a timestamp")
	}
}

func TestK8sManagerListNotReadyIsStarting(t *testing.T) {
	client := fake.NewClientset(ptr(mcpPod("thulu-github", "thulu-github-1", corev1.PodRunning, false, 0)))
	m := NewK8sManager(client, "octopus")
	got, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Health != "starting" {
		t.Fatalf("want one starting MCP, got %+v", got)
	}
}

func TestK8sManagerListEmpty(t *testing.T) {
	m := NewK8sManager(fake.NewClientset(), "octopus")
	got, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestK8sManagerRestartDeletesPods(t *testing.T) {
	client := fake.NewClientset(ptr(mcpPod("thulu-browser", "thulu-browser-1", corev1.PodRunning, true, 0)))
	m := NewK8sManager(client, "octopus")
	if err := m.Restart(context.Background(), "thulu-browser"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	pods, err := client.CoreV1().Pods("octopus").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list after restart: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("pod not deleted on restart: %+v", pods.Items)
	}
}

func TestK8sManagerRestartUnknownMCP(t *testing.T) {
	m := NewK8sManager(fake.NewClientset(), "octopus")
	if err := m.Restart(context.Background(), "nope"); err == nil {
		t.Fatal("want error restarting an MCP with no pods")
	}
}

func TestK8sManagerStartStopUnsupported(t *testing.T) {
	m := NewK8sManager(fake.NewClientset(), "octopus")
	if err := m.Start(context.Background(), "thulu-browser"); err == nil {
		t.Error("Start should be unsupported in k8s mode")
	}
	if err := m.Stop(context.Background(), "thulu-browser"); err == nil {
		t.Error("Stop should be unsupported in k8s mode")
	}
}

func ptr(p corev1.Pod) *corev1.Pod { return &p }
