package evidence_test

import (
	"testing"

	"github.com/MisterDonnuts/thulu/internal/evidence"
)

func TestEnvPermissions(t *testing.T) {
	standalone := evidence.Environment{Kind: evidence.KindStandalone, BaseURL: "https://a.example.com"}
	prodSite := evidence.Environment{Kind: evidence.KindStandalone, BaseURL: "https://a.example.com", IsProduction: true}
	projectEnv := evidence.Environment{Kind: evidence.KindProject, ProjectID: "p1", BaseURL: "https://s.example.com"}
	noURL := evidence.Environment{Kind: evidence.KindProject, ProjectID: "p1"}
	sandboxOK := evidence.Environment{Kind: evidence.KindSandbox, ProjectID: "p1",
		Sandbox: &evidence.SandboxInfo{Available: true, Port: 3000}}
	sandboxNoScript := evidence.Environment{Kind: evidence.KindSandbox, ProjectID: "p1",
		Sandbox: &evidence.SandboxInfo{Reason: evidence.ReasonNoInitScript}}

	cases := []struct {
		role   string
		env    evidence.Environment
		action string
		reason string
	}{
		{evidence.RoleViewer, standalone, evidence.ActionRun, evidence.ReasonViewer},
		{evidence.RoleViewer, standalone, evidence.ActionEdit, evidence.ReasonViewer},
		{evidence.RoleViewer, sandboxOK, evidence.ActionRun, evidence.ReasonViewer},
		{"", standalone, evidence.ActionRun, evidence.ReasonViewer},

		{evidence.RoleDeveloper, standalone, evidence.ActionRun, ""},
		{evidence.RoleDeveloper, standalone, evidence.ActionRecord, ""},
		{evidence.RoleDeveloper, standalone, evidence.ActionEdit, ""},
		{evidence.RoleDeveloper, standalone, evidence.ActionSecrets, ""},
		{evidence.RoleDeveloper, standalone, evidence.ActionDelete, evidence.ReasonAdminOnly},
		{evidence.RoleDeveloper, standalone, evidence.ActionLink, evidence.ReasonAdminOnly},
		{evidence.RoleDeveloper, prodSite, evidence.ActionRun, ""},
		{evidence.RoleDeveloper, prodSite, evidence.ActionEdit, evidence.ReasonProduction},
		{evidence.RoleDeveloper, prodSite, evidence.ActionSecrets, evidence.ReasonProduction},
		{evidence.RoleDeveloper, projectEnv, evidence.ActionEdit, ""},
		{evidence.RoleDeveloper, projectEnv, evidence.ActionDelete, evidence.ReasonDeleteOnProject},
		{evidence.RoleDeveloper, noURL, evidence.ActionRun, evidence.ReasonNoURL},
		{evidence.RoleDeveloper, noURL, evidence.ActionRecord, evidence.ReasonNoURL},
		{evidence.RoleDeveloper, sandboxOK, evidence.ActionRun, ""},
		{evidence.RoleDeveloper, sandboxOK, evidence.ActionRecord, evidence.ReasonSandboxNoRecord},
		{evidence.RoleDeveloper, sandboxOK, evidence.ActionEdit, ""},
		{evidence.RoleDeveloper, sandboxNoScript, evidence.ActionRun, evidence.ReasonNoInitScript},

		{evidence.RoleAdmin, prodSite, evidence.ActionEdit, ""},
		{evidence.RoleAdmin, prodSite, evidence.ActionSecrets, ""},
		{evidence.RoleAdmin, standalone, evidence.ActionDelete, ""},
		{evidence.RoleAdmin, standalone, evidence.ActionLink, ""},
		{evidence.RoleAdmin, projectEnv, evidence.ActionDelete, evidence.ReasonDeleteOnProject},
		{evidence.RoleAdmin, projectEnv, evidence.ActionLink, evidence.ReasonDeleteOnProject},
		{evidence.RoleAdmin, sandboxOK, evidence.ActionDelete, evidence.ReasonSandboxFixed},
		{evidence.RoleAdmin, sandboxOK, evidence.ActionLink, evidence.ReasonSandboxFixed},
		{evidence.RoleOwner, prodSite, evidence.ActionDelete, ""},
	}
	for _, c := range cases {
		got := evidence.EnvPermissions(c.role, c.env)[c.action]
		want := evidence.Permission{Allowed: c.reason == "", Reason: c.reason}
		if got != want {
			t.Errorf("%q on %s (production %v) %s: got %+v, want %+v", c.role, c.env.Kind, c.env.IsProduction, c.action, got, want)
		}
	}
}

func TestWorkspacePermissions(t *testing.T) {
	for _, c := range []struct {
		role      string
		create    bool
		createPrd bool
		reasonPrd string
	}{
		{evidence.RoleViewer, false, false, evidence.ReasonViewer},
		{evidence.RoleDeveloper, true, false, evidence.ReasonProduction},
		{evidence.RoleAdmin, true, true, ""},
		{evidence.RoleOwner, true, true, ""},
	} {
		p := evidence.WorkspacePermissions(c.role)
		if p[evidence.ActionCreateEnvironment].Allowed != c.create || p[evidence.ActionCreateCase].Allowed != c.create ||
			p[evidence.ActionRun].Allowed != c.create {
			t.Errorf("%s: %+v", c.role, p)
		}
		if prd := p[evidence.ActionCreateProductionEnvironment]; prd.Allowed != c.createPrd || prd.Reason != c.reasonPrd {
			t.Errorf("%s production: %+v", c.role, prd)
		}
		if evidence.CanSetProduction(c.role) != c.createPrd {
			t.Errorf("%s CanSetProduction", c.role)
		}
	}
}
