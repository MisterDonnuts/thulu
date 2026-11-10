// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/MisterDonnuts/thulu/internal/browserrun"
)

// The NetworkPolicy controller (kube-router on k3s) programs a new pod's IP asynchronously; until
// then the daemon's port refuses the pod for up to a few seconds.
// wait-network must run before every other init container and sidecar so no pod's first request
// hits that window.
const waitNetworkContainerName = "wait-network"

// The dispatcher maps this exit code to a NetworkPolicy run-failure hint.
const ExitNetworkClosed = 94

// Far beyond the few seconds kube-router takes; reaching it means the policy is wrong, not slow.
const waitNetworkSeconds = 60

// curl exit 6 (DNS), 7 (refused), or 28 with no connect time mean no TCP connection; any other
// result, HTTP or TLS errors included, means the daemon is reachable.
// An image without curl skips the wait rather than block the pod.
//
//i18n:ignore shell script; its lines go to the container log, which stays in English
const waitNetworkScript = `limit=$1
url="${OCTOPUS_API%/}/health/live"
if ! command -v curl >/dev/null 2>&1; then
  echo "wait-network: no curl in this image, not waiting for the network" >&2
  exit 0
fi
start=$(date +%s)
n=0
while :; do
  tc=$(curl -s -o /dev/null -w '%{time_connect}' --connect-timeout 1 --max-time 3 "$url")
  rc=$?
  case "$rc:$tc" in
    6:*|7:*|28:|28:0|28:0.000000) ;;
    *)
      if [ "$n" -gt 0 ]; then echo "wait-network: reached the daemon after $n failed attempts"; fi
      exit 0 ;;
  esac
  n=$((n+1))
  if [ $(( $(date +%s) - start )) -ge "$limit" ]; then
    echo "wait-network: the pod's network never opened toward the daemon (NetworkPolicy)" >&2
    echo "wait-network: $url unreachable for ${limit}s, $n attempts, last curl exit $rc" >&2
    exit "$2"
  fi
  sleep 0.5
done
`

func (r *Runtime) withNetworkWait(
	spec LaunchSpec, secretName string, secCtx *corev1.SecurityContext, inits []corev1.Container,
) []corev1.Container {
	if spec.Env[browserrun.DaemonURLEnv] == "" {
		return inits
	}
	wait := corev1.Container{
		Name:            waitNetworkContainerName,
		Image:           r.loaderImage(spec),
		ImagePullPolicy: agentPullPolicy,
		Command: []string{
			"sh", "-c", waitNetworkScript, waitNetworkContainerName,
			strconv.Itoa(waitNetworkSeconds), strconv.Itoa(ExitNetworkClosed),
		},
		Env: []corev1.EnvVar{{
			Name: browserrun.DaemonURLEnv,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  browserrun.DaemonURLEnv,
			}},
		}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("16Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		SecurityContext: loaderSecurityContext(secCtx),
	}
	return append([]corev1.Container{wait}, inits...)
}
