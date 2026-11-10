//go:build !foss

package sandbox

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/internal/sandbox/execagent"
)

func refusingClient(addr string, refuse int32) (*http.Client, *atomic.Int32) {
	var dials atomic.Int32
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		if dials.Add(1) <= refuse {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	return &http.Client{Transport: tr}, &dials
}

func execAgentStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(execagent.ExecResult{Stdout: "ok"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setPodCreated(t *testing.T, client *fake.Clientset, ns, name string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	pod, err := client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	pod.CreationTimestamp = metav1.NewTime(at)
	if _, err := client.CoreV1().Pods(ns).Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update pod: %v", err)
	}
}

func TestKubeExecWaitsForAStartingPod(t *testing.T) {
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img"})
	ctx := context.Background()
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	setPodCreated(t, client, "tenant-x", res.ContainerID, time.Now())
	httpc, dials := refusingClient(execAgentStub(t).Listener.Addr().String(), 2)
	k.httpc = httpc

	out, err := k.Exec(ctx, res.ContainerID, ExecOpts{Command: "true"})
	if err != nil {
		t.Fatalf("Exec on a starting pod: %v", err)
	}
	if out.Stdout != "ok" || dials.Load() != 3 {
		t.Errorf("stdout=%q dials=%d, want ok after 3 dials", out.Stdout, dials.Load())
	}
}

func TestKubeExecDoesNotWaitForAnOldPod(t *testing.T) {
	client := fake.NewClientset()
	k := NewKube(client, KubeConfig{Image: "img"})
	ctx := context.Background()
	res, err := k.Create(ctx, CreateOpts{TaskID: "t1", WorkspaceID: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	setPodCreated(t, client, "tenant-x", res.ContainerID, time.Now().Add(-2*execStartGrace))
	httpc, dials := refusingClient(execAgentStub(t).Listener.Addr().String(), 100)
	k.httpc = httpc

	started := time.Now()
	if _, err := k.Exec(ctx, res.ContainerID, ExecOpts{Command: "true"}); err == nil {
		t.Fatal("Exec on an old pod that refuses: want an error")
	}
	if dials.Load() != 1 || time.Since(started) > time.Second {
		t.Errorf("dials=%d after %s, want a single attempt", dials.Load(), time.Since(started))
	}
}
