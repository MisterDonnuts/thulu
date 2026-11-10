package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/sandbox"
	"github.com/MisterDonnuts/thulu/internal/usage"
)

type recordingRuntime struct {
	mu      sync.Mutex
	removed []string
}

func (r *recordingRuntime) Image() string { return "runtime:test" }
func (r *recordingRuntime) Create(context.Context, sandbox.CreateOpts) (*sandbox.CreateResult, error) {
	return nil, errors.New("not used")
}
func (r *recordingRuntime) Exec(context.Context, string, sandbox.ExecOpts) (*sandbox.ExecResult, error) {
	return nil, errors.New("not used")
}
func (r *recordingRuntime) Alive(context.Context, string) bool { return false }
func (r *recordingRuntime) Remove(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, id)
	return nil
}

func (r *recordingRuntime) wasRemoved(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.removed {
		if got == id {
			return true
		}
	}
	return false
}

func withSandboxManager(t *testing.T, srv *Server) *recordingRuntime {
	t.Helper()
	rt := &recordingRuntime{}
	srv.sandbox.mgr = sandbox.NewManager(srv.sandbox.store, srv.settingsStore, rt, sandbox.ManagerConfig{})
	return rt
}

type taskLeftovers struct {
	container, attachment, logFile, outputDir, runKey string
}

func seedTaskLeftovers(t *testing.T, srv *Server, taskID, wsID string) taskLeftovers {
	t.Helper()
	ctx := t.Context()
	lo := taskLeftovers{container: "ctr-" + taskID}
	now := time.Now().UTC()
	if err := srv.sandbox.store.Upsert(ctx, &sandbox.Sandbox{
		TaskID: taskID, WorkspaceID: wsID, ContainerID: lo.container, Image: "runtime:test",
		Status: sandbox.StatusReady, CreatedAt: now, LastActivityAt: now,
	}); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}

	lo.attachment = filepath.Join(srv.taskStore.AttachmentsDir(), taskID, "1-shot.png")
	writeTestFile(t, lo.attachment)

	lo.logFile = filepath.Join(srv.usageStore.DataDir(), "logs", "developer_2026_"+taskID+".log")
	writeTestFile(t, lo.logFile)
	lo.outputDir = filepath.Join(srv.agentsDir, "agents", "developer", "runs", "developer-task-"+taskID)
	writeTestFile(t, filepath.Join(lo.outputDir, "_index.md"))
	if err := srv.usageStore.Add(ctx, usage.Session{
		ID: "run-" + taskID, Agent: "developer", StartedAt: now, FinishedAt: now,
		LogsFile: lo.logFile, OutputDir: lo.outputDir, TaskID: taskID, WorkspaceID: wsID,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	raw, _, err := srv.authStore.CreateRunScopedAPIKey(ctx,
		auth.RunScope{TaskID: taskID, Agent: "developer", Stage: "in_progress"}, wsID, time.Hour)
	if err != nil {
		t.Fatalf("seed run key: %v", err)
	}
	lo.runKey = raw
	return lo
}

func (lo taskLeftovers) assertGone(t *testing.T, srv *Server, rt *recordingRuntime, taskID string) {
	t.Helper()
	if !rt.wasRemoved(lo.container) {
		t.Errorf("sandbox container %s was not removed", lo.container)
	}
	if _, err := srv.sandbox.store.Get(t.Context(), taskID); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("sandbox row survived: %v", err)
	}
	for what, path := range map[string]string{
		"attachment dir": filepath.Dir(lo.attachment),
		"run log":        lo.logFile,
		"run output dir": lo.outputDir,
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived (%s, stat err = %v)", what, path, err)
		}
	}
	if srv.usageStore.Get(t.Context(), "run-"+taskID) != nil {
		t.Error("session row survived")
	}
	if srv.authStore.LookupAPIKeyInfo(t.Context(), lo.runKey) != nil {
		t.Error("run key still authenticates")
	}
	assertCount(t, srv.authStore.DB(),
		`SELECT COUNT(*) FROM api_keys WHERE scope_task_id = '`+taskID+`'`, 0, "run key rows")
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteTask_TearsDownEverything(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	rt := withSandboxManager(t, srv)
	token, wsID := registerUserInWorkspace(t, h, "teardown@test.com", "TD", "")
	doomed := createTaskViaAPI(t, h, token, "doomed")
	kept := createTaskViaAPI(t, h, token, "kept")
	lo := seedTaskLeftovers(t, srv, doomed, wsID)
	keptLo := seedTaskLeftovers(t, srv, kept, wsID)

	if w := sendJSON(t, h, http.MethodDelete, "/tasks/"+doomed, token, nil); w.Code != http.StatusOK {
		t.Fatalf("DELETE /tasks/%s = %d: %s", doomed, w.Code, w.Body.String())
	}
	lo.assertGone(t, srv, rt, doomed)

	if rt.wasRemoved(keptLo.container) {
		t.Error("the sibling's sandbox container was removed")
	}
	if _, err := srv.sandbox.store.Get(t.Context(), kept); err != nil {
		t.Errorf("the sibling's sandbox row went: %v", err)
	}
	for _, path := range []string{keptLo.attachment, keptLo.logFile, keptLo.outputDir} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("the sibling's %s went: %v", path, err)
		}
	}
	if srv.authStore.LookupAPIKeyInfo(t.Context(), keptLo.runKey) == nil {
		t.Error("the sibling's run key went")
	}
}

func TestDeleteSubtask_KeepsParentSandbox(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	rt := withSandboxManager(t, srv)
	token, wsID := registerUserInWorkspace(t, h, "teardown-sub@test.com", "TDS", "")
	parentID := createTaskViaAPI(t, h, token, "parent")
	sub := createSubtaskViaAPI(t, h, token, parentID, map[string]any{"title": "child"})
	subID, _ := sub["id"].(string)
	parentLo := seedTaskLeftovers(t, srv, parentID, wsID)

	if w := sendJSON(t, h, http.MethodDelete, "/tasks/"+subID, token, nil); w.Code != http.StatusOK {
		t.Fatalf("DELETE subtask = %d: %s", w.Code, w.Body.String())
	}
	if rt.wasRemoved(parentLo.container) {
		t.Error("deleting a subtask removed the parent's sandbox container")
	}
	if _, err := srv.sandbox.store.Get(t.Context(), parentID); err != nil {
		t.Errorf("deleting a subtask removed the parent's sandbox row: %v", err)
	}
}

func TestRemoveRunFiles_RefusesPathsOutsideTheirRoots(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	outside := filepath.Join(t.TempDir(), "victim.log")
	writeTestFile(t, outside)
	notRuns := filepath.Join(srv.agentsDir, "agents", "developer", "prompts")
	writeTestFile(t, filepath.Join(notRuns, "main.md"))

	srv.removeRunFiles([]usage.RunFiles{
		{LogsFile: outside, OutputDir: notRuns},
		{LogsFile: filepath.Join(srv.usageStore.DataDir(), "logs", "..", "..", "victim.log")},
	})
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("a log path outside <data>/logs was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(notRuns, "main.md")); err != nil {
		t.Errorf("a non-runs dir under the agents dir was removed: %v", err)
	}
}

func TestDeleteTask_RemovesRunOutputDirs(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token, _ := registerUserInWorkspace(t, h, "teardown-runs@test.com", "TDR", "")
	doomed := createTaskViaAPI(t, h, token, "doomed")
	kept := createTaskViaAPI(t, h, token, "kept")

	dirOf := func(inst string) string {
		t.Helper()
		dir := srv.runOutputDirOf(inst)
		if dir == "" {
			t.Fatalf("no run output dir for %q", inst)
		}
		writeTestFile(t, filepath.Join(dir, "_index.md"))
		return dir
	}
	gone := []string{
		dirOf("developer-task-" + doomed + "-in_progress-1727500000000"),
		dirOf("qa-mention-" + doomed + "-c0ffee-1727500000001"),
	}
	stay := []string{
		dirOf("developer-task-" + kept + "-in_progress-1727500000002"),
		dirOf("developer-chat-" + doomed),
	}

	if w := sendJSON(t, h, http.MethodDelete, "/tasks/"+doomed, token, nil); w.Code != http.StatusOK {
		t.Fatalf("DELETE /tasks/%s = %d: %s", doomed, w.Code, w.Body.String())
	}
	for _, dir := range gone {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("run output dir survived the task: %s (stat err = %v)", dir, err)
		}
	}
	for _, dir := range stay {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("run output dir of something else went: %s: %v", dir, err)
		}
	}
}

func TestRemoveRunFiles_RunsDir(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	run := srv.runOutputDirOf("developer-task-abc-in_progress-1")
	writeTestFile(t, filepath.Join(run, "_index.md"))
	nested := filepath.Join(srv.runsDir, "other", "deeper")
	writeTestFile(t, filepath.Join(nested, "x"))

	srv.removeRunFiles([]usage.RunFiles{{OutputDir: run}, {OutputDir: nested}, {OutputDir: srv.runsDir}})

	if _, err := os.Stat(run); !os.IsNotExist(err) {
		t.Errorf("the run's output dir survived (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(nested, "x")); err != nil {
		t.Errorf("a nested dir under the runs dir was removed: %v", err)
	}
	if _, err := os.Stat(srv.runsDir); err != nil {
		t.Errorf("the runs dir itself was removed: %v", err)
	}
}

func TestPurgeWorkspace_TearsDownTasksAndTenant(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	rt := withSandboxManager(t, srv)
	es, _ := entitlements.NewStore(newEntDB(t), entitlements.PlanFree)
	srv.SetEntitlements(es)
	seedWorkspaceData(t, srv, "WSP", "purged", "owner-p")
	seedWorkspaceData(t, srv, "WSK", "survivor", "owner-k")
	lo := seedTaskLeftovers(t, srv, "task-purged", "WSP")
	keptLo := seedTaskLeftovers(t, srv, "task-survivor", "WSK")

	now := time.Now().UTC()
	if err := srv.sandbox.store.Upsert(t.Context(), &sandbox.Sandbox{
		TaskID: "auto:run-1", WorkspaceID: "WSP", ContainerID: "ctr-auto", Image: "runtime:test",
		Status: sandbox.StatusReady, CreatedAt: now, LastActivityAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	opKey, _, err := srv.authStore.CreateAPIKey(t.Context(), "ci", "owner-p",
		auth.KeyGrant{WorkspaceID: "WSP", Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermAll}})
	if err != nil {
		t.Fatal(err)
	}

	var tornDown []string
	srv.SetTenantTeardown(func(_ context.Context, wsID string) error {
		tornDown = append(tornDown, wsID)
		return nil
	})

	if err := srv.purgeWorkspace(t.Context(), "WSP"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	lo.assertGone(t, srv, rt, "task-purged")
	if !rt.wasRemoved("ctr-auto") {
		t.Error("the automation sandbox container was not removed")
	}
	assertCount(t, srv.sandbox.store.DB(), `SELECT COUNT(*) FROM task_sandboxes WHERE workspace_id='WSP'`, 0, "WSP sandbox rows")
	assertCount(t, srv.usageStore.DB(), `SELECT COUNT(*) FROM usage_daily WHERE workspace_id='WSP'`, 0, "WSP usage rollup")
	if srv.authStore.LookupAPIKeyInfo(t.Context(), opKey) != nil {
		t.Error("an operator key of the erased workspace still authenticates")
	}
	if len(tornDown) != 1 || tornDown[0] != "WSP" {
		t.Errorf("tenant teardown calls = %v, want [WSP]", tornDown)
	}

	if rt.wasRemoved(keptLo.container) {
		t.Error("the other workspace's sandbox was removed")
	}
	if srv.usageStore.Get(t.Context(), "run-task-survivor") == nil {
		t.Error("the other workspace's session went")
	}
	assertCount(t, srv.usageStore.DB(), `SELECT COUNT(*) FROM usage_daily WHERE workspace_id='WSK'`, 1, "WSK usage rollup")
	if srv.authStore.LookupAPIKeyInfo(t.Context(), keptLo.runKey) == nil {
		t.Error("the other workspace's run key went")
	}
}

func TestPurgeWorkspace_TenantTeardownFailureKeepsWorkspace(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	es, _ := entitlements.NewStore(newEntDB(t), entitlements.PlanFree)
	srv.SetEntitlements(es)
	seedWorkspaceData(t, srv, "WSF", "flaky", "owner-f")
	srv.SetTenantTeardown(func(context.Context, string) error { return errors.New("namespace stuck") })

	if err := srv.purgeWorkspace(t.Context(), "WSF"); err == nil {
		t.Fatal("purge succeeded with a failing tenant teardown")
	}
	assertCount(t, srv.authStore.DB(), `SELECT COUNT(*) FROM workspaces WHERE id='WSF'`, 1, "workspace kept for retry")
}
