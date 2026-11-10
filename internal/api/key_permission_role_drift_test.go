package api

import (
	"os"
	"regexp"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestPermissionMinRoleMatchesRoutes(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}

	rank := map[auth.Role]int{
		auth.RoleViewer: 1, auth.RoleDeveloper: 2, auth.RoleAdmin: 3, auth.RoleOwner: 4,
	}
	roleByName := map[string]auth.Role{
		"RoleViewer": auth.RoleViewer, "RoleDeveloper": auth.RoleDeveloper,
		"RoleAdmin": auth.RoleAdmin, "RoleOwner": auth.RoleOwner,
	}

	re := regexp.MustCompile(`HandleFunc\("([A-Z]+) ([^"]+)",\s*rr\(auth\.(Role\w+)`)
	matches := re.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no rr-gated routes parsed from server.go — the regex has drifted, not the table")
	}

	minRank := map[auth.Permission]int{}
	for _, m := range matches {
		method, path, roleName := m[1], m[2], m[3]
		role, ok := roleByName[roleName]
		if !ok {
			t.Fatalf("unknown role %q on %s %s", roleName, method, path)
		}
		perm := auth.PermissionForRequest(method, path)
		if perm == "" {
			continue
		}
		if cur, seen := minRank[perm]; !seen || rank[role] < cur {
			minRank[perm] = rank[role]
		}
	}

	for perm, want := range minRank {
		got := rank[auth.MinRoleForPermission(perm)]
		if got != want {
			t.Errorf("MinRoleForPermission(%s) = rank %d, but its lowest-role route needs rank %d — update the table in auth/permissions.go",
				perm, got, want)
		}
	}

	for _, perm := range auth.AllPermissions() {
		tableRank := rank[auth.MinRoleForPermission(perm)]
		if tableRank <= rank[auth.RoleViewer] {
			continue
		}
		if _, seen := minRank[perm]; !seen {
			t.Errorf("the table raises %s above viewer, but no rr-gated route requires it — the entry is stale",
				perm)
		}
	}
}
