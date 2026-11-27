package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/project"
)

// Two sets of a new watch that both read it before either armed it (an agent calling
// deploy_watch_set twice in parallel) must leave one automation. A second one would open every
// release task twice and, managed, could not be deleted from the automations page.
func TestDeployWatchConcurrentArmLeavesOneAutomation(t *testing.T) {
	e := newWatchEnv(t)
	ctx := t.Context()
	proj, err := e.srv.projectStore.Get(ctx, e.project)
	if err != nil {
		t.Fatal(err)
	}
	watch, _, err := e.srv.projectStore.UpsertDeployWatch(ctx, &project.DeployWatch{
		WorkspaceID: e.ws, ProjectID: e.project, Source: project.WatchGitHubDeployment, Target: "production",
		BoardID: rbBoard, TypeID: "release",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRead, secondRead := *watch, *watch
	first, err := e.srv.armDeployWatch(ctx, &firstRead, proj, rbRelease)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.srv.armDeployWatch(ctx, &secondRead, proj, rbRelease)
	if err != nil {
		t.Fatal(err)
	}
	all, err := e.auto.List(ctx, automation.InWorkspace(e.ws))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || first != second {
		t.Fatalf("two arms of one watch left %d automations (%s, %s), want 1", len(all), first, second)
	}
	stored, err := e.srv.projectStore.GetDeployWatch(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AutomationID != first {
		t.Fatalf("watch points at %q, want %q", stored.AutomationID, first)
	}
}

// A managed automation whose watch is gone (or points at another automation) belongs to nobody:
// an admin can delete it from the automations page.
func TestOrphanedManagedAutomationCanBeDeleted(t *testing.T) {
	e := newWatchEnv(t)
	spec := "name: orphan\ntrigger:\n  manual: true\nsteps:\n  - id: s\n    condition: \"true\"\n"
	orphan, err := e.auto.CreateManaged(t.Context(), e.ws, spec, managedByDeployWatch+"gone")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/automations/"+orphan.ID, nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete orphaned managed automation = %d %s, want 200", rec.Code, rec.Body)
	}
}

// A person who suspends a watch's automation decides; the release agent registering the same watch
// again must not resume it.
func TestDeployWatchSetKeepsASuspendedAutomationSuspended(t *testing.T) {
	e := newWatchEnv(t)
	path := "/tasks/" + e.release.ID + "/deploy-watches"
	key := e.watchKey(t, "deploy_watch_set")
	body := map[string]any{"source": "github_deployment", "target": "production"}
	w := keyDo(t, e.h, http.MethodPut, path, key, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("set = %d %s", w.Code, w.Body)
	}
	watch := decodeWatch(t, w)
	if s := putJSON(t, e.h, "/automations/"+watch.AutomationID+"/enabled", map[string]any{"enabled": false}, e.token); s.Code != http.StatusOK {
		t.Fatalf("suspend = %d %s", s.Code, s.Body)
	}
	if w := keyDo(t, e.h, http.MethodPut, path, key, body); w.Code != http.StatusOK {
		t.Fatalf("second set = %d %s", w.Code, w.Body)
	}
	a, err := e.auto.Get(t.Context(), watch.AutomationID, automation.InWorkspace(e.ws))
	if err != nil {
		t.Fatal(err)
	}
	if a.Enabled {
		t.Fatal("the agent's second set resumed an automation a person suspended")
	}
}
