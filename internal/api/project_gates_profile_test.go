package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/audit"
	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

// A person sets a project's gate profile; a bad one is refused with the reason; an agent run's key
// never changes it, since the profile decides what that run's own gates accept.
func TestPatchProjectGatesProfile(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	srv, h := newIntegrationServer(t)
	token, ws := mintWorkspaceUser(t, srv, "gates-profile@test.com", "Gates")
	proj, err := srv.projectStore.Create(t.Context(), "app", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: ws})
	if err != nil {
		t.Fatal(err)
	}
	path := "/projects/" + proj.ID

	profile := map[string]any{
		"timeouts":          map[string]any{"tests": 2400},
		"accept_unverified": map[string]any{"tests": "Karma disconnects on base (ABC-123)"},
	}
	w := patchJSONAuth(t, h, path, token, map[string]any{"gates_profile": profile})
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH profile = %d: %s", w.Code, w.Body.String())
	}
	var got project.Project
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if string(got.GatesProfile) != `{"accept_unverified":{"tests":"Karma disconnects on base (ABC-123)"},"timeouts":{"tests":2400}}` {
		t.Errorf("profile = %s", got.GatesProfile)
	}

	entries, err := srv.auditStore.List(audit.ListOptions{Kind: audit.KindProjectGatesProfileSet, ResourceID: proj.ID})
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit entries = %d, %v; want 1", len(entries), err)
	}
	if entries[0].Metadata["gates_profile"] != string(got.GatesProfile) || entries[0].Metadata["previous"] != "" {
		t.Errorf("audit metadata = %v", entries[0].Metadata)
	}
	// Saving the same profile again changes nothing and logs nothing.
	if w = patchJSONAuth(t, h, path, token, map[string]any{"gates_profile": profile}); w.Code != http.StatusOK {
		t.Fatalf("PATCH same profile = %d", w.Code)
	}
	if entries, _ = srv.auditStore.List(audit.ListOptions{Kind: audit.KindProjectGatesProfileSet, ResourceID: proj.ID}); len(entries) != 1 {
		t.Errorf("audit entries after an unchanged save = %d, want 1", len(entries))
	}

	w = patchJSONAuth(t, h, path, token, map[string]any{"gates_profile": map[string]any{"accept_unverified": map[string]any{"tests": ""}}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("a profile with no reason = %d, want 400: %s", w.Code, w.Body.String())
	}

	taskID := taskIDFor(t, h, token, "gated")
	for _, scope := range []auth.RunScope{
		{TaskID: taskID, Agent: "thulu-verifier", Stage: "verification"},
		{ChatID: "c1", Agent: "admin", Stage: "chat", DelegatedBy: "gates-profile@test.com"},
	} {
		raw, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(), scope, ws, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		w = keyDo(t, h, http.MethodPatch, path, raw, map[string]any{"gates_profile": map[string]any{"coverage_min": 0}})
		if w.Code != http.StatusForbidden {
			t.Errorf("run key %+v changing the profile = %d, want 403: %s", scope, w.Code, w.Body.String())
		}
	}
	after, err := srv.projectStore.Get(t.Context(), proj.ID)
	if err != nil || string(after.GatesProfile) != string(got.GatesProfile) {
		t.Errorf("profile after the refused calls = %s, %v", after.GatesProfile, err)
	}
}

// A job on a runner's machine carries each project's profile, which the runner writes for
// thulu-gates.
func TestRunnerJobProjectsCarryGatesProfile(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	prof := json.RawMessage(`{"accept_unverified":{"tests":"Karma hangs on base"}}`)
	if _, err := f.projects.Update(t.Context(), f.project.ID, project.Patch{GatesProfile: &prof}); err != nil {
		t.Fatal(err)
	}
	got, err := f.launcher.runnerFor(t.Context(), &task.Task{ID: "t1", ProjectIDs: []string{f.project.ID}}, f.reg.RunnerID, false)
	if err != nil || len(got) != 1 {
		t.Fatalf("runnerFor = %+v, %v", got, err)
	}
	if string(got[0].GatesProfile) != string(prof) {
		t.Errorf("job project profile = %s, want %s", got[0].GatesProfile, prof)
	}
}

// The sandbox of a task, and of its subtasks, gets the profiles of the owner's projects under the
// directory names it mounts them by.
func TestSandboxGatesProfileFollowsTheSandboxOwner(t *testing.T) {
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	srv, _ := newIntegrationServer(t)
	ctx := t.Context()
	withProfile, err := srv.projectStore.Create(ctx, "app", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: "ws-test"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := srv.projectStore.Create(ctx, "api", testgit.BareRepo(t), project.CreateOpts{WorkspaceID: "ws-test"})
	if err != nil {
		t.Fatal(err)
	}
	prof := json.RawMessage(`{"accept_unverified":{"tests":"Karma hangs on base"}}`)
	if _, err := srv.projectStore.Update(ctx, withProfile.ID, project.Patch{GatesProfile: &prof}); err != nil {
		t.Fatal(err)
	}
	parent := &task.Task{WorkspaceID: "ws-test", Title: "parent", ProjectIDs: []string{withProfile.ID, plain.ID}}
	if err := srv.taskStore.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}
	sub := &task.Task{WorkspaceID: "ws-test", Title: "child", ParentID: parent.ID}
	if err := srv.taskStore.Create(ctx, sub); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, sub.ID} {
		var doc struct {
			Projects map[string]json.RawMessage `json:"projects"`
		}
		body := srv.sandboxGatesProfile(ctx, id)
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: profile %s: %v", id, body, err)
		}
		name := filepath.Base(withProfile.Path)
		if len(doc.Projects) != 1 || string(doc.Projects[name]) != string(prof) {
			t.Errorf("%s: profile = %s, want only %s under %q", id, body, prof, name)
		}
	}

	bare := &task.Task{WorkspaceID: "ws-test", Title: "no profile", ProjectIDs: []string{plain.ID}}
	if err := srv.taskStore.Create(ctx, bare); err != nil {
		t.Fatal(err)
	}
	if body := srv.sandboxGatesProfile(ctx, bare.ID); body != nil {
		t.Errorf("a task whose projects have no profile got %s", body)
	}
}
