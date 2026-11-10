//go:build !foss

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/catrust"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/proxyenv"
	"github.com/MisterDonnuts/thulu/internal/sandbox/execagent"
)

type Kube struct {
	client       kubernetes.Interface
	nsTemplate   string
	defaultNS    string
	image        string
	runtimeClass string
	execPort     int32
	worktreeSize string
	cacheSize    resource.Quantity
	cacheOn      bool
	provisioner  NamespaceProvisioner
	resources    kubeResources
	caBundle     bool
	env          map[string]string
	httpc        *http.Client

	mu   sync.Mutex
	pods map[string]sbRec
}

type NamespaceProvisioner interface {
	EnsureNamespace(ctx context.Context, name, tenant string) error
}

type PreviewAccessProvisioner interface {
	EnsureSandboxPreview(ctx context.Context, p kuberuntime.SandboxPreview) error
}

type kubeResources struct {
	cpuRequest, cpuLimit, memRequest, memLimit resource.Quantity
	ephRequest, ephLimit                       resource.Quantity
}

type sbRec struct {
	namespace string
	token     string
}

const (
	sandboxContainerName = "sandbox"
	execAgentAuthEnv     = "OCTOPUS_EXEC_TOKEN"
	sandboxLabel         = kuberuntime.LabelSandbox
	podDeleteTimeout     = 2 * time.Minute // Kata pods are slow to unwind.
	podDeletePoll        = 500 * time.Millisecond
)

type KubeConfig struct {
	Namespace string
	// Must match the agent pods' namespace template so both co-locate in one tenant namespace.
	NamespaceTemplate string
	Image             string
	RuntimeClass      string
	ExecPort          int
	WorktreeSize      string
	CacheSize         string

	Provisioner NamespaceProvisioner

	CPURequest       string
	CPULimit         string
	MemoryRequest    string
	MemoryLimit      string
	EphemeralRequest string
	EphemeralLimit   string

	CABundle bool

	// Travels in the per-pod Secret, never as literals in the pod spec: proxy URLs can embed
	// credentials.
	Env map[string]string
}

const (
	EnvSandboxNamespace        = "OCTOPUS_SANDBOX_NAMESPACE"
	EnvSandboxCPURequest       = "OCTOPUS_SANDBOX_CPU_REQUEST"
	EnvSandboxCPULimit         = "OCTOPUS_SANDBOX_CPU_LIMIT"
	EnvSandboxMemoryRequest    = "OCTOPUS_SANDBOX_MEMORY_REQUEST"
	EnvSandboxEphemeralRequest = "OCTOPUS_SANDBOX_EPHEMERAL_REQUEST"
	EnvSandboxEphemeralLimit   = "OCTOPUS_SANDBOX_EPHEMERAL_LIMIT"
)

func KubeConfigFromEnv(getenv func(string) string) KubeConfig {
	env := func(k string) string { return strings.TrimSpace(getenv(k)) }
	return KubeConfig{
		Namespace:        env(EnvSandboxNamespace),
		Image:            env(EnvSandboxImage),
		RuntimeClass:     env("OCTOPUS_RUNTIME_CLASS"),
		CPURequest:       env(EnvSandboxCPURequest),
		CPULimit:         env(EnvSandboxCPULimit),
		MemoryRequest:    env(EnvSandboxMemoryRequest),
		CABundle:         env(catrust.BundleEnv) != "",
		EphemeralRequest: env(EnvSandboxEphemeralRequest),
		EphemeralLimit:   env(EnvSandboxEphemeralLimit),
		CacheSize:        env(kuberuntime.EnvCacheSize),
		Env:              proxyenv.FromEnv(getenv),
	}
}

var (
	_ Runtime                  = (*Kube)(nil)
	_ PreviewAccessProvisioner = (*kuberuntime.Runtime)(nil)
)

// Must match the agent pods' fsGroup so both can write the shared worktree claim; numeric so
// runAsNonRoot admission can verify it.
const sandboxUID = 1000

func NewKube(client kubernetes.Interface, cfg KubeConfig) *Kube {
	if cfg.ExecPort <= 0 || cfg.ExecPort > 65535 {
		cfg.ExecPort = 8587
	}
	if cfg.NamespaceTemplate == "" {
		cfg.NamespaceTemplate = "tenant-%s"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "octopus-sandboxes"
	}
	if cfg.WorktreeSize == "" {
		cfg.WorktreeSize = "10Gi"
	}
	res := kubeResources{
		cpuRequest: quantityOr("sandbox.log.invalid_cpu_request", cfg.CPURequest, "250m"),
		cpuLimit:   quantityOr("sandbox.log.invalid_cpu_limit", cfg.CPULimit, "2"),
		memRequest: quantityOr("sandbox.log.invalid_memory_request", cfg.MemoryRequest, "512Mi"),
		memLimit:   quantityOr("sandbox.log.invalid_memory_limit", cfg.MemoryLimit, "6Gi"),
		ephRequest: quantityOr("sandbox.log.invalid_ephemeral_request", cfg.EphemeralRequest, "1Gi"),
		ephLimit:   quantityOr("sandbox.log.invalid_ephemeral_limit", cfg.EphemeralLimit, "20Gi"),
	}
	cacheSize, cacheOn := cacheSetting(cfg.CacheSize)
	return &Kube{
		cacheSize:    cacheSize,
		cacheOn:      cacheOn,
		client:       client,
		nsTemplate:   cfg.NamespaceTemplate,
		defaultNS:    cfg.Namespace,
		image:        imageref.Resolve(cfg.Image),
		runtimeClass: cfg.RuntimeClass,
		worktreeSize: cfg.WorktreeSize,
		provisioner:  cfg.Provisioner,
		resources:    res,
		caBundle:     cfg.CABundle,
		env:          cfg.Env,
		//nolint:gosec // G115: ExecPort is bounds-checked to a valid TCP port (1-65535) just above.
		execPort: int32(cfg.ExecPort),
		// No client-wide timeout: each Exec bounds its own request (execRequestTimeout).
		httpc: &http.Client{},
		pods:  map[string]sbRec{},
	}
}

func quantityOr(logKey, v, def string) resource.Quantity {
	if v != "" {
		q, err := resource.ParseQuantity(v)
		if err == nil {
			return q
		}
		log.Printf("[sandbox] %s", i18n.Log(logKey, "value", v, "default", def, "err", err))
	}
	return resource.MustParse(def)
}

func (k *Kube) nsFor(workspaceID string) string {
	if workspaceID == "" {
		return k.defaultNS
	}
	return fmt.Sprintf(k.nsTemplate, sanitizeDNS(workspaceID))
}

func (k *Kube) Image() string { return k.image }

func (k *Kube) Create(ctx context.Context, opts CreateOpts) (*CreateResult, error) {
	name := sandboxPodName(opts.TaskID)
	ns := k.nsFor(opts.WorkspaceID)
	if err := k.ensureNamespace(ctx, ns, opts.WorkspaceID); err != nil {
		return nil, err
	}
	logx.Warn(kuberuntime.EnsureWorktreeClaim(ctx, k.client, kuberuntime.WorktreeClaim{
		Namespace:   ns,
		Name:        WorktreePVCName(opts.TaskID),
		Size:        k.worktreeSize,
		OwnerID:     opts.TaskID,
		WorkspaceID: opts.WorkspaceID,
	}), "sandbox.log.ensure_worktree_claim", "task", opts.TaskID)
	cache := k.ensureCache(ctx, ns, opts)
	token, pod, err := k.ensurePod(ctx, ns, name, opts, cache)
	if err != nil {
		return nil, err
	}
	ports := k.appPorts(opts)
	if svcErr := k.ensureService(ctx, pod, opts, ports); svcErr != nil {
		logx.Warn(k.client.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{}), "sandbox.log.rollback_pod", "pod", name)
		return nil, svcErr
	}
	k.ensurePreviewAccess(ctx, pod, opts.TaskID, ports)
	k.mu.Lock()
	k.pods[name] = sbRec{namespace: ns, token: token}
	k.mu.Unlock()
	// Kubernetes has no host ports: each app port maps to itself, which is what preview_url checks
	// the port against.
	exposed := make(map[int]int, len(ports))
	for _, p := range ports {
		exposed[int(p)] = int(p)
	}
	return &CreateResult{ContainerID: name, HostPorts: exposed}, nil
}

// Without a workspace id the sandbox lands in the shared fallback namespace, where one cache would
// be shared across tenants.
func (k *Kube) cacheFor(opts CreateOpts) bool {
	return k.cacheOn && opts.WorkspaceID != ""
}

// On failure, start without the cache: a pod naming a claim that does not exist stays Pending
// forever.
func (k *Kube) ensureCache(ctx context.Context, ns string, opts CreateOpts) bool {
	if !k.cacheFor(opts) {
		return false
	}
	err := kuberuntime.EnsureCacheClaim(ctx, k.client, kuberuntime.CacheClaim{
		Namespace: ns, WorkspaceID: opts.WorkspaceID, Size: k.cacheSize,
	})
	if err != nil {
		logx.Warn(err, "sandbox.log.ensure_cache_claim", "ns", ns)
		return false
	}
	return true
}

func (k *Kube) appPorts(opts CreateOpts) []int32 {
	var out []int32
	for _, p := range appPorts(opts) {
		if p <= 0 || p > 65535 {
			continue
		}
		port := int32(p)
		if port == k.execPort {
			log.Printf("[sandbox] %s", i18n.Log("sandbox.log.app_port_reserved", "port", p))
			continue
		}
		out = append(out, port)
	}
	return out
}

func (k *Kube) ensurePreviewAccess(ctx context.Context, pod *corev1.Pod, taskID string, ports []int32) {
	pa, ok := k.provisioner.(PreviewAccessProvisioner)
	if !ok {
		return
	}
	logx.Warn(pa.EnsureSandboxPreview(ctx, kuberuntime.SandboxPreview{Pod: pod, RootTaskID: taskID, Ports: ports}),
		"sandbox.log.preview_access", "ns", pod.Namespace, "pod", pod.Name)
}

// Sandbox pods are bare (RestartPolicy=Never, no controller), so an evicted pod is never collected
// and blocks Create by name.
// Inspect the existing pod BEFORE writing anything: minting first would overwrite the token of a
// pod we are about to adopt.
func (k *Kube) ensurePod(ctx context.Context, ns, name string, opts CreateOpts, cache bool) (string, *corev1.Pod, error) {
	existing, err := k.client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return "", nil, apperr.Wrap(err, "sandbox.errors.inspect_existing_pod", "ns", ns, "name", name)
	default:
		// A pod without the current sandbox label was made by an older release: no lookup or
		// network policy selects it any more, so it is replaced instead of adopted.
		if podUsable(existing) && existing.Labels[sandboxLabel] == sanitizeDNS(opts.TaskID) {
			if tok := k.execTokenOf(ctx, existing); tok != "" {
				log.Printf("[sandbox] %s", i18n.Log("sandbox.log.adopting_pod", "ns", ns, "name", name, "phase", existing.Status.Phase))
				return tok, existing, nil
			}
		}
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.replacing_dead_pod",
			"ns", ns, "name", name, "phase", existing.Status.Phase, "reason", existing.Status.Reason))
		if err := k.deletePodAndWait(ctx, ns, name); err != nil {
			return "", nil, err
		}
		// Delete the corpse's Secret and Service ourselves: an object rewritten in place keeps its
		// UID and owner and could still be garbage-collected from under the fresh pod.
		k.deleteEnvSecret(ctx, ns, name)
		k.deleteService(ctx, ns, name)
		kuberuntime.DeleteSandboxPreview(ctx, k.client, ns, name)
	}
	return k.createPod(ctx, ns, name, opts, cache)
}

func (k *Kube) createPod(ctx context.Context, ns, name string, opts CreateOpts, cache bool) (string, *corev1.Pod, error) {
	token, err := mintToken()
	if err != nil {
		return "", nil, err
	}
	env := make(map[string]string, len(k.env)+1)
	for key, v := range k.env {
		env[key] = v
	}
	env[execAgentAuthEnv] = token
	pod := k.buildPod(name, opts, cache)
	pod.Namespace = ns
	want, err := kuberuntime.EnvSecret(name, pod.Labels, env)
	if err != nil {
		return "", nil, apperr.Wrap(err, "sandbox.errors.env_secret", "ns", ns, "name", name)
	}
	want.Namespace = ns
	secrets := k.client.CoreV1().Secrets(ns)
	secret, err := kuberuntime.PutSecret(ctx, secrets, want)
	if err != nil {
		return "", nil, apperr.Wrap(err, "sandbox.errors.store_env_secret", "ns", ns, "name", name)
	}
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, kuberuntime.SecretEnv(secret.Name, env)...)
	created, err := k.client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		k.deleteEnvSecret(ctx, ns, name)
		if quotaErr := kuberuntime.QuotaRefusal(err); quotaErr != nil {
			return "", nil, quotaErr
		}
		return "", nil, apperr.Wrap(err, "sandbox.errors.create_pod", "ns", ns, "name", name)
	}
	kuberuntime.OwnSecretByPod(ctx, secrets, secret, created)
	return token, created, nil
}

func (k *Kube) deleteEnvSecret(ctx context.Context, ns, pod string) {
	err := k.client.CoreV1().Secrets(ns).Delete(ctx, kuberuntime.EnvSecretName(pod), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		logx.Warn(err, "sandbox.log.delete_env_secret", "ns", ns, "pod", pod)
	}
}

// Waits until the API no longer serves the pod so the following Create cannot race the deletion.
func (k *Kube) deletePodAndWait(ctx context.Context, ns, name string) error {
	err := k.client.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return apperr.Wrap(err, "sandbox.errors.delete_stale_pod", "ns", ns, "name", name)
	}
	deadline := time.Now().Add(podDeleteTimeout)
	for {
		if _, err := k.client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return apperr.Timeout("sandbox.errors.stale_pod_present", "ns", ns, "name", name, "timeout", podDeleteTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(podDeletePoll):
		}
	}
}

func podUsable(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending
}

// Pods created by older daemons carry the token as a literal env value instead of a Secret ref.
func (k *Kube) execTokenOf(ctx context.Context, pod *corev1.Pod) string {
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if c.Name != sandboxContainerName {
			continue
		}
		for _, e := range c.Env {
			if e.Name != execAgentAuthEnv {
				continue
			}
			if e.Value != "" {
				return e.Value
			}
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
				return ""
			}
			ref := e.ValueFrom.SecretKeyRef
			secret, err := k.client.CoreV1().Secrets(pod.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				logx.Warn(err, "sandbox.log.read_exec_token", "ns", pod.Namespace, "pod", pod.Name)
				return ""
			}
			return string(secret.Data[ref.Key])
		}
	}
	return ""
}

func (k *Kube) recFor(ctx context.Context, podName string) (sbRec, bool) {
	k.mu.Lock()
	rec, ok := k.pods[podName]
	k.mu.Unlock()
	if ok && rec.namespace != "" {
		return rec, true
	}
	pods, err := k.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: sandboxLabel + "=" + strings.TrimPrefix(podName, kuberuntime.SandboxPodPrefix),
	})
	if err != nil || len(pods.Items) == 0 {
		logx.Warn(err, "sandbox.log.rehydrate", "pod", podName)
		return sbRec{}, false
	}
	pod := pods.Items[0]
	rec = sbRec{namespace: pod.Namespace, token: k.execTokenOf(ctx, &pod)}
	k.mu.Lock()
	k.pods[podName] = rec
	k.mu.Unlock()
	return rec, true
}

// With a provisioner, failure is fatal: a sandbox runs untrusted tenant code and must not land in
// an unfenced namespace.
func (k *Kube) ensureNamespace(ctx context.Context, ns, workspaceID string) error {
	if k.provisioner != nil {
		tenant := ""
		if workspaceID != "" {
			tenant = sanitizeDNS(workspaceID)
		}
		if err := k.provisioner.EnsureNamespace(ctx, ns, tenant); err != nil {
			return apperr.Wrap(err, "sandbox.errors.provision_namespace", "ns", ns)
		}
		return nil
	}
	nsClient := k.client.CoreV1().Namespaces()
	_, err := nsClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{kuberuntime.LabelPlane: "data"}},
	}, metav1.CreateOptions{})
	if err == nil || !apierrors.IsAlreadyExists(err) {
		logx.Warn(err, "sandbox.log.ensure_namespace", "ns", ns)
		return nil
	}
	// An existing namespace from an older release may lack the current label.
	existing, err := nsClient.Get(ctx, ns, metav1.GetOptions{})
	if err != nil || existing.Labels[kuberuntime.LabelPlane] == "data" {
		logx.Warn(err, "sandbox.log.ensure_namespace", "ns", ns)
		return nil
	}
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	existing.Labels[kuberuntime.LabelPlane] = "data"
	_, err = nsClient.Update(ctx, existing, metav1.UpdateOptions{})
	logx.Warn(err, "sandbox.log.ensure_namespace", "ns", ns)
	return nil
}

func (k *Kube) buildSandboxPod(name string, opts CreateOpts) *corev1.Pod {
	return k.buildPod(name, opts, k.cacheFor(opts))
}

func (k *Kube) buildPod(name string, opts CreateOpts, cache bool) *corev1.Pod {
	noEscalation := false
	var rc *string
	if k.runtimeClass != "" {
		v := k.runtimeClass
		rc = &v
	}
	automount := false
	nonRoot := true
	// Mirrors kuberuntime's agentFSGroup: a fresh PVC mounts root-owned and both pods run non-root.
	uid := int64(sandboxUID)
	labels := map[string]string{
		sandboxLabel:           sanitizeDNS(opts.TaskID),
		kuberuntime.LabelPlane: "data",
	}
	for key, v := range opts.ExtraLabels {
		labels[key] = v
	}
	volumes, mounts, env := k.caWiring()
	if cache {
		volumes = append(volumes, kubeCacheVolume())
		mounts = append(mounts, kubeCacheMounts(opts.SnapshotsWritable)...)
		env = append(env, cacheEnvVars()...)
	}
	// OnRootMismatch avoids chowning the cache's hundreds of thousands of files on every sandbox
	// start.
	fsGroupPolicy := corev1.FSGroupChangeOnRootMismatch
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: &automount,
			RuntimeClassName:             rc,
			// Numeric non-root uid so restricted Pod Security can verify it; overrides the image's
			// root user, so apt-get does not work here.
			SecurityContext: &corev1.PodSecurityContext{
				SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				RunAsNonRoot:        &nonRoot,
				RunAsUser:           &uid,
				RunAsGroup:          &uid,
				FSGroup:             &uid,
				FSGroupChangePolicy: &fsGroupPolicy,
			},
			// RWO pins the worktree claim to one node, which co-schedules the agent pod mounting
			// the same claim (kuberuntime, kubelauncher).
			Volumes: append([]corev1.Volume{{
				Name: "workspace",
				VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: WorktreePVCName(opts.TaskID),
				}},
			}}, volumes...),
			Containers: []corev1.Container{{
				Name:  sandboxContainerName,
				Image: k.image,
				Ports: []corev1.ContainerPort{{Name: "exec", ContainerPort: k.execPort}},
				Env:   env,
				// Must equal kuberuntime.worktreeMountRoot: sandbox and agent see the same
				// worktrees at the same paths.
				VolumeMounts: append([]corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace/projects"}}, mounts...),
				Resources:    k.resourcesFor(opts),
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &noEscalation,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					RunAsNonRoot:             &nonRoot,
				},
			}},
		},
	}
	return pod
}

func (k *Kube) resourcesFor(opts CreateOpts) corev1.ResourceRequirements {
	memLimit := k.resources.memLimit
	if opts.MemoryBytes >= 256*1024*1024 { // same floor as the Docker runtime
		memLimit = *resource.NewQuantity(opts.MemoryBytes, resource.BinarySI)
	}
	memRequest := k.resources.memRequest
	if memRequest.Cmp(memLimit) > 0 {
		memRequest = memLimit
	}
	cpuRequest := k.resources.cpuRequest
	if cpuRequest.Cmp(k.resources.cpuLimit) > 0 {
		cpuRequest = k.resources.cpuLimit
	}
	ephRequest := k.resources.ephRequest
	if ephRequest.Cmp(k.resources.ephLimit) > 0 {
		ephRequest = k.resources.ephLimit
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: cpuRequest, corev1.ResourceMemory: memRequest,
			corev1.ResourceEphemeralStorage: ephRequest,
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU: k.resources.cpuLimit, corev1.ResourceMemory: memLimit,
			corev1.ResourceEphemeralStorage: k.resources.ephLimit,
		},
	}
}

func (k *Kube) caWiring() ([]corev1.Volume, []corev1.VolumeMount, []corev1.EnvVar) {
	if !k.caBundle {
		return nil, nil, nil
	}
	const vol = "octopus-ca"
	volumes := []corev1.Volume{{
		Name: vol,
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: catrust.ConfigMapName},
		}},
	}}
	mounts := []corev1.VolumeMount{
		{Name: vol, MountPath: catrust.ContainerPath, SubPath: catrust.ConfigMapCAKey, ReadOnly: true},
		{Name: vol, MountPath: catrust.ProfilePath, SubPath: catrust.ConfigMapProfileKey, ReadOnly: true},
	}
	env := []corev1.EnvVar{{Name: "NODE_EXTRA_CA_CERTS", Value: catrust.ContainerPath}}
	return volumes, mounts, env
}

func WorktreePVCName(taskID string) string { return "octopus-wt-" + sanitizeDNS(taskID) }

func (k *Kube) buildSandboxService(pod *corev1.Pod, opts CreateOpts, appPorts []int32) *corev1.Service {
	labels := map[string]string{
		sandboxLabel:               pod.Labels[sandboxLabel],
		kuberuntime.LabelPlane:     "data",
		kuberuntime.LabelManagedBy: "octopus",
		kuberuntime.LabelTaskID:    sanitizeDNS(opts.TaskID),
	}
	if ws := sanitizeDNS(opts.WorkspaceID); ws != "" {
		labels[kuberuntime.LabelWorkspace] = ws
	}
	ports := make([]corev1.ServicePort, 0, 1+len(appPorts))
	ports = append(ports, corev1.ServicePort{Name: "exec", Port: k.execPort})
	for _, p := range appPorts {
		ports = append(ports, corev1.ServicePort{Name: fmt.Sprintf("app-%d", p), Protocol: corev1.ProtocolTCP, Port: p})
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   pod.Name,
			Labels: labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID, //i18n:ignore Kubernetes object kind
			}},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{sandboxLabel: strings.TrimPrefix(pod.Name, kuberuntime.SandboxPodPrefix)},
			Ports:    ports,
		},
	}
}

// A Service not owned by this pod is replaced: an unowned one (older daemon) would leak, and one
// owned by a replaced pod is about to be garbage-collected.
func (k *Kube) ensureService(ctx context.Context, pod *corev1.Pod, opts CreateOpts, appPorts []int32) error {
	svcs := k.client.CoreV1().Services(pod.Namespace)
	want := k.buildSandboxService(pod, opts, appPorts)
	for attempt := 0; ; attempt++ {
		_, err := svcs.Create(ctx, want, metav1.CreateOptions{})
		if err == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(err) || attempt > 0 {
			return apperr.Wrap(err, "sandbox.errors.create_service", "ns", pod.Namespace, "name", pod.Name)
		}
		existing, err := svcs.Get(ctx, pod.Name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return apperr.Wrap(err, "sandbox.errors.get_service", "ns", pod.Namespace, "name", pod.Name)
		case ownedBy(existing.OwnerReferences, pod.UID):
			return k.syncServicePorts(ctx, existing, want.Spec.Ports)
		}
		uid := existing.UID
		err = svcs.Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if err != nil && !apierrors.IsNotFound(err) {
			return apperr.Wrap(err, "sandbox.errors.replace_service", "ns", pod.Namespace, "name", pod.Name)
		}
	}
}

func (k *Kube) syncServicePorts(ctx context.Context, svc *corev1.Service, want []corev1.ServicePort) error {
	if servicePortsMatch(svc.Spec.Ports, want) {
		return nil
	}
	svc.Spec.Ports = want
	if _, err := k.client.CoreV1().Services(svc.Namespace).Update(ctx, svc, metav1.UpdateOptions{}); err != nil {
		return apperr.Wrap(err, "sandbox.errors.update_service", "ns", svc.Namespace, "name", svc.Name)
	}
	return nil
}

// Compares only the fields buildSandboxService sets; the API server fills in the rest, so a full
// compare would update forever.
func servicePortsMatch(have, want []corev1.ServicePort) bool {
	if len(have) != len(want) {
		return false
	}
	for i := range want {
		if have[i].Name != want[i].Name || have[i].Port != want[i].Port {
			return false
		}
	}
	return true
}

func (k *Kube) ClusterAddress(ctx context.Context, containerID string, port int) (string, bool) {
	rec, ok := k.recFor(ctx, containerID)
	if !ok || rec.namespace == "" {
		return "", false
	}
	return fmt.Sprintf("http://%s.%s.svc:%d", containerID, rec.namespace, port), true
}

func ownedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	if uid == "" {
		return false
	}
	for _, r := range refs {
		if r.UID == uid {
			return true
		}
	}
	return false
}

func (k *Kube) Exec(ctx context.Context, containerID string, opts ExecOpts) (*ExecResult, error) {
	rec, _ := k.recFor(ctx, containerID)
	if rec.token == "" {
		return nil, apperr.NotFound("sandbox.errors.no_exec_token", "container", containerID)
	}
	baseURL := fmt.Sprintf("http://%s.%s.svc:%d", containerID, rec.namespace, k.execPort)
	req := execagent.ExecRequest{
		Command:   []string{"bash", "-lc", opts.Command},
		Cwd:       opts.Cwd,
		Env:       opts.Env,
		TimeoutMs: opts.TimeoutMs,
		// opts.Env goes on top of the pod's env, as with `docker exec`; without it a command with
		// its own env loses HOME, proxy and cache vars.
		InheritEnv: true,
		Stdin:      opts.Stdin,
	}
	var res *execagent.ExecResult
	started := time.Now()
	err := k.retryDial(ctx, rec.namespace, containerID, func() error {
		actx, cancel := context.WithTimeout(ctx, execRequestTimeout(opts.TimeoutMs))
		defer cancel()
		started = time.Now()
		r, xerr := execagent.Exec(actx, k.httpc, baseURL, rec.token, req)
		res = r
		return xerr
	})
	if err != nil {
		return nil, err
	}
	return &ExecResult{
		Stdout:     res.Stdout,
		Stderr:     res.Stderr,
		ExitCode:   res.ExitCode,
		TimedOut:   res.TimedOut,
		DurationMs: time.Since(started).Milliseconds(),
	}, nil
}

func (k *Kube) Alive(ctx context.Context, containerID string) bool {
	rec, ok := k.recFor(ctx, containerID)
	if !ok {
		return false
	}
	pod, err := k.client.CoreV1().Pods(rec.namespace).Get(ctx, containerID, metav1.GetOptions{})
	if err != nil {
		return false
	}
	return podUsable(pod)
}

func (k *Kube) Remove(ctx context.Context, containerID string) error {
	rec, ok := k.recFor(ctx, containerID)
	if !ok {
		return nil
	}
	return k.removePod(ctx, rec.namespace, containerID)
}

// Owner references are best-effort (they may have failed to attach), and an explicit delete frees
// the names at once.
func (k *Kube) removePod(ctx context.Context, ns, name string) error {
	k.mu.Lock()
	delete(k.pods, name)
	k.mu.Unlock()
	k.deleteService(ctx, ns, name)
	kuberuntime.DeleteSandboxPreview(ctx, k.client, ns, name)
	err := k.client.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return apperr.Wrap(err, "sandbox.errors.delete_pod", "ns", ns, "name", name)
	}
	k.deleteEnvSecret(ctx, ns, name)
	return nil
}

func (k *Kube) deleteService(ctx context.Context, ns, name string) {
	err := k.client.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		logx.Warn(err, "sandbox.log.delete_service", "ns", ns, "name", name)
	}
}

func (k *Kube) ReleaseStorage(ctx context.Context, taskID string) error {
	_, err := kuberuntime.ReleaseWorktreeClaims(ctx, k.client, taskID)
	return err
}

func (k *Kube) SweepTerminated(ctx context.Context, olderThan time.Duration) (int, error) {
	list, err := k.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: sandboxLabel})
	if err != nil {
		return 0, apperr.Wrap(err, "sandbox.errors.list_pods")
	}
	cutoff := time.Now().Add(-olderThan)
	deleted := 0
	for i := range list.Items {
		pod := &list.Items[i]
		finished := kuberuntime.PodFinishedAt(pod)
		if pod.DeletionTimestamp != nil || finished.IsZero() || finished.After(cutoff) {
			continue
		}
		if err := k.removePod(ctx, pod.Namespace, pod.Name); err != nil {
			logx.Warn(err, "sandbox.log.kube_sweep")
			continue
		}
		log.Printf("[sandbox] %s", i18n.Log("sandbox.log.swept_pod",
			"ns", pod.Namespace, "name", pod.Name, "phase", pod.Status.Phase, "age", time.Since(finished).Round(time.Second)))
		deleted++
	}
	return deleted, nil
}

func sandboxPodName(taskID string) string { return kuberuntime.SandboxPodPrefix + sanitizeDNS(taskID) }

// Capped at 55 so the "sandbox-" prefix keeps the name within 63 chars.
func sanitizeDNS(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 55 {
		out = out[:55]
	}
	return out
}

func mintToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.Wrap(err, "sandbox.errors.mint_token")
	}
	return hex.EncodeToString(buf), nil
}

var (
	_ StorageReleaser   = (*Kube)(nil)
	_ TerminatedSweeper = (*Kube)(nil)
	_ ClusterAddresser  = (*Kube)(nil)
)

func (k *Kube) CacheQuotaBytes() int64 {
	if !k.cacheOn {
		return 0
	}
	return k.cacheSize.Value()
}
