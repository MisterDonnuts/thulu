package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/project"
	"github.com/MisterDonnuts/thulu/internal/runner"
	"github.com/MisterDonnuts/thulu/internal/task"
	"github.com/MisterDonnuts/thulu/internal/testgit"
)

func gitProjectOn(t *testing.T, f *runnerFixture, ws, name, runnerID string) *project.Project {
	t.Helper()
	t.Setenv("OCTOPUS_GIT_ALLOW_LOCAL_REMOTES", "1")
	proj, err := f.projects.Create(t.Context(), name, testgit.BareRepo(t), project.CreateOpts{WorkspaceID: ws})
	if err != nil {
		t.Fatal(err)
	}
	if runnerID == "" {
		return proj
	}
	if proj, err = f.projects.Update(t.Context(), proj.ID, project.Patch{RunnerID: &runnerID}); err != nil {
		t.Fatal(err)
	}
	return proj
}

func TestPatchProjectRunner(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	srv := &Server{projectStore: f.projects, runnerStore: f.store, runnerRoute: &routedLauncher{runner: f.launcher}}
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /projects/{id}", srv.handleUpdateProject)
	patch := func(id, body string) *httptest.ResponseRecorder {
		r := tenantRequestBody(http.MethodPatch, "/projects/"+id, "ws1", body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	ctx := context.Background()
	proj := gitProjectOn(t, f, "ws1", "web", "")

	tok, _, err := f.store.CreateRegistration(ctx, "ws2", "elsewhere", "")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := f.store.Register(ctx, runner.RegisterRequest{Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err = f.store.CreateRegistration(ctx, "ws1", "old", "")
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := f.store.Register(ctx, runner.RegisterRequest{Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Revoke(ctx, "ws1", revoked.RunnerID); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, id, runner string
		want             int
	}{
		{"unknown runner", proj.ID, "nope", http.StatusNotFound},
		{"another workspace's runner", proj.ID, foreign.RunnerID, http.StatusNotFound},
		{"revoked runner", proj.ID, revoked.RunnerID, http.StatusConflict},
		{"runner project cleared", f.project.ID, "", http.StatusBadRequest},
		{"the workspace's runner", proj.ID, f.reg.RunnerID, http.StatusOK},
	} {
		w := patch(c.id, `{"runner_id":"`+c.runner+`"}`)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
	}
	got, err := f.projects.Get(ctx, proj.ID)
	if err != nil || got.RunnerID != f.reg.RunnerID {
		t.Fatalf("after PATCH: %+v, %v", got, err)
	}
	if w := patch(proj.ID, `{"runner_id":""}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	var back project.Project
	if w := patch(proj.ID, `{"description":"x"}`); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &back) != nil ||
		back.RunnerID != "" {
		t.Errorf("after clear: %d %+v", w.Code, back)
	}
}

func TestRunnerJobProjectsCarryRepoAndPush(t *testing.T) {
	f := newRunnerFixture(t, "ws1")
	ctx := context.Background()
	cloned := gitProjectOn(t, f, "ws1", "web", f.reg.RunnerID)

	projects, err := f.launcher.runnerFor(ctx, &task.Task{ID: "t1", ProjectIDs: []string{f.project.ID, cloned.ID}}, f.reg.RunnerID, false)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]runner.JobProject{}
	for _, p := range projects {
		byID[p.ID] = p
	}
	if p := byID[cloned.ID]; !p.Push || p.RepoURL != cloned.RepoURL || p.Branch != project.TaskBranchName("t1") {
		t.Errorf("cloned project = %+v, want push to %s", p, cloned.RepoURL)
	}
	if p := byID[f.project.ID]; p.Push {
		t.Errorf("runner-only project = %+v, want no push after every run", p)
	}

	plain := gitProjectOn(t, f, "ws1", "plain", "")
	withWorktree := &task.Task{ID: "t2", ProjectIDs: []string{cloned.ID, plain.ID},
		WorktreePaths: map[string]string{plain.ID: "/data/worktrees/t2"}}
	mixed, err := f.launcher.runnerFor(ctx, withWorktree, f.reg.RunnerID, false)
	if err != nil || len(mixed) != 2 || mixed[0].FromDaemon() || !mixed[1].FromDaemon() {
		t.Errorf("a task mixing a machine project with a Thulu one = %+v, %v; want the Thulu one from the daemon", mixed, err)
	}
	// Without a daemon worktree there is no task branch to fetch: a run that writes goes without the
	// project, as in Docker, and a read-only one reads the default branch.
	noWorktree := &task.Task{ID: "t3", ProjectIDs: []string{cloned.ID, plain.ID}}
	for _, tc := range []struct {
		readOnly bool
		want     []runner.JobProject
	}{
		{false, []runner.JobProject{{ID: cloned.ID, Branch: project.TaskBranchName("t3")}}},
		{true, []runner.JobProject{{ID: cloned.ID, Branch: project.TaskBranchName("t3")}, {ID: plain.ID, Branch: ""}}},
	} {
		got, err := f.launcher.runnerFor(ctx, noWorktree, f.reg.RunnerID, tc.readOnly)
		if err != nil || len(got) != len(tc.want) {
			t.Fatalf("read-only %v: runnerFor = %+v, %v; want %d projects", tc.readOnly, got, err, len(tc.want))
		}
		for i, w := range tc.want {
			if got[i].ID != w.ID || got[i].Branch != w.Branch {
				t.Errorf("read-only %v: project %d = %s on %q, want %s on %q", tc.readOnly, i, got[i].ID, got[i].Branch, w.ID, w.Branch)
			}
		}
	}
	route := &routedLauncher{projects: f.projects, runner: f.launcher}
	if !route.needsRunner(ctx, &task.Task{ProjectIDs: []string{cloned.ID}}) {
		t.Error("a cloned project with a runner is not routed to it")
	}
	if route.needsRunner(ctx, &task.Task{ProjectIDs: []string{plain.ID}}) {
		t.Error("a project without a runner is routed to one")
	}
	srv := &Server{projectStore: f.projects}
	if _, _, err := srv.pushTaskBranchForProject(ctx, &task.Task{ID: "t1"}, cloned.ID, false); apperr.KindOf(err) != apperr.KindPrecondition {
		t.Errorf("daemon push of a runner project: %v, want refused", err)
	}
}
