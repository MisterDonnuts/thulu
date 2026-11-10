package auth

import (
	"net/http/httptest"
	"testing"
)

func TestActorFromRequest(t *testing.T) {
	t.Setenv("OCTOPUS_DEV_MODE", "")

	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(withKeyAuth(r.Context(), &APIKeyInfo{
		ID: "k1", Name: "ci-deployer", WorkspaceID: "ws1", Role: RoleDeveloper,
	}))
	a := ActorFromRequest(r)
	if a.Type != ActorAPIKey || a.ID != "key:k1" || a.Label != "ci-deployer" || a.WorkspaceID != "ws1" {
		t.Errorf("api key actor = %+v", a)
	}

	r = httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(withKeyAuth(r.Context(), &APIKeyInfo{
		ID: "k2", WorkspaceID: "ws1", Role: RoleDeveloper,
		Scope: RunScope{TaskID: "t9", Agent: "coder", Stage: "dev"},
	}))
	a = ActorFromRequest(r)
	if a.Type != ActorAgent || a.ID != "key:k2" || a.WorkspaceID != "ws1" || a.Label == "" {
		t.Errorf("run key actor = %+v, want an agent with a derived label", a)
	}

	r = httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(WithClaims(r.Context(), &Claims{UserID: "u1", Email: "ana@acme.com", WorkspaceID: "ws2"}))
	a = ActorFromRequest(r)
	if a.Type != ActorUser || a.ID != "u1" || a.Email != "ana@acme.com" || a.WorkspaceID != "ws2" {
		t.Errorf("session actor = %+v", a)
	}

	if a := ActorFromRequest(httptest.NewRequest("GET", "/", nil)); a.Type != ActorSystem {
		t.Errorf("anonymous actor = %+v, want system", a)
	}
	if a := ActorFromRequest(nil); a.Type != ActorSystem {
		t.Errorf("nil request actor = %+v, want system", a)
	}
}
