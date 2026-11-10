// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/MisterDonnuts/thulu/internal/agentimage"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/browserrun"
	"github.com/MisterDonnuts/thulu/internal/catrust"
	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/imageref"
	"github.com/MisterDonnuts/thulu/internal/kubeapi"
	"github.com/MisterDonnuts/thulu/internal/types"
)

const (
	LabelTaskID    = kubeapi.LabelTaskID
	LabelWorkspace = kubeapi.LabelWorkspace
	LabelStage     = kubeapi.LabelStage
	LabelManagedBy = kubeapi.LabelManagedBy
	LabelTenant    = kubeapi.LabelTenant
	LabelPlane     = kubeapi.LabelPlane

	managedByValue = kubeapi.ManagedByValue
	planeValue     = "data"

	AnnotationInstanceID = kubeapi.AnnotationInstanceID
	AnnotationAgentType  = kubeapi.AnnotationAgentType
	// Same key the Docker launcher uses as a container label; Reconcile reads it in either runtime.
	AnnotationLaunchKind = "octopus.launch_kind"
)

const (
	defaultNamespaceTemplate = "tenant-%s"
	defaultMemoryLimit       = "6Gi" // mirrors the sandbox memory cap
	defaultMemoryRequest     = "512Mi"
	defaultCPURequest        = "250m"
	defaultCPULimit          = "2"
	defaultGracePeriod       = int64(10)
	defaultPollInterval      = 2 * time.Second
)

// Mirrors the Docker watcher's exit code for lost containers.
const ExitDeleted = -1

type Config struct {
	KubeconfigPath string
	Context        string
	InCluster      bool

	NamespaceTemplate   string
	RuntimeClassName    string
	AllowNoRuntimeClass bool
	Image               string
	EngineImages        map[string]string
	LoaderImage         string

	MemoryRequest     string
	MemoryLimit       string
	CPURequest        string
	CPULimit          string
	EphemeralRequest  string
	EphemeralLimit    string
	EmptyDirSizeLimit string

	RunAsNonRoot bool

	TerminationGracePeriodSeconds int64

	Isolation TenantIsolation

	CABundle []byte

	// Travels in the per-pod Secret like the rest of the env: a proxy URL can hold a password.
	ForwardEnv map[string]string

	TenantSecrets TenantSecrets

	Browser browserrun.Config

	// Off when Namespace is empty (imagebuild.go).
	ImageBuild ImageBuildConfig
}

// Env is the only credential channel; its values go into a per-run Secret (envsecret.go), never as
// literals in the pod spec.
type LaunchSpec struct {
	WorkspaceID string
	TaskID      string
	Stage       string
	InstanceID  string
	Image       string
	Engine      string
	Command     []string
	Env         map[string]string
	Labels      map[string]string
	Annotations map[string]string
	Worktrees   []WorktreeMount
	// The same RWO claim the sandbox pod mounts, which co-locates both pods on one node sharing a
	// live worktree.
	WorktreePVC           string
	WorktreeOwnerID       string
	SandboxRoot           string
	ActiveDeadlineSeconds int64
	Browser               bool
	BrowserJob            bool
}

// CloneURL must carry no credentials: an embedded token would persist in the pod spec and in
// .git/config on the PVC after the run key is revoked.
// CredentialEnv names the env var a git credential helper reads at use time.
type WorktreeMount struct {
	Name          string
	CloneURL      string
	Branch        string
	ReadOnly      bool
	CredentialEnv string
}

type ExitedPod struct {
	Name       string
	TaskID     string
	InstanceID string
	AgentType  string
	LaunchKind string
	ExitCode   int
}

type Runtime struct {
	client        kubernetes.Interface
	cfg           Config
	resources     corev1.ResourceRequirements
	emptyDirSize  resource.Quantity
	pollInterval  time.Duration
	noBindingOnce sync.Once
	nodes         nodeAddrCache
	images        *imageBuilder
}

// Refuses to run without a RuntimeClass unless explicitly allowed: on the node's shared kernel, one
// kernel exploit reaches the node and every other tenant.
func New(cfg Config) (*Runtime, error) {
	if err := checkRuntimeClass(cfg); err != nil {
		return nil, err
	}
	rc, err := newRESTConfig(cfg)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.build_clientset")
	}
	return NewWithClient(client, cfg)
}

func NewWithClient(client kubernetes.Interface, cfg Config) (*Runtime, error) {
	if cfg.NamespaceTemplate == "" {
		cfg.NamespaceTemplate = defaultNamespaceTemplate
	}
	if !strings.Contains(cfg.NamespaceTemplate, "%s") {
		return nil, apperr.Invalid("kuberuntime.errors.namespace_template_placeholder", "template", cfg.NamespaceTemplate)
	}
	if cfg.TerminationGracePeriodSeconds <= 0 {
		cfg.TerminationGracePeriodSeconds = defaultGracePeriod
	}
	res, err := buildResources(cfg)
	if err != nil {
		return nil, err
	}
	cfg.Isolation.applyDefaults()
	if err := cfg.Isolation.validate(res); err != nil {
		return nil, err
	}
	if cfg.Browser.PerRun() {
		if err := cfg.Isolation.validateBrowser(cfg.Browser); err != nil {
			return nil, err
		}
	}
	if err := cfg.TenantSecrets.validate(); err != nil {
		return nil, err
	}
	emptyDir, sizeErr := parseEmptyDirSizeLimit(cfg.EmptyDirSizeLimit)
	if sizeErr != nil {
		return nil, sizeErr
	}
	cfg.Image = imageref.Resolve(cfg.Image)
	engineImages := make(map[string]string, len(cfg.EngineImages))
	for eng, img := range cfg.EngineImages {
		engineImages[eng] = imageref.Resolve(img)
	}
	cfg.EngineImages = engineImages
	cfg.LoaderImage = imageref.Resolve(cfg.LoaderImage)
	cfg.Browser.Image = imageref.Resolve(cfg.Browser.Image)
	rt := &Runtime{client: client, cfg: cfg, resources: res, emptyDirSize: emptyDir, pollInterval: defaultPollInterval}
	if cfg.ImageBuild.enabled() {
		rt.images = newImageBuilder(rt, cfg.ImageBuild)
	}
	return rt, nil
}

func checkRuntimeClass(cfg Config) error {
	if cfg.RuntimeClassName != "" {
		return nil
	}
	if !cfg.AllowNoRuntimeClass {
		return apperr.Invalid("kuberuntime.errors.runtime_class_required", "runtime_class_env", EnvRuntimeClass, "allow_env", EnvAllowNoRuntimeClass)
	}
	log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.no_runtime_class", "allow_env", EnvAllowNoRuntimeClass))
	return nil
}

func (r *Runtime) Clientset() kubernetes.Interface { return r.client }

func newRESTConfig(cfg Config) (*rest.Config, error) {
	if cfg.InCluster {
		rc, err := rest.InClusterConfig()
		if err != nil {
			return nil, apperr.Wrap(err, "kuberuntime.errors.in_cluster_config")
		}
		return rc, nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if cfg.KubeconfigPath != "" {
		rules.ExplicitPath = cfg.KubeconfigPath
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: cfg.Context}
	rc, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.load_kubeconfig", "context", cfg.Context)
	}
	return rc, nil
}

func buildResources(cfg Config) (corev1.ResourceRequirements, error) {
	parse := func(value, fallback, key string) (resource.Quantity, error) {
		if value == "" {
			value = fallback
		}
		q, err := resource.ParseQuantity(value)
		if err != nil {
			return resource.Quantity{}, apperr.WrapKind(apperr.KindInvalid, err, key, "value", value)
		}
		return q, nil
	}
	memReq, err := parse(cfg.MemoryRequest, defaultMemoryRequest, "kuberuntime.errors.parse_memory_request")
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	memLim, err := parse(cfg.MemoryLimit, defaultMemoryLimit, "kuberuntime.errors.parse_memory_limit")
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	cpuReq, err := parse(cfg.CPURequest, defaultCPURequest, "kuberuntime.errors.parse_cpu_request")
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	cpuLim, err := parse(cfg.CPULimit, defaultCPULimit, "kuberuntime.errors.parse_cpu_limit")
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	ephReq, err := parse(cfg.EphemeralRequest, defaultEphemeralRequest, "kuberuntime.errors.parse_ephemeral_request")
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	ephLim, err := parse(cfg.EphemeralLimit, defaultEphemeralLimit, "kuberuntime.errors.parse_ephemeral_limit")
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: memReq, corev1.ResourceCPU: cpuReq, corev1.ResourceEphemeralStorage: ephReq},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: memLim, corev1.ResourceCPU: cpuLim, corev1.ResourceEphemeralStorage: ephLim},
	}, nil
}

func (r *Runtime) NamespaceFor(workspaceID string) string {
	return fmt.Sprintf(r.cfg.NamespaceTemplate, sanitizeDNS1123(workspaceID))
}

func (r *Runtime) EnsureTenantNamespace(ctx context.Context, workspaceID string) error {
	if sanitizeDNS1123(workspaceID) == "" {
		return apperr.Invalid("kuberuntime.errors.launch_workspace_required")
	}
	return r.EnsureNamespace(ctx, r.NamespaceFor(workspaceID), sanitizeDNS1123(workspaceID))
}

func (r *Runtime) LaunchPod(ctx context.Context, spec LaunchSpec) (string, error) {
	if spec.WorkspaceID == "" || spec.TaskID == "" || spec.InstanceID == "" {
		return "", apperr.Invalid("kuberuntime.errors.launch_ids_required",
			"workspace", spec.WorkspaceID, "task", spec.TaskID, "instance", spec.InstanceID)
	}
	spec.Image = imageref.Resolve(spec.Image)
	if spec.Image == "" {
		var env string
		imgs := agentimage.Images{Override: r.cfg.Image, ByEngine: r.cfg.EngineImages}
		if spec.Image, env = imgs.For(spec.Engine); spec.Image == "" {
			return "", apperr.Invalid("kuberuntime.errors.no_image", "env", env)
		}
	}
	if r.images != nil {
		if built, ok := r.images.lookup(spec.Image); ok {
			spec.Image = built
		}
	}
	ns := r.NamespaceFor(spec.WorkspaceID)
	// The claim must exist before either pod references it: the sandbox is acquired lazily, so
	// without this the agent pod sits Pending forever.
	if spec.WorktreePVC != "" {
		if err := r.ensureWorktreeClaim(ctx, ns, spec); err != nil {
			return "", err
		}
	}
	spec.Env = withForwardEnv(spec.Env, r.cfg.ForwardEnv)
	spec.Env = withWorktreeSafeDirs(spec)
	pod := r.buildPod(spec)
	// The Secret must exist before the pod: the kubelet resolves secretKeyRef at container start.
	secret, err := r.putEnvSecret(ctx, ns, pod.Name, pod.Labels, spec.Env)
	if err != nil {
		return "", err
	}
	created, err := r.client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		r.deleteEnvSecret(ctx, ns, pod.Name)
		if quotaErr := QuotaRefusal(err); quotaErr != nil {
			return "", quotaErr
		}
		return "", apperr.Wrap(err, "kuberuntime.errors.create_pod", "name", pod.Name)
	}
	r.ownSecretByPod(ctx, ns, secret, created)
	return created.Name, nil
}

func withForwardEnv(env, forward map[string]string) map[string]string {
	if len(forward) == 0 {
		return env
	}
	out := make(map[string]string, len(env)+len(forward))
	for k, v := range forward {
		out[k] = v
	}
	for k, v := range env {
		out[k] = v
	}
	return out
}

// An emptyDir mounted at the repo path makes the repo root root-owned while git runs as the agent
// uid; git 2.36+ refuses it without safe.directory.
// Claim worktrees don't need this. The caller's map is not modified.
func withWorktreeSafeDirs(spec LaunchSpec) map[string]string {
	if spec.WorktreePVC != "" || len(spec.Worktrees) == 0 {
		return spec.Env
	}
	names := make([]string, 0, len(spec.Worktrees))
	for _, wt := range spec.Worktrees {
		names = append(names, wt.Name)
	}
	lookup := func(k string) (string, bool) {
		v, ok := spec.Env[k]
		return v, ok
	}
	extra := types.WorktreeGitEnv(lookup, types.ProjectMountTargets(names))
	out := make(map[string]string, len(spec.Env)+len(extra))
	for k, v := range spec.Env {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (spec LaunchSpec) sandboxRoot() string {
	switch {
	case spec.SandboxRoot != "":
		return spec.SandboxRoot
	case spec.WorktreeOwnerID != "":
		return spec.WorktreeOwnerID
	}
	return spec.TaskID
}

func (r *Runtime) buildPod(spec LaunchSpec) *corev1.Pod {
	labels := make(map[string]string, len(spec.Labels)+5)
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[LabelTaskID] = sanitizeDNS1123(spec.TaskID)
	labels[LabelWorkspace] = sanitizeDNS1123(spec.WorkspaceID)
	labels[LabelManagedBy] = managedByValue
	// sandboxpreview.go policies select this label to admit the pod to its sandbox's app ports.
	sandboxRoot := spec.sandboxRoot()
	labels[LabelSandboxRoot] = sanitizeDNS1123(sandboxRoot)
	if spec.Stage != "" {
		labels[LabelStage] = sanitizeDNS1123(spec.Stage)
	}

	podName := sanitizeDNS1123(spec.InstanceID)
	env := SecretEnv(EnvSecretName(podName), spec.Env)

	automount := false
	grace := r.cfg.TerminationGracePeriodSeconds
	noEscalation := false
	var runtimeClass *string
	if r.cfg.RuntimeClassName != "" {
		rc := r.cfg.RuntimeClassName
		runtimeClass = &rc
	}
	podSec := &corev1.PodSecurityContext{
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if r.cfg.RunAsNonRoot {
		nonRoot := true
		podSec.RunAsNonRoot = &nonRoot
	}

	var annotations map[string]string
	if len(spec.Annotations) > 0 {
		annotations = make(map[string]string, len(spec.Annotations))
		for k, v := range spec.Annotations {
			annotations[k] = v
		}
	}

	secCtx := &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noEscalation,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	volumes, mounts, initContainers := r.buildWorktreeVolumes(spec, env, secCtx)
	if r.CABundleConfigured() {
		volumes = append(volumes, caVolume())
		mounts = append(mounts, caVolumeMount())
		for i := range initContainers {
			// Loaders run no entrypoint to merge CA bundles, so git gets the CA through
			// GIT_SSL_CAINFO.
			initContainers[i].VolumeMounts = append(initContainers[i].VolumeMounts, caVolumeMount())
			initContainers[i].Env = append(initContainers[i].Env, corev1.EnvVar{Name: "GIT_SSL_CAINFO", Value: catrust.ContainerPath})
		}
	}
	if spec.Browser && r.cfg.Browser.PerRun() {
		// Last, so the worktree clones finish before the browser starts and holds its memory.
		sidecar, sidecarVolumes := r.browserSidecar(EnvSecretName(podName), spec.Env["MCP_TOOLS"])
		initContainers = append(initContainers, sidecar)
		volumes = append(volumes, sidecarVolumes...)
	}
	// Must be the first init container: no loader or sidecar may start before the pod's network
	// reaches the daemon (waitnetwork.go).
	initContainers = r.withNetworkWait(spec, EnvSecretName(podName), secCtx, initContainers)
	if len(volumes) > 0 {
		// Fresh volumes mount root-owned; without fsGroup the non-root agent cannot write its
		// worktree.
		fsGroup := int64(agentFSGroup)
		podSec.FSGroup = &fsGroup
	}

	resources := r.resources
	if spec.BrowserJob {
		jobMounts, jobVolumes := browserJobVolumes()
		mounts = append(mounts, jobMounts...)
		volumes = append(volumes, jobVolumes...)
		secCtx = browserSecurityContext()
		resources = browserResources(r.cfg.Browser)
		fsGroup := int64(browserUser)
		podSec.FSGroup = &fsGroup
	}

	var deadline *int64
	if spec.ActiveDeadlineSeconds > 0 {
		d := spec.ActiveDeadlineSeconds
		deadline = &d
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         deadline,
			AutomountServiceAccountToken:  &automount,
			TerminationGracePeriodSeconds: &grace,
			RuntimeClassName:              runtimeClass,
			SecurityContext:               podSec,
			InitContainers:                initContainers,
			Volumes:                       volumes,
			Containers: []corev1.Container{{
				Name:            agentContainerName,
				Image:           spec.Image,
				ImagePullPolicy: agentPullPolicy,
				Command:         spec.Command,
				Env:             env,
				Resources:       resources,
				VolumeMounts:    mounts,
				SecurityContext: secCtx,
			}},
		},
	}
}

// Must be explicit: Kubernetes defaults to Always for `:latest`, and agent images may live only in
// containerd with no registry behind them, where Always turns into a failing pull. IfNotPresent
// still pulls remote images that are missing.
const agentPullPolicy = corev1.PullIfNotPresent

// Must match the numeric user the agent images run as.
const agentFSGroup = 1000

// Must match the Docker launcher's paths. The PVC mounts here, not at /workspace, because the
// image's runtime lives under /workspace and would be shadowed.
const worktreeMountRoot = "/workspace/projects"

func (r *Runtime) buildWorktreeVolumes(spec LaunchSpec, env []corev1.EnvVar, secCtx *corev1.SecurityContext) (
	[]corev1.Volume, []corev1.VolumeMount, []corev1.Container,
) {
	if spec.WorktreePVC != "" {
		volumes := []corev1.Volume{{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: spec.WorktreePVC,
			}},
		}}
		mounts := []corev1.VolumeMount{{Name: "workspace", MountPath: worktreeMountRoot}}
		initConts := make([]corev1.Container, 0, len(spec.Worktrees))
		for _, wt := range spec.Worktrees {
			initConts = append(initConts, corev1.Container{
				Name:            "clone-" + sanitizeDNS1123(wt.Name),
				Image:           r.loaderImage(spec),
				ImagePullPolicy: agentPullPolicy,
				Command:         pvcCloneCommand(wt, worktreeMountRoot+"/"+wt.Name),
				Env:             append([]corev1.EnvVar{{Name: "HOME", Value: "/tmp"}}, env...),
				Resources:       r.resources,
				SecurityContext: loaderSecurityContext(secCtx),
				VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: worktreeMountRoot}},
			})
		}
		return volumes, mounts, initConts
	}
	return r.buildWorktreeHydration(spec, env, secCtx)
}

func (r *Runtime) loaderImage(spec LaunchSpec) string {
	if r.cfg.LoaderImage != "" {
		return r.cfg.LoaderImage
	}
	return spec.Image
}

// The loader image may declare a non-numeric USER, which RunAsNonRoot admission rejects ("cannot
// verify user is non-root"); pin uid 1000.
func loaderSecurityContext(base *corev1.SecurityContext) *corev1.SecurityContext {
	cp := base.DeepCopy()
	uid := int64(agentFSGroup)
	cp.RunAsUser = &uid
	return cp
}

// The clone is a new pod's first request to the daemon, refused until the NetworkPolicy controller
// (kube-router on k3s) programs the pod IP.
// 10 attempts 2s apart; the destination is emptied, not removed, since it may be a mount point.
//
//i18n:ignore shell function; its lines go to the container log, which stays in English
const cloneRetryFunc = `clone_with_retry() {
  n=0
  while :; do
    if [ -n "$2" ]; then
      git -c credential.helper= -c "credential.helper=$4" clone --branch "$2" --single-branch -- "$3" "$1" && return 0
    else
      git -c credential.helper= -c "credential.helper=$4" clone -- "$3" "$1" && return 0
    fi
    n=$((n+1))
    if [ "$n" -ge 10 ]; then
      echo "clone failed after $n attempts" >&2
      return 1
    fi
    echo "clone attempt $n failed; retrying in 2s" >&2
    find "$1" -mindepth 1 -delete 2>/dev/null || true
    sleep 2
  done
}
`

// Arguments go as positional parameters ($1..$4), never spliced into the script.
// An existing clone may embed a credential in origin; it is scrubbed.
func pvcCloneCommand(wt WorktreeMount, dest string) []string {
	const script = `set -e
if [ -e "$1/.git" ]; then
  echo "worktree $1 already hydrated"
  cur=$(git -C "$1" remote get-url origin 2>/dev/null || true)
  clean=$(printf '%s' "$cur" | sed -E 's#^([A-Za-z][A-Za-z0-9+.-]*://)[^/@]*@#\1#')
  if [ -n "$cur" ] && [ "$clean" != "$cur" ]; then git -C "$1" remote set-url origin "$clean"; fi
else
  clone_with_retry "$1" "$2" "$3" "$4"
fi
if [ -n "$4" ]; then git -C "$1" config credential.helper "$4"; fi`
	return []string{"sh", "-c", cloneRetryFunc + script, "clone", dest, wt.Branch, wt.CloneURL, gitCredentialHelper(wt.CredentialEnv)}
}

func (r *Runtime) buildWorktreeHydration(spec LaunchSpec, env []corev1.EnvVar, secCtx *corev1.SecurityContext) (
	[]corev1.Volume, []corev1.VolumeMount, []corev1.Container,
) {
	if len(spec.Worktrees) == 0 {
		return nil, nil, nil
	}
	loader := r.cfg.LoaderImage
	if loader == "" {
		loader = spec.Image
	}
	var (
		volumes   []corev1.Volume
		mounts    []corev1.VolumeMount
		initConts []corev1.Container
	)
	for _, wt := range spec.Worktrees {
		volName := "wt-" + sanitizeDNS1123(wt.Name)
		mountPath := types.ProjectMountTarget(wt.Name)
		volumes = append(volumes, corev1.Volume{
			Name:         volName,
			VolumeSource: corev1.VolumeSource{EmptyDir: r.worktreeEmptyDir()},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      volName,
			MountPath: mountPath,
			ReadOnly:  wt.ReadOnly,
		})
		initConts = append(initConts, corev1.Container{
			Name:            "clone-" + sanitizeDNS1123(wt.Name),
			Image:           loader,
			ImagePullPolicy: agentPullPolicy,
			Command:         cloneCommand(wt, mountPath),
			Env:             env,
			Resources:       r.resources,
			SecurityContext: secCtx,
			// Read-write even when the agent mounts it read-only: this container writes the clone.
			VolumeMounts: []corev1.VolumeMount{{Name: volName, MountPath: mountPath}},
		})
	}
	return volumes, mounts, initConts
}

func cloneCommand(wt WorktreeMount, mountPath string) []string {
	const script = `set -e
clone_with_retry "$1" "$2" "$3" "$4"
if [ -n "$4" ]; then git -C "$1" config credential.helper "$4"; fi`
	return []string{"sh", "-c", cloneRetryFunc + script, "clone", mountPath, wt.Branch, wt.CloneURL, gitCredentialHelper(wt.CredentialEnv)}
}

func (r *Runtime) WatchExit(ctx context.Context, podName, namespace string, cb func(exitCode int)) {
	go func() {
		ticker := time.NewTicker(r.pollInterval)
		defer ticker.Stop()
		for {
			pod, err := r.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			switch {
			case apierrors.IsNotFound(err):
				cb(ExitDeleted)
				return
			case err == nil:
				if code, done := terminalExitCode(pod); done {
					cb(code)
					return
				}
			default:
				// Transient API error: keep polling.
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func terminalExitCode(pod *corev1.Pod) (int, bool) {
	for i := range pod.Status.ContainerStatuses {
		if term := pod.Status.ContainerStatuses[i].State.Terminated; term != nil {
			return int(term.ExitCode), true
		}
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return 0, true
	case corev1.PodFailed:
		return failedInitExitCode(pod), true
	case corev1.PodPending, corev1.PodRunning, corev1.PodUnknown:
		return 0, false
	}
	return 0, false
}

// The browser sidecar is an init container only to be a native sidecar; its status never decides
// the run.
func failedInitExitCode(pod *corev1.Pod) int {
	for i := range pod.Status.InitContainerStatuses {
		cs := &pod.Status.InitContainerStatuses[i]
		if cs.Name == browserContainerName {
			continue
		}
		if term := cs.State.Terminated; term != nil && term.ExitCode != 0 {
			return int(term.ExitCode)
		}
	}
	return ExitDeleted
}

func (r *Runtime) PodAlive(ctx context.Context, podName, namespace string) bool {
	pod, err := r.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	_, done := terminalExitCode(pod)
	return !done
}

type PodExit struct {
	Container string
	ExitCode  int
	Reason    string
}

func (r *Runtime) PodExited(ctx context.Context, podName, namespace string) (PodExit, bool) {
	pod, err := r.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return PodExit{ExitCode: ExitDeleted, Reason: string(metav1.StatusReasonNotFound)}, true
	}
	if err != nil {
		return PodExit{}, false
	}
	code, done := terminalExitCode(pod)
	if !done {
		return PodExit{}, false
	}
	out := PodExit{ExitCode: code, Reason: pod.Status.Reason}
	if out.Reason == "" {
		out.Reason = string(pod.Status.Phase)
	}
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if term := cs.State.Terminated; term != nil {
			out.Container, out.Reason = cs.Name, term.Reason
			return out, true
		}
	}
	for i := range pod.Status.InitContainerStatuses {
		cs := &pod.Status.InitContainerStatuses[i]
		if term := cs.State.Terminated; term != nil && term.ExitCode != 0 {
			out.Container, out.ExitCode, out.Reason = cs.Name, int(term.ExitCode), term.Reason
			return out, true
		}
	}
	return out, true
}

func (r *Runtime) ListExited(ctx context.Context, namespace string) ([]ExitedPod, error) {
	list, err := r.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: LabelManagedBy + "=" + managedByValue,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.list_pods", "ns", namespace)
	}
	out := make([]ExitedPod, 0, len(list.Items))
	for i := range list.Items {
		pod := &list.Items[i]
		code, done := terminalExitCode(pod)
		if !done {
			continue
		}
		out = append(out, ExitedPod{
			Name:       pod.Name,
			TaskID:     pod.Labels[LabelTaskID],
			InstanceID: pod.Annotations[AnnotationInstanceID],
			AgentType:  pod.Annotations[AnnotationAgentType],
			LaunchKind: pod.Annotations[AnnotationLaunchKind],
			ExitCode:   code,
		})
	}
	return out, nil
}

type RunningPod = kubeapi.RunningPod

func (r *Runtime) ListPods(ctx context.Context, namespace string) ([]RunningPod, error) {
	list, err := r.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: LabelManagedBy + "=" + managedByValue,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.list_pods", "ns", namespace)
	}
	out := make([]RunningPod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, podInfo(&list.Items[i]))
	}
	return out, nil
}

func (r *Runtime) ListPodsAll(ctx context.Context) ([]RunningPod, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	var out []RunningPod
	for _, ns := range namespaces {
		pods, err := r.ListPods(ctx, ns)
		if err != nil {
			return nil, err
		}
		out = append(out, pods...)
	}
	return out, nil
}

func (r *Runtime) FindPod(ctx context.Context, podName string) (RunningPod, bool) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return RunningPod{}, false
	}
	for _, ns := range namespaces {
		pod, gerr := r.client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
		if gerr != nil {
			continue
		}
		if pod.Labels[LabelManagedBy] != managedByValue {
			continue
		}
		return podInfo(pod), true
	}
	return RunningPod{}, false
}

func podInfo(pod *corev1.Pod) RunningPod {
	code, done := terminalExitCode(pod)
	info := RunningPod{
		Name:        pod.Name,
		Namespace:   pod.Namespace,
		TaskID:      pod.Labels[LabelTaskID],
		WorkspaceID: pod.Labels[LabelWorkspace],
		Stage:       pod.Labels[LabelStage],
		InstanceID:  pod.Annotations[AnnotationInstanceID],
		AgentType:   pod.Annotations[AnnotationAgentType],
		Running:     !done,
	}
	if done {
		c := code
		info.ExitCode = &c
	}
	if pod.Status.StartTime != nil {
		info.StartedAt = pod.Status.StartTime.Time
	}
	for i := range pod.Status.ContainerStatuses {
		st := pod.Status.ContainerStatuses[i].State
		if st.Running != nil && !st.Running.StartedAt.IsZero() {
			info.StartedAt = st.Running.StartedAt.Time
		}
		if st.Terminated != nil {
			if !st.Terminated.StartedAt.IsZero() {
				info.StartedAt = st.Terminated.StartedAt.Time
			}
			info.FinishedAt = st.Terminated.FinishedAt.Time
		}
	}
	if info.StartedAt.IsZero() {
		info.StartedAt = pod.CreationTimestamp.Time
	}
	return info
}

func (r *Runtime) ListManagedNamespaces(ctx context.Context) ([]string, error) {
	list, err := r.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: LabelPlane + "=" + planeValue,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.list_tenant_namespaces_all")
	}
	out := make([]string, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, list.Items[i].Name)
	}
	return out, nil
}

func (r *Runtime) ListExitedAll(ctx context.Context) ([]ExitedPod, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	var out []ExitedPod
	for _, ns := range namespaces {
		exited, err := r.ListExited(ctx, ns)
		if err != nil {
			return nil, err
		}
		out = append(out, exited...)
	}
	return out, nil
}

func (r *Runtime) DeletePod(ctx context.Context, podName, namespace string) error {
	err := r.client.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return apperr.Wrap(err, "kuberuntime.errors.delete_pod", "ns", namespace, "pod", podName)
	}
	r.deleteEnvSecret(ctx, namespace, podName)
	return nil
}

func (r *Runtime) PodLogs(ctx context.Context, podName, namespace string, follow bool) (io.ReadCloser, error) {
	return r.PodLogsTail(ctx, podName, namespace, follow, 0)
}

func (r *Runtime) PodLogsTail(ctx context.Context, podName, namespace string, follow bool, tailLines int64) (io.ReadCloser, error) {
	return r.logs(ctx, podName, namespace, &corev1.PodLogOptions{Follow: follow, Timestamps: true}, tailLines)
}

// SinceTime has one-second resolution, so the first lines may repeat; the caller skips them by
// timestamp.
func (r *Runtime) PodLogsSince(ctx context.Context, podName, namespace string, since time.Time) (io.ReadCloser, error) {
	at := metav1.NewTime(since)
	return r.logs(ctx, podName, namespace, &corev1.PodLogOptions{Follow: true, Timestamps: true, SinceTime: &at}, 0)
}

type LogReadiness = kubeapi.LogReadiness

const (
	LogsGone    = kubeapi.LogsGone
	LogsPending = kubeapi.LogsPending
	LogsRunning = kubeapi.LogsRunning
	LogsDone    = kubeapi.LogsDone
)

// For a pod with no node the log API answers 200 with an empty body, which a follower would read as
// the run ending.
func (r *Runtime) PodLogReadiness(ctx context.Context, podName, namespace string) (LogReadiness, error) {
	pod, err := r.client.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return LogsGone, nil
	}
	if err != nil {
		return LogsGone, apperr.Wrap(err, "kuberuntime.errors.pod_logs", "ns", namespace, "pod", podName)
	}
	return podLogReadiness(pod), nil
}

func podLogReadiness(pod *corev1.Pod) LogReadiness {
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return LogsDone
	}
	if pod.Spec.NodeName == "" {
		return LogsPending
	}
	name := agentContainerName
	if len(pod.Spec.Containers) == 1 {
		name = pod.Spec.Containers[0].Name
	}
	for i := range pod.Status.ContainerStatuses {
		cs := &pod.Status.ContainerStatuses[i]
		if cs.Name != name {
			continue
		}
		switch {
		case cs.State.Running != nil:
			return LogsRunning
		case cs.State.Terminated != nil:
			return LogsDone
		}
	}
	return LogsPending
}

func (r *Runtime) ContainerLogsTail(ctx context.Context, podName, namespace, container string, tailLines int64) (io.ReadCloser, error) {
	return r.logs(ctx, podName, namespace, &corev1.PodLogOptions{Container: container}, tailLines)
}

func (r *Runtime) logs(ctx context.Context, podName, namespace string, opts *corev1.PodLogOptions, tailLines int64) (io.ReadCloser, error) {
	if tailLines > 0 {
		opts.TailLines = &tailLines
	}
	req := r.client.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.pod_logs", "ns", namespace, "pod", podName)
	}
	return stream, nil
}

func (r *Runtime) FindPodNamespace(ctx context.Context, podName string) (string, error) {
	namespaces, err := r.ListManagedNamespaces(ctx)
	if err != nil {
		return "", err
	}
	for _, ns := range namespaces {
		if _, gerr := r.client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{}); gerr == nil {
			return ns, nil
		}
	}
	return "", nil
}

func sanitizeDNS1123(s string) string { return kubeapi.SanitizeLabel(s) }
