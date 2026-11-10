//go:build !foss

package chatruntime

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/chat"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestKubeChatClonesTheTasksCode(t *testing.T) {
	b := newKubeBackend(nil, "http://octopus.ns.svc:8585/git/")
	code := map[string]types.CodeView{
		"web": {Path: "/data/projects/web--task-t1", Branch: "task-t1"},
		"api": {Path: "/data/projects/api"},
	}
	keyed := map[string]string{runKeyEnv: "k"}

	got := b.codeMounts(&chat.Chat{ID: "chat-1", TaskID: "t1"}, code, keyed)
	want := []kuberuntime.WorktreeMount{
		{Name: "api", CloneURL: "http://octopus.ns.svc:8585/git/t1/api", ReadOnly: true, CredentialEnv: runKeyEnv},
		{Name: "web", CloneURL: "http://octopus.ns.svc:8585/git/t1/web", Branch: "task-t1", ReadOnly: true, CredentialEnv: runKeyEnv},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mounts = %+v\nwant %+v", got, want)
	}

	cases := map[string]struct {
		b   *kubeBackend
		c   *chat.Chat
		env map[string]string
	}{
		"no task":       {b, &chat.Chat{ID: "chat-1"}, keyed},
		"no run key":    {b, &chat.Chat{ID: "chat-1", TaskID: "t1"}, map[string]string{}},
		"no git remote": {newKubeBackend(nil, ""), &chat.Chat{ID: "chat-1", TaskID: "t1"}, keyed},
	}
	for name, c := range cases {
		if got := c.b.codeMounts(c.c, code, c.env); got != nil {
			t.Errorf("%s: mounts = %+v, want none", name, got)
		}
	}
}

func TestKubeChatWaitHealthyStopsOnAnExitedPod(t *testing.T) {
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ns := "tenant-acme"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "chat-1", Namespace: ns},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "agent",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 91, Reason: "Error"}},
		}}},
	}
	if _, err := client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	b := newKubeBackend(rt, "")

	start := time.Now()
	err = b.waitHealthy(ctx, "chat-1", ns)
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("waitHealthy took %s on a pod that had already exited", took)
	}
	if apperr.KindOf(err) != apperr.KindUnavailable {
		t.Fatalf("err = %v, want an Unavailable error", err)
	}
	// The fake clientset answers every log request with "fake logs".
	for _, want := range []string{"chat-1", "agent", "91", "Error", "fake logs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err, want)
		}
	}

	if err := b.waitHealthy(ctx, "chat-gone", ns); err == nil || !strings.Contains(err.Error(), "NotFound") {
		t.Errorf("vanished pod: err = %v, want NotFound", err)
	}
}
