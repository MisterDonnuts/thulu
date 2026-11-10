//go:build !foss

package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/apperr"
)

func quotaFull(t *testing.T) error {
	t.Helper()
	err := kuberuntime.QuotaRefusal(apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "sandbox-x",
		errors.New("exceeded quota: octopus-tenant-quota, requested: requests.cpu=500m, used: requests.cpu=6, limited: requests.cpu=6")))
	if !kuberuntime.IsQuotaFull(err) {
		t.Fatalf("fixture is not a quota refusal: %v", err)
	}
	return err
}

func TestExecWaitsForRoom(t *testing.T) {
	m, _, _, rt := newFakeManager(t)
	m.roomWait, m.roomRetry = 5*time.Second, 10*time.Millisecond
	rt.refuse = []error{quotaFull(t), quotaFull(t)}

	res, ar, err := m.Exec(context.Background(), "task-room", "in_progress", AcquireDockerOpts{}, ExecOpts{Command: "true"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if ar != nil && ar.Outcome == AcquireQueued {
		t.Fatal("a quota wait must not answer queued: nobody would relaunch the run")
	}
	if res == nil {
		t.Fatal("the command must run once room frees")
	}
}

func TestExecGivesUpWithTheQuotaReason(t *testing.T) {
	m, _, _, rt := newFakeManager(t)
	m.roomWait, m.roomRetry = 30*time.Millisecond, 10*time.Millisecond
	for range 20 {
		rt.refuse = append(rt.refuse, quotaFull(t))
	}

	_, _, err := m.Exec(context.Background(), "task-full", "in_progress", AcquireDockerOpts{}, ExecOpts{Command: "true"})
	if err == nil {
		t.Fatal("Exec must fail when the quota never has room")
	}
	if text := apperr.Text(err); !strings.Contains(text, "6 of 6 CPU reserved") {
		t.Errorf("the error must name what is short: %s", text)
	}
	rt.mu.Lock()
	rt.refuse = nil
	rt.mu.Unlock()
	if _, _, err := m.Exec(context.Background(), "task-next", "in_progress", AcquireDockerOpts{}, ExecOpts{Command: "true"}); err != nil {
		t.Fatalf("the slot must be free for the next task: %v", err)
	}
}
