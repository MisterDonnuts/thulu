package auth

import (
	"context"
	"testing"
)

func TestCallerRole(t *testing.T) {
	bg := context.Background()
	key := func(info APIKeyInfo) context.Context { return withKeyAuth(bg, &info) }
	cases := []struct {
		name string
		ctx  context.Context
		want Role
	}{
		{"no credential", bg, ""},
		{"signed-in viewer", WithClaims(bg, &Claims{Role: RoleViewer}), RoleViewer},
		{"signed-in admin", WithClaims(bg, &Claims{Role: RoleAdmin}), RoleAdmin},
		{"operator key keeps its role", key(APIKeyInfo{Role: RoleAdmin}), RoleAdmin},
		{"viewer key", key(APIKeyInfo{Role: RoleViewer}), RoleViewer},
		{"run key capped at developer", key(APIKeyInfo{Role: RoleOwner, Scope: RunScope{TaskID: "t1"}}), RoleDeveloper},
		{"run key below developer stays", key(APIKeyInfo{Role: RoleViewer, Scope: RunScope{Agent: "a"}}), RoleViewer},
		{"key without a role", key(APIKeyInfo{}), ""},
		{"a key wins over claims", WithClaims(key(APIKeyInfo{Role: RoleViewer}), &Claims{Role: RoleOwner}), RoleViewer},
	}
	for _, c := range cases {
		if got := CallerRole(c.ctx); got != c.want {
			t.Errorf("%s: CallerRole = %q, want %q", c.name, got, c.want)
		}
	}

	t.Setenv("OCTOPUS_DEV_MODE", "1")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if got := CallerRole(context.WithValue(bg, devSessionKey{}, true)); got != RoleOwner {
		t.Errorf("dev session: CallerRole = %q, want owner", got)
	}
}
