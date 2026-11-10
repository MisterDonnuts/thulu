package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/entitlements"
	"github.com/MisterDonnuts/thulu/internal/mcp"
	"github.com/MisterDonnuts/thulu/internal/testpg"
)

func callAs(t *testing.T, h http.Handler, method, path, token, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("X-API-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type operatorRig struct {
	srv                   *Server
	h                     http.Handler
	opToken, tenantToken  string
	opWS, tenantWS        string
	opKey, tenantAdminKey string
}

func newOperatorRig(t *testing.T) *operatorRig {
	t.Helper()
	srv, h := newIntegrationServer(t)
	es, err := entitlements.NewStore(testpg.New(t), entitlements.PlanFree)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetEntitlements(es)
	reg, err := mcp.NewRegistry(srv.taskStore.DB())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	srv.SetMCPRegistry(reg)

	rig := &operatorRig{srv: srv, h: h}
	rig.opToken, rig.opWS = mintWorkspaceUserAs(t, srv, "operator@test.com", "Operator", auth.RoleOwner)
	rig.tenantToken, rig.tenantWS = mintWorkspaceUserAs(t, srv, "tenant-admin@test.com", "Tenant", auth.RoleOwner)
	t.Setenv(OperatorWorkspaceEnv, rig.opWS)
	rig.opKey = mintKey(t, srv, rig.opWS, auth.RoleAdmin, auth.PermAll)
	rig.tenantAdminKey = mintKey(t, srv, rig.tenantWS, auth.RoleOwner, auth.PermAll)
	return rig
}

func TestOperatorGate_InstallWideWritesRefuseTenantAdmins(t *testing.T) {
	rig := newOperatorRig(t)

	type call struct {
		method, path string
		body         any
		operatorWant int
	}
	calls := []call{
		{http.MethodPut, "/settings/sandbox.max", "4", http.StatusNoContent},
		{http.MethodPost, "/mcps/register", map[string]any{"name": "op-mcp", "url": "https://mcp.example.com/sse"}, http.StatusCreated},
		{http.MethodPatch, "/mcps/op-mcp", map[string]any{"description": "changed"}, http.StatusOK},
		{http.MethodPut, "/entitlements", map[string]string{"plan": "enterprise"}, http.StatusOK},
		{http.MethodPut, "/github/app", map[string]any{}, http.StatusBadRequest},
		{http.MethodDelete, "/github/app", nil, 0},
		{http.MethodDelete, "/mcps/op-mcp", nil, 0},
	}
	for _, c := range calls {
		for name, cred := range map[string][2]string{
			"tenant admin session": {rig.tenantToken, ""},
			"tenant owner key":     {"", rig.tenantAdminKey},
		} {
			w := callAs(t, rig.h, c.method, c.path, cred[0], cred[1], c.body)
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), OperatorWorkspaceEnv) {
				t.Errorf("%s %s as %s = %d, want 403 naming %s: %s",
					c.method, c.path, name, w.Code, OperatorWorkspaceEnv, w.Body.String())
			}
		}
		w := callAs(t, rig.h, c.method, c.path, rig.opToken, "", c.body)
		if w.Code == http.StatusForbidden || (c.operatorWant != 0 && w.Code != c.operatorWant) {
			t.Errorf("%s %s as the operator = %d, want %d: %s", c.method, c.path, w.Code, c.operatorWant, w.Body.String())
		}
	}

	for _, action := range []string{"start", "stop", "restart"} {
		w := httptest.NewRecorder()
		r := tenantRequest(http.MethodPost, "/mcps/op-mcp/"+action, rig.tenantWS)
		r.SetPathValue("name", "op-mcp")
		rig.srv.runMCPAction(w, r, action)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s from a tenant admin = %d, want 403: %s", action, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	r := tenantRequest(http.MethodPost, "/mcps/op-mcp/none", rig.opWS)
	r.SetPathValue("name", "op-mcp")
	rig.srv.runMCPAction(w, r, "none")
	if w.Code != http.StatusBadRequest {
		t.Errorf("an unknown action from the operator = %d, want 400 past the gate: %s", w.Code, w.Body.String())
	}

	ent, err := rig.srv.entitlements.Get(rig.tenantWS)
	if err != nil || ent.Plan != entitlements.PlanFree {
		t.Errorf("tenant plan = %+v (%v), want the Free default", ent, err)
	}
	w = callAs(t, rig.h, http.MethodPut, "/entitlements?workspace="+rig.tenantWS, "", rig.opKey,
		map[string]string{"plan": "business"})
	if w.Code != http.StatusOK {
		t.Fatalf("operator key PUT /entitlements?workspace= = %d: %s", w.Code, w.Body.String())
	}
	if ent, _ := rig.srv.entitlements.Get(rig.tenantWS); ent.Plan != entitlements.PlanBusiness {
		t.Errorf("tenant plan after the operator's assignment = %s, want business", ent.Plan)
	}
}

func TestOperatorGate_RunKeysAreNotOperators(t *testing.T) {
	rig := newOperatorRig(t)
	for _, scope := range []auth.RunScope{
		{TaskID: "task-x", Agent: "developer"},
		{ChatID: "chat-x", Agent: "chat"},
	} {
		key, _, err := rig.srv.authStore.CreateRunScopedAPIKey(t.Context(), scope, rig.opWS, time.Hour)
		if err != nil {
			t.Fatalf("mint run key: %v", err)
		}
		if w := callAs(t, rig.h, http.MethodPut, "/settings/sandbox.max", "", key, "4"); w.Code != http.StatusForbidden {
			t.Errorf("PUT /settings with a %+v run key = %d, want 403", scope, w.Code)
		}
	}
}

func TestCallerIsAdmin_ReadsTheKeysRole(t *testing.T) {
	t.Parallel()
	srv, _ := newIntegrationServer(t)
	_, ws := mintWorkspaceUser(t, srv, "calleradmin@test.com", "CA")
	adminSession, _ := mintWorkspaceUserAs(t, srv, "calleradmin-a@test.com", "CA", auth.RoleAdmin)
	viewerSession, _ := mintWorkspaceUserAs(t, srv, "calleradmin-v@test.com", "CV", auth.RoleViewer)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe-admin", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strconv.FormatBool(callerIsAdmin(r))))
	})
	probe := srv.buildHandler(mux)

	runKey := func(scope auth.RunScope) string {
		k, _, err := srv.authStore.CreateRunScopedAPIKey(context.Background(), scope, ws, time.Hour)
		if err != nil {
			t.Fatalf("mint run key: %v", err)
		}
		return k
	}
	cases := []struct {
		name, token, key string
		want             bool
	}{
		{"viewer key", "", mintKey(t, srv, ws, auth.RoleViewer, auth.PermAll), false},
		{"developer key", "", mintKey(t, srv, ws, auth.RoleDeveloper, auth.PermAll), false},
		{"chat run key", "", runKey(auth.RunScope{ChatID: "c1", Agent: "chat"}), false},
		{"task run key", "", runKey(auth.RunScope{TaskID: "t1", Agent: "developer"}), false},
		{"viewer session", viewerSession, "", false},
		{"admin key", "", mintKey(t, srv, ws, auth.RoleAdmin, auth.PermAll), true},
		{"owner key", "", mintKey(t, srv, ws, auth.RoleOwner, auth.PermAll), true},
		{"admin session", adminSession, "", true},
	}
	for _, c := range cases {
		w := callAs(t, probe, http.MethodGet, "/probe-admin", c.token, c.key, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: probe = %d: %s", c.name, w.Code, w.Body.String())
		}
		if got := w.Body.String() == "true"; got != c.want {
			t.Errorf("%s: callerIsAdmin = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMetrics_WithoutATokenOnlyTheOperatorReads(t *testing.T) {
	t.Setenv("OCTOPUS_METRICS_TOKEN", "")
	rig := newOperatorRig(t)
	viewer, _ := mintWorkspaceUserAs(t, rig.srv, "metrics-viewer@test.com", "MV", auth.RoleViewer)

	for name, cred := range map[string][2]string{
		"tenant admin session": {rig.tenantToken, ""},
		"tenant owner key":     {"", rig.tenantAdminKey},
		"viewer session":       {viewer, ""},
	} {
		if w := callAs(t, rig.h, http.MethodGet, "/metrics", cred[0], cred[1], nil); w.Code != http.StatusForbidden {
			t.Errorf("GET /metrics as %s = %d, want 403", name, w.Code)
		}
	}
	if w := callAs(t, rig.h, http.MethodGet, "/metrics", rig.opToken, "", nil); w.Code != http.StatusOK {
		t.Errorf("GET /metrics as the operator = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestInfo_AnonymousCallersGetTheEditionAndTheSourceOnly(t *testing.T) {
	srv, h := newIntegrationServer(t)
	token, _ := mintWorkspaceUser(t, srv, "info@test.com", "Info")
	t.Setenv("OCTOPUS_DEV_MODE", "0")

	w := getJSON(t, h, "/info", "")
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous GET /info = %d: %s", w.Code, w.Body.String())
	}
	var anon map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &anon)
	if len(anon) != 2 || anon["edition"] == nil || anon["source_url"] != DefaultSourceURL {
		t.Errorf("anonymous /info = %v, want only the edition and the source link", anon)
	}

	w = getJSON(t, h, "/info", token)
	var full map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &full)
	if _, ok := full["version"]; !ok || w.Code != http.StatusOK {
		t.Errorf("signed-in /info = %d %v, want the version too", w.Code, full)
	}
}

func TestCredentialForwardingURLError(t *testing.T) {
	cases := map[string]bool{
		"https://mcp.example.com/sse":                       true,
		"http://127.0.0.1:3014/sse":                         true,
		"http://localhost:3014/sse":                         true,
		"http://10.20.30.40:3014/sse":                       true,
		"http://10.0.0.5/sse":                               true,
		"http://thulu-github:3000/sse":                      true,
		"http://thulu-github.octopus.svc:3000/sse":          true,
		"http://thulu-github.octopus.svc.cluster.local/sse": true,
		"http://host.docker.internal:3014/sse":              true,
		"http://mcp.example.com/sse":                        false,
		"http://8.8.8.8/sse":                                false,
		"http://mcp.example.com.evil.io/thulu.svc/sse":      false,
		"ftp://":                false,
		"not a url":             false,
		"http://[::1]:3014/sse": true,
		"http://metadata.google.internal.attacker.com/sse":   false,
		"http://attacker.com/?host=thulu-github.octopus.svc": false,
	}
	for raw, ok := range cases {
		if got := credentialForwardingURLError(raw) == ""; got != ok {
			t.Errorf("credentialForwardingURLError(%q) allowed=%v, want %v", raw, got, ok)
		}
	}
}

func TestMCPRegistry_ForwardedCredentialsNeedAProtectedTransport(t *testing.T) {
	rig := newOperatorRig(t)
	reg := func(name, url string, forwards bool) int {
		return callAs(t, rig.h, http.MethodPost, "/mcps/register", rig.opToken, "",
			map[string]any{"name": name, "url": url, "forwards_credential": forwards}).Code
	}
	if c := reg("public-http", "http://mcp.example.com/sse", true); c != http.StatusBadRequest {
		t.Errorf("forwarding to public http = %d, want 400", c)
	}
	if c := reg("public-https", "https://mcp.example.com/sse", true); c != http.StatusCreated {
		t.Errorf("forwarding to https = %d, want 201", c)
	}
	if c := reg("in-cluster", "http://thulu-github.octopus.svc:3000/sse", true); c != http.StatusCreated {
		t.Errorf("forwarding to a cluster service = %d, want 201", c)
	}
	if c := reg("plain", "http://mcp.example.com/sse", false); c != http.StatusCreated {
		t.Errorf("a non-forwarding public http server = %d, want 201", c)
	}

	patch := func(name string, body map[string]any) int {
		return callAs(t, rig.h, http.MethodPatch, "/mcps/"+name, rig.opToken, "", body).Code
	}
	if c := patch("plain", map[string]any{"forwards_credential": true}); c != http.StatusBadRequest {
		t.Errorf("turning forwarding on for a public http URL = %d, want 400", c)
	}
	if c := patch("public-https", map[string]any{"url": "http://mcp.example.com/sse"}); c != http.StatusBadRequest {
		t.Errorf("moving a forwarding server to public http = %d, want 400", c)
	}
	if c := patch("public-https", map[string]any{"url": "https://other.example.com/sse"}); c != http.StatusOK {
		t.Errorf("moving a forwarding server to another https URL = %d, want 200", c)
	}
}

func TestDeleteWorkspace_KeysConfinedToTheirWorkspace(t *testing.T) {
	srv, h := newIntegrationServer(t)
	es, _ := entitlements.NewStore(newEntDB(t), entitlements.PlanFree)
	srv.SetEntitlements(es)
	seedWorkspaceData(t, srv, "WSX", "acme", "owner-x")
	seedWorkspaceData(t, srv, "WSY", "beta", "owner-y")
	seedWorkspaceData(t, srv, "WSZ", "gamma", "owner-z")
	t.Setenv(OperatorWorkspaceEnv, "WSZ")

	del := func(ws, slug, key string) int {
		return callAs(t, h, http.MethodDelete, "/workspaces/"+ws, "", key, map[string]string{"confirm": slug}).Code
	}
	runKey, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: "t1", Agent: "developer"}, "WSX", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{
		"another tenant's owner key": mintKey(t, srv, "WSY", auth.RoleOwner, auth.PermAll),
		"the workspace's admin key":  mintKey(t, srv, "WSX", auth.RoleAdmin, auth.PermAll),
		"the workspace's run key":    runKey,
		"the operator's admin key":   mintKey(t, srv, "WSZ", auth.RoleAdmin, auth.PermAll),
	} {
		if c := del("WSX", "acme", key); c != http.StatusForbidden {
			t.Errorf("DELETE /workspaces/WSX with %s = %d, want 403", name, c)
		}
	}
	assertCount(t, srv.authStore.DB(), `SELECT COUNT(*) FROM workspaces WHERE id='WSX'`, 1, "WSX survives")

	if c := del("WSX", "acme", mintKey(t, srv, "WSX", auth.RoleOwner, auth.PermAll)); c != http.StatusOK {
		t.Errorf("DELETE /workspaces/WSX with its own owner key = %d, want 200", c)
	}
	if c := del("WSY", "beta", mintKey(t, srv, "WSZ", auth.RoleOwner, auth.PermAll)); c != http.StatusOK {
		t.Errorf("DELETE /workspaces/WSY with the operator's owner key = %d, want 200", c)
	}
}
