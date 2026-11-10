package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/task"

	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func newProjectWithTask(t *testing.T, h http.Handler, name string) (projectID, taskID string) {
	t.Helper()
	w := postJSON(t, h, "/projects?workspace=ws-test", map[string]any{"name": name, "repo_url": testgit.BareRepo(t)})
	if w.Code != http.StatusCreated {
		t.Fatalf("create project = %d, body %s", w.Code, w.Body.String())
	}
	var proj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	w = postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{"title": "work on " + name, "project_ids": []string{proj.ID}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d, body %s", w.Code, w.Body.String())
	}
	var tk struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tk); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	return proj.ID, tk.ID
}

func TestDeleteProject_UnlinksItsTasks(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	projectID, taskID := newProjectWithTask(t, h, "doomed")

	if w := deleteRequest(t, h, "/projects/"+projectID, ""); w.Code != http.StatusOK {
		t.Fatalf("DELETE project = %d, body %s", w.Code, w.Body.String())
	}

	got, err := srv.taskStore.Get(t.Context(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	for _, pid := range got.ProjectIDs {
		if pid == projectID {
			t.Fatalf("task still links to the deleted project: %v", got.ProjectIDs)
		}
	}
	if len(got.ProjectIDs) != 0 {
		t.Errorf("project ids = %v, want empty", got.ProjectIDs)
	}
}

func TestDeleteProject_KeepsTheOtherLinks(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	doomed, _ := newProjectWithTask(t, h, "doomed")
	keeper, _ := newProjectWithTask(t, h, "keeper")

	w := postJSON(t, h, "/tasks?workspace=ws-test", map[string]any{
		"title": "spans both", "project_ids": []string{doomed, keeper},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create task = %d, body %s", w.Code, w.Body.String())
	}
	var tk struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &tk)

	if w := deleteRequest(t, h, "/projects/"+doomed, ""); w.Code != http.StatusOK {
		t.Fatalf("DELETE = %d, body %s", w.Code, w.Body.String())
	}
	got, err := srv.taskStore.Get(t.Context(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.ProjectIDs) != 1 || got.ProjectIDs[0] != keeper {
		t.Errorf("project ids = %v, want just the surviving project", got.ProjectIDs)
	}
}

func TestRepoRequirement_DanglingProjectDoesNotCountAsARepo(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	setupAgent(t, srv, "needsrepo")

	tk := &task.Task{WorkspaceID: "ws-test",
		Title:            "orphaned link",
		ProjectIDs:       []string{"project-that-never-existed"},
		StageAssignments: map[task.Stage]string{"refinement": "needsrepo"},
	}
	if err := srv.taskStore.Create(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, label, blocked := srv.repoRequirementError(t.Context(), tk, "refinement", tk.StageAssignments)
	if !blocked {
		t.Errorf("a task whose only project link is dangling passed the repo gate (agent %q)", label)
	}
}

func TestRepoRequirement_RealProjectPasses(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	setupAgent(t, srv, "needsrepo")
	projectID, _ := newProjectWithTask(t, h, "realrepo")

	tk := &task.Task{WorkspaceID: "ws-test",
		Title:            "properly linked",
		ProjectIDs:       []string{projectID},
		StageAssignments: map[task.Stage]string{"refinement": "needsrepo"},
	}
	if err := srv.taskStore.Create(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, _, blocked := srv.repoRequirementError(t.Context(), tk, "refinement", tk.StageAssignments); blocked {
		t.Error("a task with a real linked project was blocked by the repo gate")
	}
}

func TestRepoRequirement_NoProjectsStillBlocked(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	setupAgent(t, srv, "needsrepo")

	tk := &task.Task{WorkspaceID: "ws-test",
		Title:            "no repos",
		StageAssignments: map[task.Stage]string{"refinement": "needsrepo"},
	}
	if err := srv.taskStore.Create(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, _, blocked := srv.repoRequirementError(t.Context(), tk, "refinement", tk.StageAssignments); !blocked {
		t.Error("a task with no projects should be blocked from a stage whose agent needs one")
	}
}
