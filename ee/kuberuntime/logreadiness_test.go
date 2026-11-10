// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodLogReadiness(t *testing.T) {
	agent := func(state corev1.ContainerState) []corev1.ContainerStatus {
		return []corev1.ContainerStatus{
			{Name: browserContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			{Name: agentContainerName, State: state},
		}
	}
	cases := []struct {
		name string
		pod  corev1.Pod
		want LogReadiness
	}{
		{
			name: "not scheduled yet",
			pod:  corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}},
			want: LogsPending,
		},
		{
			name: "in its init containers",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{NodeName: "octopus-b"},
				Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: agent(corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"},
				})},
			},
			want: LogsPending,
		},
		{
			name: "scheduled with no status yet",
			pod:  corev1.Pod{Spec: corev1.PodSpec{NodeName: "octopus-b"}},
			want: LogsPending,
		},
		{
			name: "agent running",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{NodeName: "octopus-b"},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: agent(corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{},
				})},
			},
			want: LogsRunning,
		},
		{
			name: "sidecar running, agent waiting",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{NodeName: "octopus-b"},
				Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: agent(corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
				})},
			},
			want: LogsPending,
		},
		{
			name: "agent exited, pod still running its sidecar",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{NodeName: "octopus-b"},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: agent(corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: 0},
				})},
			},
			want: LogsDone,
		},
		{
			name: "pod failed before the agent started",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{NodeName: "octopus-b"},
				Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: agent(corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"},
				})},
			},
			want: LogsDone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, client := newFakeRuntime(t, Config{})
			tc.pod.Name, tc.pod.Namespace = "run-1", "tenant-acme"
			if len(tc.pod.Spec.Containers) == 0 {
				tc.pod.Spec.Containers = []corev1.Container{{Name: agentContainerName}}
			}
			if _, err := client.CoreV1().Pods("tenant-acme").Create(context.Background(), &tc.pod, metav1.CreateOptions{}); err != nil {
				t.Fatalf("create pod: %v", err)
			}
			got, err := rt.PodLogReadiness(context.Background(), "run-1", "tenant-acme")
			if err != nil {
				t.Fatalf("PodLogReadiness: %v", err)
			}
			if got != tc.want {
				t.Errorf("readiness = %d, want %d", got, tc.want)
			}
		})
	}

	t.Run("no such pod", func(t *testing.T) {
		rt, _ := newFakeRuntime(t, Config{})
		got, err := rt.PodLogReadiness(context.Background(), "gone", "tenant-acme")
		if err != nil || got != LogsGone {
			t.Errorf("readiness = %d, %v; want LogsGone, nil", got, err)
		}
	})
}
