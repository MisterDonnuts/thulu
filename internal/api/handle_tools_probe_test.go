package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/mcp"
)

type countingToolsProber struct{ calls int }

func (p *countingToolsProber) Tools(context.Context, string) ([]mcp.ToolInfo, error) {
	p.calls++
	return []mcp.ToolInfo{{Name: "task_get", Description: "read a task"}}, nil
}

func (p *countingToolsProber) Ping(context.Context, string) error { return nil }

func TestToolsProbeIsForAdminsAndCached(t *testing.T) {
	prober := &countingToolsProber{}
	fwd := mcp.NewForwarder()
	fwd.Set(mcp.NameToEnvKey("octopus-api"), "http://octopus-api:3000/sse")
	srv := &Server{agentsDir: t.TempDir()}
	srv.mcp.prober = prober
	srv.mcp.fwd = fwd

	defs := func(role auth.Role) []ToolDef {
		r := httptest.NewRequest(http.MethodGet, "/tools", nil)
		r = r.WithContext(auth.WithClaims(r.Context(), &auth.Claims{
			UserID: "u", WorkspaceID: "ws-tenant", Role: role,
		}))
		if !callerIsAdmin(r) {
			return nil
		}
		out := []ToolDef{{Name: "octopus-api"}}
		srv.enrichExposedTools(r.Context(), out)
		return out
	}

	if out := defs(auth.RoleViewer); out != nil || prober.calls != 0 {
		t.Fatalf("a viewer triggered the probe (%d calls)", prober.calls)
	}
	out := defs(auth.RoleAdmin)
	if len(out[0].Tools) != 1 || out[0].Tools[0].Name != "task_get" {
		t.Fatalf("a workspace admin got no tool list: %+v", out)
	}
	defs(auth.RoleAdmin)
	if prober.calls != 1 {
		t.Fatalf("probe ran %d times for two loads inside the TTL, want 1", prober.calls)
	}
}
