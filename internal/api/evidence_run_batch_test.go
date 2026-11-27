package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/evidence"
	"github.com/MisterDonnuts/thulu/internal/mcp"
)

// An agent run reaches POST /evidence/environments/{id}/run only when its octopus-api grant names
// evidence_batch_run. Against production the batch runs only the cases a person marked safe, even
// if the request asked for every case, and it records the run's task.
func TestRunKey_EvidenceBatchRunNamedTool(t *testing.T) {
	srv, h, _ := evidenceServer(t)
	ctx := t.Context()
	token, ws := registerUserInWorkspace(t, h, "release@example.com", "Release", "")
	prod := decode[evidence.Environment](t, postJSONAuth(t, h, "/evidence/environments", token,
		map[string]any{"name": "Production", "base_url": "https://shop.example.com", "is_production": true}))
	staging := decode[evidence.Environment](t, postJSONAuth(t, h, "/evidence/environments", token,
		map[string]any{"name": "Staging", "base_url": "https://staging.example.com"}))
	for _, title := range []string{"Home loads", "Checkout pays"} {
		c := decode[evidence.Case](t, postJSONAuth(t, h, "/evidence/cases", token, map[string]any{"title": title, "steps": "Open it"}))
		r := httptest.NewRequest(http.MethodPut, "/evidence/cases/"+c.ID+"/take", strings.NewReader(evidenceTake))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("take: %d %s", w.Code, w.Body)
		}
		if title == "Home loads" {
			if w := patchJSONAuth(t, h, "/evidence/cases/"+c.ID, token, map[string]any{"safe_for_prod": true}); w.Code != http.StatusOK {
				t.Fatalf("mark safe: %d %s", w.Code, w.Body)
			}
		}
	}
	taskID := taskIDFor(t, h, token, "Deploy acme/shop@9f2c1e7")
	reg, err := mcp.NewRegistry(srv.taskStore.DB())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetMCPRegistry(reg)
	mint := func(stage, grant string) string {
		t.Helper()
		if err := reg.RecordRunGrant(ctx, taskID, "thulu-release", stage, mcp.RunGrant{Tools: grant}); err != nil {
			t.Fatal(err)
		}
		raw, _, err := srv.authStore.MintRunKey(ctx, auth.RunScope{TaskID: taskID, Agent: "thulu-release", Stage: stage}, ws, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	scoped := mint("verify", "octopus-api@write")
	if w := keyDo(t, h, http.MethodPost, "/evidence/environments/"+prod.ID+"/run", scoped, map[string]any{}); w.Code != http.StatusForbidden ||
		errCode(t, w) != "api.scope.errors.run_evidence_scope" {
		t.Fatalf("a scope alone queued a batch: %d %s", w.Code, w.Body)
	}

	named := mint("release", "octopus-api@read+evidence_batch_run+evidence_batch_get")
	w := keyDo(t, h, http.MethodPost, "/evidence/environments/"+prod.ID+"/run", named, map[string]any{"safe_only": false})
	if w.Code != http.StatusCreated {
		t.Fatalf("named tool on production = %d %s", w.Code, w.Body)
	}
	b := decode[evidence.Batch](t, w)
	if !b.SafeOnly || b.Total != 1 || b.Trigger != evidence.TriggerAgent || b.TaskID != taskID || b.TriggerRef != taskID ||
		len(b.SkippedUnsafe) != 1 {
		t.Fatalf("production batch: safe_only %v total %d trigger %q task %q skipped %v", b.SafeOnly, b.Total, b.Trigger, b.TaskID, b.SkippedUnsafe)
	}

	w = keyDo(t, h, http.MethodPost, "/evidence/environments/"+staging.ID+"/run", named, map[string]any{})
	if s := decode[evidence.Batch](t, w); w.Code != http.StatusCreated || s.SafeOnly || s.Total != 2 {
		t.Fatalf("staging keeps every case: %d safe_only %v total %d", w.Code, s.SafeOnly, s.Total)
	}

	if w = keyDo(t, h, http.MethodPost, "/evidence/environments/"+staging.ID+"/run", named, map[string]any{"branch": "main"}); w.Code != http.StatusForbidden ||
		errCode(t, w) != "api.evidence.errors.run_branch_scope" {
		t.Fatalf("a run built a sandbox off its project: %d %s", w.Code, w.Body)
	}

	// The tool lets a run queue a batch; it does not let it judge a case safe.
	if w = keyDo(t, h, http.MethodPatch, "/evidence/cases/EV-2", named, map[string]any{"safe_for_prod": true}); w.Code != http.StatusForbidden {
		t.Fatalf("a run marked a case safe: %d %s", w.Code, w.Body)
	}

	w = keyDo(t, h, http.MethodGet, "/evidence/batches?trigger=agent&task_id="+taskID, named, nil)
	list := decode[struct {
		Batches []evidence.Batch `json:"batches"`
	}](t, w)
	if w.Code != http.StatusOK || len(list.Batches) != 2 {
		t.Fatalf("the run finds its batches: %d %s", w.Code, w.Body)
	}
}

// release_environment names production too, by name, display name or the slug a deploy uses.
func TestEvidenceEnvIsProduction_FollowsReleaseEnvironment(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "rel@test.com", "Rel")
	projID := createProject(t, h, token, "shop")
	if w := patchJSONAuth(t, h, "/projects/"+projID, token, map[string]any{"release_environment": "Live Shop"}); w.Code != http.StatusOK {
		t.Fatalf("PATCH release_environment = %d %s", w.Code, w.Body)
	}
	for name, c := range map[string]struct {
		env  evidence.Environment
		want bool
	}{
		"marked":       {evidence.Environment{Name: "x", IsProduction: true}, true},
		"by name":      {evidence.Environment{Name: "live shop", ProjectID: projID}, true},
		"by display":   {evidence.Environment{Name: "p1", DisplayName: "LIVE SHOP", ProjectID: projID}, true},
		"by slug":      {evidence.Environment{Name: "live-shop", ProjectID: projID}, true},
		"other env":    {evidence.Environment{Name: "staging", ProjectID: projID}, false},
		"no project":   {evidence.Environment{Name: "live shop"}, false},
		"gone project": {evidence.Environment{Name: "staging", ProjectID: "missing"}, true},
	} {
		if got := srv.evidenceEnvIsProduction(t.Context(), &c.env); got != c.want {
			t.Errorf("%s = %v, want %v", name, got, c.want)
		}
	}
	if !strings.EqualFold(evidence.DeploySlug("Live Shop"), "live-shop") {
		t.Fatal("slug changed; the case above tests the wrong thing")
	}
}
