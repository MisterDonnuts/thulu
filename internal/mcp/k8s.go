package mcp

// Reads pods, not Deployments: the daemon's ClusterRole (deploy/helm/octopus/templates/rbac.yaml)
// grants pods get/list/watch/delete but no apps/deployments or scale.

import (
	"context"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Mirrors octopus.mcpLabels in deploy/helm/octopus/templates/_mcp.tpl.
const MCPPartOfLabel = "app.kubernetes.io/part-of=octopus-mcp"

const mcpNameLabel = "app.kubernetes.io/name"

type K8sManager struct {
	client    kubernetes.Interface
	namespace string
}

func NewK8sManager(client kubernetes.Interface, namespace string) *K8sManager {
	return &K8sManager{client: client, namespace: namespace}
}

func (m *K8sManager) List(ctx context.Context) ([]Service, error) {
	pods, err := m.client.CoreV1().Pods(m.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: MCPPartOfLabel,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "mcp.errors.list_mcp_pods", "namespace", m.namespace)
	}

	byName := map[string][]corev1.Pod{}
	for i := range pods.Items {
		name := pods.Items[i].Labels[mcpNameLabel]
		if name == "" {
			continue
		}
		byName[name] = append(byName[name], pods.Items[i])
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Service, 0, len(names))
	for _, name := range names {
		out = append(out, toServiceFromPods(name, byName[name]))
	}
	return out, nil
}

func toServiceFromPods(name string, pods []corev1.Pod) Service {
	sv := Service{
		Name:           name,
		ContainerName:  name,
		Status:         "unknown",
		ImageAvailable: true,
	}
	pod := pickRepresentative(pods)
	if pod == nil {
		sv.Status = "missing"
		return sv
	}

	sv.ContainerID = pod.Name
	sv.State = string(pod.Status.Phase)
	sv.Status = mapPodStatus(pod)
	sv.Health = podHealth(pod)
	if len(pod.Spec.Containers) > 0 {
		c := pod.Spec.Containers[0]
		sv.Image = c.Image
		if len(c.Ports) > 0 {
			sv.ContainerPort = int(c.Ports[0].ContainerPort)
		}
	}
	if pod.Status.StartTime != nil {
		sv.StartedAt = pod.Status.StartTime.Time.UTC().Format(time.RFC3339)
	}
	for i := range pod.Status.ContainerStatuses {
		sv.RestartCount += int(pod.Status.ContainerStatuses[i].RestartCount)
	}
	return sv
}

func pickRepresentative(pods []corev1.Pod) *corev1.Pod {
	var best *corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.Status.Phase == corev1.PodRunning && podReady(p) {
			return p
		}
		if best == nil || startedAfter(p, best) {
			best = p
		}
	}
	return best
}

func startedAfter(a, b *corev1.Pod) bool {
	return podTime(a).After(podTime(b))
}

func podTime(p *corev1.Pod) time.Time {
	if p.Status.StartTime != nil {
		return p.Status.StartTime.Time
	}
	return p.CreationTimestamp.Time
}

func mapPodStatus(p *corev1.Pod) string {
	switch p.Status.Phase {
	case corev1.PodRunning:
		return "running"
	case corev1.PodPending:
		if waitingReasonLooksLikeCrash(p) {
			return "restarting"
		}
		return "created"
	case corev1.PodFailed, corev1.PodSucceeded:
		return "exited"
	default:
		return "unknown"
	}
}

func podHealth(p *corev1.Pod) string {
	if podReady(p) {
		return "healthy"
	}
	if p.Status.Phase == corev1.PodRunning {
		return "starting"
	}
	return "unhealthy"
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func waitingReasonLooksLikeCrash(p *corev1.Pod) bool {
	for i := range p.Status.ContainerStatuses {
		w := p.Status.ContainerStatuses[i].State.Waiting
		if w != nil && (w.Reason == "CrashLoopBackOff" || w.Reason == "ImagePullBackOff" || w.Reason == "ErrImagePull") {
			return true
		}
	}
	return false
}

func (m *K8sManager) Restart(ctx context.Context, name string) error {
	if name == "" {
		return apperr.Invalid("mcp.errors.missing_name")
	}
	pods, err := m.client.CoreV1().Pods(m.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: mcpNameLabel + "=" + name + "," + MCPPartOfLabel,
	})
	if err != nil {
		return apperr.Wrap(err, "mcp.errors.list_pods", "name", name)
	}
	if len(pods.Items) == 0 {
		return apperr.NotFound("mcp.errors.no_running_pods", "name", name)
	}
	for i := range pods.Items {
		if err := m.client.CoreV1().Pods(m.namespace).Delete(ctx, pods.Items[i].Name, metav1.DeleteOptions{}); err != nil {
			return apperr.Wrap(err, "mcp.errors.delete_pod", "pod", pods.Items[i].Name)
		}
	}
	return nil
}

var errK8sLifecycle error = apperr.NotImplemented("mcp.errors.k8s_lifecycle")

func (m *K8sManager) Start(_ context.Context, _ string) error { return errK8sLifecycle }

func (m *K8sManager) Stop(_ context.Context, _ string) error { return errK8sLifecycle }
