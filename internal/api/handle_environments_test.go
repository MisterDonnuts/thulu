package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/auth"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/envmetrics"
)

func sendJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func decodeEnv(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestIntegration_EnvironmentCRUD(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "env@test.com", "E", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "EnvProject")

	w := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token, map[string]any{
		"name":          "Prod",
		"provider":      "AWS",
		"region":        "us-east-1",
		"account":       "1234567890",
		"is_production": true,
		"url":           "https://example.com",
		"selectors":     map[string]string{"tag:Environment": "prod"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST environments = %d, want 201: %s", w.Code, w.Body.String())
	}
	env := decodeEnv(t, w)
	envID, _ := env["id"].(string)
	if envID == "" {
		t.Fatal("expected environment id")
	}
	if env["name"] != "prod" {
		t.Errorf("name = %v, want normalized to %q", env["name"], "prod")
	}
	if env["provider"] != "aws" {
		t.Errorf("provider = %v, want normalized to %q", env["provider"], "aws")
	}
	if env["has_credential"] != false {
		t.Errorf("has_credential = %v, want false on a fresh environment", env["has_credential"])
	}

	lw := getJSON(t, h, "/projects/"+projID+"/environments", token)
	if lw.Code != http.StatusOK {
		t.Fatalf("GET environments = %d, want 200", lw.Code)
	}
	var list []map[string]any
	if err := json.NewDecoder(lw.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 environment, got %d", len(list))
	}

	uw := sendJSON(t, h, http.MethodPatch, "/projects/"+projID+"/environments/"+envID, token, map[string]any{
		"name": "prod", "provider": "aws", "region": "eu-west-1", "is_production": true,
	})
	if uw.Code != http.StatusOK {
		t.Fatalf("PATCH environment = %d, want 200: %s", uw.Code, uw.Body.String())
	}
	if got := decodeEnv(t, uw)["region"]; got != "eu-west-1" {
		t.Errorf("region = %v, want eu-west-1", got)
	}

	dw := sendJSON(t, h, http.MethodDelete, "/projects/"+projID+"/environments/"+envID, token, nil)
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE environment = %d, want 200", dw.Code)
	}
	if aw := getJSON(t, h, "/projects/"+projID+"/environments", token); aw.Body.String() == "" ||
		bytes.Contains(aw.Body.Bytes(), []byte(envID)) {
		t.Errorf("environment still listed after delete: %s", aw.Body.String())
	}
}

func TestIntegration_EnvironmentPatchIsPartial(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "patch@test.com", "P", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "PatchProject")

	optStr := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}

	w := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token, map[string]any{
		"name": "prod", "system": "acme", "provider": "aws",
		"region": "us-east-1", "account": "123456789012",
		"url": "https://app.example.com", "is_production": true,
		"selectors": map[string]string{"tag:Environment": "prod"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST environments = %d, want 201: %s", w.Code, w.Body.String())
	}
	envID, _ := decodeEnv(t, w)["id"].(string)
	path := "/projects/" + projID + "/environments/" + envID

	uw := sendJSON(t, h, http.MethodPatch, path, token, map[string]any{"region": "eu-west-1"})
	if uw.Code != http.StatusOK {
		t.Fatalf("PATCH region = %d, want 200: %s", uw.Code, uw.Body.String())
	}
	got := decodeEnv(t, uw)
	if got["region"] != "eu-west-1" {
		t.Errorf("region = %v, want eu-west-1", got["region"])
	}
	for field, want := range map[string]string{
		"name": "prod", "system": "acme", "provider": "aws",
		"account": "123456789012", "url": "https://app.example.com",
	} {
		if optStr(got, field) != want {
			t.Errorf("%s = %q after a region-only PATCH, want %q preserved", field, optStr(got, field), want)
		}
	}
	if got["is_production"] != true {
		t.Errorf("is_production = %v after a region-only PATCH, want true preserved", got["is_production"])
	}
	if sel, _ := got["selectors"].(map[string]any); sel["tag:Environment"] != "prod" {
		t.Errorf("selectors = %v after a region-only PATCH, want preserved", got["selectors"])
	}

	fw := sendJSON(t, h, http.MethodPatch, path, token, map[string]any{"is_production": false})
	if fw.Code != http.StatusOK {
		t.Fatalf("PATCH is_production = %d, want 200: %s", fw.Code, fw.Body.String())
	}
	if got := decodeEnv(t, fw); got["is_production"] != false {
		t.Errorf("is_production = %v, want an explicit false to apply", got["is_production"])
	}

	cw := sendJSON(t, h, http.MethodPatch, path, token, map[string]any{"account": ""})
	if cw.Code != http.StatusOK {
		t.Fatalf("PATCH account = %d, want 200: %s", cw.Code, cw.Body.String())
	}
	if got := decodeEnv(t, cw); optStr(got, "account") != "" {
		t.Errorf("account = %q, want an explicit empty string to clear it", optStr(got, "account"))
	}
}

func TestIntegration_EnvironmentValidation(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "envval@test.com", "EV", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "EnvValidation")
	base := "/projects/" + projID + "/environments"

	bad := []struct {
		name string
		body map[string]any
	}{
		{"empty name", map[string]any{"name": "", "provider": "aws"}},
		{"name with spaces", map[string]any{"name": "my prod", "provider": "aws"}},
		{"unknown provider", map[string]any{"name": "prod", "provider": "awss"}},
		{"url without scheme", map[string]any{"name": "prod", "provider": "aws", "url": "example.com"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			w := sendJSON(t, h, http.MethodPost, base, token, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("POST %v = %d, want 400", tc.body, w.Code)
			}
		})
	}

	first := sendJSON(t, h, http.MethodPost, base, token, map[string]any{"name": "dev", "provider": "aws"})
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d, want 201: %s", first.Code, first.Body.String())
	}
	dup := sendJSON(t, h, http.MethodPost, base, token, map[string]any{"name": "dev", "provider": "gcp"})
	if dup.Code != http.StatusBadRequest {
		t.Errorf("duplicate name = %d, want 400", dup.Code)
	}
}

func TestIntegration_EnvironmentCredentialIsAGrant(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, fullCaps())
	token := registerUser(t, h, "envcred@test.com", "EC", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "EnvCred")
	base := "/projects/" + projID + "/environments"

	cw := sendJSON(t, h, http.MethodPost, base, token, map[string]any{"name": "prod", "provider": "aws"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create env = %d: %s", cw.Code, cw.Body.String())
	}
	envID := decodeEnv(t, cw)["id"].(string)

	if hasCred(t, h, token, base, envID) {
		t.Fatal("a fresh environment reports a credential")
	}

	crw := postJSONAuth(t, h, "/credentials", token, awsCredBody("prod-aws", []map[string]string{
		{"subject_type": "environment", "subject_id": envID},
	}))
	if crw.Code != http.StatusOK {
		t.Fatalf("create credential = %d: %s", crw.Code, crw.Body.String())
	}
	credID, _ := decodeCredential(t, crw)["id"].(string)

	if !hasCred(t, h, token, base, envID) {
		t.Error("granting a credential did not set has_credential on the environment")
	}

	if sw := getJSON(t, h, "/internal/environments/"+envID, token); sw.Code != http.StatusForbidden {
		t.Errorf("session on /internal/environments = %d, want 403", sw.Code)
	}
	claims, err := auth.ValidateJWT(token)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	wsKey := mintKey(t, srv, claims.WorkspaceID, auth.RoleDeveloper, auth.PermInternalUse)
	iw := getWithKey(t, h, "/internal/environments/"+envID, wsKey)
	if iw.Code != http.StatusOK {
		t.Fatalf("internal environment = %d: %s", iw.Code, iw.Body.String())
	}
	if bytes.Contains(iw.Body.Bytes(), []byte(secretValue)) {
		t.Fatal("the environment metadata route returned a secret")
	}
	meta := decodeEnv(t, iw)
	if meta["region"] == nil || meta["project_id"] != projID {
		t.Errorf("metadata = %+v, want the environment's own fields", meta)
	}
	if _, leaked := meta["payload"]; leaked {
		t.Error("metadata carried a payload field")
	}

	if w := sendJSON(t, h, http.MethodPut, "/credentials/"+credID+"/grants", token,
		map[string]any{"grants": []any{}}); w.Code != http.StatusOK {
		t.Fatalf("clear grants = %d: %s", w.Code, w.Body.String())
	}
	if hasCred(t, h, token, base, envID) {
		t.Error("has_credential stayed true after the grant was removed")
	}
	if w := getJSON(t, h, "/credentials/"+credID, token); w.Code != http.StatusOK {
		t.Error("detaching an environment deleted the credential")
	}
}

func TestIntegration_EnvironmentCredentialFlagIsProjectScoped(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	stubAWS(t, srv, fullCaps())
	token := registerUser(t, h, "envcred-scope@test.com", "ECS", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "EnvCredScope")
	base := "/projects/" + projID + "/environments"

	cw := sendJSON(t, h, http.MethodPost, base, token, map[string]any{"name": "prod", "provider": "aws"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create env = %d: %s", cw.Code, cw.Body.String())
	}
	envID := decodeEnv(t, cw)["id"].(string)

	if w := postJSONAuth(t, h, "/credentials", token, awsCredBody("scope-aws", []map[string]string{
		{"subject_type": "environment", "subject_id": envID},
	})); w.Code != http.StatusOK {
		t.Fatalf("create credential = %d: %s", w.Code, w.Body.String())
	}

	if !hasCred(t, h, token, base, envID) {
		t.Fatal("the owning user does not see the grant")
	}
	if !hasCred(t, h, "", base, envID) {
		t.Error("an unscoped caller reports has_credential=false for a granted environment")
	}
}

func hasCred(t *testing.T, h http.Handler, token, base, envID string) bool {
	t.Helper()
	var list []map[string]any
	if err := json.NewDecoder(getJSON(t, h, base, token).Body).Decode(&list); err != nil {
		t.Fatalf("decode environment list: %v", err)
	}
	for _, e := range list {
		if e["id"] == envID {
			return e["has_credential"] == true
		}
	}
	t.Fatalf("environment %s not in the list", envID)
	return false
}

func TestIntegration_EnvironmentScopedToProject(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "envscope@test.com", "ES", "SecurePassword1")
	projA := createProjectForMetrics(t, h, token, "ScopeA")
	projB := createProjectForMetrics(t, h, token, "ScopeB")

	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projA+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", cw.Code, cw.Body.String())
	}
	envID := decodeEnv(t, cw)["id"].(string)

	if w := sendJSON(t, h, http.MethodPatch, "/projects/"+projB+"/environments/"+envID, token,
		map[string]any{"name": "stolen"}); w.Code != http.StatusNotFound {
		t.Errorf("cross-project patch = %d, want 404", w.Code)
	}
	if w := sendJSON(t, h, http.MethodDelete, "/projects/"+projB+"/environments/"+envID, token, nil); w.Code != http.StatusNotFound {
		t.Errorf("cross-project delete = %d, want 404", w.Code)
	}
}

func TestIntegration_DeleteProjectPurgesEnvironments(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "envpurge@test.com", "EP", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "PurgeMe")

	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create env = %d: %s", cw.Code, cw.Body.String())
	}
	envID := decodeEnv(t, cw)["id"].(string)
	stubAWS(t, srv, fullCaps())
	crw := postJSONAuth(t, h, "/credentials", token, awsCredBody("purge-aws", []map[string]string{
		{"subject_type": "environment", "subject_id": envID},
	}))
	if crw.Code != http.StatusOK {
		t.Fatalf("create credential = %d: %s", crw.Code, crw.Body.String())
	}
	credID, _ := decodeCredential(t, crw)["id"].(string)

	if w := sendJSON(t, h, http.MethodDelete, "/projects/"+projID, token, nil); w.Code != http.StatusOK {
		t.Fatalf("delete project = %d, want 200: %s", w.Code, w.Body.String())
	}
	granted, err := srv.credStore.GrantedSubjects(t.Context(), defaultWorkspaceID, credentials.SubjectEnvironment)
	if err != nil {
		t.Fatalf("GrantedSubjects: %v", err)
	}
	if granted[envID] {
		t.Error("the credential grant survived the project delete")
	}
	if w := getJSON(t, h, "/credentials/"+credID, token); w.Code != http.StatusOK {
		t.Error("deleting a project deleted a workspace credential")
	}
	if _, err := srv.projectStore.FindEnvironment(t.Context(), envID); err == nil {
		t.Error("environment row survived the project delete")
	}
}

func TestIntegration_EnvMappingLifecycle(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "envmap@test.com", "EM", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "MappingProject")

	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws", "system": "acme"})
	if cw.Code != http.StatusCreated {
		t.Fatalf("create env = %d: %s", cw.Code, cw.Body.String())
	}
	env := decodeEnv(t, cw)
	envID := env["id"].(string)
	if env["system"] != "acme" {
		t.Errorf("system = %v, want acme — it's what ties multi-repo environments together", env["system"])
	}

	base := "/projects/" + projID + "/environments/" + envID + "/mappings"
	pw := sendJSON(t, h, http.MethodPost, base, token, map[string]any{
		"mappings": []map[string]any{{
			"service": "api",
			"signal":  "latency_p95",
			"source":  map[string]any{"namespace": "AWS/ApplicationELB", "metric_name": "TargetResponseTime"},
		}},
	})
	if pw.Code != http.StatusCreated {
		t.Fatalf("propose = %d, want 201: %s", pw.Code, pw.Body.String())
	}

	active, err := srv.envMetrics.ActiveMappings(t.Context())
	if err != nil {
		t.Fatalf("ActiveMappings: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("a proposal must not feed the collector, got %d active", len(active))
	}

	lw := getJSON(t, h, base, token)
	var listed []map[string]any
	if err := json.NewDecoder(lw.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 || listed[0]["status"] != "proposed" {
		t.Fatalf("list = %+v, want one proposed mapping", listed)
	}

	aw := sendJSON(t, h, http.MethodPost, base+"/approve", token, nil)
	if aw.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200: %s", aw.Code, aw.Body.String())
	}
	if got := decodeEnv(t, aw)["activated"]; got != float64(1) {
		t.Errorf("activated = %v, want 1", got)
	}
	if active, err = srv.envMetrics.ActiveMappings(t.Context()); err != nil || len(active) != 1 {
		t.Fatalf("after approve: %d active (err %v), want 1", len(active), err)
	}

	mapID := listed[0]["id"].(string)
	if dw := sendJSON(t, h, http.MethodDelete, base+"/"+mapID, token, nil); dw.Code != http.StatusOK {
		t.Errorf("delete mapping = %d, want 200", dw.Code)
	}
}

func TestIntegration_EnvMappingRaisesAndResolvesItsFlag(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "envmapflag@test.com", "EMF", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "MappingFlagProject")

	tw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "connect observability", "body": "", "project_ids": []string{projID},
	})
	var createdTask map[string]any
	if err := json.NewDecoder(tw.Body).Decode(&createdTask); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID := createdTask["id"].(string)

	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws", "is_production": true})
	envID := decodeEnv(t, cw)["id"].(string)
	base := "/projects/" + projID + "/environments/" + envID + "/mappings"

	pw := sendJSON(t, h, http.MethodPost, base, token, map[string]any{
		"task_id": taskID,
		"agent":   "devops",
		"mappings": []map[string]any{
			{"service": "api", "signal": "latency_p95",
				"source": map[string]any{"namespace": "AWS/Lambda", "metric_name": "Duration"}},
			{"service": "api", "signal": "error_rate",
				"source": map[string]any{"namespace": "AWS/Lambda", "metric_name": "Errors"}},
		},
	})
	if pw.Code != http.StatusCreated {
		t.Fatalf("propose = %d, want 201: %s", pw.Code, pw.Body.String())
	}

	fw := getJSON(t, h, "/tasks/"+taskID+"/flags", token)
	var flags []map[string]any
	if err := json.NewDecoder(fw.Body).Decode(&flags); err != nil {
		t.Fatalf("decode flags: %v", err)
	}
	if len(flags) != 1 {
		t.Fatalf("proposing raised %d flags, want exactly 1 — the review gate depends on it", len(flags))
	}
	flag := flags[0]
	if flag["status"] != "pending" {
		t.Errorf("flag status = %v, want pending", flag["status"])
	}
	if flag["kind"] != "infra_impact" {
		t.Errorf("flag kind = %v, want infra_impact", flag["kind"])
	}
	if flag["created_by"] != "envmapflag@test.com" {
		t.Errorf("created_by = %v, want the authenticated user to win over the body's agent field", flag["created_by"])
	}
	payload, _ := flag["payload"].(map[string]any)
	missing, _ := payload["signals_missing"].([]any)
	if len(missing) != len(envmetrics.ValidSignals)-2 {
		t.Errorf("signals_missing = %v, want the 6 unmapped catalog signals", missing)
	}

	aw := sendJSON(t, h, http.MethodPost, base+"/approve", token, nil)
	if aw.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200: %s", aw.Code, aw.Body.String())
	}
	if got := decodeEnv(t, aw)["flags_resolved"]; got != float64(1) {
		t.Fatalf("flags_resolved = %v, want 1 — approving must close the flag it raised", got)
	}

	fw = getJSON(t, h, "/tasks/"+taskID+"/flags", token)
	flags = nil
	if err := json.NewDecoder(fw.Body).Decode(&flags); err != nil {
		t.Fatalf("decode flags after approve: %v", err)
	}
	if len(flags) != 1 || flags[0]["status"] != "approved" {
		t.Errorf("flag after approve = %+v, want a single approved flag", flags)
	}
}

func TestIntegration_ApprovingTheFlagActivatesTheMappings(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagdoor@test.com", "FD", "SecurePassword1")
	taskID, flagID := proposeReviewedMapping(t, srv, h, token, "FlagDoorProject")

	uw := sendJSON(t, h, http.MethodPatch, "/tasks/"+taskID+"/flags/"+flagID, token,
		map[string]any{"status": "approved"})
	if uw.Code != http.StatusOK {
		t.Fatalf("approve flag = %d: %s", uw.Code, uw.Body.String())
	}
	assertActiveMappings(t, srv, 1)
}

func TestIntegration_DecidingTheApprovalActivatesTheMappings(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "approvaldoor@test.com", "AD", "SecurePassword1")
	taskID, approvalID := proposeReviewedMapping(t, srv, h, token, "ApprovalDoorProject")

	dw := sendJSON(t, h, http.MethodPost, "/tasks/"+taskID+"/approvals/"+approvalID+"/decide", token,
		map[string]any{"decision": "approved"})
	if dw.Code != http.StatusOK {
		t.Fatalf("decide = %d: %s", dw.Code, dw.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(dw.Body).Decode(&out); err != nil {
		t.Fatalf("decode decision: %v", err)
	}
	if out["status"] != "approved" || out["mappings_activated"] != float64(1) {
		t.Fatalf("decision = %v, want approved with mappings_activated 1", out)
	}
	assertActiveMappings(t, srv, 1)
}

func proposeReviewedMapping(t *testing.T, srv *Server, h http.Handler, token, project string) (taskID, flagID string) {
	t.Helper()
	projID := createProjectForMetrics(t, h, token, project)

	tw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "connect observability", "body": "", "project_ids": []string{projID},
	})
	var createdTask map[string]any
	if err := json.NewDecoder(tw.Body).Decode(&createdTask); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID = createdTask["id"].(string)

	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws"})
	envID := decodeEnv(t, cw)["id"].(string)

	pw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments/"+envID+"/mappings", token,
		map[string]any{
			"task_id": taskID,
			"mappings": []map[string]any{{"service": "api", "signal": "throughput",
				"source": map[string]any{"namespace": "AWS/Lambda", "metric_name": "Invocations"}}},
		})
	if pw.Code != http.StatusCreated {
		t.Fatalf("propose = %d: %s", pw.Code, pw.Body.String())
	}

	fw := getJSON(t, h, "/tasks/"+taskID+"/flags", token)
	var flags []map[string]any
	if err := json.NewDecoder(fw.Body).Decode(&flags); err != nil {
		t.Fatalf("decode flags: %v", err)
	}
	if len(flags) != 1 {
		t.Fatalf("expected the auto-raised review flag, got %d", len(flags))
	}
	assertActiveMappings(t, srv, 0)
	return taskID, flags[0]["id"].(string)
}

func assertActiveMappings(t *testing.T, srv *Server, want int) {
	t.Helper()
	active, err := srv.envMetrics.ActiveMappings(t.Context())
	if err != nil {
		t.Fatalf("ActiveMappings: %v", err)
	}
	if len(active) != want {
		t.Fatalf("%d active mappings, want %d: every door that approves the review must activate", len(active), want)
	}
}

func TestIntegration_ApprovingAPlainFlagTouchesNoMappings(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "flagplain@test.com", "FP", "SecurePassword1")
	tw := postJSONAuth(t, h, "/tasks", token, map[string]any{
		"title": "plain", "body": "", "project_ids": []string{},
	})
	var createdTask map[string]any
	if err := json.NewDecoder(tw.Body).Decode(&createdTask); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	taskID := createdTask["id"].(string)

	fw := postJSONAuth(t, h, "/tasks/"+taskID+"/flags", token, map[string]any{
		"kind": "design", "label": "no tracing anywhere",
	})
	var flag map[string]any
	if err := json.NewDecoder(fw.Body).Decode(&flag); err != nil {
		t.Fatalf("decode flag: %v", err)
	}
	uw := sendJSON(t, h, http.MethodPatch, "/tasks/"+taskID+"/flags/"+flag["id"].(string), token,
		map[string]any{"status": "approved"})
	if uw.Code != http.StatusOK {
		t.Fatalf("approve plain flag = %d: %s", uw.Code, uw.Body.String())
	}
}

func TestIntegration_EnvMappingNotApplicable(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "envmapna@test.com", "ENA", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "NAProject")
	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws"})
	envID := decodeEnv(t, cw)["id"].(string)
	base := "/projects/" + projID + "/environments/" + envID + "/mappings"

	pw := sendJSON(t, h, http.MethodPost, base, token, map[string]any{
		"mappings": []map[string]any{
			{"service": "api", "signal": "latency_p95",
				"source": map[string]any{"namespace": "AWS/Lambda", "metric_name": "Duration"}},
			{"service": "api", "signal": "cpu", "not_applicable": true,
				"note": "Lambda exposes no CPU metric"},
		},
	})
	if pw.Code != http.StatusCreated {
		t.Fatalf("propose = %d, want 201: %s", pw.Code, pw.Body.String())
	}

	if aw := sendJSON(t, h, http.MethodPost, base+"/approve", token, nil); aw.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200: %s", aw.Code, aw.Body.String())
	}

	lw := getJSON(t, h, base, token)
	var listed []map[string]any
	if err := json.NewDecoder(lw.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	byStatus := map[string]map[string]any{}
	for _, m := range listed {
		byStatus[m["status"].(string)] = m
	}
	if _, ok := byStatus["active"]; !ok {
		t.Errorf("the real mapping did not go active: %+v", listed)
	}
	na, ok := byStatus["not_applicable"]
	if !ok {
		t.Fatalf("approving promoted the not-applicable row to something else: %+v", listed)
	}
	if na["note"] != "Lambda exposes no CPU metric" {
		t.Errorf("note = %v, want the reason preserved — the reason is the whole value", na["note"])
	}

	active, err := srv.envMetrics.ActiveMappings(t.Context())
	if err != nil {
		t.Fatalf("ActiveMappings: %v", err)
	}
	if len(active) != 1 || active[0].Signal != envmetrics.SignalLatencyP95 {
		t.Fatalf("collector sees %+v, want only the mapping that has a source", active)
	}
}

func TestIntegration_EnvMappingNotApplicableNeedsReason(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "envmapnar@test.com", "ENR", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "NARProject")
	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws"})
	envID := decodeEnv(t, cw)["id"].(string)

	pw := sendJSON(t, h, http.MethodPost,
		"/projects/"+projID+"/environments/"+envID+"/mappings", token, map[string]any{
			"mappings": []map[string]any{{"service": "api", "signal": "cpu", "not_applicable": true}},
		})
	if pw.Code != http.StatusBadRequest {
		t.Errorf("not_applicable without a note = %d, want 400: %s", pw.Code, pw.Body.String())
	}
}

func TestIntegration_EnvMappingWithoutTaskRaisesNoFlag(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "envmapnoflag@test.com", "EMN", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "NoFlagProject")
	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "dev", "provider": "aws"})
	envID := decodeEnv(t, cw)["id"].(string)
	base := "/projects/" + projID + "/environments/" + envID + "/mappings"

	pw := sendJSON(t, h, http.MethodPost, base, token, map[string]any{
		"mappings": []map[string]any{{"service": "api", "signal": "cpu",
			"source": map[string]any{"namespace": "AWS/ECS", "metric_name": "CPUUtilization"}}},
	})
	if pw.Code != http.StatusCreated {
		t.Fatalf("propose without task = %d, want 201: %s", pw.Code, pw.Body.String())
	}
	aw := sendJSON(t, h, http.MethodPost, base+"/approve", token, nil)
	if got := decodeEnv(t, aw)["flags_resolved"]; got != float64(0) {
		t.Errorf("flags_resolved = %v, want 0", got)
	}
}

func TestIntegration_EnvMappingValidation(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "envmapval@test.com", "EMV", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "MappingValidation")
	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws"})
	envID := decodeEnv(t, cw)["id"].(string)
	base := "/projects/" + projID + "/environments/" + envID + "/mappings"

	bad := []struct {
		name string
		body map[string]any
	}{
		{"empty batch", map[string]any{"mappings": []map[string]any{}}},
		{"unknown signal", map[string]any{"mappings": []map[string]any{
			{"service": "api", "signal": "made_up", "source": map[string]any{"a": "b"}}}}},
		{"no source", map[string]any{"mappings": []map[string]any{
			{"service": "api", "signal": "cpu"}}}},
		{"no service", map[string]any{"mappings": []map[string]any{
			{"signal": "cpu", "source": map[string]any{"a": "b"}}}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if w := sendJSON(t, h, http.MethodPost, base, token, tc.body); w.Code != http.StatusBadRequest {
				t.Errorf("POST %v = %d, want 400", tc.body, w.Code)
			}
		})
	}
}

func TestIntegration_EnvSeriesEmpty(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	token := registerUser(t, h, "envseries@test.com", "ES2", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "SeriesProject")
	cw := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token,
		map[string]any{"name": "prod", "provider": "aws"})
	envID := decodeEnv(t, cw)["id"].(string)

	w := getJSON(t, h, "/projects/"+projID+"/environments/"+envID+"/series", token)
	if w.Code != http.StatusOK {
		t.Fatalf("series = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := decodeEnv(t, w)
	pts, ok := body["points"].([]any)
	if !ok || len(pts) != 0 {
		t.Errorf("points = %v, want an empty array", body["points"])
	}

	if bw := getJSON(t, h, "/projects/"+projID+"/environments/"+envID+"/series?signal=nope", token); bw.Code != http.StatusBadRequest {
		t.Errorf("unknown signal = %d, want 400", bw.Code)
	}
}

func TestIntegration_EnvironmentCarriesTheIngestEndpoint(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	token := registerUser(t, h, "ingest-url@test.com", "I", "SecurePassword1")
	projID := createProjectForMetrics(t, h, token, "IngestURLProject")

	w := sendJSON(t, h, http.MethodPost, "/projects/"+projID+"/environments", token, map[string]any{
		"name": "prod", "provider": "gcp", "is_production": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("POST environments = %d: %s", w.Code, w.Body.String())
	}

	if got := decodeEnv(t, w)["ingest_endpoint"]; got != nil {
		t.Errorf("ingest_endpoint = %v before an operator set one, want absent", got)
	}

	if err := srv.settingsStore.SetIngestPublicURL(t.Context(), "https://thulu.example.com"); err != nil {
		t.Fatal(err)
	}

	w = getJSON(t, h, "/projects/"+projID+"/environments", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET environments = %d: %s", w.Code, w.Body.String())
	}
	var list []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d environments, want 1", len(list))
	}
	if got := list[0]["ingest_endpoint"]; got != "https://thulu.example.com/ingest" {
		t.Errorf("ingest_endpoint = %v, want the base an exporter posts to", got)
	}
}
