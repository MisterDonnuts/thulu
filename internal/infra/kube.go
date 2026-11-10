package infra

import (
	"context"
	"encoding/json"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
)

type MetricsGetter func(ctx context.Context, absPath string) ([]byte, error)

const (
	metricsNodesPath = "/apis/metrics.k8s.io/v1beta1/nodes"
	metricsPodsPath  = "/apis/metrics.k8s.io/v1beta1/pods"
)

type KubeSource struct {
	Client                kubernetes.Interface
	Metrics               MetricsGetter
	ControlPlaneNamespace string
	RuntimeClass          string
	AgentImages           func() []AgentImageBuild
}

type usageList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Usage      map[string]string `json:"usage"`
		Containers []struct {
			Usage map[string]string `json:"usage"`
		} `json:"containers"`
	} `json:"items"`
}

type cpuMem struct {
	cpu, mem int64
}

type usageMap map[string]cpuMem

func (u usageMap) add(k string, v cpuMem) {
	cur := u[k]
	u[k] = cpuMem{cpu: cur.cpu + v.cpu, mem: cur.mem + v.mem}
}

func parseUsage(b []byte, pods bool) (usageMap, error) {
	var l usageList
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	out := usageMap{}
	for _, it := range l.Items {
		key := it.Metadata.Name
		if pods {
			key = it.Metadata.Namespace + "/" + it.Metadata.Name
			for _, c := range it.Containers {
				out.add(key, quantities(c.Usage))
			}
			continue
		}
		out.add(key, quantities(it.Usage))
	}
	return out, nil
}

func quantities(u map[string]string) cpuMem {
	var v cpuMem
	if q, err := resource.ParseQuantity(u["cpu"]); err == nil {
		v.cpu = q.MilliValue()
	}
	if q, err := resource.ParseQuantity(u["memory"]); err == nil {
		v.mem = q.Value()
	}
	return v
}

func sampleKube(ctx context.Context, src KubeSource) (*KubeInfo, []Container, []Problem) {
	k := &kubeSample{ctx: ctx, src: src, info: &KubeInfo{
		RuntimeClass: src.RuntimeClass, ControlPlaneNamespace: src.ControlPlaneNamespace,
	}}
	nodeUse, podUse := k.metrics()
	tenants := k.tenants()
	pods, requests := k.pods(tenants, podUse)
	k.nodes(nodeUse, requests)
	k.quotas(tenants)
	k.pvcs(tenants)
	if src.AgentImages != nil {
		k.info.AgentImages = src.AgentImages()
	}
	return k.info, pods, k.problems
}

type kubeSample struct {
	ctx      context.Context
	src      KubeSource
	info     *KubeInfo
	problems []Problem
}

func (k *kubeSample) fail(err error, key string) {
	k.problems = append(k.problems, Problem{Source: "kubernetes", Err: apperr.Wrap(err, key)})
}

func (k *kubeSample) metrics() (nodeUse, podUse usageMap) {
	if k.src.Metrics == nil {
		return usageMap{}, usageMap{}
	}
	nb, nerr := k.src.Metrics(k.ctx, metricsNodesPath)
	pb, perr := k.src.Metrics(k.ctx, metricsPodsPath)
	if nerr == nil && perr == nil {
		nodeUse, nerr = parseUsage(nb, false)
		podUse, perr = parseUsage(pb, true)
		if nerr == nil && perr == nil {
			k.info.MetricsAvailable = true
			return nodeUse, podUse
		}
	}
	k.problems = append(k.problems, Problem{Source: "metrics",
		Err: apperr.Wrap(firstErr(nerr, perr), "infra.errors.kube_metrics")})
	return usageMap{}, usageMap{}
}

func (k *kubeSample) tenants() map[string]string {
	tenants := map[string]string{}
	nss, err := k.src.Client.CoreV1().Namespaces().List(k.ctx, metav1.ListOptions{})
	if err != nil {
		k.fail(err, "infra.errors.kube_namespaces")
		return tenants
	}
	for i := range nss.Items {
		ns := &nss.Items[i]
		if ns.Labels[kubeapi.LabelPlane] == "data" {
			tenants[ns.Name] = ns.Labels[kubeapi.LabelTenant]
		}
	}
	return tenants
}

func (k *kubeSample) pods(tenants map[string]string, podUse usageMap) ([]Container, usageMap) {
	requests := usageMap{}
	list, err := k.src.Client.CoreV1().Pods("").List(k.ctx, metav1.ListOptions{})
	if err != nil {
		k.fail(err, "infra.errors.kube_pods")
		return nil, requests
	}
	pods := make([]Container, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		c := podContainer(p, k.src.ControlPlaneNamespace, tenants)
		if u, ok := podUse[p.Namespace+"/"+p.Name]; ok {
			c.HasUsage = true
			c.CPUCores = float64(u.cpu) / 1000
			c.MemBytes = nonNegative(u.mem)
		}
		tallyPhase(&k.info.Pods, p.Status.Phase)
		if p.Spec.NodeName != "" && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			requests.add(p.Spec.NodeName, cpuMem{cpu: c.CPURequestMilli, mem: c.MemRequest})
		}
		pods = append(pods, c)
	}
	return pods, requests
}

func (k *kubeSample) nodes(use, requests usageMap) {
	nodes, err := k.src.Client.CoreV1().Nodes().List(k.ctx, metav1.ListOptions{})
	if err != nil {
		k.fail(err, "infra.errors.kube_nodes")
		return
	}
	for i := range nodes.Items {
		k.info.Nodes = append(k.info.Nodes, nodeRow(&nodes.Items[i], use, requests))
	}
	sort.Slice(k.info.Nodes, func(i, j int) bool { return k.info.Nodes[i].Name < k.info.Nodes[j].Name })
}

func (k *kubeSample) quotas(tenants map[string]string) {
	qs, err := k.src.Client.CoreV1().ResourceQuotas("").List(k.ctx, metav1.ListOptions{})
	if err != nil {
		k.fail(err, "infra.errors.kube_quotas")
		return
	}
	for i := range qs.Items {
		q := &qs.Items[i]
		tenant, ok := tenants[q.Namespace]
		if !ok {
			continue
		}
		k.info.Quotas = append(k.info.Quotas, Quota{
			Namespace: q.Namespace, Name: q.Name, WorkspaceID: tenant,
			Hard: quantityMap(q.Status.Hard), Used: quantityMap(q.Status.Used),
		})
	}
	sort.Slice(k.info.Quotas, func(i, j int) bool { return k.info.Quotas[i].Namespace < k.info.Quotas[j].Namespace })
}

func (k *kubeSample) pvcs(tenants map[string]string) {
	pvcs, err := k.src.Client.CoreV1().PersistentVolumeClaims("").List(k.ctx, metav1.ListOptions{})
	if err != nil {
		k.fail(err, "infra.errors.kube_pvcs")
		return
	}
	for i := range pvcs.Items {
		k.info.PVCs = append(k.info.PVCs, pvcRow(&pvcs.Items[i], tenants))
	}
}

func podContainer(p *corev1.Pod, controlNS string, tenants map[string]string) Container {
	tenant, isTenant := tenants[p.Namespace]
	c := Container{
		ID:           string(p.UID),
		Name:         p.Name,
		Namespace:    p.Namespace,
		Node:         p.Spec.NodeName,
		Kind:         classifyPod(p.Namespace, p.Labels, p.Name, controlNS, isTenant),
		State:        string(p.Status.Phase),
		Status:       podStatus(p),
		WorkspaceID:  firstNonEmpty(p.Labels[kubeapi.LabelWorkspace], tenant),
		TaskID:       p.Labels[kubeapi.LabelTaskID],
		Agent:        p.Annotations[kubeapi.AnnotationAgentType],
		Stage:        p.Labels[kubeapi.LabelStage],
		Service:      firstNonEmpty(p.Labels[labelAppName], p.Labels["app"]),
		RuntimeClass: deref(p.Spec.RuntimeClassName),
	}
	c.running = p.Status.Phase == corev1.PodRunning
	if c.Kind == KindAgent {
		c.Instance = p.Annotations[kubeapi.AnnotationInstanceID]
	}
	if p.Status.StartTime != nil {
		c.StartedAt = p.Status.StartTime.UTC()
	}
	for i := range p.Spec.Containers {
		ct := &p.Spec.Containers[i]
		if i == 0 {
			c.Image = ct.Image
		}
		c.CPURequestMilli += ct.Resources.Requests.Cpu().MilliValue()
		c.CPULimitMilli += ct.Resources.Limits.Cpu().MilliValue()
		c.MemRequest += ct.Resources.Requests.Memory().Value()
		c.MemLimit += nonNegative(ct.Resources.Limits.Memory().Value())
	}
	for i := range p.Status.ContainerStatuses {
		c.Restarts += p.Status.ContainerStatuses[i].RestartCount
	}
	return c
}

func podStatus(p *corev1.Pod) string {
	if p.Status.Reason != "" {
		return p.Status.Reason
	}
	for i := range p.Status.ContainerStatuses {
		cs := &p.Status.ContainerStatuses[i]
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			return w.Reason
		}
		if t := cs.State.Terminated; t != nil && t.Reason != "" {
			return t.Reason
		}
	}
	return string(p.Status.Phase)
}

func nodeRow(n *corev1.Node, use, requests usageMap) Node {
	row := Node{
		Name:             n.Name,
		KubeletVersion:   n.Status.NodeInfo.KubeletVersion,
		OSImage:          n.Status.NodeInfo.OSImage,
		Kernel:           n.Status.NodeInfo.KernelVersion,
		ContainerRuntime: n.Status.NodeInfo.ContainerRuntimeVersion,
		CPUCapacityMilli: n.Status.Capacity.Cpu().MilliValue(),
		CPUAllocMilli:    n.Status.Allocatable.Cpu().MilliValue(),
		MemCapacity:      n.Status.Capacity.Memory().Value(),
		MemAlloc:         n.Status.Allocatable.Memory().Value(),
		EphemeralCap:     n.Status.Capacity.StorageEphemeral().Value(),
		PodsCapacity:     n.Status.Capacity.Pods().Value(),
	}
	if u, ok := use[n.Name]; ok {
		row.CPUUsageMilli, row.MemUsage = u.cpu, u.mem
	}
	if r, ok := requests[n.Name]; ok {
		row.CPURequestsMilli, row.MemRequests = r.cpu, r.mem
	}
	for _, c := range n.Status.Conditions {
		row.Conditions = append(row.Conditions, NodeCondition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message,
		})
	}
	return row
}

func pvcRow(p *corev1.PersistentVolumeClaim, tenants map[string]string) PVC {
	row := PVC{
		Namespace:    p.Namespace,
		Name:         p.Name,
		Phase:        string(p.Status.Phase),
		StorageClass: deref(p.Spec.StorageClassName),
		WorkspaceID:  tenants[p.Namespace],
	}
	if q, ok := p.Status.Capacity[corev1.ResourceStorage]; ok {
		row.Capacity = q.Value()
	} else if q, ok := p.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		row.Capacity = q.Value()
	}
	return row
}

func quantityMap(l corev1.ResourceList) map[string]string {
	out := make(map[string]string, len(l))
	for k, v := range l {
		out[string(k)] = v.String()
	}
	return out
}

func tallyPhase(t *PodPhaseTally, phase corev1.PodPhase) {
	t.Total++
	switch phase {
	case corev1.PodRunning:
		t.Running++
	case corev1.PodPending:
		t.Pending++
	case corev1.PodSucceeded:
		t.Succeeded++
	case corev1.PodFailed:
		t.Failed++
	case corev1.PodUnknown:
		t.Unknown++
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonNegative(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}
