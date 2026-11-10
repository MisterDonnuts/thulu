package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/errdefs"

	"github.com/MisterDonnuts/thulu/internal/agent"
	"github.com/MisterDonnuts/thulu/internal/infra"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/task"
)

type orphanDocker struct {
	mu   sync.Mutex
	list []types.Container
}

func (f *orphanDocker) Info(context.Context) (system.Info, error) { return system.Info{}, nil }
func (f *orphanDocker) ContainerList(context.Context, container.ListOptions) ([]types.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]types.Container(nil), f.list...), nil
}
func (f *orphanDocker) ContainerStatsOneShot(context.Context, string) (container.StatsResponseReader, error) {
	return container.StatsResponseReader{}, errors.New("no stats")
}
func (f *orphanDocker) DiskUsage(context.Context, types.DiskUsageOptions) (types.DiskUsage, error) {
	return types.DiskUsage{}, nil
}

func agentCtr(id, taskID, instance string, created time.Time) types.Container {
	return types.Container{
		ID: id, Names: []string{"/" + instance}, State: "running", Created: created.Unix(),
		Labels: map[string]string{
			agent.LabelManaged: "true", agent.LabelType: "developer", agent.LabelTask: taskID,
			agent.LabelInstance: instance, agent.LabelStage: "in_progress",
		},
	}
}

func TestOrphanAgents(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	ctx := t.Context()
	id := createWSTask(t, h, "Has a stray container")
	old := time.Now().Add(-10 * time.Minute)
	known := "developer-task-" + id + "-in_progress-1"
	if err := srv.taskStore.RecordEvent(ctx, id, task.EventAgentRunStarted, known, "agent:developer", "", nil); err != nil {
		t.Fatal(err)
	}
	current := "developer-task-" + id + "-in_progress-5"
	if _, err := srv.taskStore.Update(ctx, id, task.Patch{CurrentInstance: &current}); err != nil {
		t.Fatal(err)
	}
	fd := &orphanDocker{list: []types.Container{
		agentCtr("c-known", id, known, old),
		agentCtr("c-current", id, current, old),
		agentCtr("c-stray", id, "developer-task-"+id+"-in_progress-2", old),
		agentCtr("c-fresh", id, "developer-task-"+id+"-in_progress-3", time.Now()),
		agentCtr("c-gone", "deadbeefdeadbeef", "developer-task-deadbeefdeadbeef-in_progress-4", old),
		{ID: "c-sbx", Names: []string{"/octopus-sandbox-" + id}, State: "running", Created: old.Unix(),
			Labels: map[string]string{sandbox.SandboxLabel: "true", sandbox.SandboxTaskLabel: id}},
	}}
	c := infra.New(infra.Config{ProcRoot: filepath.Join(t.TempDir(), "none"), Docker: fd, Interval: time.Hour})
	srv.SetInfra(c)
	runCtx, cancel := context.WithCancel(ctx)
	go c.Run(runCtx)
	waitFor(t, 5*time.Second, func() bool { return len(c.Containers()) == 6 })
	cancel()

	orphans := map[string]bool{}
	for _, ct := range srv.markOrphans(ctx, c.Containers()) {
		orphans[ct.ID] = ct.Orphan
	}
	want := map[string]bool{"c-known": false, "c-current": false, "c-stray": true, "c-fresh": false, "c-gone": true, "c-sbx": false}
	for cid, w := range want {
		if orphans[cid] != w {
			t.Errorf("%s: orphan = %v, want %v", cid, orphans[cid], w)
		}
	}

	get := func() []task.OrphanAgent {
		w := getJSON(t, h, "/tasks/"+id, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET task = %d: %s", w.Code, w.Body.String())
		}
		var got task.Task
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.OrphanAgents
	}
	if got := get(); len(got) != 1 || got[0].ContainerID != "c-stray" || got[0].Agent != "developer" {
		t.Fatalf("drawer orphans = %+v, want only c-stray", got)
	}

	var removed []string
	srv.removeContainerHook = func(_ context.Context, ct *infra.Container) error {
		removed = append(removed, ct.ID)
		return nil
	}
	if w := requestWithHeaders(t, h, http.MethodDelete, "/tasks/"+id+"/orphan-agents/c-known", nil, nil); w.Code != http.StatusConflict {
		t.Errorf("remove a container a run accounts for = %d, want 409: %s", w.Code, w.Body.String())
	}
	if w := requestWithHeaders(t, h, http.MethodDelete, "/tasks/"+id+"/orphan-agents/c-current", nil, nil); w.Code != http.StatusConflict {
		t.Errorf("remove the current run's container = %d, want 409: %s", w.Code, w.Body.String())
	}
	if w := requestWithHeaders(t, h, http.MethodDelete, "/tasks/"+id+"/orphan-agents/c-gone", nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("remove another task's container = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := requestWithHeaders(t, h, http.MethodDelete, "/tasks/"+id+"/orphan-agents/c-stray", nil, nil); w.Code != http.StatusNoContent {
		t.Fatalf("remove the orphan = %d: %s", w.Code, w.Body.String())
	}
	if len(removed) != 1 || removed[0] != "c-stray" {
		t.Errorf("removed %v, want [c-stray]", removed)
	}
	if got := get(); len(got) != 0 {
		t.Errorf("after the removal the drawer still lists %+v", got)
	}
}

type removerFake struct {
	removeErr  error
	inspectErr error
	state      *types.ContainerState
}

func (f removerFake) ContainerRemove(context.Context, string, container.RemoveOptions) error {
	return f.removeErr
}

func (f removerFake) ContainerInspect(context.Context, string) (types.ContainerJSON, error) {
	if f.inspectErr != nil {
		return types.ContainerJSON{}, f.inspectErr
	}
	return types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{State: f.state}}, nil
}

func TestRemoveDockerContainer_GoneCountsAsRemoved(t *testing.T) {
	ctx := context.Background()
	failed := errors.New("tried to kill container, but did not receive an exit event")
	notFound := errdefs.NotFound(errors.New("no such container"))
	cases := []struct {
		name string
		f    removerFake
		ok   bool
	}{
		{"removed", removerFake{}, true},
		{"already gone", removerFake{removeErr: notFound}, true},
		{"gone by the time it is checked", removerFake{removeErr: failed, inspectErr: notFound}, true},
		{"still being removed", removerFake{removeErr: failed, state: &types.ContainerState{Status: "removing"}}, true},
		{"dead", removerFake{removeErr: failed, state: &types.ContainerState{Dead: true}}, true},
		{"still running", removerFake{removeErr: failed, state: &types.ContainerState{Status: "running", Running: true}}, false},
		{"cannot even be inspected", removerFake{removeErr: failed, inspectErr: errors.New("daemon down")}, false},
	}
	for _, c := range cases {
		err := removeDockerContainer(ctx, c.f, "c1")
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestRemovalContext_OutlivesTheRequest(t *testing.T) {
	req, cancelReq := context.WithCancel(context.Background())
	ctx, cancel := removalContext(req)
	defer cancel()
	cancelReq()
	if err := ctx.Err(); err != nil {
		t.Fatalf("removal context ended with its request: %v", err)
	}
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > time.Minute {
		t.Errorf("deadline = %v, %v; want one within a minute", dl, ok)
	}
}

func TestInfraStop_RemovesAnOrphanPastTheRequest(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	id := createWSTask(t, h, "Has a stray container on Infra")
	fd := &orphanDocker{list: []types.Container{
		agentCtr("c-stray", id, "developer-task-"+id+"-in_progress-1", time.Now().Add(-10*time.Minute)),
	}}
	c := infra.New(infra.Config{ProcRoot: filepath.Join(t.TempDir(), "none"), Docker: fd, Interval: time.Hour})
	srv.SetInfra(c)
	runCtx, stop := context.WithCancel(t.Context())
	go c.Run(runCtx)
	waitFor(t, 5*time.Second, func() bool { return len(c.Containers()) == 1 })
	stop()

	reqCtx, cancelReq := context.WithCancel(t.Context())
	defer cancelReq()
	var removed []string
	var removalErr error
	srv.removeContainerHook = func(ctx context.Context, ct *infra.Container) error {
		cancelReq()
		removalErr = ctx.Err()
		removed = append(removed, ct.ID)
		return nil
	}
	r := httptest.NewRequestWithContext(reqCtx, http.MethodPost, "/infra/containers/c-stray/stop", nil)
	r.SetPathValue("id", "c-stray")
	w := httptest.NewRecorder()
	srv.handleInfraStopContainer(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("stop the orphan from Infra = %d: %s", w.Code, w.Body.String())
	}
	if len(removed) != 1 || removed[0] != "c-stray" {
		t.Fatalf("removed %v, want [c-stray]", removed)
	}
	if removalErr != nil {
		t.Errorf("the removal's context ended with its request: %v", removalErr)
	}
	if len(c.Containers()) != 0 {
		t.Errorf("Infra still lists %+v after the removal", c.Containers())
	}
}
