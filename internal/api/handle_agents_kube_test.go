//go:build !foss

package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/MisterDonnuts/thulu/ee/kuberuntime"
	"github.com/MisterDonnuts/thulu/internal/registry"
)

func TestListAgentsReadsPodsInKubeMode(t *testing.T) {
	ctx := context.Background()
	rt, err := kuberuntime.NewWithClient(fake.NewClientset(), kuberuntime.Config{Image: "octopus/agent:test"})
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	instance := "developer-task-task1234-in-progress-99"
	podName, err := rt.LaunchPod(ctx, kuberuntime.LaunchSpec{
		WorkspaceID: "acme",
		TaskID:      "task1234",
		Stage:       "in_progress",
		InstanceID:  instance,
		Annotations: map[string]string{
			kuberuntime.AnnotationInstanceID: instance,
			kuberuntime.AnnotationAgentType:  "developer",
		},
	})
	if err != nil {
		t.Fatalf("LaunchPod: %v", err)
	}

	s := &Server{kube: rt, registry: registry.New()}
	w := httptest.NewRecorder()
	s.handleListAgents(w, asDevOperator(httptest.NewRequest("GET", "/agents", nil)))

	if w.Code != 200 {
		t.Fatalf("GET /agents = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var got []agentView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d agents, want the 1 running pod: %+v", len(got), got)
	}
	a := got[0]
	if a.ID != podName || a.Status != "running" || a.Type != "developer" || a.Instance != instance {
		t.Errorf("agent = %+v, want running developer pod %s instance %s", a, podName, instance)
	}
	if a.ExitCode != nil {
		t.Errorf("running pod must have no exit code, got %d", *a.ExitCode)
	}
}

func TestStopAndRemoveAgentDeletePod(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    func(*Server, *httptest.ResponseRecorder, string)
		wantMsg string
	}{
		{"stop", func(s *Server, w *httptest.ResponseRecorder, id string) {
			r := asDevOperator(httptest.NewRequest("POST", "/agents/"+id+"/stop", nil))
			r.SetPathValue("id", id)
			s.handleStopAgent(w, r)
		}, "stopped"},
		{"remove", func(s *Server, w *httptest.ResponseRecorder, id string) {
			r := asDevOperator(httptest.NewRequest("DELETE", "/agents/"+id, nil))
			r.SetPathValue("id", id)
			s.handleRemoveAgent(w, r)
		}, "removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rt, err := kuberuntime.NewWithClient(fake.NewClientset(), kuberuntime.Config{Image: "octopus/agent:test"})
			if err != nil {
				t.Fatalf("NewWithClient: %v", err)
			}
			if err := rt.EnsureTenantNamespace(ctx, "acme"); err != nil {
				t.Fatal(err)
			}
			podName, err := rt.LaunchPod(ctx, kuberuntime.LaunchSpec{
				WorkspaceID: "acme", TaskID: "task1234", Stage: "in_progress",
				InstanceID: "developer-task-task1234-in-progress-99",
			})
			if err != nil {
				t.Fatalf("LaunchPod: %v", err)
			}

			s := &Server{kube: rt, registry: registry.New()}
			w := httptest.NewRecorder()
			tc.call(s, w, podName)
			if w.Code != 200 {
				t.Fatalf("%s = %d, want 200 (%s)", tc.name, w.Code, w.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["status"] != tc.wantMsg {
				t.Errorf("status = %q, want %q", body["status"], tc.wantMsg)
			}
			if _, ok := rt.FindPod(ctx, podName); ok {
				t.Errorf("pod %s still exists after %s", podName, tc.name)
			}
		})
	}
}

func TestGetAgentUnknownPodIs404(t *testing.T) {
	rt, err := kuberuntime.NewWithClient(fake.NewClientset(), kuberuntime.Config{Image: "octopus/agent:test"})
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	s := &Server{kube: rt, registry: registry.New()}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/agents/ghost", nil)
	r.SetPathValue("id", "ghost")
	s.handleGetAgent(w, r)
	if w.Code != 404 {
		t.Fatalf("GET /agents/ghost = %d, want 404 (%s)", w.Code, w.Body.String())
	}
}
