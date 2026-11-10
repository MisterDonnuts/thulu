// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"log"
	"regexp"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/catrust"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

// Pod specs are readable with `get pods` and stored in etcd as-is, so env values go in a per-pod
// Secret via secretKeyRef, never as literals.

var envSecretKey = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

func EnvSecretName(podName string) string { return podName + "-env" }

func SecretEnv(secretName string, env map[string]string) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(env))
	for k := range env {
		out = append(out, corev1.EnvVar{
			Name: k,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  k,
			}},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func EnvSecret(podName string, labels, env map[string]string) (*corev1.Secret, error) {
	data := make(map[string][]byte, len(env))
	for k, v := range env {
		if !envSecretKey.MatchString(k) {
			return nil, apperr.Invalid("kuberuntime.errors.env_var_name_invalid", "name", k)
		}
		data[k] = []byte(v)
	}
	sl := make(map[string]string, len(labels))
	for k, v := range labels {
		sl[k] = v
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: EnvSecretName(podName), Labels: sl},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}, nil
}

func (r *Runtime) putEnvSecret(ctx context.Context, ns, podName string, labels, env map[string]string) (*corev1.Secret, error) {
	want, err := EnvSecret(podName, labels, env)
	if err != nil {
		return nil, err
	}
	return PutSecret(ctx, r.client.CoreV1().Secrets(ns), want)
}

type secretsClient interface {
	Create(ctx context.Context, s *corev1.Secret, opts metav1.CreateOptions) (*corev1.Secret, error)
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error)
	Update(ctx context.Context, s *corev1.Secret, opts metav1.UpdateOptions) (*corev1.Secret, error)
}

func PutSecret(ctx context.Context, cl secretsClient, want *corev1.Secret) (*corev1.Secret, error) {
	created, err := cl.Create(ctx, want, metav1.CreateOptions{})
	if err == nil {
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, apperr.Wrap(err, "kuberuntime.errors.create_secret", "name", want.Name)
	}
	existing, err := cl.Get(ctx, want.Name, metav1.GetOptions{})
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.get_secret", "name", want.Name)
	}
	existing.Data = want.Data
	existing.StringData = nil
	existing.OwnerReferences = nil // Drop the stale owner, or the GC deletes this Secret along with the old pod.
	updated, err := cl.Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return nil, apperr.Wrap(err, "kuberuntime.errors.update_secret", "name", want.Name)
	}
	return updated, nil
}

func (r *Runtime) ownSecretByPod(ctx context.Context, ns string, secret *corev1.Secret, pod *corev1.Pod) {
	OwnSecretByPod(ctx, r.client.CoreV1().Secrets(ns), secret, pod)
}

func OwnSecretByPod(ctx context.Context, cl secretsClient, secret *corev1.Secret, pod *corev1.Pod) {
	if secret == nil || pod == nil || pod.UID == "" {
		return
	}
	//i18n:ignore Kubernetes object kind
	secret.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID}}
	if _, err := cl.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.set_secret_owner_failed",
			"ns", pod.Namespace, "secret", secret.Name, "pod", pod.Name, "err", err))
	}
}

func (r *Runtime) deleteEnvSecret(ctx context.Context, ns, podName string) {
	err := r.client.CoreV1().Secrets(ns).Delete(ctx, EnvSecretName(podName), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		log.Printf("[kuberuntime] %s", i18n.Log("kuberuntime.log.delete_env_secret_failed", "ns", ns, "secret", EnvSecretName(podName), "err", err))
	}
}

// The name is spliced into a shell snippet; anything outside this pattern is a shell injection.
var envVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// The helper reads the password from env when git asks, so the secret never lands in a URL, a pod
// spec or .git/config.
func gitCredentialHelper(envName string) string {
	if !envVarName.MatchString(envName) {
		return ""
	}
	//i18n:ignore credential helper shell snippet
	return `!f() { test "$1" = get || exit 0; echo username=octopus; echo "password=${` + envName + `}"; }; f`
}

const caVolumeName = "octopus-ca"

func caVolume() corev1.Volume {
	return corev1.Volume{
		Name: caVolumeName,
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: catrust.ConfigMapName},
			Items:                []corev1.KeyToPath{{Key: catrust.ConfigMapCAKey, Path: "ca.crt"}},
		}},
	}
}

func caVolumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: caVolumeName, MountPath: catrust.ContainerDir, ReadOnly: true}
}
