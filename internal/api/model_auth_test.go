package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/engine/claudecode"
	"github.com/MisterDonnuts/thulu/internal/workloadid"
)

const oauthToken = "sk-ant-oat01-" + "testtokentesttokentesttokentesttoken"

func opAdminToken(t *testing.T, rig *operatorRig) string {
	t.Helper()
	u, err := rig.srv.authStore.CreateUser(t.Context(), "op-admin@test.com", "Op Admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.srv.authStore.AddMember(t.Context(), rig.opWS, u.ID, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	tok, err := auth.IssueJWT(auth.Claims{UserID: u.ID, Email: u.Email, WorkspaceID: rig.opWS, Role: auth.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestOAuthKind_InstallationOwnerOnly(t *testing.T) {
	rig := newOperatorRig(t)
	adminTok := opAdminToken(t, rig)

	type caller struct {
		name       string
		token, key string
		owner      bool
	}
	callers := []caller{
		{"installation owner", rig.opToken, "", true},
		{"admin of the operator workspace", adminTok, "", false},
		{"owner of another workspace", rig.tenantToken, "", false},
		{"owner key of another workspace", "", rig.tenantAdminKey, false},
	}
	for i, c := range callers {
		w := callAs(t, rig.h, http.MethodPost, "/llm-profiles", c.token, c.key, map[string]any{
			"name": "oauth-" + c.name, "engine": "claude-code", "key": oauthToken,
		})
		if c.owner && w.Code != http.StatusCreated {
			t.Errorf("POST as %s = %d, want 201: %s", c.name, w.Code, w.Body.String())
		}
		if !c.owner && (w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), OperatorWorkspaceEnv)) {
			t.Errorf("POST as %s = %d, want 403 naming %s: %s", c.name, w.Code, OperatorWorkspaceEnv, w.Body.String())
		}
		w = callAs(t, rig.h, http.MethodPost, "/llm-profiles", c.token, c.key, map[string]any{
			"name": "oauth-url-" + c.name, "engine": "claude-code", "key": oauthToken,
			"base_url": "https://llm.example.com",
		})
		if !c.owner && w.Code != http.StatusForbidden {
			t.Errorf("POST with base_url as %s = %d, want 403: %s", c.name, w.Code, w.Body.String())
		}

		w = callAs(t, rig.h, http.MethodPost, "/llm-profiles", c.token, c.key, map[string]any{
			"name": "api-" + c.name, "engine": "claude-code", "key": "sk-ant-api03-" + strings.Repeat("k", 30+i),
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("create api-key profile as %s = %d: %s", c.name, w.Code, w.Body.String())
		}
		var prof struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &prof)
		w = callAs(t, rig.h, http.MethodPatch, "/llm-profiles/"+prof.ID, c.token, c.key, map[string]any{"key": oauthToken})
		if c.owner != (w.Code == http.StatusOK) || (!c.owner && w.Code != http.StatusForbidden) {
			t.Errorf("PATCH as %s = %d (owner=%v): %s", c.name, w.Code, c.owner, w.Body.String())
		}

		w = callAs(t, rig.h, http.MethodPut, "/llm-key", c.token, c.key, map[string]any{"engine": "claude-code", "key": oauthToken})
		if c.owner == (w.Code == http.StatusForbidden) {
			t.Errorf("PUT /llm-key as %s = %d (owner=%v): %s", c.name, w.Code, c.owner, w.Body.String())
		}

		w = callAs(t, rig.h, http.MethodGet, "/engines", c.token, c.key, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /engines as %s = %d", c.name, w.Code)
		}
		var list struct {
			Engines []EngineInfo `json:"engines"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		sawOAuth := false
		for _, e := range list.Engines {
			for _, k := range e.KeyKinds {
				if k.OwnerOnly {
					sawOAuth = true
				}
			}
		}
		if sawOAuth != c.owner {
			t.Errorf("GET /engines as %s lists the owner-only kind = %v, want %v", c.name, sawOAuth, c.owner)
		}
	}
}

func TestOAuthKind_StoredTokenStaysWithOwner(t *testing.T) {
	rig := newOperatorRig(t)
	adminTok := opAdminToken(t, rig)

	w := callAs(t, rig.h, http.MethodPost, "/llm-profiles", rig.opToken, "", map[string]any{
		"name": "owner-oauth", "engine": "claude-code", "key": oauthToken,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("owner create = %d: %s", w.Code, w.Body.String())
	}
	var prof struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &prof)
	path := "/llm-profiles/" + prof.ID

	for _, body := range []map[string]any{
		{"base_url": "https://attacker.example"},
		{"base_url": "https://attacker.example", "auth_header": "x-api-key"},
		{"name": "renamed"},
	} {
		w = callAs(t, rig.h, http.MethodPatch, path, adminTok, "", body)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), OperatorWorkspaceEnv) {
			t.Errorf("admin PATCH %v = %d, want 403: %s", body, w.Code, w.Body.String())
		}
	}
	got, err := rig.srv.secretsStore.GetLLMProfile(t.Context(), rig.opWS, prof.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != "" || got.Name != "owner-oauth" {
		t.Errorf("a refused PATCH changed the profile: %+v", got)
	}

	if w = callAs(t, rig.h, http.MethodPatch, path, rig.opToken, "", map[string]any{"model": "claude-sonnet-4-5"}); w.Code != http.StatusOK {
		t.Errorf("owner PATCH = %d: %s", w.Code, w.Body.String())
	}

	apiKey := "sk-ant-api03-" + strings.Repeat("a", 32)
	if w = callAs(t, rig.h, http.MethodPatch, path, adminTok, "", map[string]any{"key": apiKey}); w.Code != http.StatusOK {
		t.Fatalf("admin key replacement = %d: %s", w.Code, w.Body.String())
	}
	if key, err := rig.srv.secretsStore.GetLLMProfileCredential(t.Context(), rig.opWS, prof.ID); err != nil || key != apiKey {
		t.Errorf("stored key after replacement = %q, %v", key, err)
	}
	if w = callAs(t, rig.h, http.MethodPatch, path, adminTok, "", map[string]any{"name": "renamed"}); w.Code != http.StatusOK {
		t.Errorf("admin PATCH after takeover = %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkloadIdentity_IssuerAndFederationProfile(t *testing.T) {
	rig := newOperatorRig(t)

	if w := callAs(t, rig.h, http.MethodGet, workloadid.JWKSPath, "", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("JWKS with no issuer = %d, want 404", w.Code)
	}
	fed := map[string]any{
		"federation_rule_id": "fdrl_01abc", "organization_id": "123e4567-e89b-12d3-a456-426614174000",
		"service_account_id": "svac_01abc",
	}
	if w := callAs(t, rig.h, http.MethodPost, "/llm-profiles", "", rig.tenantAdminKey, map[string]any{
		"name": "fed", "engine": "claude-code", "federation": fed,
	}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), workloadid.IssuerEnv) {
		t.Errorf("federation profile with no issuer = %d, want 400 naming %s: %s", w.Code, workloadid.IssuerEnv, w.Body.String())
	}

	t.Setenv(workloadid.IssuerEnv, "https://thulu.example.com")
	w := callAs(t, rig.h, http.MethodGet, workloadid.DiscoveryPath, "", "", nil)
	var disc workloadid.Discovery
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &disc) != nil ||
		disc.Issuer != "https://thulu.example.com" || disc.JWKSURI != "https://thulu.example.com/.well-known/jwks.json" {
		t.Fatalf("discovery = %d %s", w.Code, w.Body.String())
	}
	w = callAs(t, rig.h, http.MethodGet, workloadid.JWKSPath, "", "", nil)
	var set workloadid.JWKS
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &set) != nil || len(set.Keys) != 1 ||
		set.Keys[0].Alg != "ES256" || set.Keys[0].Kid == "" {
		t.Fatalf("jwks = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"d"`) {
		t.Fatal("the published key set carries a private key")
	}

	w = callAs(t, rig.h, http.MethodGet, "/workload-identity", "", rig.tenantAdminKey, nil)
	var view workloadIdentityView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil || !view.Configured ||
		view.Issuer != "https://thulu.example.com" || view.CanRotate ||
		view.SubjectPrefix != "octopus:workspace:"+rig.tenantWS+":" {
		t.Errorf("GET /workload-identity = %d %s", w.Code, w.Body.String())
	}
	if w := callAs(t, rig.h, http.MethodPost, "/workload-identity/rotate", "", rig.tenantAdminKey, nil); w.Code != http.StatusForbidden {
		t.Errorf("rotate as a tenant = %d, want 403", w.Code)
	}
	if w := callAs(t, rig.h, http.MethodPost, "/workload-identity/rotate", rig.opToken, "", nil); w.Code != http.StatusOK {
		t.Errorf("rotate as the owner = %d: %s", w.Code, w.Body.String())
	}

	w = callAs(t, rig.h, http.MethodPost, "/llm-profiles", "", rig.tenantAdminKey, map[string]any{
		"name": "fed", "engine": "claude-code", "federation": fed,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create federation profile = %d: %s", w.Code, w.Body.String())
	}
	var prof struct {
		ID         string            `json:"id"`
		HasKey     bool              `json:"has_key"`
		Federation map[string]string `json:"federation"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &prof)
	if prof.HasKey || prof.Federation["federation_rule_id"] != "fdrl_01abc" {
		t.Errorf("federation profile = %s", w.Body.String())
	}
	for name, body := range map[string]map[string]any{
		"with a key":       {"name": "f2", "engine": "claude-code", "federation": fed, "key": "sk-ant-api03-xxxxxxxxxxxxxxxxxxxxxx"},
		"with a base_url":  {"name": "f3", "engine": "claude-code", "federation": fed, "base_url": "https://llm.example.com"},
		"bad rule id":      {"name": "f4", "engine": "claude-code", "federation": map[string]any{"federation_rule_id": "x", "organization_id": fed["organization_id"], "service_account_id": "svac_1"}},
		"kind without ids": {"name": "f5", "engine": "claude-code", "kind": "federation"},
		"codex":            {"name": "f6", "engine": "codex", "federation": fed},
	} {
		if w := callAs(t, rig.h, http.MethodPost, "/llm-profiles", "", rig.tenantAdminKey, body); w.Code != http.StatusBadRequest {
			t.Errorf("federation profile %s = %d, want 400: %s", name, w.Code, w.Body.String())
		}
	}
	w = callAs(t, rig.h, http.MethodPatch, "/llm-profiles/"+prof.ID, "", rig.tenantAdminKey, map[string]any{"name": "fed-renamed"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "fdrl_01abc") {
		t.Errorf("rename = %d %s", w.Code, w.Body.String())
	}
	w = callAs(t, rig.h, http.MethodPatch, "/llm-profiles/"+prof.ID, "", rig.tenantAdminKey,
		map[string]any{"key": "sk-ant-api03-yyyyyyyyyyyyyyyyyyyyyyyy"})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "fdrl_01abc") || !strings.Contains(w.Body.String(), `"has_key":true`) {
		t.Errorf("key over federation = %d %s", w.Code, w.Body.String())
	}
}

// In Kubernetes the subscription token is off unless the operator opts in, for the installation
// owner too; the opt-in brings back the owner-only rule above.
func TestOAuthKind_KubernetesNeedsTheOperatorsOptIn(t *testing.T) {
	t.Setenv("OCTOPUS_AGENT_RUNTIME", "kubernetes")
	t.Setenv(claudecode.KubeOAuthOptInEnv, "")
	rig := newOperatorRig(t)

	listsOAuth := func() bool {
		w := callAs(t, rig.h, http.MethodGet, "/engines", rig.opToken, "", nil)
		var list struct {
			Engines []EngineInfo `json:"engines"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		for _, e := range list.Engines {
			for _, k := range e.KeyKinds {
				if k.Prefix == "sk-ant-oat" {
					return true
				}
			}
		}
		return false
	}
	if listsOAuth() {
		t.Error("GET /engines offers the subscription token in Kubernetes without the opt-in")
	}
	w := callAs(t, rig.h, http.MethodPost, "/llm-profiles", rig.opToken, "", map[string]any{
		"name": "oauth", "engine": "claude-code", "key": oauthToken,
	})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), claudecode.KubeOAuthOptInEnv) {
		t.Errorf("POST /llm-profiles as owner = %d, want 403 naming %s: %s", w.Code, claudecode.KubeOAuthOptInEnv, w.Body.String())
	}
	w = callAs(t, rig.h, http.MethodPut, "/llm-key", rig.opToken, "", map[string]any{"engine": "claude-code", "kind": "oauth", "key": oauthToken})
	if w.Code != http.StatusForbidden {
		t.Errorf("PUT /llm-key as owner = %d, want 403: %s", w.Code, w.Body.String())
	}

	t.Setenv(claudecode.KubeOAuthOptInEnv, "1")
	if !listsOAuth() {
		t.Error("GET /engines hides the subscription token from the owner with the opt-in on")
	}
	w = callAs(t, rig.h, http.MethodPost, "/llm-profiles", rig.opToken, "", map[string]any{
		"name": "oauth", "engine": "claude-code", "key": oauthToken,
	})
	if w.Code != http.StatusCreated {
		t.Errorf("POST /llm-profiles as owner with the opt-in = %d, want 201: %s", w.Code, w.Body.String())
	}
	w = callAs(t, rig.h, http.MethodPost, "/llm-profiles", rig.tenantToken, "", map[string]any{
		"name": "oauth", "engine": "claude-code", "key": oauthToken,
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("POST /llm-profiles as another workspace's owner with the opt-in = %d, want 403", w.Code)
	}
}
