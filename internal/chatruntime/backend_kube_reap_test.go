//go:build !foss

package chatruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/sandbox/execagent"
)

// execAgentRecorder stands in for the chat pod's exec agent: it records each request and answers
// with an empty, successful result.
type execAgentRecorder struct {
	mu   sync.Mutex
	urls []string
	reqs []execagent.ExecRequest
}

func (e *execAgentRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	var req execagent.ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.urls = append(e.urls, r.URL.String())
	e.reqs = append(e.reqs, req)
	e.mu.Unlock()
	body, _ := json.Marshal(execagent.ExecResult{})
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

func (e *execAgentRecorder) snapshot() []execagent.ExecRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]execagent.ExecRequest(nil), e.reqs...)
}

func kubeChatRig(t *testing.T) (*kubeBackend, *fake.Clientset, *execAgentRecorder) {
	t.Helper()
	client := fake.NewClientset()
	ctx := context.Background()
	ns := "tenant-acme"
	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: ns, Labels: map[string]string{kuberuntime.LabelPlane: "data"},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "chat-1", Namespace: ns},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &execAgentRecorder{}
	b := newKubeBackend(rt, "")
	b.httpc = &http.Client{Transport: rec, Timeout: 5 * time.Second}
	return b, client, rec
}

// The exec agent kills a turn's group only on a timeout or a cancel. A turn that ends normally
// leaves its background processes in the chat pod, which lives on while the chat is in use: each
// turn carries its own marker, and a reap of that marker follows it.
func TestKubeChatTurnReapsWhatItLeft(t *testing.T) {
	b, _, rec := kubeChatRig(t)
	b.tokens["chat-1"] = "tok"

	if _, err := b.Exec(context.Background(), ExecSpec{ContainerID: "chat-1", Env: []string{"SESSION_ID=s"}, Stdin: "hi"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.snapshot()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	reqs := rec.snapshot()
	if len(reqs) != 2 {
		t.Fatalf("want the turn and its reap, got %d requests: %+v", len(reqs), reqs)
	}
	turn, reap := reqs[0], reqs[1]
	marker := turn.Env[envChatTurn]
	if marker == "" || turn.Env["SESSION_ID"] != "s" {
		t.Fatalf("the turn's env lost the session or the marker: %v", turn.Env)
	}
	if reap.Command[0] != "bash" || reap.Env["OCTOPUS_REAP_MODE"] != "run" ||
		reap.Env["OCTOPUS_REAP_VAR"] != envChatTurn || reap.Env["OCTOPUS_REAP_VALUE"] != marker || !reap.InheritEnv {
		t.Fatalf("the reap does not target this turn's marker %q: %+v", marker, reap)
	}
}

// After a restart the daemon holds no exec token for a chat pod, so it can neither drive nor reap
// it: the boot sweep removes the pod, the way its next turn would.
func TestKubeChatSweepRemovesAPodItCannotDrive(t *testing.T) {
	b, client, rec := kubeChatRig(t)

	if b.SweepStaleTurns(context.Background(), "chat-1") {
		t.Fatal("a pod without an exec token was kept")
	}
	if _, err := client.CoreV1().Pods("tenant-acme").Get(context.Background(), "chat-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the pod is still there: %v", err)
	}
	if n := len(rec.snapshot()); n != 0 {
		t.Fatalf("%d exec requests went to a pod without a token", n)
	}
}

func TestKubeChatSweepReapsEveryTurnOfAPodItDrives(t *testing.T) {
	b, _, rec := kubeChatRig(t)
	b.tokens["chat-1"] = "tok"

	if !b.SweepStaleTurns(context.Background(), "chat-1") {
		t.Fatal("a pod the daemon drives was removed")
	}
	reqs := rec.snapshot()
	if len(reqs) != 1 || reqs[0].Env["OCTOPUS_REAP_MODE"] != "any" || reqs[0].Env["OCTOPUS_REAP_VAR"] != envChatTurn {
		t.Fatalf("want one sweep of every turn's processes, got %+v", reqs)
	}
}
