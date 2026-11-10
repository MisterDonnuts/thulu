package auth

import "testing"

func TestRoleLabel(t *testing.T) {
	if got := RoleLabel("es", "viewer"); got != "lector" {
		t.Errorf("viewer in es = %q", got)
	}
	if got := RoleLabel("en", "admin"); got != "admin" {
		t.Errorf("admin in en = %q", got)
	}
	if got := RoleLabel("es", "robot"); got != "robot" {
		t.Errorf("an unknown role changed: %q", got)
	}
}

func TestPermissionsLabel(t *testing.T) {
	cases := []struct {
		perms []Permission
		want  string
	}{
		{[]Permission{PermAll}, "Todo"},
		{nil, "sin permisos"},
		{
			[]Permission{PermTasksRead, PermTasksWrite, PermProjectsRead, PermInternalUse, "future:thing"},
			"Lectura: Tareas, Proyectos · Escritura: Tareas · Rutas de servicio · future:thing",
		},
	}
	for _, c := range cases {
		if got := PermissionsLabel("es", c.perms); got != c.want {
			t.Errorf("PermissionsLabel(%v) = %q, want %q", c.perms, got, c.want)
		}
	}
	if got := PermissionsLabel("en", []Permission{PermRunsRead}); got != "Read: Runs & usage" {
		t.Errorf("en = %q", got)
	}
}

func TestAuditTimeLabel(t *testing.T) {
	if got := AuditTimeLabel("es", "2026-10-18 17:17 UTC"); got != "18 oct 2026, 17:17 UTC" {
		t.Errorf("es = %q", got)
	}
	if got := AuditTimeLabel("en", "2026-10-18 17:17 UTC"); got != "Oct 18, 2026, 17:17 UTC" {
		t.Errorf("en = %q", got)
	}
	if got := AuditTimeLabel("es", "mañana"); got != "mañana" {
		t.Errorf("unparseable time changed: %q", got)
	}
}
