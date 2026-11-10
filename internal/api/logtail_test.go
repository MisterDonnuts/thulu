//go:build !foss

package api

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
)

type fakePod struct {
	streams []string
	states  []kuberuntime.LogReadiness
	sinces  []time.Time
}

func (f *fakePod) tail() *podTail {
	next := func() io.ReadCloser {
		s := f.streams[0]
		f.streams = f.streams[1:]
		return io.NopCloser(strings.NewReader(s))
	}
	return &podTail{
		reader: next(),
		state: func(context.Context) (kuberuntime.LogReadiness, error) {
			s := f.states[0]
			f.states = f.states[1:]
			return s, nil
		},
		reopen: func(_ context.Context, since time.Time) (io.ReadCloser, error) {
			f.sinces = append(f.sinces, since)
			return next(), nil
		},
	}
}

func fastReopen(t *testing.T) {
	t.Helper()
	lo, hi := podReopenMin, podReopenMax
	podReopenMin, podReopenMax = time.Millisecond, time.Millisecond
	t.Cleanup(func() { podReopenMin, podReopenMax = lo, hi })
}

func collect(ctx context.Context, tail *podTail) []string {
	var got []string
	followPod(ctx, tail, func(line string) bool {
		got = append(got, line)
		return true
	})
	return got
}

func TestFollowPod_ReopensWhileTheAgentRuns(t *testing.T) {
	fastReopen(t)
	pod := &fakePod{
		streams: []string{
			"2026-10-19T17:19:30.100000000Z one\n2026-10-19T17:19:31.200000000Z two\n",
			"2026-10-19T17:19:31.200000000Z two\n2026-10-19T17:19:32.000000001Z three\n",
		},
		states: []kuberuntime.LogReadiness{kuberuntime.LogsRunning, kuberuntime.LogsDone},
	}
	got := collect(context.Background(), pod.tail())
	want := []string{
		"2026-10-19T17:19:30.100000000Z one",
		"2026-10-19T17:19:31.200000000Z two",
		"2026-10-19T17:19:32.000000001Z three",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if len(pod.sinces) != 1 || !pod.sinces[0].Equal(time.Date(2026, 10, 19, 17, 19, 31, 200000000, time.UTC)) {
		t.Errorf("reopened since %v, want the last line's timestamp", pod.sinces)
	}
}

func TestFollowPod_DropsALineCutByTheBreak(t *testing.T) {
	fastReopen(t)
	pod := &fakePod{
		streams: []string{
			"2026-10-19T17:19:30.1Z one\n2026-10-19T17:19:31.2Z {\"type\":\"assis",
			"2026-10-19T17:19:30.1Z one\n2026-10-19T17:19:31.2Z {\"type\":\"assistant\"}\n",
		},
		states: []kuberuntime.LogReadiness{kuberuntime.LogsRunning, kuberuntime.LogsGone},
	}
	got := collect(context.Background(), pod.tail())
	want := []string{"2026-10-19T17:19:30.1Z one", "2026-10-19T17:19:31.2Z {\"type\":\"assistant\"}"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func TestFollowPod_EndsWithTheRun(t *testing.T) {
	fastReopen(t)
	pod := &fakePod{
		streams: []string{"2026-10-19T17:19:30.1Z one\n2026-10-19T17:19:31.2Z bye"},
		states:  []kuberuntime.LogReadiness{kuberuntime.LogsDone},
	}
	got := collect(context.Background(), pod.tail())
	want := []string{"2026-10-19T17:19:30.1Z one", "2026-10-19T17:19:31.2Z bye"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
	if len(pod.sinces) != 0 {
		t.Errorf("reopened %d times after the run ended", len(pod.sinces))
	}
}

func TestFollowPod_EmptyStreamWhileRunningReopensFromTheStart(t *testing.T) {
	fastReopen(t)
	pod := &fakePod{
		streams: []string{"", "2026-10-19T17:19:30.1Z one\n"},
		states:  []kuberuntime.LogReadiness{kuberuntime.LogsRunning, kuberuntime.LogsDone},
	}
	got := collect(context.Background(), pod.tail())
	if !slices.Equal(got, []string{"2026-10-19T17:19:30.1Z one"}) {
		t.Fatalf("lines = %q", got)
	}
	if len(pod.sinces) != 1 || !pod.sinces[0].Equal(time.Unix(0, 0)) {
		t.Errorf("reopened since %v, want the epoch", pod.sinces)
	}
}

func TestFollowPod_StopsWhenTheViewerLeaves(t *testing.T) {
	fastReopen(t)
	ctx, cancel := context.WithCancel(context.Background())
	pod := &fakePod{
		streams: []string{"2026-10-19T17:19:30.1Z one\n2026-10-19T17:19:31.2Z two\n"},
	}
	var got []string
	followPod(ctx, pod.tail(), func(line string) bool {
		got = append(got, line)
		cancel()
		return false
	})
	if len(got) != 1 {
		t.Fatalf("lines = %q, want just the first", got)
	}
}

func TestResolvePodLogs_WaitsForTheAgentToStart(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{Image: "octopus/agent:test"})
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	instance := "developer-task-task1234-in_progress-99"
	podName, err := rt.LaunchPod(ctx, kuberuntime.LaunchSpec{
		WorkspaceID: "acme", TaskID: "task1234", Stage: "in_progress", InstanceID: instance,
	})
	if err != nil {
		t.Fatalf("LaunchPod: %v", err)
	}
	s := &Server{kube: rt}

	if _, err := s.resolvePodLogs(ctx, podName); !errors.Is(err, errLogsPending) {
		t.Fatalf("unscheduled pod: err = %v, want errLogsPending", err)
	}

	ns := rt.NamespaceFor("acme")
	pod, err := client.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Spec.NodeName = "octopus-b"
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: pod.Spec.Containers[0].Name, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	if _, err := client.CoreV1().Pods(ns).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	src, err := s.resolvePodLogs(ctx, podName)
	if err != nil || src.pod == nil {
		t.Fatalf("running pod: src = %+v, err = %v; want a pod tail", src, err)
	}
	src.pod.reader.Close()

	if _, err := s.resolvePodLogs(ctx, "no-such-run"); !errors.Is(err, errNoLogs) || errors.Is(err, errLogsPending) {
		t.Errorf("missing pod: err = %v, want errNoLogs and not errLogsPending", err)
	}
}
