// Copyright (c) 2026 Mister.D LLC. Licensed under the Thulu Enterprise License, see ee/LICENSE.
// SPDX-License-Identifier: LicenseRef-Thulu-Enterprise

package kuberuntime

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	return &buf
}

func TestCheckOwnPodSelector(t *testing.T) {
	rt, client := newFakeRuntime(t, Config{Isolation: TenantIsolation{ControlPlaneNamespace: "octopus"}})
	for name, labels := range map[string]map[string]string{
		"daemon-ok":  {"app.kubernetes.io/name": "octopus", "app.kubernetes.io/instance": "prod"},
		"daemon-bad": {"app": "octopus"},
	} {
		if _, err := client.CoreV1().Pods("octopus").Create(context.Background(), &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "octopus", Labels: labels},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	buf := captureLog(t)
	rt.CheckOwnPodSelector(context.Background(), "daemon-ok")
	rt.CheckOwnPodSelector(context.Background(), "not-a-pod")
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning: %s", buf)
	}
	rt.CheckOwnPodSelector(context.Background(), "daemon-bad")
	if !strings.Contains(buf.String(), "app.kubernetes.io/name=octopus") || !strings.Contains(buf.String(), EnvControlPlanePodSelector) {
		t.Fatalf("mismatched daemon pod not reported: %q", buf)
	}
}
