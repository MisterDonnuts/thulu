package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/automation"
	"github.com/MisterDonnuts/thulu/internal/githubapp"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

// deployment_status and workflow_run reach recipes as automation events with their own fields; the
// filter check knows every field the daemon publishes.
func TestGithubWebhookPublishesDeploymentAndWorkflowRun(t *testing.T) {
	configureTestGithubApp(t)
	srv, h := newIntegrationServer(t)
	autoStore, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(autoStore, automation.Deps{})
	srv.SetAutomations(autoStore, engine, automation.NewScheduler(autoStore, engine))
	if err := srv.gh.store.UpsertInstallation(t.Context(), githubapp.Installation{InstallationID: 100, WorkspaceID: "ws-a", AccountLogin: "acme"}); err != nil {
		t.Fatal(err)
	}
	common := func(extra map[string]any) map[string]any {
		out := map[string]any{"action": "created", "installation": map[string]any{"id": 100},
			"repository": map[string]any{"full_name": "acme/shop"}, "sender": map[string]any{"login": "ci", "type": "Bot"}}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	deliver := func(event string, payload map[string]any) {
		t.Helper()
		if code := deliverWebhook(t, h, event, payload); code != http.StatusNoContent {
			t.Fatalf("%s delivery = %d, want 204", event, code)
		}
	}
	deliver("deployment_status", common(map[string]any{
		"deployment_status": map[string]any{"state": "success", "environment": "production",
			"environment_url": "https://shop.example.com"},
		"deployment": map[string]any{"id": 77, "sha": "9F2C1E7AA", "ref": "main", "environment": "production"},
	}))
	deliver("workflow_run", common(map[string]any{
		"action": "completed",
		"workflow_run": map[string]any{"id": 9001, "name": "Deploy", "path": ".github/workflows/deploy.yml",
			"html_url": "https://github.com/acme/shop/actions/runs/9001", "status": "completed", "conclusion": "failure",
			"head_branch": "main", "head_sha": "abc1234", "event": "workflow_dispatch"},
	}))

	got := queuedEvents(t, autoStore)
	if len(got) != 2 {
		t.Fatalf("queued %d events, want 2: %+v", len(got), got)
	}
	dep, run := got[0], got[1]
	if dep.Kind != automation.EventDeployment || dep.WorkspaceID != "ws-a" {
		t.Fatalf("deployment event = %s in %s", dep.Kind, dep.WorkspaceID)
	}
	for k, want := range map[string]any{"environment": "production", "deploy_state": "success", "sha": "9f2c1e7aa",
		"ref": "main", "deployment_id": float64(77), "target_url": "https://shop.example.com", "repo": "acme/shop"} {
		if dep.Payload[k] != want {
			t.Errorf("deployment payload %s = %v, want %v", k, dep.Payload[k], want)
		}
	}
	if ids, ok := dep.Payload["evidence_batch_ids"].([]any); !ok || len(ids) != 0 {
		t.Errorf("evidence_batch_ids = %#v, want an empty list with no Evidence service", dep.Payload["evidence_batch_ids"])
	}
	if run.Kind != automation.EventWorkflowRun {
		t.Fatalf("workflow event kind = %s", run.Kind)
	}
	for k, want := range map[string]any{"workflow_name": "Deploy", "conclusion": "failure", "run_id": float64(9001),
		"head_branch": "main", "sha": "abc1234", "run_event": "workflow_dispatch", "action": "completed"} {
		if run.Payload[k] != want {
			t.Errorf("workflow payload %s = %v, want %v", k, run.Payload[k], want)
		}
	}

	for _, e := range got {
		for field := range e.Payload {
			spec := &automation.Trigger{Event: e.Kind, Filter: "trigger.payload." + field + " != nil"}
			if err := automation.CheckFilterFields(spec); err != nil {
				t.Errorf("%s publishes %q but a filter on it is refused: %v", e.Kind, field, err)
			}
		}
	}
}

// The batches the deploy hook queued ride on the event, so a recipe hands them to the release agent.
func TestDeploymentEventCarriesTheQueuedBatches(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"deployment": map[string]any{"id": 5, "sha": "ABC"},
		"deployment_status": map[string]any{"state": "success", "environment": "", "target_url": "https://ci/run/1"}})
	payload := map[string]any{}
	if key := addGithubEventFields("deployment_status", raw, []string{"b1", "b2"}, payload); key != "deployment:5" {
		t.Errorf("dedupe key = %q", key)
	}
	ids, _ := payload["evidence_batch_ids"].([]any)
	if len(ids) != 2 || ids[0] != "b1" || payload["target_url"] != "https://ci/run/1" {
		t.Errorf("payload = %+v", payload)
	}
	if key := addGithubEventFields("push", raw, nil, payload); key != "" {
		t.Errorf("push got dedupe key %q", key)
	}
}

// A push and a merged pull request carry the commits a release recipe keys on: the pushed head and
// whether it landed on the default branch, and the PR's merge commit.
func TestGithubPushAndMergedPRCarryTheirCommits(t *testing.T) {
	configureTestGithubApp(t)
	srv, h := newIntegrationServer(t)
	autoStore, err := automation.NewStore(testpg.New(t))
	if err != nil {
		t.Fatal(err)
	}
	engine := automation.NewEngine(autoStore, automation.Deps{})
	srv.SetAutomations(autoStore, engine, automation.NewScheduler(autoStore, engine))
	if err := srv.gh.store.UpsertInstallation(t.Context(), githubapp.Installation{InstallationID: 100, WorkspaceID: "ws-a", AccountLogin: "acme"}); err != nil {
		t.Fatal(err)
	}
	repo := map[string]any{"full_name": "acme/shop", "default_branch": "main"}
	for _, d := range []struct {
		event   string
		payload map[string]any
	}{
		{"push", map[string]any{"ref": "refs/heads/main", "before": "1111111AAA", "after": "2222222BBB"}},
		{"push", map[string]any{"ref": "refs/heads/feature", "before": "3333333", "after": "4444444"}},
		{"pull_request", map[string]any{"action": "closed", "pull_request": map[string]any{
			"number": 7, "html_url": "https://github.com/acme/shop/pull/7", "merged": true, "merge_commit_sha": "5555555CCC",
			"head": map[string]any{"ref": "feature", "sha": "4444444"}, "base": map[string]any{"ref": "main"}}}},
	} {
		d.payload["installation"] = map[string]any{"id": 100}
		d.payload["repository"] = repo
		d.payload["sender"] = map[string]any{"login": "ana", "type": "User"}
		if code := deliverWebhook(t, h, d.event, d.payload); code != http.StatusNoContent {
			t.Fatalf("%s delivery = %d", d.event, code)
		}
	}
	got := queuedEvents(t, autoStore)
	if len(got) != 3 {
		t.Fatalf("queued %d events, want 3", len(got))
	}
	for i, want := range []map[string]any{
		{"head_sha": "2222222bbb", "before": "1111111aaa", "default_branch": "main", "is_default_branch": true, "merged": false, "merge_sha": ""},
		{"head_sha": "4444444", "is_default_branch": false},
		{"head_sha": "4444444", "base_ref": "main", "merged": true, "merge_sha": "5555555ccc", "is_default_branch": false},
	} {
		for k, v := range want {
			if got[i].Payload[k] != v {
				t.Errorf("event %d (%s) %s = %v, want %v", i, got[i].Kind, k, got[i].Payload[k], v)
			}
		}
		for field := range got[i].Payload {
			spec := &automation.Trigger{Event: got[i].Kind, Filter: "trigger.payload." + field + " != nil"}
			if err := automation.CheckFilterFields(spec); err != nil {
				t.Errorf("%s publishes %q but a filter on it is refused: %v", got[i].Kind, field, err)
			}
		}
	}
}
