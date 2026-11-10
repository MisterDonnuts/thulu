package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/engine"
	"github.com/MisterDonnuts/thulu/internal/types"
)

func putJSONWithHeaders(t *testing.T, h http.Handler, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func installAgentCompose(t *testing.T, agentsDir, name, engine, supported string) {
	t.Helper()
	dir := filepath.Join(agentsDir, "agents", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir agent: %v", err)
	}
	supLabel := ""
	if supported != "" {
		supLabel = `      thulu.engines.supported: "` + supported + "\"\n"
	}
	compose := `services:
  agent:
    image: octopus-docker
    labels:
      thulu.agent: "true"
      thulu.type: "` + name + `"
      thulu.engine: "` + engine + `"
` + supLabel + `      thulu.stages: "in_progress"
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
}

func TestLLMProfile_CreateGetListUpdateDelete(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)

	body := map[string]string{
		"name":     "team-anthropic",
		"engine":   "claude-code",
		"model":    "sonnet",
		"effort":   "medium",
		"base_url": "https://api.anthropic.com",
		"key":      "sk-ant-api-create-1",
	}
	w := postJSON(t, h, "/llm-profiles", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-ant-api-create-1") {
		t.Fatal("response leaked raw key")
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("no id in response: %s", w.Body.String())
	}

	lw := getJSON(t, h, "/llm-profiles", "")
	if lw.Code != http.StatusOK {
		t.Fatalf("LIST = %d: %s", lw.Code, lw.Body.String())
	}
	if !strings.Contains(lw.Body.String(), "team-anthropic") {
		t.Errorf("list response missing profile: %s", lw.Body.String())
	}

	pw := patchJSONAuth(t, h, "/llm-profiles/"+id, "", map[string]any{
		"model": "opus",
	})
	if pw.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", pw.Code, pw.Body.String())
	}
	var patched map[string]any
	_ = json.Unmarshal(pw.Body.Bytes(), &patched)
	if patched["model"] != "opus" {
		t.Errorf("model not rotated: %v", patched["model"])
	}

	dw := deleteRequest(t, h, "/llm-profiles/"+id, "")
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", dw.Code, dw.Body.String())
	}
	gw := getJSON(t, h, "/llm-profiles/"+id, "")
	if gw.Code != http.StatusNotFound {
		t.Errorf("after delete, GET should be 404; got %d", gw.Code)
	}
}

func TestLLMProfile_DuplicateNameConflict(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	body := map[string]string{
		"name":   "dup",
		"engine": "claude-code",
		"key":    "sk-ant-api-1",
	}
	if w := postJSON(t, h, "/llm-profiles", body); w.Code != http.StatusCreated {
		t.Fatalf("first POST = %d: %s", w.Code, w.Body.String())
	}
	body["key"] = "sk-ant-api-2"
	if w := postJSON(t, h, "/llm-profiles", body); w.Code != http.StatusConflict {
		t.Errorf("dup POST = %d (want 409): %s", w.Code, w.Body.String())
	}
}

func TestLLMProfile_RejectsBadKeyPrefix(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	body := map[string]string{
		"name":   "bad-prefix",
		"engine": "claude-code",
		"key":    "nope-this-is-not-anthropic",
	}
	if w := postJSON(t, h, "/llm-profiles", body); w.Code != http.StatusBadRequest {
		t.Errorf("bad prefix POST = %d (want 400): %s", w.Code, w.Body.String())
	}
}

func TestLLMProfile_RejectsInsecureBaseURL(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	body := map[string]string{
		"name":     "bad-url",
		"engine":   "claude-code",
		"base_url": "http://api.example.com",
		"key":      "sk-ant-api-1",
	}
	if w := postJSON(t, h, "/llm-profiles", body); w.Code != http.StatusBadRequest {
		t.Errorf("http:// remote URL POST = %d (want 400): %s", w.Code, w.Body.String())
	}
}

func TestAgentBinding_PutGetDelete_GuardrailHonored(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	installAgentCompose(t, srv.agentsDir, "reviewer", "claude-code", "claude-code,openai-codex")

	okBody := map[string]string{
		"name": "p-claude", "engine": "claude-code", "key": "sk-ant-api-1",
	}
	pw := postJSON(t, h, "/llm-profiles", okBody)
	if pw.Code != http.StatusCreated {
		t.Fatalf("create p-claude: %d body=%s", pw.Code, pw.Body.String())
	}
	var pClaude map[string]any
	_ = json.Unmarshal(pw.Body.Bytes(), &pClaude)

	bw := putJSONWithHeaders(t, h, "/agents/reviewer/llm-binding",
		map[string]string{"profile_id": pClaude["id"].(string)}, nil)
	if bw.Code != http.StatusOK {
		t.Fatalf("PUT binding = %d: %s", bw.Code, bw.Body.String())
	}
	gw := getJSON(t, h, "/agents/reviewer/llm-binding", "")
	if gw.Code != http.StatusOK || !strings.Contains(gw.Body.String(), `"bound":true`) {
		t.Errorf("GET binding = %d %s", gw.Code, gw.Body.String())
	}

	ew := getJSON(t, h, "/agents/reviewer/effective-llm", "")
	if ew.Code != http.StatusOK {
		t.Fatalf("GET effective-llm = %d: %s", ew.Code, ew.Body.String())
	}
	if !strings.Contains(ew.Body.String(), `"source":"binding"`) {
		t.Errorf("effective-llm not from binding: %s", ew.Body.String())
	}
	if !strings.Contains(ew.Body.String(), `"model":"sonnet"`) {
		t.Errorf("effective-llm hides the model the run falls back to: %s", ew.Body.String())
	}

	dw := deleteRequest(t, h, "/agents/reviewer/llm-binding", "")
	if dw.Code != http.StatusOK {
		t.Fatalf("DELETE binding = %d: %s", dw.Code, dw.Body.String())
	}
	gw2 := getJSON(t, h, "/agents/reviewer/llm-binding", "")
	if !strings.Contains(gw2.Body.String(), `"bound":false`) {
		t.Errorf("binding should be cleared: %s", gw2.Body.String())
	}
}

func TestEffectiveLLM_HostSourceSaysWhetherTheHostMayRun(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	installAgentCompose(t, srv.agentsDir, "loner", "claude-code", "claude-code")

	ew := getJSON(t, h, "/agents/loner/effective-llm", "")
	if ew.Code != http.StatusOK {
		t.Fatalf("GET effective-llm = %d: %s", ew.Code, ew.Body.String())
	}
	var got struct {
		Source       string `json:"source"`
		HasCred      bool   `json:"has_credential"`
		HostFallback *bool  `json:"host_fallback"`
	}
	if err := json.Unmarshal(ew.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Source != "host" || got.HasCred {
		t.Fatalf("effective-llm = %s, want the host source with no credential", ew.Body.String())
	}
	if got.HostFallback == nil {
		t.Fatalf("effective-llm = %s, want host_fallback", ew.Body.String())
	}
	if want := srv.hostLLMCredentialAllowed(); *got.HostFallback != want {
		t.Errorf("host_fallback = %v, want %v (the dispatcher's rule)", *got.HostFallback, want)
	}
}

func TestAgentBinding_RejectsUnsupportedEngine(t *testing.T) {
	t.Parallel()
	srv, h := newIntegrationServer(t)
	installAgentCompose(t, srv.agentsDir, "narrow-agent", "claude-code", "claude-code")

	prof := postJSON(t, h, "/llm-profiles", map[string]string{
		"name": "p-other", "engine": "claude-code", "key": "sk-ant-api-1",
	})
	if prof.Code != http.StatusCreated {
		t.Fatalf("create profile: %d", prof.Code)
	}
	var p map[string]any
	_ = json.Unmarshal(prof.Body.Bytes(), &p)

	rewriteProfileEngine(t, srv, p["id"].(string), "openai-codex")

	bw := putJSONWithHeaders(t, h, "/agents/narrow-agent/llm-binding",
		map[string]string{"profile_id": p["id"].(string)}, nil)
	if bw.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for unsupported engine, got %d: %s", bw.Code, bw.Body.String())
	}

	bw2 := putJSONWithHeaders(t, h, "/agents/narrow-agent/llm-binding",
		map[string]string{"profile_id": p["id"].(string)},
		map[string]string{"X-Force-Engine": "true"})
	if bw2.Code != http.StatusOK {
		t.Errorf("force-engine bind = %d: %s", bw2.Code, bw2.Body.String())
	}
	if !strings.Contains(bw2.Body.String(), `"forced":true`) {
		t.Errorf("response missing forced=true: %s", bw2.Body.String())
	}
}

func rewriteProfileEngine(t *testing.T, srv *Server, profileID, engine string) {
	t.Helper()
	if srv.secretsStore == nil {
		t.Fatal("no secrets store")
	}
	if _, err := srv.secretsStore.DB().Exec(`UPDATE llm_profiles SET engine = $1 WHERE id = $2`, engine, profileID); err != nil {
		t.Fatalf("rewrite engine: %v", err)
	}
}

func TestAgentSupportsEngine_UnlabeledAgentRunsOnDefault(t *testing.T) {
	unlabeled := &types.AgentType{Name: "plain"}
	if !agentSupportsEngine(unlabeled, engine.DefaultName) {
		t.Errorf("an agent with no engine runs on %s and must accept its profiles", engine.DefaultName)
	}
	if agentSupportsEngine(unlabeled, "codex") {
		t.Error("an unlabeled agent must not accept another engine")
	}
	pinned := &types.AgentType{Name: "cx", Engine: "codex", SupportedEngines: []string{"codex"}}
	if agentSupportsEngine(pinned, engine.DefaultName) {
		t.Error("a declared engine list must stay authoritative")
	}
}

func createProfile(t *testing.T, h http.Handler, body map[string]any) (int, map[string]any, string) {
	t.Helper()
	w := postJSON(t, h, "/llm-profiles", body)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func TestLLMProfile_CustomEndpointKeysAndAuthHeader(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"virtual key on a custom endpoint", map[string]any{
			"name": "litellm", "engine": "claude-code", "base_url": "https://litellm.corp", "auth_header": "bearer", "key": "lt-virtual-1",
		}, http.StatusCreated},
		{"same key without base_url", map[string]any{
			"name": "no-url", "engine": "claude-code", "key": "lt-virtual-1",
		}, http.StatusBadRequest},
		{"bearer against the provider's own API", map[string]any{
			"name": "bearer-direct", "engine": "claude-code", "auth_header": "bearer", "key": "sk-ant-api-1",
		}, http.StatusBadRequest},
		{"unknown auth_header", map[string]any{
			"name": "weird", "engine": "claude-code", "base_url": "https://x.corp", "auth_header": "basic", "key": "k",
		}, http.StatusBadRequest},
		{"codex has no auth_header", map[string]any{
			"name": "codex-hdr", "engine": "codex", "base_url": "https://x.corp/v1", "auth_header": "bearer", "key": "k",
		}, http.StatusBadRequest},
	}
	for _, c := range cases {
		code, out, raw := createProfile(t, h, c.body)
		if code != c.want {
			t.Errorf("%s: %d (want %d): %s", c.name, code, c.want, raw)
		}
		if code == http.StatusCreated && out["auth_header"] != "bearer" {
			t.Errorf("%s: auth_header not stored: %v", c.name, out)
		}
	}
}

func TestLLMProfile_OpenCodeProviders(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"DeepSeek through Chat Completions", map[string]any{
			"name": "deepseek", "engine": "opencode", "provider": "openai-compatible",
			"base_url": "https://api.deepseek.com", "key": "sk-deepseek-1", "model": "deepseek-chat",
		}, http.StatusCreated},
		{"DeepSeek's Anthropic endpoint", map[string]any{
			"name": "deepseek-anthropic", "engine": "opencode", "provider": "anthropic",
			"base_url": "https://api.deepseek.com/anthropic", "key": "sk-deepseek-2", "model": "deepseek-chat",
		}, http.StatusCreated},
		{"Anthropic's own API with any key shape", map[string]any{
			"name": "oc-anthropic", "engine": "opencode", "provider": "anthropic", "key": "sk-ant-api03-x",
		}, http.StatusCreated},
		{"openai-compatible needs its endpoint", map[string]any{
			"name": "no-endpoint", "engine": "opencode", "provider": "openai-compatible", "key": "sk-x",
		}, http.StatusBadRequest},
		{"the engine's own provider by default", map[string]any{
			"name": "implicit", "engine": "opencode", "key": "sk-x",
		}, http.StatusBadRequest},
		{"no cloud route", map[string]any{
			"name": "oc-bedrock", "engine": "opencode", "provider": "bedrock", "region": "us-east-1",
		}, http.StatusBadRequest},
		{"claude-code does not speak Chat Completions", map[string]any{
			"name": "cc-compat", "engine": "claude-code", "provider": "openai-compatible",
			"base_url": "https://api.deepseek.com", "key": "sk-x",
		}, http.StatusBadRequest},
	}
	for _, c := range cases {
		code, out, raw := createProfile(t, h, c.body)
		if code != c.want {
			t.Errorf("%s: %d (want %d): %s", c.name, code, c.want, raw)
		}
		if code == http.StatusCreated && out["provider"] != c.body["provider"] {
			t.Errorf("%s: provider not stored: %v", c.name, out)
		}
	}
}

func TestLLMProfile_BaseURLHTTPAllowlist(t *testing.T) {
	_, h := newIntegrationServer(t)
	t.Setenv("OCTOPUS_LLM_ALLOW_HTTP_HOSTS", "*.svc.cluster.local, proxy.internal")
	cases := []struct {
		url  string
		want int
	}{
		{"http://litellm.llm.svc.cluster.local:4000", http.StatusCreated},
		{"http://proxy.internal", http.StatusCreated},
		{"http://localhost:4000", http.StatusCreated},
		{"http://svc.cluster.local", http.StatusBadRequest},
		{"http://localhost.evil.com", http.StatusBadRequest},
		{"http://api.example.com", http.StatusBadRequest},
		{"https://user:pass@llm.corp", http.StatusBadRequest},
		{"ftp://llm.corp", http.StatusBadRequest},
	}
	for i, c := range cases {
		code, _, raw := createProfile(t, h, map[string]any{
			"name": "u" + string(rune('a'+i)), "engine": "claude-code", "base_url": c.url, "key": "any-key",
		})
		if code != c.want {
			t.Errorf("%s: %d (want %d): %s", c.url, code, c.want, raw)
		}
	}
}

func TestLLMProfile_Bedrock(t *testing.T) {
	_, h := newIntegrationServer(t)
	code, out, raw := createProfile(t, h, map[string]any{
		"name": "bedrock", "engine": "claude-code", "provider": "bedrock", "region": "eu-west-1",
		"aws_access_key_id": "AKIAEXAMPLE1234", "aws_secret_access_key": "wJalrXUtnFEMI/SECRET",
		"small_fast_model": "eu.anthropic.claude-haiku-4-5",
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, raw)
	}
	if strings.Contains(raw, "SECRET") || strings.Contains(raw, "AKIAEXAMPLE1234") {
		t.Fatalf("response leaked the AWS credential: %s", raw)
	}
	if out["key_hint"] != "…1234" || out["has_key"] != true || out["provider"] != "bedrock" || out["region"] != "eu-west-1" {
		t.Errorf("profile = %v", out)
	}

	irsa := map[string]any{"name": "bedrock-irsa", "engine": "claude-code", "provider": "bedrock", "region": "us-east-1"}
	t.Setenv(engine.EnvAmbientCloudIdentity, "")
	if code, _, raw := createProfile(t, h, irsa); code != http.StatusBadRequest ||
		!strings.Contains(raw, engine.EnvAmbientCloudIdentity) {
		t.Errorf("profile on the install's identity without the opt-in = %d: %s", code, raw)
	}
	t.Setenv(engine.EnvAmbientCloudIdentity, "1")
	code, out, raw = createProfile(t, h, irsa)
	if code != http.StatusCreated || out["has_key"] != false {
		t.Errorf("workload identity profile = %d: %s", code, raw)
	}

	for name, body := range map[string]map[string]any{
		"no region":           {"provider": "bedrock"},
		"half a key pair":     {"provider": "bedrock", "region": "us-east-1", "aws_secret_access_key": "s"},
		"pair and api key":    {"provider": "bedrock", "region": "us-east-1", "aws_access_key_id": "A", "aws_secret_access_key": "s", "key": "k"},
		"org_id":              {"provider": "bedrock", "region": "us-east-1", "org_id": "org-1"},
		"codex on bedrock":    {"provider": "bedrock", "region": "us-east-1", "engine": "codex"},
		"aws on anthropic":    {"aws_access_key_id": "A", "aws_secret_access_key": "s", "key": "sk-ant-api-1"},
		"region on anthropic": {"region": "us-east-1", "key": "sk-ant-api-1"},
	} {
		body["name"] = "bad-" + strings.ReplaceAll(name, " ", "-")
		if body["engine"] == nil {
			body["engine"] = "claude-code"
		}
		if code, _, raw := createProfile(t, h, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d (want 400): %s", name, code, raw)
		}
	}
}

func TestLLMProfile_Vertex(t *testing.T) {
	t.Parallel()
	_, h := newIntegrationServer(t)
	sa := `{"type":"service_account","client_email":"octopus@proj.iam.gserviceaccount.com",` +
		`"private_key":"-----BEGIN PRIVATE KEY-----\nMIIsecret\n-----END PRIVATE KEY-----\n"}`
	code, out, raw := createProfile(t, h, map[string]any{
		"name": "vertex", "engine": "claude-code", "provider": "vertex", "region": "us-east5", "project_id": "proj",
		"gcp_service_account_json": sa,
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, raw)
	}
	if strings.Contains(raw, "MIIsecret") {
		t.Fatalf("response leaked the service account key: %s", raw)
	}
	if out["key_hint"] != "octopus@proj.iam.gserviceaccount.com" || out["project_id"] != "proj" {
		t.Errorf("profile = %v", out)
	}
	for name, body := range map[string]map[string]any{
		"no project":   {"region": "us-east5"},
		"not json":     {"region": "us-east5", "project_id": "p", "gcp_service_account_json": "nope"},
		"not a sa key": {"region": "us-east5", "project_id": "p", "gcp_service_account_json": `{"type":"authorized_user"}`},
		"api key":      {"region": "us-east5", "project_id": "p", "key": "sk-ant-api-1"},
	} {
		body["name"], body["engine"], body["provider"] = "bad-"+strings.ReplaceAll(name, " ", "-"), "claude-code", "vertex"
		if code, _, raw := createProfile(t, h, body); code != http.StatusBadRequest {
			t.Errorf("%s: %d (want 400): %s", name, code, raw)
		}
	}
}

func TestLLMProfile_ProviderSwitchOnUpdate(t *testing.T) {
	t.Setenv(engine.EnvAmbientCloudIdentity, "1")
	_, h := newIntegrationServer(t)
	code, out, raw := createProfile(t, h, map[string]any{"name": "p", "engine": "claude-code", "key": "sk-ant-api-abcd"})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, raw)
	}
	id, _ := out["id"].(string)

	w := patchJSONAuth(t, h, "/llm-profiles/"+id, "", map[string]any{"provider": "bedrock", "region": "us-west-2"})
	if w.Code != http.StatusOK {
		t.Fatalf("to bedrock = %d: %s", w.Code, w.Body.String())
	}
	out = map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["has_key"] != false || out["key_hint"] != nil {
		t.Errorf("the anthropic key followed the profile to bedrock: %v", out)
	}

	w = patchJSONAuth(t, h, "/llm-profiles/"+id, "", map[string]any{"provider": "anthropic", "region": ""})
	if w.Code != http.StatusBadRequest {
		t.Errorf("back to anthropic without a key = %d (want 400): %s", w.Code, w.Body.String())
	}
	w = patchJSONAuth(t, h, "/llm-profiles/"+id, "", map[string]any{"provider": "anthropic", "region": "", "key": "sk-ant-api-wxyz"})
	if w.Code != http.StatusOK {
		t.Errorf("back to anthropic with a key = %d: %s", w.Code, w.Body.String())
	}
}
