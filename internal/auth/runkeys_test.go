package auth_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestRunKeyCarriesItsAgentAndStage(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	user, _ := store.CreateUser(t.Context(), "run@test.com", "Run")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "Run WS", "run-ws")

	scope := auth.RunScope{TaskID: "task1", Agent: "devops", Stage: "mention", LLMAgent: "dev", LLMEngine: "codex"}
	raw, meta, err := store.CreateRunScopedAPIKey(t.Context(), scope, ws.ID, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if meta.ScopeAgent != "devops" || meta.ScopeStage != "mention" {
		t.Errorf("minted meta = %+v, want the agent and stage it was minted for", meta)
	}

	info := store.LookupAPIKeyInfo(t.Context(), raw)
	if info == nil {
		t.Fatal("a fresh key must authenticate")
	}
	if info.Scope != scope {
		t.Errorf("scope round trip = %+v, want %+v", info.Scope, scope)
	}
	if !info.Scope.IsRun() {
		t.Error("a run key must read as a run, not as an operator")
	}
}

func TestChatRunKeyWithoutTaskCannotWriteTasks(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	user, _ := store.CreateUser(t.Context(), "chat@test.com", "Chat")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "Chat WS", "chat-ws")

	raw, _, err := store.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{ChatID: "chat-1", Agent: "manager", Stage: "chat"}, ws.ID, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	info := store.LookupAPIKeyInfo(t.Context(), raw)
	if info == nil {
		t.Fatal("a fresh key must authenticate")
	}
	for _, p := range info.Permissions {
		if p == auth.PermTasksWrite {
			t.Fatal("a conversation with no task must not be able to move somebody's task")
		}
	}
	if !info.Scope.IsRun() {
		t.Error("a task-less chat key still belongs to a run, not to an operator")
	}
}

func TestDelegatedChatKey(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	user, _ := store.CreateUser(t.Context(), "deleg@test.com", "Deleg")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "Deleg WS", "deleg-ws")

	scope := auth.RunScope{ChatID: "chat-1", Agent: "admin", Stage: "chat", DelegatedBy: "deleg@test.com"}
	raw, meta, err := store.CreateRunScopedAPIKey(t.Context(), scope, ws.ID, time.Hour)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if meta.Role != auth.RoleDeveloper {
		t.Errorf("delegated key role = %s, want developer", meta.Role)
	}
	info := store.LookupAPIKeyInfo(t.Context(), raw)
	if info == nil || info.Scope != scope || !info.Scope.Delegated() {
		t.Fatalf("scope round trip = %+v, want %+v", info, scope)
	}
	writes := false
	for _, p := range info.Permissions {
		writes = writes || p == auth.PermTasksWrite
	}
	if !writes {
		t.Error("a delegated chat's key cannot write tasks")
	}

	if err := store.RevokeChatRunKeys(t.Context(), "chat-1"); err != nil {
		t.Fatal(err)
	}
	if store.LookupAPIKeyInfo(t.Context(), raw) != nil {
		t.Error("the delegated key still authenticates after its chat's keys were revoked")
	}

	if (auth.RunScope{TaskID: "t1", DelegatedBy: "x"}).Delegated() {
		t.Error("a task run key read as delegated")
	}
}

func TestDelegatedChatKeyCapsAtTheDelegatorsRole(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	owner, _ := store.CreateUser(t.Context(), "cap-owner@test.com", "Owner")
	ws, _ := store.CreateWorkspace(t.Context(), owner.ID, "Cap WS", "cap-ws")

	cases := []struct {
		email    string
		member   auth.Role
		want     auth.Role
		internal bool
	}{
		{"cap-viewer@test.com", auth.RoleViewer, auth.RoleViewer, false},
		{"cap-dev@test.com", auth.RoleDeveloper, auth.RoleDeveloper, true},
		{"cap-admin@test.com", auth.RoleAdmin, auth.RoleDeveloper, true},
		{"cap-gone@test.com", "", auth.RoleViewer, false},
	}
	for i, c := range cases {
		u, err := store.CreateUser(t.Context(), c.email, "U")
		if err != nil {
			t.Fatalf("create %s: %v", c.email, err)
		}
		if c.member != "" {
			if err := store.AddMember(t.Context(), ws.ID, u.ID, c.member); err != nil {
				t.Fatalf("add %s: %v", c.email, err)
			}
		}
		scope := auth.RunScope{ChatID: fmt.Sprintf("chat-%d", i), Agent: "admin", Stage: "chat",
			DelegatedBy: strings.ToUpper(c.email)}
		raw, meta, err := store.CreateRunScopedAPIKey(t.Context(), scope, ws.ID, time.Hour)
		if err != nil {
			t.Fatalf("%s: mint: %v", c.email, err)
		}
		if meta.Role != c.want {
			t.Errorf("%s (%q): key role = %s, want %s", c.email, c.member, meta.Role, c.want)
		}
		info := store.LookupAPIKeyInfo(t.Context(), raw)
		if info == nil {
			t.Fatalf("%s: a fresh key must authenticate", c.email)
		}
		if got := slices.Contains(info.Permissions, auth.PermInternalUse); got != c.internal {
			t.Errorf("%s: internal:use = %v, want %v", c.email, got, c.internal)
		}
		if dead := auth.PermissionsAboveRole(info.Permissions, info.Role); len(dead) > 0 {
			t.Errorf("%s: key holds %v its role %s cannot use", c.email, dead, info.Role)
		}
	}

	_, meta, err := store.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{ChatID: "plain", Agent: "admin", Stage: "chat"}, ws.ID, time.Hour)
	if err != nil || meta.Role != auth.RoleDeveloper {
		t.Errorf("undelegated chat key = %+v (%v), want developer", meta, err)
	}
}

func TestRunKeysStayOutOfTheOperatorKeyList(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	user, _ := store.CreateUser(t.Context(), "list@test.com", "List")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "List WS", "list-ws")

	if _, _, err := store.CreateAPIKey(t.Context(), "ci", user.ID, auth.KeyGrant{
		WorkspaceID: ws.ID, Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermTasksRead},
	}); err != nil {
		t.Fatalf("mint operator key: %v", err)
	}
	for _, scope := range []auth.RunScope{
		{TaskID: "task1", Agent: "devops", Stage: "in_progress"},
		{ChatID: "chat-1", Agent: "manager", Stage: "chat"},
	} {
		if _, _, err := store.CreateRunScopedAPIKey(t.Context(), scope, ws.ID, time.Hour); err != nil {
			t.Fatalf("mint run key %+v: %v", scope, err)
		}
	}

	keys, err := store.ListAPIKeys(t.Context(), ws.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 || keys[0].Name != "ci" {
		t.Errorf("list = %+v, want only the operator's key — machine-managed run keys are noise", keys)
	}
}

func TestDeleteTaskRunKeys(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	user, _ := store.CreateUser(t.Context(), "del@test.com", "Del")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "Del WS", "del-ws")

	mint := func(scope auth.RunScope) string {
		t.Helper()
		raw, _, err := store.CreateRunScopedAPIKey(t.Context(), scope, ws.ID, time.Hour)
		if err != nil {
			t.Fatalf("mint %+v: %v", scope, err)
		}
		return raw
	}
	stageKey := mint(auth.RunScope{TaskID: "task-x", Agent: "developer", Stage: "in_progress"})
	chatKey := mint(auth.RunScope{TaskID: "task-x", ChatID: "chat-x", Agent: "manager", Stage: "chat"})
	otherKey := mint(auth.RunScope{TaskID: "task-y", Agent: "developer", Stage: "in_progress"})
	opKey, _, err := store.CreateAPIKey(t.Context(), "ci", user.ID,
		auth.KeyGrant{WorkspaceID: ws.ID, Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermAll}})
	if err != nil {
		t.Fatalf("operator key: %v", err)
	}

	n, err := store.DeleteTaskRunKeys(t.Context(), "task-x")
	if err != nil {
		t.Fatalf("DeleteTaskRunKeys: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d keys, want task-x's two", n)
	}
	for name, raw := range map[string]string{"stage": stageKey, "chat": chatKey} {
		if store.LookupAPIKeyInfo(t.Context(), raw) != nil {
			t.Errorf("task-x %s key still authenticates", name)
		}
	}
	for name, raw := range map[string]string{"other task": otherKey, "operator": opKey} {
		if store.LookupAPIKeyInfo(t.Context(), raw) == nil {
			t.Errorf("%s key was deleted", name)
		}
	}
	if n, err := store.DeleteTaskRunKeys(t.Context(), ""); err != nil || n != 0 {
		t.Errorf("DeleteTaskRunKeys(\"\") = %d, %v; the empty scope is every operator key", n, err)
	}
}

func TestDeleteWorkspaceDeletesItsKeys(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	user, _ := store.CreateUser(t.Context(), "wsdel@test.com", "WsDel")
	gone, _ := store.CreateWorkspace(t.Context(), user.ID, "Gone", "gone")
	kept, _ := store.CreateWorkspace(t.Context(), user.ID, "Kept", "kept")

	grant := func(wsID string) auth.KeyGrant {
		return auth.KeyGrant{WorkspaceID: wsID, Role: auth.RoleDeveloper, Permissions: []auth.Permission{auth.PermAll}}
	}
	goneOp, _, _ := store.CreateAPIKey(t.Context(), "ci", user.ID, grant(gone.ID))
	goneRun, _, _ := store.CreateRunScopedAPIKey(t.Context(), auth.RunScope{TaskID: "t1", Agent: "dev"}, gone.ID, time.Hour)
	keptOp, _, _ := store.CreateAPIKey(t.Context(), "ci", user.ID, grant(kept.ID))

	if _, err := store.DeleteWorkspace(t.Context(), gone.ID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if store.LookupAPIKeyInfo(t.Context(), goneOp) != nil || store.LookupAPIKeyInfo(t.Context(), goneRun) != nil {
		t.Error("a key of the erased workspace still authenticates")
	}
	if store.LookupAPIKeyInfo(t.Context(), keptOp) == nil {
		t.Error("another workspace's key was deleted")
	}
	var rows int
	if err := store.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM api_keys WHERE workspace_id = $1`, gone.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d key rows of the erased workspace left", rows)
	}
}
