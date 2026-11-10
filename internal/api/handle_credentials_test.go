package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/types"
)

type stubProvider struct {
	id     string
	result credentials.Introspection
	err    error
	seen   []string
}

func (p *stubProvider) Spec() credentials.ProviderSpec {
	return credentials.ProviderSpec{
		ID:    p.id,
		Label: "Stub " + p.id,
		Kinds: []credentials.KindSpec{{
			ID:    "aws_keys",
			Label: "Access keys",
			Fields: []credentials.FieldSpec{
				{Name: "access_key_id", Label: "Access key ID"},
				{Name: "secret_access_key", Label: "Secret access key", Secret: true},
			},
		}},
	}
}

func (p *stubProvider) Introspect(_ context.Context, _ string, _ map[string]string, want []string) (credentials.Introspection, error) {
	p.seen = want
	return p.result, p.err
}

func stubAWS(t *testing.T, srv *Server, caps []string) *stubProvider {
	t.Helper()
	p := &stubProvider{id: "aws", result: credentials.Introspection{
		State:        credentials.StateValid,
		Identity:     "arn:aws:iam::1:user/octopus",
		Hint:         "AKIATEST",
		Capabilities: caps,
	}}
	srv.credRegistry.Register(p)
	return p
}

const secretValue = "super-secret-value"

func awsCredBody(name string, grants []map[string]string) map[string]any {
	return map[string]any{
		"name":     name,
		"provider": "aws",
		"kind":     "aws_keys",
		"payload": map[string]string{
			"access_key_id":     "AKIATEST",
			"secret_access_key": secretValue,
		},
		"grants": grants,
	}
}

func decodeCredential(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode credential response: %v (body=%s)", err, w.Body.String())
	}
	return out
}

func fullCaps() []string {
	return []string{
		"aws:logs:StartQuery", "aws:logs:GetQueryResults", "aws:cloudwatch:GetMetricData",
	}
}

func TestIntegration_CreateCredential_ValidatesAndReturnsToolMatrix(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stub := stubAWS(t, srv, fullCaps())
	token := registerUser(t, h, "credcreate@test.com", "CC", "SecurePassword1")

	w := postJSONAuth(t, h, "/credentials", token, awsCredBody("prod-aws", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("POST /credentials = %d, body=%s", w.Code, w.Body.String())
	}
	got := decodeCredential(t, w)
	if got["state"] != "valid" {
		t.Errorf("state = %v, want valid", got["state"])
	}
	if got["identity"] != "arn:aws:iam::1:user/octopus" {
		t.Errorf("identity = %v", got["identity"])
	}
	tools, _ := got["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tool matrix returned — the operator cannot tell what the key can do")
	}
	if len(stub.seen) == 0 {
		t.Error("introspection was not given a permission list")
	}
	for _, perm := range stub.seen {
		if !strings.Contains(perm, ":") {
			t.Errorf("permission %q is not namespaced", perm)
		}
	}
}

func TestIntegration_CredentialEndpointsNeverReturnTheSecret(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, fullCaps())
	token := registerUser(t, h, "credsecret@test.com", "CS", "SecurePassword1")

	w := postJSONAuth(t, h, "/credentials", token, awsCredBody("prod-aws", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	id, _ := decodeCredential(t, w)["id"].(string)

	for _, path := range []string{"/credentials", "/credentials/" + id} {
		resp := getJSON(t, h, path, token)
		if strings.Contains(resp.Body.String(), secretValue) {
			t.Errorf("%s leaked the secret: %s", path, resp.Body.String())
		}
	}
}

func TestIntegration_CreateCredential_DuplicateNameIsConflict(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, nil)
	token := registerUser(t, h, "creddup@test.com", "CD", "SecurePassword1")

	if w := postJSONAuth(t, h, "/credentials", token, awsCredBody("dup", nil)); w.Code != http.StatusOK {
		t.Fatalf("first create = %d: %s", w.Code, w.Body.String())
	}
	if w := postJSONAuth(t, h, "/credentials", token, awsCredBody("dup", nil)); w.Code != http.StatusConflict {
		t.Errorf("duplicate create = %d, want 409", w.Code)
	}
}

func TestIntegration_CreateCredential_RejectsIncompletePayload(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, nil)
	token := registerUser(t, h, "credincomplete@test.com", "CI", "SecurePassword1")

	w := postJSONAuth(t, h, "/credentials", token, map[string]any{
		"name": "half", "provider": "aws", "kind": "aws_keys",
		"payload": map[string]string{"access_key_id": "AKIA"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("create with no secret = %d, want 400", w.Code)
	}
}

// An empty introspection blanks capabilities, and a credential with no capabilities is treated as
// capable of everything.
// A provider outage must therefore leave the stored capabilities and state untouched.
func TestIntegration_FailedValidationDoesNotWidenTheCredential(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stub := stubAWS(t, srv, []string{"aws:cloudwatch:GetMetricData"})
	token := registerUser(t, h, "credblip@test.com", "CB", "SecurePassword1")

	w := postJSONAuth(t, h, "/credentials", token, awsCredBody("narrow", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	id, _ := decodeCredential(t, w)["id"].(string)

	stub.err = errors.New("dial tcp 169.254.169.254:443: i/o timeout")
	if w := postJSONAuth(t, h, "/credentials/"+id+"/validate", token, map[string]any{}); w.Code != http.StatusOK {
		t.Fatalf("validate during an outage = %d: %s", w.Code, w.Body.String())
	}

	got := decodeCredential(t, getJSON(t, h, "/credentials/"+id, token))
	caps, _ := got["capabilities"].([]any)
	if len(caps) != 1 || caps[0] != "aws:cloudwatch:GetMetricData" {
		t.Errorf("capabilities after a failed check = %v, want them untouched", caps)
	}
	if got["state"] != "valid" {
		t.Errorf("state after a failed check = %v, want the previous verdict", got["state"])
	}
	if s, _ := got["introspect_error"].(string); s != credentials.IntrospectUnreachable {
		t.Errorf("introspect_error after a failed check = %q, want %q", s, credentials.IntrospectUnreachable)
	}
	tools, _ := got["tools"].([]any)
	for _, raw := range tools {
		row, _ := raw.(map[string]any)
		if row["tool"] != "logs_search" {
			continue
		}
		dec, _ := row["decision"].(map[string]any)
		if allowed, _ := dec["allowed"].(bool); allowed {
			t.Error("a failed check re-enabled a tool the credential cannot drive")
		}
	}
}

func TestIntegration_ToolMatrixSeparatesCapabilityFromPolicy(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, []string{"aws:cloudwatch:GetMetricData"})
	token := registerUser(t, h, "credmatrix@test.com", "CM", "SecurePassword1")

	w := postJSONAuth(t, h, "/credentials", token, awsCredBody("narrow", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	id, _ := decodeCredential(t, w)["id"].(string)

	reasons := toolReasons(t, decodeCredential(t, getJSON(t, h, "/credentials/"+id, token)))
	if reasons["thulu-cloud/logs_search"] != "missing_capability" {
		t.Errorf("logs_search reason = %q, want missing_capability", reasons["thulu-cloud/logs_search"])
	}
	if reasons["thulu-cloud/metrics_query"] != "ok" {
		t.Errorf("metrics_query reason = %q, want ok", reasons["thulu-cloud/metrics_query"])
	}

	pw := putJSON(t, h, "/credentials/"+id+"/tools", map[string]any{
		"tools": []map[string]any{
			{"server": "thulu-cloud", "tool": "metrics_query", "enabled": false},
		},
	}, token)
	if pw.Code != http.StatusOK {
		t.Fatalf("PUT tools = %d: %s", pw.Code, pw.Body.String())
	}
	reasons = toolReasons(t, decodeCredential(t, pw))
	if reasons["thulu-cloud/metrics_query"] != "tool_disabled_by_policy" {
		t.Errorf("after disabling, reason = %q", reasons["thulu-cloud/metrics_query"])
	}
}

func toolReasons(t *testing.T, view map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	tools, _ := view["tools"].([]any)
	for _, raw := range tools {
		row, _ := raw.(map[string]any)
		dec, _ := row["decision"].(map[string]any)
		key, _ := row["server"].(string)
		tool, _ := row["tool"].(string)
		reason, _ := dec["reason"].(string)
		out[key+"/"+tool] = reason
	}
	return out
}

func TestIntegration_CredentialIsWorkspaceScoped(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, nil)
	// Self-registration closes after the first user, so two workspaces cannot come from two
	// /auth/register calls.
	owner, _ := mintWorkspaceUser(t, srv, "credowner@test.com", "CO")
	other, _ := mintWorkspaceUser(t, srv, "credother@test.com", "CT")

	w := postJSONAuth(t, h, "/credentials", owner, awsCredBody("prod-aws", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	id, _ := decodeCredential(t, w)["id"].(string)

	if got := getJSON(t, h, "/credentials/"+id, other); got.Code != http.StatusNotFound {
		t.Errorf("another workspace read the credential: %d %s", got.Code, got.Body.String())
	}
	list := getJSON(t, h, "/credentials", other)
	if strings.Contains(list.Body.String(), "prod-aws") {
		t.Errorf("another workspace saw the credential in its list: %s", list.Body.String())
	}
}

func TestIntegration_DeleteCredential(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, nil)
	token := registerUser(t, h, "creddel@test.com", "CDel", "SecurePassword1")

	w := postJSONAuth(t, h, "/credentials", token, awsCredBody("goner", nil))
	id, _ := decodeCredential(t, w)["id"].(string)

	req := httptest.NewRequest(http.MethodDelete, "/credentials/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	if got := getJSON(t, h, "/credentials/"+id, token); got.Code != http.StatusNotFound {
		t.Errorf("credential survived the delete: %d", got.Code)
	}
}

func TestIntegration_CredentialsAPIRouteIsNotShadowedBySPA(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, nil)
	token := registerUser(t, h, "credspa@test.com", "CSPA", "SecurePassword1")

	w := getJSON(t, h, "/credentials", token)
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("GET /credentials served %q, want JSON — the SPA shadowed the API route", ct)
	}
}

type credFixture struct {
	srv     *Server
	h       http.Handler
	token   string
	projID  string
	envID   string
	taskID  string
	taskKey string
	credID  string
}

func newCredFixture(t *testing.T, email, name string, caps []string) *credFixture {
	t.Helper()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, caps)
	srv.agentSource = staticSource{{
		Name: "developer", Type: "developer",
		MCPByStage: map[string][]string{"in_progress": {"thulu-cloud"}},
	}}
	token := registerUser(t, h, email, name, "SecurePassword1")

	projID := createProject(t, h, token, "cred-proj-"+name)
	ew := postJSONAuth(t, h, "/projects/"+projID+"/environments", token, map[string]any{
		"name": "prod", "provider": "aws", "is_production": true,
	})
	if ew.Code != http.StatusCreated && ew.Code != http.StatusOK {
		t.Fatalf("create environment = %d: %s", ew.Code, ew.Body.String())
	}
	var env struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(ew.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode environment: %v", err)
	}

	tw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "investigate", "project_ids": []string{projID},
	})
	if tw.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", tw.Code, tw.Body.String())
	}
	var task struct {
		ID          string `json:"id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(tw.Body.Bytes(), &task); err != nil {
		t.Fatalf("decode task: %v", err)
	}

	cw := postJSONAuth(t, h, "/credentials", token, awsCredBody("prod-aws",
		[]map[string]string{{"subject_type": "environment", "subject_id": env.ID}}))
	if cw.Code != http.StatusOK {
		t.Fatalf("create credential = %d: %s", cw.Code, cw.Body.String())
	}
	credID, _ := decodeCredential(t, cw)["id"].(string)

	rawKey, _, err := srv.authStore.CreateRunScopedAPIKey(t.Context(),
		auth.RunScope{TaskID: task.ID, Agent: "developer", Stage: "in_progress"}, task.WorkspaceID, time.Hour)
	if err != nil {
		t.Fatalf("mint task key: %v", err)
	}
	return &credFixture{
		srv: srv, h: h, token: token, projID: projID, envID: env.ID,
		taskID: task.ID, taskKey: rawKey, credID: credID,
	}
}

func (f *credFixture) getWithKey(t *testing.T, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-API-Key", key)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func TestIntegration_InternalResolve_ReturnsPayloadWhenAllowed(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credresolve@test.com", "CR", fullCaps())

	w := f.getWithKey(t,
		"/internal/credentials/resolve?server=thulu-cloud&tool=logs_search&environment_id="+f.envID,
		f.taskKey)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Decision struct {
			Allowed bool `json:"allowed"`
		} `json:"decision"`
		Payload map[string]string `json:"payload"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Decision.Allowed {
		t.Fatalf("decision = %+v", out.Decision)
	}
	if out.Payload["secret_access_key"] != secretValue {
		t.Errorf("payload did not carry the secret to the MCP: %v", out.Payload)
	}
}

func TestIntegration_InternalResolve_RefusesPolicyDisabledTool(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credresolvedeny@test.com", "CRD", fullCaps())

	if w := putJSON(t, f.h, "/credentials/"+f.credID+"/tools", map[string]any{
		"tools": []map[string]any{
			{"server": "thulu-cloud", "tool": "logs_search", "enabled": false},
		},
	}, f.token); w.Code != http.StatusOK {
		t.Fatalf("PUT tools = %d: %s", w.Code, w.Body.String())
	}

	w := f.getWithKey(t,
		"/internal/credentials/resolve?server=thulu-cloud&tool=logs_search&environment_id="+f.envID,
		f.taskKey)
	if w.Code != http.StatusForbidden {
		t.Fatalf("resolve of a disabled tool = %d, want 403: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secretValue) {
		t.Fatal("a refused resolve still returned the secret")
	}
}

func TestIntegration_InternalResolve_QueryTaskCannotOverrideTheKey(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credresolvespoof@test.com", "CRS", fullCaps())

	w := f.getWithKey(t,
		"/internal/credentials/resolve?server=thulu-cloud&tool=logs_search&task=some-other-task&environment_id="+f.envID,
		f.taskKey)
	if w.Code != http.StatusForbidden {
		t.Errorf("claiming another task = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_InternalAvailable_ListsOnlyReachableCredentials(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credavail@test.com", "CA", fullCaps())

	w := f.getWithKey(t, "/internal/credentials/available?server=thulu-cloud", f.taskKey)
	if w.Code != http.StatusOK {
		t.Fatalf("available = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Credentials []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"credentials"`
		Tools []struct {
			Tool     string `json:"tool"`
			Decision struct {
				Allowed bool `json:"allowed"`
			} `json:"decision"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Credentials) != 1 || out.Credentials[0].ID != f.credID {
		t.Fatalf("credentials = %+v, want just the granted one", out.Credentials)
	}
	if strings.Contains(w.Body.String(), secretValue) {
		t.Fatal("the discovery endpoint leaked a secret")
	}
	var allowed int
	for _, tool := range out.Tools {
		if tool.Decision.Allowed {
			allowed++
		}
	}
	if allowed == 0 {
		t.Error("no tool was available despite a valid, granted credential")
	}
}

func TestIntegration_InternalResolve_RefusesAfterGrantRemoved(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credungrant@test.com", "CU", fullCaps())

	if w := putJSON(t, f.h, "/credentials/"+f.credID+"/grants",
		map[string]any{"grants": []map[string]string{}}, f.token); w.Code != http.StatusOK {
		t.Fatalf("PUT grants = %d: %s", w.Code, w.Body.String())
	}
	w := f.getWithKey(t,
		"/internal/credentials/resolve?server=thulu-cloud&tool=logs_search&environment_id="+f.envID,
		f.taskKey)
	if w.Code != http.StatusForbidden {
		t.Errorf("resolve after ungranting = %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_InternalEnvironment_RefusesForeignEnvironment(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credenvscope@test.com", "CES", fullCaps())

	otherProj := createProject(t, f.h, f.token, "other-proj")
	ew := postJSONAuth(t, f.h, "/projects/"+otherProj+"/environments", f.token, map[string]any{
		"name": "prod", "provider": "aws", "region": "eu-west-1",
	})
	if ew.Code != http.StatusCreated && ew.Code != http.StatusOK {
		t.Fatalf("create other environment = %d: %s", ew.Code, ew.Body.String())
	}
	var other struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(ew.Body.Bytes(), &other); err != nil {
		t.Fatalf("decode: %v", err)
	}

	w := f.getWithKey(t, "/internal/environments/"+other.ID, f.taskKey)
	if w.Code == http.StatusOK {
		t.Fatalf("a task read another project's environment: %s", w.Body.String())
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("refusal = %d, want 404 so it cannot be told from a missing id", w.Code)
	}
	if own := f.getWithKey(t, "/internal/environments/"+f.envID, f.taskKey); own.Code != http.StatusOK {
		t.Errorf("the task's own environment stopped resolving: %d %s", own.Code, own.Body.String())
	}
}

func TestIntegration_InternalEnvironment_CarriesNoSecret(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credenvmeta@test.com", "CEM", fullCaps())

	w := f.getWithKey(t, "/internal/environments/"+f.envID, f.taskKey)
	if w.Code != http.StatusOK {
		t.Fatalf("internal environment = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secretValue) {
		t.Fatal("the environment metadata route returned the credential payload")
	}
	for _, banned := range []string{"payload", "secret_access_key"} {
		if strings.Contains(w.Body.String(), banned) {
			t.Errorf("metadata response mentions %q", banned)
		}
	}
}

func TestIntegration_ResolveDecisionsAreAudited(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credaudit@test.com", "CAu", fullCaps())

	f.getWithKey(t, "/internal/credentials/resolve?server=thulu-cloud&tool=logs_search&environment_id="+f.envID, f.taskKey)
	f.getWithKey(t, "/internal/credentials/resolve?server=thulu-cloud&tool=deploys&environment_id="+f.envID, f.taskKey)

	w := getJSON(t, f.h, "/credentials/decisions", f.token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /credentials/decisions = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Decisions []struct {
			Tool      string `json:"tool"`
			Allowed   bool   `json:"allowed"`
			Reason    string `json:"reason"`
			TaskID    string `json:"task_id"`
			TaskTitle string `json:"task_title"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sawAllow, sawDeny bool
	for _, d := range out.Decisions {
		if d.TaskID == f.taskID && d.TaskTitle != "investigate" {
			t.Errorf("decision for task %s titled %q, want investigate", d.TaskID, d.TaskTitle)
		}
		if d.Allowed {
			sawAllow = true
		} else {
			sawDeny = true
		}
	}
	if !sawAllow || !sawDeny {
		t.Errorf("audit trail missing outcomes (allow=%v deny=%v): %+v", sawAllow, sawDeny, out.Decisions)
	}
}

func TestIntegration_CredentialRoutesRefuseWhenUnwired(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	srv.SetCredentials(nil)
	token := registerUser(t, h, "credunwired@test.com", "CUw", "SecurePassword1")

	for _, path := range []string{"/credentials", "/credential-providers"} {
		if w := getJSON(t, h, path, token); w.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s with no credential manager = %d, want 503", path, w.Code)
		}
	}
}

func TestIntegration_ResolveRefusesCallersThatAreNotRuns(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credcollect@test.com", "CCo", fullCaps())
	path := "/internal/credentials/resolve?server=thulu-cloud&tool=metrics_query&environment_id=" + f.envID

	claims, err := auth.ValidateJWT(f.token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	wsKey := mintKey(t, f.srv, claims.WorkspaceID, auth.RoleOwner, auth.PermAll)

	for name, w := range map[string]*httptest.ResponseRecorder{
		"owner session":          getJSON(t, f.h, path, f.token),
		"workspace owner key":    f.getWithKey(t, path, wsKey),
		"session naming a task":  getJSON(t, f.h, path+"&task="+f.taskID, f.token),
		"workspace key and task": f.getWithKey(t, path+"&task="+f.taskID, wsKey),
	} {
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: resolve = %d, want 403: %s", name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), secretValue) {
			t.Errorf("%s: the refusal leaked the payload", name)
		}
	}

	if w := f.getWithKey(t, path, f.taskKey); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), secretValue) {
		t.Fatalf("run key resolve = %d, want 200 with the payload: %s", w.Code, w.Body.String())
	}
}

func TestIntegration_AvailableCredentialsScopedToTheKeysWorkspace(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credavail@test.com", "CAv", fullCaps())
	claims, err := auth.ValidateJWT(f.token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	own := mintKey(t, f.srv, claims.WorkspaceID, auth.RoleDeveloper, auth.PermInternalUse)
	_, otherWS := mintWorkspaceUser(t, f.srv, "credavail-other@test.com", "CAvO")
	foreign := mintKey(t, f.srv, otherWS, auth.RoleOwner, auth.PermAll)

	path := "/internal/credentials/available?server=thulu-cloud&environment_id=" + f.envID
	if w := f.getWithKey(t, path, own); w.Code != http.StatusOK {
		t.Fatalf("own workspace key = %d: %s", w.Code, w.Body.String())
	}
	if w := f.getWithKey(t, path, foreign); w.Code != http.StatusNotFound {
		t.Errorf("foreign workspace key = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := f.getWithKey(t, "/internal/credentials/available?server=thulu-cloud&task="+f.taskID, foreign); w.Code != http.StatusNotFound {
		t.Errorf("foreign workspace key naming the task = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := f.getWithKey(t, "/internal/environments/"+f.envID, foreign); w.Code != http.StatusNotFound {
		t.Errorf("foreign workspace key on environment metadata = %d, want 404", w.Code)
	}
	if w := getJSON(t, f.h, path, f.token); w.Code != http.StatusForbidden {
		t.Errorf("session = %d, want 403", w.Code)
	}
}

func TestIntegration_TaskScopedKeyCannotEscapeItsTaskByOmittingIt(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credescape@test.com", "CEs", fullCaps())

	otherProj := createProject(t, f.h, f.token, "other-proj")
	ew := postJSONAuth(t, f.h, "/projects/"+otherProj+"/environments", f.token, map[string]any{
		"name": "prod", "provider": "aws",
	})
	if ew.Code != http.StatusCreated && ew.Code != http.StatusOK {
		t.Fatalf("create other environment = %d: %s", ew.Code, ew.Body.String())
	}
	var other struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(ew.Body.Bytes(), &other); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cw := postJSONAuth(t, f.h, "/credentials", f.token, awsCredBody("other-aws",
		[]map[string]string{{"subject_type": "environment", "subject_id": other.ID}})); cw.Code != http.StatusOK {
		t.Fatalf("create other credential = %d: %s", cw.Code, cw.Body.String())
	}

	w := f.getWithKey(t,
		"/internal/credentials/resolve?server=thulu-cloud&tool=metrics_query&environment_id="+other.ID,
		f.taskKey)
	if w.Code == http.StatusOK {
		t.Fatalf("naming an unreachable environment was allowed: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret_access_key") {
		t.Fatalf("the refusal leaked a payload: %s", w.Body.String())
	}
}

func TestIntegration_InternalResolve_EnforcesTheRunStageAllowlist(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credstage@test.com", "CST", fullCaps())
	resolve := "/internal/credentials/resolve?server=thulu-cloud&tool=logs_search&environment_id=" + f.envID

	cases := []struct {
		name  string
		agent []types.AgentType
		want  int
	}{
		{"server wired for the stage", []types.AgentType{{Name: "developer",
			MCPByStage: map[string][]string{"in_progress": {"thulu-cloud@logs_search"}}}}, http.StatusOK},
		{"other tool of the server", []types.AgentType{{Name: "developer",
			MCPByStage: map[string][]string{"in_progress": {"thulu-cloud@metrics_query"}}}}, http.StatusForbidden},
		{"wired for another stage only", []types.AgentType{{Name: "developer",
			MCPByStage: map[string][]string{"backlog": {"thulu-cloud"}}}}, http.StatusForbidden},
		{"agent no longer defined", nil, http.StatusForbidden},
	}
	for _, c := range cases {
		f.srv.agentSource = staticSource(c.agent)
		w := f.getWithKey(t, resolve, f.taskKey)
		if w.Code != c.want {
			t.Errorf("%s: resolve = %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
		if c.want == http.StatusForbidden && !strings.Contains(w.Body.String(), string(credentials.ReasonStageBlocked)) {
			t.Errorf("%s: refusal is not a stage block: %s", c.name, w.Body.String())
		}
	}

	w := f.getWithKey(t, "/internal/credentials/available?server=thulu-cloud", f.taskKey)
	if w.Code != http.StatusOK {
		t.Fatalf("available = %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Tools []credentials.ToolAvailability `json:"tools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, tool := range out.Tools {
		if tool.Decision.Allowed {
			t.Errorf("%s listed as allowed for a run whose agent is gone", tool.Tool)
		}
	}
}

func TestIntegration_InternalResolve_RecordsTheKeysAgent(t *testing.T) {
	t.Parallel()
	f := newCredFixture(t, "credagent@test.com", "CA", fullCaps())

	w := f.getWithKey(t,
		"/internal/credentials/resolve?agent=auditor&server=thulu-cloud&tool=logs_search&environment_id="+f.envID,
		f.taskKey)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve = %d: %s", w.Code, w.Body.String())
	}
	tk, err := f.srv.taskStore.Get(t.Context(), f.taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	ds, err := f.srv.credStore.ListDecisions(t.Context(), tk.WorkspaceID, 10)
	if err != nil || len(ds) == 0 {
		t.Fatalf("decisions = %v, err = %v", ds, err)
	}
	if ds[0].Agent != "developer" {
		t.Errorf("decision agent = %q, want developer", ds[0].Agent)
	}
}
