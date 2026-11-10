//go:build !foss

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/registry"
)

func asWorkspace(r *http.Request, wid string) *http.Request {
	return r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{
		UserID: "u-" + wid, Email: "admin@" + wid + ".test",
		WorkspaceID: wid, Role: auth.RoleAdmin,
	}))
}

func twoTenantPods(t *testing.T) (*kuberuntime.Runtime, map[string]string) {
	t.Helper()
	ctx := context.Background()
	rt, err := kuberuntime.NewWithClient(fake.NewClientset(), kuberuntime.Config{Image: "octopus/agent:test"})
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	names := map[string]string{}
	for _, wid := range []string{"acme", "globex"} {
		if err := rt.EnsureTenantNamespace(ctx, wid); err != nil {
			t.Fatal(err)
		}
		name, err := rt.LaunchPod(ctx, kuberuntime.LaunchSpec{
			WorkspaceID: wid, TaskID: "task-" + wid, Stage: "in_progress",
			InstanceID: "developer-task-" + wid + "-in-progress-1",
		})
		if err != nil {
			t.Fatalf("LaunchPod %s: %v", wid, err)
		}
		names[wid] = name
	}
	return rt, names
}

func TestListAgentsIsPerWorkspaceInKubeMode(t *testing.T) {
	rt, names := twoTenantPods(t)
	s := &Server{kube: rt, registry: registry.New()}

	w := httptest.NewRecorder()
	s.handleListAgents(w, asWorkspace(httptest.NewRequest("GET", "/agents", nil), "acme"))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents = %d: %s", w.Code, w.Body.String())
	}
	var got []agentView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(got) != 1 || got[0].ID != names["acme"] {
		t.Fatalf("acme sees %+v, want only its own pod %s", got, names["acme"])
	}

	all := httptest.NewRecorder()
	s.handleListAgents(all, asDevOperator(httptest.NewRequest("GET", "/agents", nil)))
	var every []agentView
	if err := json.Unmarshal(all.Body.Bytes(), &every); err != nil {
		t.Fatalf("decode: %v (%s)", err, all.Body.String())
	}
	if len(every) != 2 {
		t.Fatalf("unrestricted caller sees %d pods, want 2", len(every))
	}
}

func TestAgentSubresourcesRefuseAnotherTenantsPod(t *testing.T) {
	rt, names := twoTenantPods(t)
	s := &Server{kube: rt, registry: registry.New()}
	victim := names["globex"]

	for _, tc := range []struct {
		name string
		call func(*Server, http.ResponseWriter, *http.Request)
	}{
		{"get", (*Server).handleGetAgent},
		{"logs", (*Server).handleLogs},
		{"stop", (*Server).handleStopAgent},
		{"remove", (*Server).handleRemoveAgent},
	} {
		r := asWorkspace(httptest.NewRequest("GET", "/agents/"+victim, nil), "acme")
		r.SetPathValue("id", victim)
		w := httptest.NewRecorder()
		tc.call(s, w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s on another tenant's pod = %d, want 404: %s", tc.name, w.Code, w.Body.String())
		}
	}

	if _, ok := rt.FindPod(context.Background(), victim); !ok {
		t.Fatal("a cross-tenant stop/remove deleted the pod")
	}
}

func TestListAgentsIsPerWorkspaceInDockerMode(t *testing.T) {
	reg := registry.New()
	reg.Upsert(&agent.Agent{
		ID: "aaa", Name: "acme-run", WorkspaceID: "acme", TaskID: "task-1",
		Status: agent.StatusRunning, StartedAt: time.Now(), OutputDir: "/data/runs/aaa",
	})
	reg.Upsert(&agent.Agent{
		ID: "bbb", Name: "globex-run", WorkspaceID: "globex", TaskID: "task-2",
		Status: agent.StatusRunning, StartedAt: time.Now(), OutputDir: "/data/runs/bbb",
	})
	reg.Upsert(&agent.Agent{
		ID: "ccc", Name: "unattributed-run",
		Status: agent.StatusExited, StartedAt: time.Now(), OutputDir: "/data/runs/ccc",
	})
	s := &Server{registry: reg}

	list := func(r *http.Request) []agentView {
		t.Helper()
		w := httptest.NewRecorder()
		s.handleListAgents(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /agents = %d: %s", w.Code, w.Body.String())
		}
		var got []agentView
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v (%s)", err, w.Body.String())
		}
		return got
	}

	mine := list(asWorkspace(httptest.NewRequest("GET", "/agents", nil), "acme"))
	if len(mine) != 1 || mine[0].ID != "aaa" {
		t.Fatalf("acme sees %+v, want only its own run", mine)
	}
	if mine[0].OutputDir != "" {
		t.Errorf("output_dir is a daemon-host path and went to a tenant: %q", mine[0].OutputDir)
	}

	every := list(asDevOperator(httptest.NewRequest("GET", "/agents", nil)))
	if len(every) != 3 {
		t.Fatalf("unrestricted caller sees %d runs, want 3", len(every))
	}

	for _, id := range []string{"bbb", "ccc"} {
		r := asWorkspace(httptest.NewRequest("GET", "/agents/"+id+"/files", nil), "acme")
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		s.handleListFiles(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("files of run %s = %d, want 404: %s", id, w.Code, w.Body.String())
		}
	}
}
