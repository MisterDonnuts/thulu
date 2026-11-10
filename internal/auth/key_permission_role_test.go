package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestPermissionsAboveRoleCatchesTheDeadOnes(t *testing.T) {
	dead := auth.PermissionsAboveRole([]auth.Permission{
		auth.PermTasksRead,
		auth.PermSettingsRead,
		auth.PermProjectsWrite,
		auth.PermCredentialsRead,
		auth.PermCredentialsWrite,
		auth.PermSettingsWrite,
		auth.PermFlowsWrite,
	}, auth.RoleDeveloper)

	got := map[auth.Permission]bool{}
	for _, p := range dead {
		got[p] = true
	}
	for _, p := range []auth.Permission{
		auth.PermCredentialsRead, auth.PermCredentialsWrite,
		auth.PermSettingsWrite, auth.PermFlowsWrite,
	} {
		if !got[p] {
			t.Errorf("%s is admin-only and must be flagged for a developer key", p)
		}
	}
	for _, p := range []auth.Permission{
		auth.PermTasksRead, auth.PermSettingsRead, auth.PermProjectsWrite,
	} {
		if got[p] {
			t.Errorf("%s is reachable by a developer and must not be flagged", p)
		}
	}
}

func TestPermissionsAboveRoleExemptsTheWildcard(t *testing.T) {
	if dead := auth.PermissionsAboveRole([]auth.Permission{auth.PermAll}, auth.RoleDeveloper); len(dead) != 0 {
		t.Errorf("PermAll must be exempt, got %v", dead)
	}
}

func TestPermissionsAboveRoleClearsAnAdmin(t *testing.T) {
	for _, p := range auth.AllPermissions() {
		if dead := auth.PermissionsAboveRole([]auth.Permission{p}, auth.RoleAdmin); len(dead) != 0 {
			t.Errorf("%s should be usable by an admin, flagged as %v", p, dead)
		}
	}
}

func TestCreateAPIKeyRefusesADeadPermission(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	user, _ := store.CreateUser(t.Context(), "dead@test.com", "Dead")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "Dead WS", "dead-ws")

	_, _, err = store.CreateAPIKey(t.Context(), "would-be-admin", user.ID, auth.KeyGrant{
		WorkspaceID: ws.ID,
		Role:        auth.RoleDeveloper,
		Permissions: []auth.Permission{auth.PermTasksRead, auth.PermCredentialsRead},
	})
	if !errors.Is(err, auth.ErrPermissionsAboveRole) {
		t.Fatalf("minting a developer key with credentials:read must be refused, got %v", err)
	}

	if _, _, err := store.CreateAPIKey(t.Context(), "real-admin", user.ID, auth.KeyGrant{
		WorkspaceID: ws.ID,
		Role:        auth.RoleAdmin,
		Permissions: []auth.Permission{auth.PermTasksRead, auth.PermCredentialsRead},
	}); err != nil {
		t.Fatalf("an admin key with credentials:read must be allowed: %v", err)
	}
}

func TestRunKeysAreUnaffectedByTheRoleCheck(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	user, _ := store.CreateUser(t.Context(), "run@test.com", "Run")
	ws, _ := store.CreateWorkspace(t.Context(), user.ID, "Run WS", "run-ws")

	if _, _, err := store.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: "t1", Agent: "devops", Stage: "in_progress"},
		ws.ID, time.Hour,
	); err != nil {
		t.Fatalf("run key mint must still work: %v", err)
	}
}
