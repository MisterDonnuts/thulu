package auth

import (
	"encoding/json"
	"testing"
)

func TestMapGroupsToRole(t *testing.T) {
	t.Setenv("OCTOPUS_OIDC_GROUP_ROLE_MAP", "octo-admins:admin,octo-devs:developer,staff:viewer")
	cases := []struct {
		name   string
		groups []string
		want   Role
	}{
		{"no groups", nil, ""},
		{"unmatched", []string{"random"}, ""},
		{"single match", []string{"octo-devs"}, RoleDeveloper},
		{"highest wins", []string{"staff", "octo-admins", "octo-devs"}, RoleAdmin},
		{"unknown role in map ignored", []string{"staff"}, RoleViewer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapGroupsToRole(tc.groups); got != tc.want {
				t.Errorf("mapGroupsToRole(%v) = %q, want %q", tc.groups, got, tc.want)
			}
		})
	}
}

func TestMapGroupsToRole_NoConfig(t *testing.T) {
	t.Setenv("OCTOPUS_OIDC_GROUP_ROLE_MAP", "")
	if got := mapGroupsToRole([]string{"octo-admins"}); got != "" {
		t.Errorf("with no map configured, expected no role, got %q", got)
	}
}

func TestParseGroupsClaim(t *testing.T) {
	arr, _ := json.Marshal([]string{"a", "b"})
	if g := parseGroupsClaim(arr); len(g) != 2 || g[0] != "a" {
		t.Errorf("array form: got %v", g)
	}
	str, _ := json.Marshal("a b,c")
	if g := parseGroupsClaim(str); len(g) != 3 {
		t.Errorf("string form: expected 3 groups, got %v", g)
	}
	if g := parseGroupsClaim(nil); g != nil {
		t.Errorf("nil claim should yield nil, got %v", g)
	}
}
