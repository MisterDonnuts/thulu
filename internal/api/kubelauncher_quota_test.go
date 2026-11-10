//go:build !foss

package api

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func TestKubeLauncher_QuotaFullIsNoCapacity(t *testing.T) {
	for _, c := range []struct {
		name    string
		refusal error
		queue   bool
	}{
		{"quota full", apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "agent-x",
			errors.New("exceeded quota: octopus-tenant-quota, requested: requests.cpu=1, used: requests.cpu=6, limited: requests.cpu=6")), true},
		{"pod alone too big", apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "agent-x",
			errors.New("exceeded quota: octopus-tenant-quota, requested: requests.cpu=8, used: requests.cpu=0, limited: requests.cpu=6")), false},
		{"policy", apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "agent-x",
			errors.New("violates PodSecurity")), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			agentsDir := seedContractAgent(t)
			client := fake.NewClientset()
			client.PrependReactor("create", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, c.refusal
			})
			rt, err := kuberuntime.NewWithClient(client, kuberuntime.Config{Image: "ghcr.io/example/octopus-agent:latest"})
			if err != nil {
				t.Fatal(err)
			}
			l := newKubeLauncher(context.Background(), rt, t.TempDir(), "", types.AllWorkspaces(types.NewCatalog(agentsDir)))
			drv := &kubeContractDriver{rt: rt, client: client, agentsDir: agentsDir}
			tk := &task.Task{ID: "task-q", Stage: types.StageInProgress, WorkspaceID: "ws-contract"}
			_, _, _, err = l.LaunchForStage(context.Background(), tk, types.StageInProgress, drv.AgentType(),
				map[string]string{"INSTANCE_ID": "dev-task-q-in_progress-1"}, nil, nil, false)
			if err == nil {
				t.Fatal("the launch must fail")
			}
			if errors.Is(err, task.ErrNoCapacity) != c.queue {
				t.Errorf("ErrNoCapacity = %v, want %v: %v", errors.Is(err, task.ErrNoCapacity), c.queue, err)
			}
		})
	}
}
