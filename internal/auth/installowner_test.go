package auth

import (
	"context"
	"testing"
)

type fakeMembers map[string]Role

func (f fakeMembers) GetMemberRole(_ context.Context, ws, user string) (Role, error) {
	return f[ws+"/"+user], nil
}

func TestIsInstallationOwner(t *testing.T) {
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "ws-op")
	members := fakeMembers{"ws-op/u-owner": RoleOwner, "ws-op/u-admin": RoleAdmin}
	bg := context.Background()
	session := func(ws, user string, role Role) context.Context {
		return WithClaims(bg, &Claims{UserID: user, WorkspaceID: ws, Role: role})
	}
	key := func(ws string, role Role, scope RunScope) context.Context {
		return withKeyAuth(bg, &APIKeyInfo{WorkspaceID: ws, Role: role, Scope: scope})
	}
	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"owner session in the operator workspace", session("ws-op", "u-owner", RoleOwner), true},
		{"owner session opened in another workspace", session("ws-tenant", "u-owner", RoleOwner), true},
		{"admin of the operator workspace", session("ws-op", "u-admin", RoleAdmin), false},
		{"owner of another workspace only", session("ws-tenant", "u-other", RoleOwner), false},
		{"owner key minted in the operator workspace", key("ws-op", RoleOwner, RunScope{}), true},
		{"admin key minted in the operator workspace", key("ws-op", RoleAdmin, RunScope{}), false},
		{"owner key of another workspace", key("ws-tenant", RoleOwner, RunScope{}), false},
		{"run key minted in the operator workspace", key("ws-op", RoleOwner, RunScope{TaskID: "t1"}), false},
		{"anonymous", bg, false},
	}
	for _, c := range cases {
		if got := IsInstallationOwner(c.ctx, members); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if IsInstallationOwner(session("ws-tenant", "u-owner", RoleOwner), nil) {
		t.Error("a session in another workspace passed without a member lookup")
	}
}

func TestIsInstallationOwner_UnsetMeansNobody(t *testing.T) {
	t.Setenv("OCTOPUS_OPERATOR_WORKSPACE", "")
	ctx := WithClaims(context.Background(), &Claims{UserID: "u", WorkspaceID: "ws", Role: RoleOwner})
	if IsInstallationOwner(ctx, fakeMembers{"ws/u": RoleOwner}) {
		t.Error("an owner passed with OCTOPUS_OPERATOR_WORKSPACE unset")
	}
}
