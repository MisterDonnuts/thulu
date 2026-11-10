package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/agentstore"
	"github.com/MisterDonnuts/thulu/internal/secrets"
	stagepkg "github.com/MisterDonnuts/thulu/internal/stage"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func TestDeletedAgentNameStartsWithoutTheOldBinding(t *testing.T) {
	db := testpg.New(t)
	store, err := agentstore.New(db)
	if err != nil {
		t.Fatalf("agent store: %v", err)
	}
	sec, err := secrets.NewStore(db)
	if err != nil {
		t.Fatalf("secrets store: %v", err)
	}
	srv := &Server{agentsDir: t.TempDir(), boards: stagepkg.StaticBoards(), secretsStore: sec}
	srv.SetAgentStore(store)
	t.Setenv(OperatorWorkspaceEnv, "")
	const ws = "ws-binding"
	ctx := t.Context()

	create := func() {
		t.Helper()
		w := httptest.NewRecorder()
		srv.handleCreateAgent(w, tenantRequestBody(http.MethodPost, "/types", ws, `{"name":"backend"}`))
		if w.Code != http.StatusCreated {
			t.Fatalf("create agent: %d %s", w.Code, w.Body.String())
		}
	}
	del := func() {
		t.Helper()
		r := tenantRequestBody(http.MethodDelete, "/types/backend", ws, "")
		r.SetPathValue("name", "backend")
		w := httptest.NewRecorder()
		srv.handleDeleteAgent(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("delete agent: %d %s", w.Code, w.Body.String())
		}
	}
	bind := func() {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO agent_llm_bindings (workspace_id, agent_name, profile_id, updated_at, updated_by)
			VALUES ($1, 'backend', 'old-profile', $2, 'tester')
			ON CONFLICT (workspace_id, agent_name) DO UPDATE SET profile_id = EXCLUDED.profile_id`, ws, time.Now().UTC()); err != nil {
			t.Fatalf("bind: %v", err)
		}
	}
	unbound := func(when string) {
		t.Helper()
		if b, err := sec.GetAgentBinding(ctx, ws, "backend"); !errors.Is(err, secrets.ErrNotFound) {
			t.Fatalf("%s: binding = %+v (%v), want none", when, b, err)
		}
	}

	create()
	bind()
	del()
	unbound("after delete")

	bind()
	create()
	unbound("after reusing the name")
}
