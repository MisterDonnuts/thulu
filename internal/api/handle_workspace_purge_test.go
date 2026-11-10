package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/settings"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func seedWorkspaceData(t *testing.T, srv *Server, wsID, slug, ownerID string) {
	t.Helper()
	if _, err := srv.authStore.DB().Exec(
		`INSERT INTO users(id,email,name,created_at)
		 VALUES($1,$2,$3,$4) ON CONFLICT (id) DO NOTHING`,
		ownerID, ownerID+"@example.com", ownerID, time.Now()); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := srv.authStore.DB().Exec(
		`INSERT INTO workspaces(id,name,slug,owner_id,created_at) VALUES($1,$2,$3,$4,$5)`,
		wsID, slug, slug, ownerID, time.Now()); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := srv.authStore.DB().Exec(
		`INSERT INTO workspace_members(workspace_id,user_id,role,invited_at) VALUES($1,$2,$3,$4)`,
		wsID, ownerID, "owner", time.Now()); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := srv.entitlements.SetPlan(wsID, entitlements.PlanBusiness); err != nil {
		t.Fatal(err)
	}
	if err := srv.secretsStore.SetLLMKey(t.Context(), wsID, "claude-code", "anthropic", "sk-ant-"+slug); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.webhookStore.CreateEndpoint(t.Context(), wsID, "https://"+slug+".example/h", []string{"*"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.settingsStore.PutWorkspaceRaw(t.Context(), wsID, settings.AgentMaxRunMinutesKey, "90"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.projectStore.DB().Exec(
		`INSERT INTO projects(id,name,path,repo_url,default_branch,description,ai_review_cmd,testing_cmd,stage_template,workspace_id,created_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		"proj-"+slug, "P-"+slug, t.TempDir(), "", "main", "", "", "", "{}", wsID, time.Now()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	// No project_ids: a fresh database has that column only until its second boot drops it
	// (task migrations 18-19).
	if _, err := srv.taskStore.DB().Exec(
		`INSERT INTO tasks(id,title,body,stage,stage_assignments,branch_name,worktree_paths,pr_urls,current_run_id,current_instance,status,tags,priority,workspace_id,created_at,updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		"task-"+slug, "T-"+slug, "", "backlog", "{}", "", "{}", "{}", "", "", "idle", "[]", "", wsID, time.Now(), time.Now()); err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

func TestPurgeWorkspace_OnlyTargetErased(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	db := newEntDB(t)
	es, _ := entitlements.NewStore(db, entitlements.PlanFree)
	srv.SetEntitlements(es)

	seedWorkspaceData(t, srv, "WS1", "victim", "owner1")
	seedWorkspaceData(t, srv, "WS2", "bystander", "owner2")

	if err := srv.purgeWorkspace(t.Context(), "WS1"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	assertCount(t, srv.projectStore.DB(), `SELECT COUNT(*) FROM projects WHERE workspace_id='WS1'`, 0, "ws1 projects")
	assertCount(t, srv.taskStore.DB(), `SELECT COUNT(*) FROM tasks WHERE workspace_id='WS1'`, 0, "ws1 tasks")
	assertCount(t, srv.authStore.DB(), `SELECT COUNT(*) FROM workspaces WHERE id='WS1'`, 0, "ws1 workspace")
	assertCount(t, srv.authStore.DB(), `SELECT COUNT(*) FROM workspace_members WHERE workspace_id='WS1'`, 0, "ws1 members")
	assertCount(t, srv.webhookStore.DB(), `SELECT COUNT(*) FROM webhook_endpoints WHERE workspace_id='WS1'`, 0, "ws1 webhooks")
	assertCount(t, srv.settingsStore.DB(), `SELECT COUNT(*) FROM workspace_settings WHERE workspace_id='WS1'`, 0, "ws1 settings")
	if _, _, err := srv.secretsStore.GetLLMKey(t.Context(), "WS1", "claude-code"); err == nil {
		t.Error("ws1 llm key should be erased")
	}

	assertCount(t, srv.projectStore.DB(), `SELECT COUNT(*) FROM projects WHERE workspace_id='WS2'`, 1, "ws2 projects")
	assertCount(t, srv.taskStore.DB(), `SELECT COUNT(*) FROM tasks WHERE workspace_id='WS2'`, 1, "ws2 tasks")
	assertCount(t, srv.authStore.DB(), `SELECT COUNT(*) FROM workspaces WHERE id='WS2'`, 1, "ws2 workspace")
	assertCount(t, srv.webhookStore.DB(), `SELECT COUNT(*) FROM webhook_endpoints WHERE workspace_id='WS2'`, 1, "ws2 webhooks")
	assertCount(t, srv.settingsStore.DB(), `SELECT COUNT(*) FROM workspace_settings WHERE workspace_id='WS2'`, 1, "ws2 settings")
	if _, _, err := srv.secretsStore.GetLLMKey(t.Context(), "WS2", "claude-code"); err != nil {
		t.Errorf("ws2 llm key must survive: %v", err)
	}
}

func newEntDB(t *testing.T) *sql.DB {
	t.Helper()
	return testpg.New(t)
}

func assertCount(t *testing.T, db *sql.DB, query string, want int, label string) {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s count: %v", label, err)
	}
	if n != want {
		t.Errorf("%s = %d, want %d", label, n, want)
	}
}

func TestDeleteWorkspace_OwnerGated(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	db := newEntDB(t)
	es, _ := entitlements.NewStore(db, entitlements.PlanFree)
	srv.SetEntitlements(es)
	seedWorkspaceData(t, srv, "WSX", "acme", "owner-x")

	b, _ := json.Marshal(map[string]string{"confirm": "acme"})
	req := httptest.NewRequest("DELETE", "/workspaces/WSX", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("non-owner delete should be 403, got %d body=%s", w.Code, w.Body.String())
	}
	assertCount(t, srv.authStore.DB(), `SELECT COUNT(*) FROM workspaces WHERE id='WSX'`, 1, "workspace survives non-owner delete")
}

func TestDeleteAccount_PurgesOwnedWorkspaceData(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	db := newEntDB(t)
	es, _ := entitlements.NewStore(db, entitlements.PlanFree)
	srv.SetEntitlements(es)
	seedWorkspaceData(t, srv, "WSA", "leaving", "owner-a")
	seedWorkspaceData(t, srv, "WSB", "staying", "owner-b")

	token, err := auth.IssueJWT(auth.Claims{UserID: "owner-a", Email: "owner-a@example.com", WorkspaceID: "WSA", Role: auth.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	w := tokenDo(t, h, http.MethodDelete, "/auth/me", token, map[string]string{"confirm": "owner-a@example.com"})
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /auth/me = %d, body %s", w.Code, w.Body)
	}

	assertCount(t, srv.projectStore.DB(), `SELECT COUNT(*) FROM projects WHERE workspace_id='WSA'`, 0, "owned projects")
	assertCount(t, srv.taskStore.DB(), `SELECT COUNT(*) FROM tasks WHERE workspace_id='WSA'`, 0, "owned tasks")
	assertCount(t, srv.webhookStore.DB(), `SELECT COUNT(*) FROM webhook_endpoints WHERE workspace_id='WSA'`, 0, "owned webhooks")
	if _, _, err := srv.secretsStore.GetLLMKey(t.Context(), "WSA", "claude-code"); err == nil {
		t.Error("the owned workspace's BYOK key survived the account deletion")
	}
	assertCount(t, srv.authStore.DB(), `SELECT COUNT(*) FROM users WHERE id='owner-a'`, 0, "user row")
	assertCount(t, srv.taskStore.DB(), `SELECT COUNT(*) FROM tasks WHERE workspace_id='WSB'`, 1, "another owner's tasks")
}
