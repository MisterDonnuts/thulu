// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	imageBuildComponent = "agent-image-build"
	// Not thulu.dev/managed-by: the sweeps that select on it treat those pods as agent runs.
	labelAppManagedBy = "app.kubernetes.io/managed-by"
	labelComponent    = "app.kubernetes.io/component"
	// buildkit's rootless image runs as this user and keeps its state under its home.
	builderUID = int64(1000)
)

// The name depends only on the image it pushes, so a restarted daemon finds the Job its predecessor
// started instead of starting a second one.
func imageBuildJobName(target string) string {
	sum := sha256.Sum256([]byte(target))
	return "agent-image-" + hex.EncodeToString(sum[:])[:12]
}

// Installs Claude Code where the first-run shim looks before it downloads anything
// (/opt/thulu-engines-image). HOME points away from /home/node so root leaves no files there.
func imageBuildDockerfile(from string) string {
	return strings.Join([]string{
		"FROM " + from,
		"USER 0:0",
		//i18n:ignore Dockerfile instruction
		"RUN HOME=/tmp/claude-build OCTOPUS_ENGINE_DIR=/opt/thulu-engines-image claude --version \\",
		"  && rm -rf /tmp/claude-build /opt/thulu-engines-image/.claude-code-*.lock",
		"LABEL " + LabelClaudeCodeInstall + "=image-build",
		"USER 1000:1000",
		"",
	}, "\n")
}

func (b *imageBuilder) buildArgs(target string) []string {
	//i18n:ignore buildctl output option
	out := "type=image,name=" + target + ",push=true"
	if b.cfg.Insecure {
		out += ",registry.insecure=true"
	}
	return []string{
		"build", "--frontend", "dockerfile.v0",
		"--local", "context=/ctx", "--local", "dockerfile=/ctx",
		"--output", out,
	}
}

func (b *imageBuilder) buildkitdFlags() string {
	if b.cfg.Insecure {
		return "--oci-worker-no-process-sandbox --config /ctx/buildkitd.toml"
	}
	return "--oci-worker-no-process-sandbox"
}

// A base the bundled registry holds (an air-gapped install that imported the published image there)
// is pulled over the same plain HTTP the push uses.
func (b *imageBuilder) buildkitdConfig() string {
	host, _, _ := strings.Cut(b.cfg.PushRegistry, "/")
	return fmt.Sprintf("[registry.%q]\n  http = true\n  insecure = true\n", host)
}

func (b *imageBuilder) jobSpec(s ImageBuildStatus) *batchv1.Job {
	labels := map[string]string{labelAppManagedBy: managedByValue, labelComponent: imageBuildComponent}
	backoff, deadline, ttl := int32(1), int64(imageBuildTimeout/time.Second), int32(24*60*60)
	nonRoot, noToken := true, false
	uid := builderUID
	mounts := []corev1.VolumeMount{
		{Name: "context", MountPath: "/ctx", ReadOnly: true},
		{Name: "state", MountPath: "/home/user/.local/share/buildkit"},
	}
	volumes := []corev1.Volume{
		{Name: "context", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: s.Job},
		}}},
		{Name: "state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	if b.cfg.AuthSecret != "" {
		mounts = append(mounts, corev1.VolumeMount{Name: "registry-auth", MountPath: "/home/user/.docker", ReadOnly: true})
		volumes = append(volumes, corev1.Volume{Name: "registry-auth", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: b.cfg.AuthSecret,
				Items:      []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}},
			},
		}})
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: s.Job, Namespace: b.cfg.Namespace, Labels: labels,
			Annotations: map[string]string{"thulu.dev/base": s.Base, "thulu.dev/target": s.Target},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &deadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					// Pre-1.30 nodes read only the annotation; appArmorProfile below is the field.
					Annotations: map[string]string{"container.apparmor.security.beta.kubernetes.io/buildkit": "unconfined"},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &noToken,
					EnableServiceLinks:           &noToken,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser: &uid, RunAsGroup: &uid, FSGroup: &uid, RunAsNonRoot: &nonRoot,
						// Rootless BuildKit makes its own user namespace and mounts, which the default
						// seccomp and AppArmor profiles refuse. That is why this runs in its own
						// privileged-PSA namespace, away from tenant pods.
						SeccompProfile:  &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
						AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined},
					},
					Containers: []corev1.Container{{
						Name:    "buildkit",
						Image:   b.cfg.BuilderImage,
						Command: []string{"buildctl-daemonless.sh"},
						Args:    b.buildArgs(s.Target),
						Env:     []corev1.EnvVar{{Name: "BUILDKITD_FLAGS", Value: b.buildkitdFlags()}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi"),
							},
						},
						VolumeMounts:             mounts,
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
					}},
					Volumes: volumes,
				},
			},
		},
	}
}

// build runs the Job for s and waits for it. A Job left over from an earlier daemon is reused while
// it runs and replaced once it has finished, since the registry check already said the image is
// missing.
func (b *imageBuilder) build(ctx context.Context, s ImageBuildStatus) error {
	ctx, cancel := context.WithTimeout(ctx, imageBuildTimeout+5*time.Minute)
	defer cancel()
	job, err := b.rt.client.BatchV1().Jobs(b.cfg.Namespace).Get(ctx, s.Job, metav1.GetOptions{})
	running := err == nil && jobFinished(job) == ""
	switch {
	case apierrors.IsNotFound(err), running:
	case err != nil:
		return apperr.Wrap(err, "kuberuntime.image_build.job_create", "job", s.Job)
	default:
		if derr := b.deleteJob(ctx, s.Job); derr != nil {
			return derr
		}
	}
	if !running {
		if cerr := b.createJob(ctx, s); cerr != nil {
			return cerr
		}
	}
	return b.wait(ctx, s)
}

func (b *imageBuilder) createJob(ctx context.Context, s ImageBuildStatus) error {
	// A ConfigMap left by an earlier Job of this name would be collected with that Job, under the
	// new pod's feet.
	cms := b.rt.client.CoreV1().ConfigMaps(b.cfg.Namespace)
	if err := cms.Delete(ctx, s.Job, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return apperr.Wrap(err, "kuberuntime.image_build.job_create", "job", s.Job)
	}
	job, err := b.rt.client.BatchV1().Jobs(b.cfg.Namespace).Create(ctx, b.jobSpec(s), metav1.CreateOptions{})
	if err != nil {
		return apperr.Wrap(err, "kuberuntime.image_build.job_create", "job", s.Job)
	}
	// Owned by the Job, so the Job's TTL removes it too. The pod waits for it to appear.
	ctrl := true
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: s.Job, Namespace: b.cfg.Namespace,
			Labels: map[string]string{labelAppManagedBy: managedByValue, labelComponent: imageBuildComponent},
			OwnerReferences: []metav1.OwnerReference{{
				//i18n:ignore Kubernetes object kind
				APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &ctrl,
			}},
		},
		//i18n:ignore build context file name
		Data: map[string]string{"Dockerfile": imageBuildDockerfile(s.from)},
	}
	if b.cfg.Insecure {
		cm.Data["buildkitd.toml"] = b.buildkitdConfig()
	}
	if _, err = cms.Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return apperr.Wrap(err, "kuberuntime.image_build.job_create", "job", s.Job)
	}
	return nil
}

func (b *imageBuilder) deleteJob(ctx context.Context, name string) error {
	bg := metav1.DeletePropagationBackground
	err := b.rt.client.BatchV1().Jobs(b.cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg})
	if err != nil && !apierrors.IsNotFound(err) {
		return apperr.Wrap(err, "kuberuntime.image_build.job_create", "job", name)
	}
	for {
		if _, err = b.rt.client.BatchV1().Jobs(b.cfg.Namespace).Get(ctx, name, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return apperr.Wrap(ctx.Err(), "kuberuntime.image_build.job_create", "job", name)
		case <-time.After(b.poll):
		}
	}
}

// Complete, Failed, or "" while the Job still runs.
func jobFinished(job *batchv1.Job) batchv1.JobConditionType {
	for _, c := range job.Status.Conditions {
		if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
			return c.Type
		}
	}
	return ""
}

func (b *imageBuilder) wait(ctx context.Context, s ImageBuildStatus) error {
	for {
		job, err := b.rt.client.BatchV1().Jobs(b.cfg.Namespace).Get(ctx, s.Job, metav1.GetOptions{})
		if err != nil {
			return apperr.Wrap(err, "kuberuntime.image_build.job_wait", "job", s.Job)
		}
		if done := jobFinished(job); done == batchv1.JobComplete {
			return nil
		} else if done == batchv1.JobFailed {
			return apperr.Unprocessable("kuberuntime.image_build.job_failed", "job", fmt.Sprintf("%s/%s", b.cfg.Namespace, s.Job),
				"detail", b.failureDetail(ctx, s.Job))
		}
		select {
		case <-ctx.Done():
			return apperr.Wrap(ctx.Err(), "kuberuntime.image_build.job_wait", "job", s.Job)
		case <-time.After(b.poll):
		}
	}
}

// The last lines the build printed (FallbackToLogsOnError puts them in the termination message).
func (b *imageBuilder) failureDetail(ctx context.Context, job string) string {
	pods, err := b.rt.client.CoreV1().Pods(b.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + job})
	if err != nil {
		return ""
	}
	for i := len(pods.Items) - 1; i >= 0; i-- {
		statuses := pods.Items[i].Status.ContainerStatuses
		for j := range statuses {
			if t := statuses[j].State.Terminated; t != nil && t.Message != "" {
				msg := strings.TrimSpace(t.Message)
				if lines := strings.Split(msg, "\n"); len(lines) > 5 {
					msg = strings.Join(lines[len(lines)-5:], "\n")
				}
				return msg
			}
		}
	}
	return ""
}
